package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// desktopFixture is the sessions fixture with desktops on the fake
// platform: alex's "desk" and an endpoint session, sam's "desk", and the
// platform's websockify and logs addresses for each, which echo what they
// are sent once upgraded.
type desktopFixture struct {
	*fixture
	viewer   *platform.Viewer
	upstream string // how a desktop socket answers: "" upgrades
	mu       sync.Mutex
	seen     []*http.Request
}

func newDesktopFixture(t *testing.T) *desktopFixture {
	f := &desktopFixture{fixture: newFixture(t)}
	f.sessions = []map[string]any{
		{"name": "desk", "user": "alex", "type": "desktop", "status": "running", "externalHref": "/sessions/alex/desk/vnc", "software": map[string]any{"type": "vnc"}},
		{"name": "web", "user": "alex", "type": "endpoint", "status": "running", "externalHref": "https://web.sessions.example.com/", "domainName": "web.sessions.example.com"},
		{"name": "notvnc", "user": "alex", "type": "desktop", "status": "running", "software": map[string]any{"type": "other"}},
		{"name": "desk", "user": "sam", "type": "desktop", "status": "running", "externalHref": "/sessions/sam/desk/vnc", "software": map[string]any{"type": "vnc"}},
	}
	listed := f.platform.Config.Handler
	f.platform.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			// Each key lists its own account's sessions; the deployment
			// key is alex's.
			owner := map[string]string{"Bearer alex-own-key": "alex", "Bearer deploy-key": "alex", "Bearer sam-own-key": "sam"}[r.Header.Get("Authorization")]
			f.mu.Lock()
			f.auth = append(f.auth, r.Header.Get("Authorization"))
			var mine []map[string]any
			for _, s := range f.sessions {
				if s["user"] == owner {
					mine = append(mine, s)
				}
			}
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(mine)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/me/session/") {
			listed.ServeHTTP(w, r)
			return
		}
		f.mu.Lock()
		f.seen = append(f.seen, r.Clone(r.Context()))
		how := f.upstream
		f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"xstartup":"started","xvnc":"Xvnc ready","other":"x"}`)
		case how == "signin":
			w.Header().Set("Location", "/login")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case how == "offline":
			w.WriteHeader(http.StatusBadGateway)
		case how == "gone":
			w.WriteHeader(http.StatusNotFound)
		default:
			conn, buf, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			// Headers a platform could add to the handshake, which must not
			// reach the browser under the Studio's origin.
			fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: x\r\nSec-WebSocket-Protocol: %s\r\nSet-Cookie: platform_session=p; Path=/; HttpOnly\r\nStrict-Transport-Security: max-age=63072000\r\nAccess-Control-Allow-Origin: *\r\nX-Platform-Node: n1\r\n\r\n", r.Header.Get("Sec-WebSocket-Protocol"))
			buf.Flush()
			b := make([]byte, 64)
			for {
				n, err := buf.Read(b)
				if n > 0 {
					conn.Write([]byte("echo:" + string(b[:n])))
				}
				if err != nil || string(b[:n]) == "bye" {
					return
				}
			}
		}
	})
	// Signed in as alex with his own key unless a test says otherwise.
	f.viewer = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id", Username: "alex"}, Cred: &platform.Cred{Key: "alex-own-key"}}
	f.routes.viewer = func(r *http.Request) (*platform.Viewer, error) { return f.viewer, nil }
	return f
}

// dial opens a desktop socket on the Studio with the given Origin ("" for
// the Studio's own) and returns the answer and the connection.
func (f *desktopFixture) dial(path, origin string) (*http.Response, net.Conn, *bufio.Reader) {
	f.t.Helper()
	host := strings.TrimPrefix(f.studio.URL, "http://")
	if origin == "" {
		origin = f.studio.URL
	}
	conn, err := net.Dial("tcp", host)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nCookie: ade_session=studio-cookie\r\nUpgrade: websocket\r\nConnection: keep-alive, Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\n\r\n", path, host, origin)
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return res, conn, br
}

func errorOf(res *http.Response) string {
	var body struct{ Error string }
	json.NewDecoder(res.Body).Decode(&body)
	return body.Error
}

func (f *desktopFixture) upstreamSeen() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.seen...)
}

func TestDesktopRelaysAsTheViewer(t *testing.T) {
	f := newDesktopFixture(t)
	res, conn, br := f.dial("/api/session-desktop/alex/desk/websockify", "")
	if res.StatusCode != http.StatusSwitchingProtocols || res.Header.Get("Sec-Websocket-Protocol") != "binary" {
		t.Fatal(res.Status, res.Header)
	}
	conn.Write([]byte("frame"))
	b := make([]byte, 64)
	n, err := br.Read(b)
	if err != nil || string(b[:n]) != "echo:frame" {
		t.Fatal(string(b[:n]), err)
	}
	// The address is the platform's for the record; the person's own key,
	// the platform's origin, and none of the Studio's cookies.
	seen := f.upstreamSeen()
	if len(seen) != 1 {
		t.Fatal(len(seen))
	}
	up := seen[0]
	if up.URL.Path != "/me/session/alex/desk/websockify" || up.Header.Get("Authorization") != "Bearer alex-own-key" ||
		up.Header.Get("Cookie") != "" || up.Header.Get("Origin") != f.platform.URL || up.Header.Get("Sec-Websocket-Key") == "" {
		t.Fatal(up.URL.Path, up.Header)
	}
	for _, a := range f.auth {
		if strings.Contains(a, "deploy-key") {
			t.Fatal("the deployment key was used for a signed-in person:", f.auth)
		}
	}
	// The platform closing the socket closes the browser's.
	conn.Write([]byte("bye"))
	n, _ = br.Read(b)
	if string(b[:n]) != "echo:bye" {
		t.Fatal(string(b[:n]))
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := br.Read(b); err != io.EOF {
		t.Fatal("the browser's side stays open after the platform's closed:", err)
	}
}

func TestDesktopHandshakeCarriesOnlyWebSocketHeaders(t *testing.T) {
	f := newDesktopFixture(t)
	res, _, _ := f.dial("/api/session-desktop/alex/desk/websockify", "")
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatal(res.Status)
	}
	for _, h := range []string{"Set-Cookie", "Strict-Transport-Security", "Access-Control-Allow-Origin", "X-Platform-Node"} {
		if v := res.Header.Values(h); len(v) > 0 {
			t.Errorf("the platform's %s reached the browser: %q", h, v)
		}
	}
	if res.Header.Get("Upgrade") != "websocket" || res.Header.Get("Sec-Websocket-Accept") != "x" || res.Header.Get("Sec-Websocket-Protocol") != "binary" ||
		!strings.Contains(strings.ToLower(res.Header.Get("Connection")), "upgrade") {
		t.Fatal(res.Header)
	}
}

func TestDesktopRefusals(t *testing.T) {
	f := newDesktopFixture(t)
	cases := []struct {
		name, path, origin string
		viewer             *platform.Viewer
		status             int
		error              string
	}{
		{"another site's page", "/api/session-desktop/alex/desk/websockify", "https://elsewhere.example.com", nil, 403, "A desktop opens only from the Studio's own pages."},
		{"another person's session", "/api/session-desktop/sam/desk/websockify", "", nil, 404, "No desktop session desk of sam is visible to you."},
		{"sam asks for alex's", "/api/session-desktop/alex/desk/websockify", "", &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "sam-id"}, Cred: &platform.Cred{Key: "sam-own-key"}}, 404, "No desktop session desk of alex is visible to you."},
		{"not listed", "/api/session-desktop/alex/other/websockify", "", nil, 404, "No desktop session other of alex is visible to you."},
		{"an endpoint session", "/api/session-desktop/alex/web/websockify", "", nil, 404, "No desktop session web of alex is visible to you."},
		{"a desktop that is not VNC", "/api/session-desktop/alex/notvnc/websockify", "", nil, 404, "No desktop session notvnc of alex is visible to you."},
		{"signed out", "/api/session-desktop/alex/desk/websockify", "", &platform.Viewer{AuthEnabled: true}, 401, "Sign in to the Studio to open a desktop."},
		{"signed in with no key of their own", "/api/session-desktop/alex/desk/websockify", "", &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id"}}, 409,
			"A desktop opens with your own ACTIVATE sign-in. Sign in with ACTIVATE under Settings, Accounts, then try again."},
		{"a provider key is not a platform key", "/api/session-desktop/alex/desk/websockify", "", &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id"}, Cred: &platform.Cred{Key: "k", BaseURL: "https://models.example.com"}}, 409,
			"A desktop opens with your own ACTIVATE sign-in. Sign in with ACTIVATE under Settings, Accounts, then try again."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ResetCache()
			f.mu.Lock()
			f.auth, f.seen = nil, nil
			f.mu.Unlock()
			f.viewer = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id"}, Cred: &platform.Cred{Key: "alex-own-key"}}
			if c.viewer != nil {
				f.viewer = c.viewer
			}
			res, _, _ := f.dial(c.path, c.origin)
			if res.StatusCode != c.status {
				t.Fatal(res.Status)
			}
			if e := errorOf(res); e != c.error {
				t.Fatal(e)
			}
			if len(f.upstreamSeen()) != 0 {
				t.Fatal("the platform's desktop was contacted")
			}
			for _, a := range f.auth {
				if strings.Contains(a, "deploy-key") {
					t.Fatal("the deployment key was used:", f.auth)
				}
			}
		})
	}
}

func TestDesktopUpstreamAnswers(t *testing.T) {
	f := newDesktopFixture(t)
	for how, want := range map[string]struct {
		status int
		error  string
	}{
		"signin":  {502, "The platform did not accept your credential for this desktop; it may have expired or been revoked."},
		"offline": {503, "The desktop's cluster is not answering; it may be starting, or offline."},
		"gone":    {404, "The platform has no desktop at that address; it may have stopped."},
	} {
		f.upstream = how
		res, _, _ := f.dial("/api/session-desktop/alex/desk/websockify", "")
		if res.StatusCode != want.status || errorOf(res) != want.error {
			t.Fatal(how, res.Status)
		}
	}
}

func TestDesktopSingleUserUsesTheDeploymentKey(t *testing.T) {
	f := newDesktopFixture(t)
	f.viewer = &platform.Viewer{}
	res, _, _ := f.dial("/api/session-desktop/alex/desk/websockify", "")
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatal(res.Status)
	}
	if got := f.upstreamSeen()[0].Header.Get("Authorization"); got != "Bearer deploy-key" {
		t.Fatal(got)
	}
}

func TestDesktopNeedsAWebSocket(t *testing.T) {
	f := newDesktopFixture(t)
	var body map[string]any
	if f.get("/api/session-desktop/alex/desk/websockify", &body) != 400 {
		t.Fatal(body)
	}
}

func TestDesktopLogs(t *testing.T) {
	f := newDesktopFixture(t)
	var body map[string]any
	if f.get("/api/session-desktop/alex/desk/logs", &body) != 200 || body["xstartup"] != "started" || body["xvnc"] != "Xvnc ready" || len(body) != 2 {
		t.Fatal(body)
	}
	if up := f.upstreamSeen()[0]; up.URL.Path != "/me/session/alex/desk/logs" || up.Header.Get("Authorization") != "Bearer alex-own-key" {
		t.Fatal(up.URL.Path, up.Header)
	}
	if f.get("/api/session-desktop/sam/desk/logs", &body) != 404 {
		t.Fatal(body)
	}
	// Sam's own key reaches sam's desktop, and only with sam's key.
	ResetCache()
	f.viewer = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "sam-id"}, Cred: &platform.Cred{Key: "sam-own-key"}}
	res, _, _ := f.dial("/api/session-desktop/sam/desk/websockify", "")
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatal(res.Status)
	}
	if up := f.upstreamSeen(); up[len(up)-1].URL.Path != "/me/session/sam/desk/websockify" || up[len(up)-1].Header.Get("Authorization") != "Bearer sam-own-key" {
		t.Fatal(up[len(up)-1].Header)
	}
	f.viewer = &platform.Viewer{AuthEnabled: true}
	if f.get("/api/session-desktop/alex/desk/logs", &body) != 401 {
		t.Fatal(body)
	}
}

func TestSameOrigin(t *testing.T) {
	req := func(host, origin, fwd string) *http.Request {
		r, _ := http.NewRequest("GET", "http://"+host+"/x", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if fwd != "" {
			r.Header.Set("X-Forwarded-Host", fwd)
		}
		return r
	}
	for _, c := range []struct {
		host, origin, fwd string
		ok                bool
	}{
		{"studio.example.com", "https://studio.example.com", "", true},
		{"STUDIO.example.com", "https://studio.example.com", "", true},
		{"127.0.0.1:4080", "http://127.0.0.1:4080", "", true},
		// The window shell and a session ingress say where it was sent.
		{"127.0.0.1:4080", "http://127.0.0.1:5123", "127.0.0.1:5123", true},
		{"127.0.0.1:4080", "http://127.0.0.1:5123", "", false},
		{"studio.example.com", "https://elsewhere.example.com", "", false},
		{"studio.example.com", "", "", false},
		{"studio.example.com", "null", "", false},
		{"studio.example.com", "file://studio.example.com", "", false},
	} {
		if got := SameOrigin(req(c.host, c.origin, c.fwd)); got != c.ok {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

func TestDesktopAddress(t *testing.T) {
	u, err := DesktopAddress("https://platform.example.com", &SessionRow{User: "a.b", Name: "my desk"}, "websockify")
	if err != nil || u.String() != "https://platform.example.com/me/session/a.b/my%20desk/websockify" {
		t.Fatal(u, err)
	}
	for _, s := range []*SessionRow{{User: "a/..", Name: "d"}, {User: "a", Name: "d?x"}, {User: "", Name: "d"}, nil} {
		if _, err := DesktopAddress("https://platform.example.com", s, "websockify"); err == nil {
			t.Fatal(s)
		}
	}
	if _, err := DesktopAddress("", &SessionRow{User: "a", Name: "d"}, "logs"); err == nil {
		t.Fatal("no platform")
	}
	rows := []*SessionRow{{User: "a", Name: "d", Desktop: true}, {User: "a", Name: "e"}}
	if DesktopOf(rows, "a", "d") != rows[0] || DesktopOf(rows, "a", "e") != nil || DesktopOf(rows, "", "") != nil {
		t.Fatal("DesktopOf")
	}
}

func TestDesktopKeyNeverFallsBackForAPerson(t *testing.T) {
	f := newFixture(t)
	p := f.routes.p
	if k, err := DesktopKey(p, &platform.Viewer{}); err != nil || k != "deploy-key" {
		t.Fatal(k, err)
	}
	// An identity on the request without sign-in on is still a person.
	if _, err := DesktopKey(p, &platform.Viewer{User: &platform.User{ID: "x"}}); err == nil {
		t.Fatal("a person without a key of their own got the deployment's")
	}
	if k, _ := DesktopKey(p, &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "x"}, SignIn: "signin-token", Cred: &platform.Cred{Key: "pasted"}}); k != "signin-token" {
		t.Fatal(k)
	}
	if _, err := DesktopKey(p, nil); err == nil {
		t.Fatal("no viewer")
	}
}
