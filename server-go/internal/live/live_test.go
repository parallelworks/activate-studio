package live

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// studio is a Studio of a few routes: the person is the X-User header, and a
// read without one is refused.
type studio struct {
	mu    sync.Mutex
	body  map[string]string
	reads map[string]int
	// cookies are the Cookie header of each read.
	cookies []string
	ended   bool
	fail    bool
}

func (s *studio) set(path, body string) { s.mu.Lock(); s.body[path] = body; s.mu.Unlock() }
func (s *studio) count(path string) int { s.mu.Lock(); defer s.mu.Unlock(); return s.reads[path] }

func (s *studio) read(w http.ResponseWriter, r *http.Request) *http.Request {
	if auth.UserOf(r) != nil {
		panic("a read carries a person the gate did not find")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := r.Header.Get("X-User")
	if u == "" || s.ended {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"sign-in required"}`))
		return nil
	}
	r = auth.WithUser(r, &auth.User{ID: u})
	p := r.URL.Path
	s.reads[r.URL.RequestURI()]++
	s.cookies = append(s.cookies, r.Header.Get("Cookie"))
	for _, h := range []string{"Upgrade", "Sec-Websocket-Key", "Connection"} {
		if r.Header.Get(h) != "" {
			panic("a read carries the handshake's " + h)
		}
	}
	if p == "/api/runs" {
		panic(http.ErrAbortHandler)
	}
	if p == "/api/sessions" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"the platform refused the key"}`))
		return r
	}
	if s.fail {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"no answer"}`))
		return r
	}
	b := s.body[p]
	if strings.Contains(b, "$user") {
		b = strings.ReplaceAll(b, "$user", u)
	}
	_, _ = w.Write([]byte(b))
	return r
}

func start(t *testing.T) (*studio, *Service, string) {
	t.Helper()
	st := &studio{body: map[string]string{"/api/fleet": `{"agents":[]}`, "/api/whoami": `{"user":"$user"}`, "/api/code/sessions": `{"sessions":[]}`}, reads: map[string]int{}}
	s := New(st.read)
	s.listEvery = 30 * time.Millisecond
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The gate in front of the socket itself.
		u := r.Header.Get("X-User")
		if u == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, auth.WithUser(r, &auth.User{ID: u}))
	}))
	t.Cleanup(srv.Close)
	return st, s, srv.URL
}

func dial(t *testing.T, base, path, user, origin string) (*websocket.Conn, error) {
	t.Helper()
	if origin == "" {
		origin = base
	}
	cfg, err := websocket.NewConfig(strings.Replace(base, "http", "ws", 1)+path, origin)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header.Set("X-User", user)
	cfg.Header.Set("Cookie", "k="+user)
	ws, err := websocket.DialConfig(cfg)
	if err == nil {
		t.Cleanup(func() { ws.Close() })
	}
	return ws, err
}

func next(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m map[string]any
	if err := websocket.JSON.Receive(ws, &m); err != nil {
		t.Fatalf("receive: %v", err)
	}
	return m
}

func closed(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var s string
		if err := websocket.Message.Receive(ws, &s); err != nil {
			if strings.Contains(err.Error(), "timeout") {
				t.Fatal("the socket stays open")
			}
			return
		}
	}
}

func say(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	if err := websocket.JSON.Send(ws, v); err != nil {
		t.Fatal(err)
	}
}

// want is the message that follows paths, each [every, first] in ms.
func want(m map[string][2]int64) map[string]any { return map[string]any{"want": m} }

func TestAPageIsSentWhatItShowsAsItChanges(t *testing.T) {
	st, _, base := start(t)
	ws, err := dial(t, base, "/api/live", "ada", "")
	if err != nil {
		t.Fatal(err)
	}
	say(t, ws, want(map[string][2]int64{"/api/fleet": {1000, 0}}))
	if f := next(t, ws); f["path"] != "/api/fleet" || f["status"] != 200.0 || len(f["body"].(map[string]any)["agents"].([]any)) != 0 {
		t.Fatalf("first: %v", f)
	}
	// A read that says the same goes as its path alone.
	if f := next(t, ws); f["path"] != nil || f["body"] != nil || f["same"].([]any)[0] != "/api/fleet" {
		t.Fatalf("second: %v", f)
	}
	st.set("/api/fleet", `{"agents":[1]}`)
	for {
		f := next(t, ws)
		if f["same"] != nil {
			continue
		}
		if len(f["body"].(map[string]any)["agents"].([]any)) != 1 {
			t.Fatalf("changed: %v", f)
		}
		break
	}
	// One the page asks for now, unchanged, is answered with its number.
	say(t, ws, map[string]any{"read": "/api/fleet", "n": 7})
	for {
		f := next(t, ws)
		if f["same"] != nil {
			continue
		}
		if n := f["n"].([]any); len(n) != 1 || n[0] != 7.0 || f["body"] == nil {
			t.Fatalf("asked: %v", f)
		}
		break
	}
	// No longer shown, no longer read.
	say(t, ws, want(map[string][2]int64{}))
	time.Sleep(50 * time.Millisecond)
	n := st.count("/api/fleet")
	time.Sleep(1200 * time.Millisecond)
	if st.count("/api/fleet") != n {
		t.Fatal("read after the page stopped showing it")
	}
}

func TestAPathThePageHasReadIsReadWhenNextDue(t *testing.T) {
	st, _, base := start(t)
	ws, err := dial(t, base, "/api/live", "ada", "")
	if err != nil {
		t.Fatal(err)
	}
	say(t, ws, want(map[string][2]int64{"/api/fleet": {60000, 400}}))
	time.Sleep(150 * time.Millisecond)
	if st.count("/api/fleet") != 0 {
		t.Fatal("read before it was due")
	}
	if f := next(t, ws); f["path"] != "/api/fleet" {
		t.Fatalf("%v", f)
	}
	// Paced faster, it is due from its last read.
	say(t, ws, want(map[string][2]int64{"/api/fleet": {1000, 0}}))
	if f := next(t, ws); f["same"] == nil {
		t.Fatalf("%v", f)
	}
}

func TestOnlyWhatPagesFollowIsRead(t *testing.T) {
	st, _, base := start(t)
	ws, err := dial(t, base, "/api/live", "ada", "")
	if err != nil {
		t.Fatal(err)
	}
	say(t, ws, want(map[string][2]int64{"/api/settings": {1000, 0}, "http://elsewhere.example/api/fleet": {1000, 0}, "//elsewhere.example/api/fleet": {1000, 0},
		"/api/fleet/../settings": {1000, 0}, "/api/%66leet": {1000, 0}, "/api/whoami": {1000, 0}}))
	if f := next(t, ws); f["path"] != "/api/whoami" || f["body"].(map[string]any)["user"] != "ada" {
		t.Fatalf("%v", f)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.reads) != 1 {
		t.Fatalf("read: %v", st.reads)
	}
}

func TestASocketIsThePersonsAndEndsWithTheirSignIn(t *testing.T) {
	st, s, base := start(t)
	if _, err := dial(t, base, "/api/live", "ada", "https://elsewhere.example"); err == nil {
		t.Fatal("opened from another site's page")
	}
	if _, err := dial(t, base, "/api/live", "", ""); err == nil {
		t.Fatal("opened without signing in")
	}
	ws, err := dial(t, base, "/api/live", "ada", "")
	if err != nil {
		t.Fatal(err)
	}
	say(t, ws, want(map[string][2]int64{"/api/whoami": {1000, 0}}))
	next(t, ws)
	st.mu.Lock()
	if st.cookies[0] != "k=ada" {
		t.Errorf("cookies %v", st.cookies)
	}
	st.mu.Unlock()
	// Another person's cookies changed, then theirs.
	s.Drop("bob")
	next(t, ws)
	s.Drop("ada")
	closed(t, ws)
	// A sign-in that ends is found at the next read, and nothing is sent.
	if ws, err = dial(t, base, "/api/live", "ada", ""); err != nil {
		t.Fatal(err)
	}
	say(t, ws, want(map[string][2]int64{"/api/whoami": {1000, 0}}))
	next(t, ws)
	st.mu.Lock()
	st.ended = true
	st.mu.Unlock()
	closed(t, ws)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.conns)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d people still have sockets", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestASocketEndsAfterItsLife(t *testing.T) {
	_, s, base := start(t)
	s.life, s.beat = 200*time.Millisecond, 40*time.Millisecond
	ws, err := dial(t, base, "/api/live", "ada", "")
	if err != nil {
		t.Fatal(err)
	}
	// It says it is there meanwhile.
	if f := next(t, ws); f["beat"] != true {
		t.Fatalf("%v", f)
	}
	closed(t, ws)
}

// What a route answers is for the page, a refusal of its own too; only the
// sign-in's end ends the socket.
func TestARoutesOwnRefusalIsAnAnswer(t *testing.T) {
	_, _, base := start(t)
	ws, err := dial(t, base, "/api/live", "ada", "")
	if err != nil {
		t.Fatal(err)
	}
	// The second ends before it says who it was for, with nothing.
	for p, status := range map[string]float64{"/api/sessions": 401, "/api/runs": 502} {
		say(t, ws, want(map[string][2]int64{p: {60000, 0}}))
		if f := next(t, ws); f["path"] != p || f["status"] != status {
			t.Fatalf("%v", f)
		}
	}
}

func TestOneRequestReadsSeveral(t *testing.T) {
	st, _, base := start(t)
	st.set("/api/version", `not json`)
	get := func(q string) (int, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/api/live/read?"+q, nil)
		req.Header.Set("X-User", "ada")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var b strings.Builder
		_, _ = jsonCopy(&b, res)
		return res.StatusCode, b.String()
	}
	code, out := get("p=" + url.QueryEscape("/api/fleet") + "&p=" + url.QueryEscape("/api/transfers?state=active") + "&p=" + url.QueryEscape("/api/version") + "&p=" + url.QueryEscape("/api/whoami"))
	st.set("/api/transfers", `{}`)
	if code != 200 || !strings.Contains(out, `{"path":"/api/fleet","status":200,"body":{"agents":[]}}`) || !strings.Contains(out, `"body":"not json"`) || !strings.Contains(out, `"user":"ada"`) {
		t.Fatalf("%d %s", code, out)
	}
	if st.count("/api/transfers?state=active") != 1 {
		t.Fatalf("%v", st.reads)
	}
	for _, q := range []string{"", "p=" + url.QueryEscape("/api/settings"), "p=" + url.QueryEscape("/api/fleet") + "&p=/etc", strings.Repeat("p=/api/fleet&", maxPaths+1)} {
		if code, out := get(q); code != 400 {
			t.Errorf("%.60s: %d %s", q, code, out)
		}
	}
}

func jsonCopy(b *strings.Builder, res *http.Response) (int64, error) {
	var v json.RawMessage
	err := json.NewDecoder(res.Body).Decode(&v)
	b.Write(v)
	return int64(len(v)), err
}

// The page's list of sessions comes over its feed: at once, as it changes,
// with the session the page has open, and it ends where it cannot be read.
func TestAListingIsSentAsItChanges(t *testing.T) {
	st, _, base := start(t)
	st.mu.Lock()
	st.fail = true
	st.mu.Unlock()
	if _, err := dial(t, base, "/api/pwcode-feed/sessions?heartbeat=10", "ada", "https://elsewhere.example"); err == nil {
		t.Fatal("opened from another site's page")
	}
	ws, err := dial(t, base, "/api/pwcode-feed/sessions?heartbeat=1", "ada", "")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing while it cannot be read, so the page polls and shows why.
	if f := next(t, ws); f["type"] != "heartbeat" {
		t.Fatalf("%v", f)
	}
	st.mu.Lock()
	st.fail = false
	st.mu.Unlock()
	list := func() map[string]any {
		for {
			if f := next(t, ws); f["type"] == "sessions" {
				return f["listing"].(map[string]any)
			}
		}
	}
	if n := len(list()["sessions"].([]any)); n != 0 {
		t.Fatalf("first: %d", n)
	}
	st.set("/api/code/sessions", `{"sessions":[{"id":"s1"}]}`)
	if n := len(list()["sessions"].([]any)); n != 1 {
		t.Fatalf("changed: %d", n)
	}
	// Another session opens in the page: the listing with it goes at once.
	say(t, ws, map[string]any{"open": "m1/s9"})
	list()
	if st.count("/api/code/sessions?open="+url.QueryEscape("m1/s9")) == 0 {
		t.Fatalf("%v", st.reads)
	}
	st.mu.Lock()
	st.fail = true
	st.mu.Unlock()
	closed(t, ws)
	for _, q := range []string{"0", "61", "x"} {
		if _, err := dial(t, base, "/api/pwcode-feed/sessions?heartbeat="+q, "ada", ""); err == nil {
			t.Errorf("heartbeat %s", q)
		}
	}
}
