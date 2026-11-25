package chat

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// relayedChat continues s1 on m2, other-box, through the relay.
func relayedChat(t *testing.T, base string) string {
	t.Helper()
	_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	if id == "" {
		t.Fatalf("continue: %v", out)
	}
	return id
}

func TestTheCodeRoutesReachOnlyTheViewersOwnChatsSessions(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, _, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	s := base + "/api/code/machines/m2/sessions/s1"

	if code, out, _ := send(t, http.MethodGet, s, ""); code != 200 || out["id"] != "s1" {
		t.Errorf("detail: %d %v", code, out)
	}
	// Another session on the machine, or the session under another machine:
	// no chat of the viewer's holds it.
	for _, u := range []string{base + "/api/code/machines/m2/sessions/s7", base + "/api/code/machines/m1/sessions/s1", base + "/api/code/machines/studio/sessions/s1"} {
		if code, out, h := send(t, http.MethodGet, u, ""); code != 404 || h.Get("Content-Type") != "application/problem+json" || out["detail"] == nil {
			t.Errorf("%s: %d %v %s", u, code, out, h.Get("Content-Type"))
		}
	}
	// What the view does not use is refused without asking the session.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/archive"}, {http.MethodDelete, ""}, {http.MethodPost, "/shell"}, {http.MethodPost, "/clear"},
		{http.MethodPost, "/fork"}, {http.MethodPost, "/attach"}, {http.MethodGet, "/mcp"}, {http.MethodPut, "/settings"}, {http.MethodPost, "/goal"},
	} {
		code, _, _ := send(t, c.method, s+c.path, `{}`)
		if code != 404 && code != 405 {
			t.Errorf("%s %s: %d", c.method, c.path, code)
		}
	}
	if remote.archived != 0 {
		t.Errorf("archived %d", remote.archived)
	}
	// Only the model, effort and permission mode change, from here.
	if code, out, _ := send(t, http.MethodPatch, s+"/settings", `{"name":"x"}`); code != 400 || !strings.Contains(out["detail"].(string), "name") {
		t.Errorf("name: %d %v", code, out)
	}
	if code, _, _ := send(t, http.MethodPatch, s+"/settings", `{"permissionMode":"yolo"}`); code != 400 {
		t.Errorf("unknown mode: %d", code)
	}
	// An allocation comes only with the model it bills.
	if code, _, _ := send(t, http.MethodPatch, s+"/settings", `{"allocation":"a1"}`); code != 400 {
		t.Errorf("allocation alone: %d", code)
	}
	if len(remote.settings) != 0 {
		t.Errorf("settings reached the session: %v", remote.settings)
	}
	// The session view's model picker sends the model with its allocation.
	for _, b := range []string{`{"model":"host/default","allocation":""}`, `{"model":"me:openai/gpt-5","allocation":"a1"}`} {
		if code, out, _ := send(t, http.MethodPatch, s+"/settings", b); code != 200 {
			t.Errorf("%s: %d %v", b, code, out)
		}
	}
	if len(remote.settings) != 2 {
		t.Errorf("model picks sent: %v", remote.settings)
	}
}

func TestARemoteModeChangeStaysUnderTheMachinesCeiling(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, plat, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	s := base + "/api/code/machines/m2/sessions/s1/settings"
	// Without a ceiling of its own, a machine takes at most accept-edits
	// from ACTIVATE.
	for _, mode := range []string{"auto", "bypass-permissions"} {
		if code, out, _ := send(t, http.MethodPatch, s, `{"permissionMode":"`+mode+`"}`); code != 403 || !strings.Contains(out["detail"].(string), "takes at most accept-edits") {
			t.Errorf("%s: %d %v", mode, code, out)
		}
	}
	for _, mode := range []string{"read-only", "accept-edits"} {
		if code, out, _ := send(t, http.MethodPatch, s, `{"permissionMode":"`+mode+`"}`); code != 200 {
			t.Errorf("%s: %d %v", mode, code, out)
		}
	}
	// A machine set to take auto takes it, and no more.
	plat.mu.Lock()
	plat.others[0]["remoteMaxPermissionMode"] = "auto"
	plat.mu.Unlock()
	if code, out, _ := send(t, http.MethodPatch, s, `{"permissionMode":"auto"}`); code != 200 {
		t.Errorf("auto under auto: %d %v", code, out)
	}
	if code, _, _ := send(t, http.MethodPatch, s, `{"permissionMode":"bypass-permissions"}`); code != 403 {
		t.Errorf("bypass under auto: %d", code)
	}
	want := `{"permissionMode":"read-only"}|{"permissionMode":"accept-edits"}|{"permissionMode":"auto"}`
	if got := strings.Join(remote.settings, "|"); got != want {
		t.Errorf("settings: %s, want %s", got, want)
	}
}

func TestOnlyTheOwnerChangesAChatsSession(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, _, _, _ := relayModule(t, local, remote)
	title := "Tune the solver"
	m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: pwcode.RelaySite("m2"), Relay: true, Session: "s1",
		Continued: true, Machine: "m2", MachineName: "other-box", CreatedAt: m.Conv.Now()})
	m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: "local", Session: "s9", CreatedAt: m.Conv.Now()})
	m.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	m.A.Cfg.FrontAuth = true
	m.Convs.SharedHistory = func() bool { return true }
	m.Convs.AuthEnabled = func() bool { return true }
	as := func(who, method, machine, id, rest, body string) int {
		path := "/api/code/machines/" + machine + "/sessions/" + id
		if rest != "" {
			path += "/" + rest
		}
		r := auth.WithUser(httptest.NewRequest(method, path, strings.NewReader(body)), &auth.User{ID: who, Username: who})
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("machine", machine)
		r.SetPathValue("id", id)
		r.SetPathValue("rest", rest)
		rec := httptest.NewRecorder()
		if err := m.codeProxy(rec, r); err != nil {
			t.Fatal(err)
		}
		return rec.Code
	}
	// Bob, on a Studio that shares chats: ada's sessions are not his to
	// read or drive, relayed or on this machine, and a mode change is never
	// made as the Studio's account.
	for _, c := range []struct{ machine, id string }{{"m2", "s1"}, {"studio", "s9"}} {
		for _, req := range [][3]string{{"GET", "", ""}, {"PATCH", "settings", `{"permissionMode":"accept-edits"}`}, {"POST", "messages", `{"text":"rm -rf results"}`}, {"POST", "interrupt", `{}`}} {
			if code := as("bob", req[0], c.machine, c.id, req[1], req[2]); code != 404 && code != 403 {
				t.Errorf("bob %s %s/%s %s: %d", req[0], c.machine, c.id, req[1], code)
			}
		}
	}
	if len(remote.settings)+len(local.settings)+len(remote.messages)+len(local.messages)+remote.interrupts+local.interrupts != 0 {
		t.Errorf("bob reached ada's sessions")
	}
}

func TestTheSessionFeedOpensOnlyForTheStudiosOwnPages(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, _, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	for _, u := range []string{"/api/pwcode-feed/machines/m2/sessions/s1/events?after=0"} {
		req, _ := http.NewRequest(http.MethodGet, base+u, nil)
		req.Header.Set("Origin", "https://elsewhere.example")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Errorf("%s from another origin: %d %v", u, res.StatusCode, out)
		}
	}
}

// logTo sends the module's log to a buffer the test reads.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func logTo(m *Module) *lockedBuf {
	buf := &lockedBuf{}
	m.A.Log = slog.New(slog.NewTextHandler(buf, nil))
	return buf
}

// A plan's answer that names a mode is a mode change: held to the
// machine's ceiling for a relayed session, and logged on either path.
func TestAPlanAnswersModeIsHeldToTheCeilingAndLogged(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, base, _, _ := relayModule(t, local, remote)
	log := logTo(m)
	relayedChat(t, base)
	s := base + "/api/code/machines/m2/sessions/s1/approvals/"
	if code, out, _ := send(t, http.MethodPost, s+"ap1", `{"allowed":true,"mode":"auto"}`); code != 403 || !strings.Contains(out["detail"].(string), "takes at most accept-edits") {
		t.Errorf("auto: %d %v", code, out)
	}
	if code, _, _ := send(t, http.MethodPost, s+"ap2", `{"allowed":true,"mode":"yolo"}`); code != 400 {
		t.Errorf("unknown mode: %d", code)
	}
	if len(remote.answers) != 0 {
		t.Errorf("a refused answer reached the session: %v", remote.answers)
	}
	if code, out, _ := send(t, http.MethodPost, s+"ap3", `{"allowed":true,"mode":"accept-edits"}`); code != 200 && code != 204 {
		t.Errorf("accept-edits: %d %v", code, out)
	}
	if l := log.String(); !strings.Contains(l, "permission mode changed") || !strings.Contains(l, "mode=accept-edits") || !strings.Contains(l, `via="plan answer"`) || !strings.Contains(l, "relay=true") {
		t.Errorf("not logged: %s", l)
	}
	// An answer without a mode is not a mode change.
	before := strings.Count(log.String(), "permission mode changed")
	send(t, http.MethodPost, s+"ap4", `{"allowed":true}`)
	if strings.Count(log.String(), "permission mode changed") != before {
		t.Errorf("an answer without a mode was logged as a change")
	}
}

// A session the Studio drives directly runs on the Studio's own machine,
// as its terminal does: no ceiling from ACTIVATE, and every mode change is
// logged, by settings or by a plan's answer on either route.
func TestTheDirectPathHasNoCeilingAndLogsEveryModeChange(t *testing.T) {
	f := &fakePW{}
	m, base, _ := continueModule(t, f, "d1")
	log := logTo(m)
	_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`)
	id, _ := out["conversationId"].(string)
	if id == "" {
		t.Fatalf("continue: %v", out)
	}
	s := base + "/api/code/machines/studio/sessions/s1"
	if code, out, _ := send(t, http.MethodPatch, s+"/settings", `{"permissionMode":"bypass-permissions"}`); code != 200 {
		t.Errorf("settings: %d %v", code, out)
	}
	if code, out, _ := send(t, http.MethodPost, s+"/approvals/ap1", `{"allowed":true,"mode":"bypass-permissions"}`); code != 200 && code != 204 {
		t.Errorf("plan answer: %d %v", code, out)
	}
	if code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap2", `{"kind":"plan","allowed":true}`); code != 200 {
		t.Errorf("chat plan answer: %d %v", code, out)
	}
	l := log.String()
	for _, want := range []string{
		`mode=bypass-permissions via=settings`,
		`mode=bypass-permissions via="plan answer"`,
		`mode=auto via="chat plan answer"`,
	} {
		if !strings.Contains(l, want) {
			t.Errorf("not logged: %s\n%s", want, l)
		}
	}
	if n := strings.Count(l, "permission mode changed"); n != 3 || strings.Count(l, "relay=false") < 3 || !strings.Contains(l, "conversation="+id) || !strings.Contains(l, "session=s1") {
		t.Errorf("each change once, with its chat and session: %d\n%s", n, l)
	}
	// A confirm's answer is not a mode change.
	send(t, http.MethodPost, base+"/api/chat/pwcode/"+id+"/approvals/ap3", `{"allowed":true}`)
	if n := strings.Count(log.String(), "permission mode changed"); n != 3 {
		t.Errorf("a confirm was logged as a mode change: %d", n)
	}
}

// An approval's or queued turn's id is one plain segment: no dot segment,
// encoded or not, no ';', space or '%', so nothing in front of the session
// can turn it into another route.
func TestARouteIDIsOnePlainSegment(t *testing.T) {
	for _, id := range []string{"..", ".", "%2e%2e", "%252e%252e", ".hidden", "a;x=1", "a b", "a%20b", "", strings.Repeat("a", 129)} {
		for _, prefix := range []string{"/approvals/", "/queued/"} {
			method := http.MethodPost
			if prefix == "/queued/" {
				method = http.MethodDelete
			}
			if k := pwcode.ProxyRoute(method, prefix+id); k != "" {
				t.Errorf("%s %s%s: %q", method, prefix, id, k)
			}
		}
	}
	for _, id := range []string{"ap1", "toolu_01ABC", "t-1.2", "a:b"} {
		if pwcode.ProxyRoute(http.MethodPost, "/approvals/"+id) == "" || pwcode.ProxyRoute(http.MethodDelete, "/queued/"+id) == "" {
			t.Errorf("%s refused", id)
		}
	}
	local, remote := &fakePW{}, &fakePW{}
	_, base, plat, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	plat.mu.Lock()
	plat.relayed = nil
	plat.mu.Unlock()
	for _, u := range []string{"/queued/%2e%2e", "/queued/%252e%252e", "/approvals/%2e%2e", "/queued/a%3Bx=1"} {
		method := http.MethodDelete
		if strings.HasPrefix(u, "/approvals/") {
			method = http.MethodPost
		}
		if code, _, _ := send(t, method, base+"/api/code/machines/m2/sessions/s1"+u, `{}`); code != 404 {
			t.Errorf("%s %s: %d", method, u, code)
		}
	}
	plat.mu.Lock()
	defer plat.mu.Unlock()
	if len(plat.relayed) != 0 {
		t.Errorf("sent on: %v", plat.relayed)
	}
}

// The ceiling check fails closed: a ceiling the Studio does not know, or a
// listing it cannot read, allows read-only, and the refusal says why.
func TestTheCeilingCheckFailsClosed(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, plat, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	s := base + "/api/code/machines/m2/sessions/s1/settings"
	plat.mu.Lock()
	plat.others[0]["remoteMaxPermissionMode"] = "plan"
	plat.mu.Unlock()
	if code, out, _ := send(t, http.MethodPatch, s, `{"permissionMode":"accept-edits"}`); code != 403 || !strings.Contains(out["detail"].(string), "does not know, plan") {
		t.Errorf("unknown ceiling: %d %v", code, out)
	}
	if code, _, _ := send(t, http.MethodPatch, s, `{"permissionMode":"read-only"}`); code != 200 {
		t.Errorf("read-only under an unknown ceiling: %d", code)
	}
	plat.mu.Lock()
	delete(plat.others[0], "remoteMaxPermissionMode")
	plat.listDown = true
	plat.mu.Unlock()
	if code, out, _ := send(t, http.MethodPatch, s, `{"permissionMode":"accept-edits"}`); code != 403 || !strings.Contains(out["detail"].(string), "could not read the machine's limit") {
		t.Errorf("listing down: %d %v", code, out)
	}
	if got := strings.Join(remote.settings, "|"); got != `{"permissionMode":"read-only"}` {
		t.Errorf("settings sent: %s", got)
	}
}

func serveAs(typ, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", typ)
		_, _ = w.Write([]byte(body))
	}
}

// The session view's images pass through as the machine serves them, as
// pictures only: anything else, such as a page or an SVG, which could run
// script from the Studio's origin, is refused.
func TestASessionsImagesPassAsPicturesOnly(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, _, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	remote.routes = map[string]http.HandlerFunc{
		"GET /v1/sessions/s1/images/img-1":        serveAs("image/png", "PNGDATA"),
		"GET /v1/sessions/s1/images/page":         serveAs("text/html", "<script>x</script>"),
		"GET /v1/sessions/s1/images/svg":          serveAs("image/svg+xml", "<svg onload=x>"),
		"GET /v1/sessions/s1/github-images/0a1b2": serveAs("image/jpeg; charset=binary", "JPG"),
	}
	s := base + "/api/code/machines/m2/sessions/s1"
	res, err := http.Get(s + "/images/img-1?preview=true")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "PNGDATA" || res.Header.Get("Content-Type") != "image/png" || res.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("image: %d %q %v", res.StatusCode, body, res.Header)
	}
	if res, _ := http.Get(s + "/github-images/0a1b2"); res == nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("github image: %v", res)
	}
	for _, ref := range []string{"page", "svg"} {
		res, _ := http.Get(s + "/images/" + ref)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 502 || strings.Contains(string(b), "<") {
			t.Errorf("%s: %d %s", ref, res.StatusCode, b)
		}
	}
	for _, ref := range []string{"%2e%2e", "a%2Fb", "%252e%252e"} {
		if res, _ := http.Get(s + "/images/" + ref); res == nil || res.StatusCode != 404 {
			t.Errorf("%s: %v", ref, res)
		}
	}
	remote.mu.Lock()
	asked := remote.asked
	remote.mu.Unlock()
	if len(asked) == 0 || asked[0] != "GET /v1/sessions/s1/images/img-1?preview=true" {
		t.Errorf("asked: %v", asked)
	}
	// Another person's chat is not found here either.
	m2 := strings.Replace(s, "/m2/", "/m9/", 1)
	if res, _ := http.Get(m2 + "/images/img-1"); res == nil || res.StatusCode != 404 {
		t.Errorf("another machine: %v", res)
	}
}

// What the session view sends beyond talking to the session reaches it on
// both paths: its goal, commands, background, recap, subagents and images.
func TestTheSessionViewsRoutesReachTheSession(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, _, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	ok := serveAs("application/json", `{"ok":true}`)
	routes := []string{
		"POST /goal", "POST /clear", "POST /compact", "POST /plan", "POST /shell", "POST /commands",
		"POST /background", "POST /recap", "POST /images", "GET /worktree", "GET /pull-requests",
		"GET /claude-code/accounts", "GET /subagents/a1", "POST /subagents/a1/messages",
		"POST /subagents/a1/direction", "POST /subagents/a1/direction/withdraw", "POST /subagents/a1/stop",
		"POST /subagents/a1/dismiss", "POST /subagents/stop-all",
	}
	remote.routes = map[string]http.HandlerFunc{}
	for _, r := range routes {
		method, suffix, _ := strings.Cut(r, " ")
		remote.routes[method+" /v1/sessions/s1"+suffix] = ok
	}
	s := base + "/api/code/machines/m2/sessions/s1"
	for _, r := range routes {
		method, suffix, _ := strings.Cut(r, " ")
		body := ""
		if method == http.MethodPost {
			body = `{}`
		}
		if code, out, _ := send(t, method, s+suffix, body); code != 200 || out["ok"] != true {
			t.Errorf("%s: %d %v", r, code, out)
		}
	}
	// Managing the session is the Code page's, not the chat's. Each would be
	// answered if it got through, so a leak counts below.
	refused := []string{"POST /rename", "POST /archive", "POST /pin", "PUT /group", "POST /fork", "POST /worktree/leave", "DELETE ", "POST /subagents/a1/x"}
	for _, r := range refused {
		method, suffix, _ := strings.Cut(r, " ")
		remote.routes[method+" /v1/sessions/s1"+suffix] = ok
	}
	for _, r := range refused {
		method, suffix, _ := strings.Cut(r, " ")
		if code, _, _ := send(t, method, s+suffix, `{}`); code != 404 && code != 405 {
			t.Errorf("%s: %d", r, code)
		}
	}
	remote.mu.Lock()
	n := len(remote.asked)
	remote.mu.Unlock()
	if n != len(routes) {
		t.Errorf("the session was asked %d times, want %d", n, len(routes))
	}
}

// A chat follows its session into the one /clear made, as PW Code on the
// web does, when the chat's own target reaches it in the same folder.
func TestAChatFollowsItsSessionCleared(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, base, _, _ := relayModule(t, local, remote)
	log := logTo(m)
	id := relayedChat(t, base)
	remote.detail = map[string]any{"workspace": "/home/me/repo"}
	remote.routes = map[string]http.HandlerFunc{
		"GET /v1/sessions/s2": serveAs("application/json", `{"id":"s2","workspace":"/home/me/repo","status":"idle"}`),
		"GET /v1/sessions/s3": serveAs("application/json", `{"id":"s3","workspace":"/home/me/other","status":"idle"}`),
	}
	follow := base + "/api/chat/pwcode/" + id + "/follow"
	if code, out, _ := send(t, http.MethodPost, follow, `{"session":"s3"}`); code != 409 {
		t.Errorf("another folder: %d %v", code, out)
	}
	for _, bad := range []string{`{"session":"../s2"}`, `{"session":""}`, `{}`} {
		if code, _, _ := send(t, http.MethodPost, follow, bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, out, _ := send(t, http.MethodPost, follow, `{"session":"s2"}`); code != 200 || out["session"] != "s2" {
		t.Errorf("follow: %d %v", code, out)
	}
	if c := m.Conv.Get(id); c == nil || c.PWCode == nil || c.PWCode.Session != "s2" {
		t.Errorf("the chat's session: %+v", c)
	}
	if !strings.Contains(log.String(), "followed a cleared session") || !strings.Contains(log.String(), "to=s2") {
		t.Errorf("not logged: %s", log.String())
	}
}

// A session the Studio drives directly serves its images the same way.
func TestADirectSessionsImagesPass(t *testing.T) {
	f := &fakePW{}
	_, base, _ := continueModule(t, f, "d1")
	if _, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`); out["conversationId"] == nil {
		t.Fatalf("continue: %v", out)
	}
	f.routes = map[string]http.HandlerFunc{"GET /v1/sessions/s1/images/r1": serveAs("image/webp", "WEBP")}
	res, err := http.Get(base + "/api/code/machines/studio/sessions/s1/images/r1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(b) != "WEBP" || res.Header.Get("Content-Type") != "image/webp" {
		t.Errorf("%d %q %v", res.StatusCode, b, res.Header)
	}
}

// The account panel reaches the viewer's own pw code on a machine: the one
// the Studio drives for them, or one of theirs through ACTIVATE as them.
// Only the account routes pass, and a change is logged.
func TestTheAccountRoutesReachTheViewersOwnPWCode(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, base, plat, _ := relayModule(t, local, remote)
	log := logTo(m)
	list := serveAs("application/json", `[{"name":"default"}]`)
	ok := serveAs("application/json", `{"ok":true}`)
	for _, f := range []*fakePW{local, remote} {
		f.routes = map[string]http.HandlerFunc{
			"GET /v1/claude-code/accounts":            list,
			"GET /v1/claude-code/models":              list,
			"POST /v1/claude-code/logins":             serveAs("application/json", `{"id":"l1"}`),
			"GET /v1/claude-code/logins/l1":           ok,
			"POST /v1/claude-code/logins/l1/input":    ok,
			"DELETE /v1/claude-code/logins/l1":        ok,
			"DELETE /v1/claude-code/accounts/work":    ok,
			"POST /v1/claude-code/accounts/work/move": ok,
			// Answered if they got through, so a leak counts below.
			"POST /v1/claude-code/accounts":    ok,
			"GET /v1/claude-code/settings":     ok,
			"POST /v1/claude-code/logins/l1/x": ok,
		}
	}
	for machine, f := range map[string]*fakePW{"studio": local, "m2": remote} {
		u := base + "/api/code/machines/" + machine + "/claude-code"
		res, err := http.Get(u + "/accounts?namesOnly=true")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(b), "default") {
			t.Errorf("%s accounts: %d %s", machine, res.StatusCode, b)
		}
		for _, r := range []string{"GET /models", "POST /logins", "GET /logins/l1", "POST /logins/l1/input", "DELETE /logins/l1", "DELETE /accounts/work", "POST /accounts/work/move"} {
			method, suffix, _ := strings.Cut(r, " ")
			body := ""
			if method == http.MethodPost {
				body = `{}`
			}
			if code, _, _ := send(t, method, u+suffix, body); code != 200 {
				t.Errorf("%s %s: %d", machine, r, code)
			}
		}
		for _, r := range []string{"POST /accounts", "PUT /accounts/work", "GET /accounts/..", "DELETE /accounts/a%2Fb", "GET /settings", "POST /logins/l1/x"} {
			method, suffix, _ := strings.Cut(r, " ")
			if code, _, _ := send(t, method, u+suffix, `{}`); code != 404 && code != 405 {
				t.Errorf("%s %s: %d", machine, r, code)
			}
		}
		f.mu.Lock()
		n := len(f.asked)
		f.mu.Unlock()
		if n != 8 {
			t.Errorf("%s was asked %d times, want 8", machine, n)
		}
	}
	plat.mu.Lock()
	relayed := strings.Join(plat.relayed, "\n")
	plat.mu.Unlock()
	if !strings.Contains(relayed, "GET /api/code/machines/m2/claude-code/accounts Bearer k") {
		t.Errorf("not relayed as the viewer: %s", relayed)
	}
	if n := strings.Count(log.String(), "Claude Code account change"); n != 6 {
		t.Errorf("account changes logged: %d\n%s", n, log.String())
	}
	// A machine id is one plain segment, refused before ACTIVATE is asked.
	plat.mu.Lock()
	before := len(plat.relayed)
	plat.mu.Unlock()
	for _, odd := range []string{"a%2Fb", "a%20b", "a;b", "%2e%2e"} {
		if code, _, _ := send(t, http.MethodGet, base+"/api/code/machines/"+odd+"/claude-code/accounts", ""); code != 404 {
			t.Errorf("odd machine %s: %d", odd, code)
		}
	}
	plat.mu.Lock()
	after := len(plat.relayed)
	plat.mu.Unlock()
	if after != before {
		t.Errorf("an odd machine id reached ACTIVATE: %d requests", after-before)
	}
}

// A slash command keeps to the chat's rules: /permissions with a mode is a
// mode change, held to the machine's ceiling and logged; one that manages
// the session is refused before it is sent.
func TestASlashCommandKeepsToTheChatsRules(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, base, _, _ := relayModule(t, local, remote)
	log := logTo(m)
	relayedChat(t, base)
	remote.routes = map[string]http.HandlerFunc{"POST /v1/sessions/s1/commands": serveAs("application/json", `{"output":"ok"}`)}
	s := base + "/api/code/machines/m2/sessions/s1/commands"
	for body, want := range map[string]int{
		`{"name":"permissions","args":"bypass-permissions"}`: 403,
		`{"name":"permissions","args":"yolo"}`:               400,
		`{"name":"permissions","args":"set auto now"}`:       400,
		`{"name":"rename","args":"x"}`:                       403,
		`{"name":"fork"}`:                                    403,
		`{"name":"archive"}`:                                 403,
		`{"name":"resume","args":"s9"}`:                      403,
		`{"name":"remote-control","args":"off"}`:             403,
	} {
		if code, out, _ := send(t, http.MethodPost, s, body); code != want {
			t.Errorf("%s: %d %v, want %d", body, code, out, want)
		}
	}
	remote.mu.Lock()
	n := len(remote.asked)
	remote.mu.Unlock()
	if n != 0 {
		t.Errorf("a refused command reached the session %d times", n)
	}
	for _, body := range []string{`{"name":"permissions","args":"read-only"}`, `{"name":"permissions"}`, `{"name":"cost"}`, `{"name":"model","args":"x"}`} {
		if code, out, _ := send(t, http.MethodPost, s, body); code != 200 {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
	remote.routes["POST /v1/sessions/s1/plan"] = serveAs("application/json", `{"ok":true}`)
	if code, _, _ := send(t, http.MethodPost, base+"/api/code/machines/m2/sessions/s1/plan", `{}`); code != 200 {
		t.Errorf("plan: %d", code)
	}
	if l := log.String(); strings.Count(l, "permission mode changed") != 2 || !strings.Contains(l, "mode=read-only via=command") || !strings.Contains(l, "mode=plan via=command") {
		t.Errorf("the mode change by command is logged once: %s", l)
	}
}

// The machine record of the Studio's own pw code carries what its daemon
// allows, as the platform's record does, so the account panel offers
// signing in and removing an account where the daemon does.
func TestTheDirectMachineCarriesWhatItsDaemonAllows(t *testing.T) {
	f := &fakePW{status: map[string]any{"machineId": "d1", "hostname": "gpu-box.example", "claudeLogin": true, "claudeAccountRemove": true, "claudeCodeAllowed": true, "claudeInstalled": true, "permissionModes": []any{"read-only", "auto"}, "secret": "x"}}
	_, base, _ := continueModule(t, f, "d1")
	if _, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`); out["conversationId"] == nil {
		t.Fatalf("continue: %v", out)
	}
	out := get(t, base+"/api/code/sessions")
	var direct map[string]any
	for _, m := range out["machines"].([]any) {
		if mm := m.(map[string]any); mm["id"] == "studio" {
			direct = mm
		}
	}
	if direct == nil || direct["claudeLogin"] != true || direct["claudeAccountRemove"] != true || direct["claudeCodeAllowed"] != true || direct["claudeInstalled"] != true {
		t.Errorf("direct machine: %v", direct)
	}
	// Without the modes the daemon accepts, the permission menu falls back
	// to a list with no auto.
	if modes, _ := direct["permissionModes"].([]any); len(modes) != 2 || modes[1] != "auto" {
		t.Errorf("direct machine's permission modes: %v", direct["permissionModes"])
	}
	if _, leaked := direct["secret"]; leaked {
		t.Errorf("a status field the record does not name was passed on: %v", direct)
	}
}

// getRaw reads a route as a browser would, asking for the encoding given
// and decoding nothing itself.
func getRaw(t *testing.T, url, encoding string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept-Encoding", encoding)
	res, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A transcript is handed on as it arrives, so the end the session sends
// first shows while the rest is on its way, gzipped for a browser that
// takes it. Only the viewer's own chat's session is read, only a JSON
// answer passes, and none of the machine's or platform's other headers.
func TestATranscriptPassesAsItArrives(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, _, _ := relayModule(t, local, remote)
	relayedChat(t, base)
	const tail = `{"tail":{"id":"s1","history":[{"kind":"assistant","text":"the end"}]},"older":[`
	const rest = `{"kind":"user","text":"the start"}]}`
	release := make(chan struct{})
	remote.routes = map[string]http.HandlerFunc{
		"GET /v1/sessions/s1/transcript": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Set-Cookie", "machine=1")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			_, _ = w.Write([]byte(tail))
			w.(http.Flusher).Flush()
			<-release
			_, _ = w.Write([]byte(rest))
		},
		"GET /v1/sessions/s1/images/x": serveAs("image/png", "PNG"),
	}
	s := base + "/api/code/machines/m2/sessions/s1/transcript?tail=20&previews=refs"

	res := getRaw(t, s, "identity")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json" || res.Header.Get("X-Content-Type-Options") != "nosniff" ||
		res.Header.Get("Set-Cookie") != "" || res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Content-Encoding") != "" {
		t.Fatalf("headers: %d %v", res.StatusCode, res.Header)
	}
	// The tail arrives while the machine is still sending the rest.
	got := make([]byte, 0, len(tail))
	buf := make([]byte, 64)
	for len(got) < len(tail) {
		n, err := res.Body.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("read %q: %v", got, err)
		}
	}
	if string(got) != tail {
		t.Fatalf("first: %q", got)
	}
	close(release)
	more, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(got)+string(more) != tail+rest {
		t.Errorf("whole: %q", string(got)+string(more))
	}
	remote.mu.Lock()
	asked := append([]string(nil), remote.asked...)
	remote.mu.Unlock()
	if len(asked) != 1 || asked[0] != "GET /v1/sessions/s1/transcript?tail=20&previews=refs" {
		t.Errorf("asked: %v", asked)
	}

	// Gzipped, for a browser that takes it.
	res = getRaw(t, s, "gzip, deflate, br")
	zr, err := gzip.NewReader(res.Body)
	if err != nil || res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: %v %v", err, res.Header)
	}
	whole, _ := io.ReadAll(zr)
	res.Body.Close()
	if string(whole) != tail+rest {
		t.Errorf("gzipped: %q", whole)
	}

	// Another session, or this one under another machine: no chat of the
	// viewer's holds it, and nothing is asked of a machine.
	for _, u := range []string{base + "/api/code/machines/m2/sessions/s7/transcript", base + "/api/code/machines/m9/sessions/s1/transcript", base + "/api/code/machines/studio/sessions/s1/transcript"} {
		res := getRaw(t, u, "identity")
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 404 || res.Header.Get("Content-Type") != "application/problem+json" || strings.Contains(string(b), "the end") {
			t.Errorf("%s: %d %s", u, res.StatusCode, b)
		}
	}
	remote.mu.Lock()
	if len(remote.asked) != 2 {
		t.Errorf("asked for another's: %v", remote.asked)
	}
	remote.mu.Unlock()

	// What is not JSON is not passed on, whatever the machine says it is.
	remote.mu.Lock()
	remote.routes["GET /v1/sessions/s1/transcript"] = serveAs("text/html", "<script>x</script>")
	remote.mu.Unlock()
	res = getRaw(t, s, "identity")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 502 || strings.Contains(string(b), "<script") || res.Header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("html: %d %s %v", res.StatusCode, b, res.Header)
	}
	// A refusal is the route's usual error.
	remote.mu.Lock()
	remote.routes["GET /v1/sessions/s1/transcript"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"title":"Forbidden","status":403,"detail":"held by another host"}`))
	}
	remote.mu.Unlock()
	if code, out, h := send(t, http.MethodGet, s, ""); code != 403 || h.Get("Content-Type") != "application/problem+json" || !strings.Contains(fmt.Sprint(out["detail"]), "held by another host") {
		t.Errorf("refusal: %d %v", code, out)
	}
}

// A pw code without the transcript route is read whole once, by the Studio,
// and answered as one with it would: the end first. The page asks once.
func TestATranscriptFromAnOlderPWCodeIsBuiltFromItsDetail(t *testing.T) {
	f := &fakePW{}
	_, base, _ := continueModule(t, f, "d1")
	if _, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`); out["conversationId"] == nil {
		t.Fatalf("continue: %v", out)
	}
	notFound := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"title":"Not Found","status":404}`))
	}
	f.mu.Lock()
	f.routes = map[string]http.HandlerFunc{
		"GET /v1/sessions/s1/transcript": notFound,
		"GET /v1/sessions/s1":            serveAs("application/json", `{"id":"s1","status":"idle","messages":3,"history":[{"kind":"user","text":"one"},{"kind":"assistant","text":"two"},{"kind":"user","text":"three"}]}`),
	}
	f.mu.Unlock()
	res := getRaw(t, base+"/api/code/machines/studio/sessions/s1/transcript?tail=2&previews=refs", "identity")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	want := `{"tail":{"id":"s1","status":"idle","messages":3,"history":[{"kind":"assistant","text":"two"},{"kind":"user","text":"three"}],"machineId":"studio"},"older":[{"kind":"user","text":"one"}]}`
	if res.StatusCode != 200 || string(b) != want || res.Header.Get("Content-Type") != "application/json" {
		t.Errorf("%d\n%s\n%s", res.StatusCode, b, want)
	}
	f.mu.Lock()
	asked := append([]string(nil), f.asked...)
	f.mu.Unlock()
	if strings.Join(asked, " | ") != "GET /v1/sessions/s1/transcript?tail=2&previews=refs | GET /v1/sessions/s1?previews=refs" {
		t.Errorf("asked: %v", asked)
	}
	// A session the daemon does not have stays not found.
	f.mu.Lock()
	f.routes["GET /v1/sessions/s1"] = notFound
	f.mu.Unlock()
	if code, _, h := send(t, http.MethodGet, base+"/api/code/machines/studio/sessions/s1/transcript", ""); code != 404 || h.Get("Content-Type") != "application/problem+json" {
		t.Errorf("gone: %d", code)
	}
}

// The session on the Studio's own machine is read from its daemon the same
// way.
func TestADirectSessionsTranscriptPasses(t *testing.T) {
	f := &fakePW{}
	_, base, _ := continueModule(t, f, "d1")
	if _, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`); out["conversationId"] == nil {
		t.Fatalf("continue: %v", out)
	}
	f.routes = map[string]http.HandlerFunc{"GET /v1/sessions/s1/transcript": serveAs("application/json", `{"tail":{"id":"s1"},"older":[]}`)}
	res := getRaw(t, base+"/api/code/machines/studio/sessions/s1/transcript?tail=20", "identity")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(b) != `{"tail":{"id":"s1"},"older":[]}` || res.Header.Get("Content-Type") != "application/json" {
		t.Errorf("%d %q %v", res.StatusCode, b, res.Header)
	}
}

// A saved session the daemon has not taken up yet (a chat just opened): a
// command typed then reads the palette first, and that takes the session
// up, as PW Code on the web's server does, so the command is not sent as
// a message. Reading the session does not take it up. A relayed session,
// which the platform does not attach, is answered in the Studio's words,
// never the daemon's "attach it first".
func TestACommandTakesASavedSessionUpFirst(t *testing.T) {
	notLive := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"session is not live; attach it first"}`))
	}
	f := &fakePW{}
	_, base, _ := continueModule(t, f, "d1")
	if _, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`); out["conversationId"] == nil {
		t.Fatalf("continue: %v", out)
	}
	var mu sync.Mutex
	live, attaches := false, 0
	isLive := func() bool { mu.Lock(); defer mu.Unlock(); return live }
	f.mu.Lock()
	f.routes = map[string]http.HandlerFunc{
		"POST /v1/sessions/s1/attach": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			live, attaches = true, attaches+1
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"s1"}`))
		},
		"GET /v1/sessions/s1/palette": func(w http.ResponseWriter, r *http.Request) {
			if !isLive() {
				notLive(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"commands":[{"name":"plan"}]}`))
		},
		"GET /v1/sessions/s1/subagents": notLive,
	}
	f.mu.Unlock()
	s := base + "/api/code/machines/studio/sessions/s1"
	if code, _, _ := send(t, http.MethodGet, s+"/subagents", ""); code != http.StatusNotFound || attaches != 0 {
		t.Fatalf("reading took the session up: %d, attached %d", code, attaches)
	}
	if code, out, _ := send(t, http.MethodGet, s+"/palette", ""); code != 200 || attaches != 1 || out["commands"] == nil {
		t.Fatalf("palette: %d %v, attached %d", code, out, attaches)
	}

	local, remote := &fakePW{}, &fakePW{}
	_, rbase, _, _ := relayModule(t, local, remote)
	relayedChat(t, rbase)
	remote.mu.Lock()
	remote.routes = map[string]http.HandlerFunc{"GET /v1/sessions/s1/palette": notLive}
	remote.mu.Unlock()
	code, out, _ := send(t, http.MethodGet, rbase+"/api/code/machines/m2/sessions/s1/palette", "")
	if detail, _ := out["detail"].(string); code != http.StatusConflict || detail != notLiveYet {
		t.Fatalf("relayed palette: %d %v", code, out)
	}
}
