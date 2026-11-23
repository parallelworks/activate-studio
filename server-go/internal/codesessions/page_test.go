package codesessions

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// asked is what a stand-in platform or daemon was sent.
type asked struct {
	mu   sync.Mutex
	reqs []string
	auth []string
	body []map[string]any
}

func (a *asked) take(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var b map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &b)
	a.reqs = append(a.reqs, r.Method+" "+r.URL.RequestURI())
	a.auth = append(a.auth, r.Header.Get("Authorization"))
	a.body = append(a.body, b)
}

func (a *asked) n() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.reqs) }

func (a *asked) last() (string, map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.reqs) == 0 {
		return "", nil
	}
	return a.reqs[len(a.reqs)-1], a.body[len(a.body)-1]
}

const pageSid = "20261123-120000-abcdef"

// pagePlatform stands in for the platform's pw code routes: machine m1
// with ceiling, which "" leaves out.
func pagePlatform(t *testing.T, got *asked, ceiling *string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.take(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/code/sessions":
			if *ceiling == "down" {
				w.WriteHeader(500)
				return
			}
			m := map[string]any{"id": "m1", "name": "box", "kind": "existing", "status": "connected", "remoteStart": true}
			if *ceiling != "" {
				m["remoteMaxPermissionMode"] = *ceiling
			}
			json.NewEncoder(w).Encode(map[string]any{"machines": []any{m},
				"sessions": []any{map[string]any{"id": pageSid, "machineId": "m1", "name": "mesh fix", "status": "idle"}}})
		case r.URL.Path == "/api/openai/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]any{"id": "model-a"}}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": "new-session", "machineId": "m1"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func send(t *testing.T, method, url, body string, header map[string]string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, url, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// The Code page reaches the platform only with the person's own
// credential, and only with the routes PW Code on the web's list and menu
// use: anything else is refused before the platform is asked.
func TestTheCodePageSendsOnlyItsRoutesAsThePerson(t *testing.T) {
	got := &asked{}
	ceiling := ""
	plat := pagePlatform(t, got, &ceiling)
	var v *platform.Viewer
	base := signedInServer(t, map[string]string{"PW_GATEWAY_URL": plat.URL + "/api/openai/v1", "PW_API_KEY": "deployment-key"}, false,
		func() *platform.Viewer { return v }, nil).URL + pagePrefix
	session := base + "/machines/m1/sessions/" + pageSid

	// No credential of their own: nothing is sent, the deployment's key
	// least of all.
	v = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "sam-id", Username: "sam"}}
	for _, c := range [][2]string{{"GET", base + "/sessions"}, {"POST", session + "/rename"}, {"POST", base + "/machines/m1/sessions"}, {"GET", base + "/machines/m1/directories"}} {
		if code, _ := send(t, c[0], c[1], `{"name":"x","workspace":"/w"}`, nil); code != 409 {
			t.Fatalf("%s %s with no key of their own: %d", c[0], c[1], code)
		}
	}
	if code, out := send(t, "GET", base+"/where", "", nil); code != 200 || out["signIn"] != true || out["codeUrl"] != plat.URL+"/code" {
		t.Fatalf("where, with no sign-in of their own: %d %v", code, out)
	}
	if got.n() != 0 {
		t.Fatalf("the platform was asked: %v", got.reqs)
	}

	v = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id", Username: "alex"}, Cred: &platform.Cred{Key: "alex-own-key"}}
	if code, out := send(t, "GET", base+"/where", "", nil); code != 200 || out["signIn"] != false {
		t.Fatalf("where, signed in: %d %v", code, out)
	}
	if code, out := send(t, "GET", base+"/sessions", "", nil); code != 200 || len(out["sessions"].([]any)) != 1 {
		t.Fatalf("listing: %d %v", code, out)
	}
	allowed := []struct{ method, suffix, body, sent string }{
		{"POST", "/rename", `{"name":"mesh refinement"}`, "POST /api/code/machines/m1/sessions/" + pageSid + "/rename"},
		{"POST", "/archive", "", "POST /api/code/machines/m1/sessions/" + pageSid + "/archive"},
		{"DELETE", "/archive", "", "DELETE /api/code/machines/m1/sessions/" + pageSid + "/archive"},
		{"POST", "/pin", "", "POST /api/code/machines/m1/sessions/" + pageSid + "/pin"},
		{"DELETE", "/pin", "", "DELETE /api/code/machines/m1/sessions/" + pageSid + "/pin"},
		{"PUT", "/group", `{"group":"g1"}`, "PUT /api/code/machines/m1/sessions/" + pageSid + "/group"},
		{"DELETE", "/group", "", "DELETE /api/code/machines/m1/sessions/" + pageSid + "/group"},
		{"POST", "/stop", "", "POST /api/code/machines/m1/sessions/" + pageSid + "/stop"},
		{"POST", "/fork", "", "POST /api/code/machines/m1/sessions/" + pageSid + "/fork"},
		{"DELETE", "?removeWorktree=%2Fw%2Ftree", "", "DELETE /api/code/machines/m1/sessions/" + pageSid + "?removeWorktree=%2Fw%2Ftree"},
		{"GET", "/worktree", "", "GET /api/code/machines/m1/sessions/" + pageSid + "/worktree"},
		{"POST", "/worktree/leave", `{}`, "POST /api/code/machines/m1/sessions/" + pageSid + "/worktree/leave"},
		{"POST", "/approvals/ap1", `{"allowed":true}`, "POST /api/code/machines/m1/sessions/" + pageSid + "/approvals/ap1"},
	}
	for _, c := range allowed {
		if code, _ := send(t, c.method, session+c.suffix, c.body, nil); code != 200 {
			t.Fatalf("%s %s: %d", c.method, c.suffix, code)
		}
		if sent, _ := got.last(); sent != c.sent {
			t.Fatalf("%s %s sent %q", c.method, c.suffix, sent)
		}
	}
	before := got.n()
	refused := [][2]string{
		{"POST", "/messages"}, {"POST", "/shell"}, {"POST", "/clear"}, {"POST", "/compact"}, {"PATCH", "/settings"},
		{"POST", "/goal"}, {"POST", "/attach"}, {"GET", "/transcript"}, {"POST", "/commands"}, {"POST", "/interrupt"},
		{"PUT", "/settings"}, {"POST", "/approvals/a%2F..%2Fx"}, {"POST", "/approvals/..%2f..%2fmessages"}, {"POST", "/approvals/a;b"},
		{"POST", "/subagents/s1/stop"}, {"GET", "/images/x"}, {"PATCH", ""},
	}
	for _, c := range refused {
		if code, _ := send(t, c[0], session+c[1], `{}`, nil); code != 404 {
			t.Fatalf("%s %s: %d", c[0], c[1], code)
		}
	}
	if got.n() != before {
		t.Fatalf("a refused route reached the platform: %v", got.reqs[before:])
	}
	for _, k := range got.auth {
		if k != "Bearer alex-own-key" {
			t.Fatalf("the platform was asked with %q", k)
		}
	}
	// The platform's own models, read as the person.
	if code, out := send(t, "GET", base+"/openai/models", "", nil); code != 200 || len(out["data"].([]any)) != 1 {
		t.Fatalf("models: %d %v", code, out)
	}
}

// A new session's permission mode, and the mode a plan's answer moves a
// session to, are held to the machine's remote ceiling as the platform
// lists it for the person; when that cannot be read, or the Studio does
// not know it, read-only is all that is allowed. Only the fields the
// platform takes are passed on.
func TestANewSessionIsHeldToTheMachinesCeiling(t *testing.T) {
	got := &asked{}
	ceiling := ""
	plat := pagePlatform(t, got, &ceiling)
	v := &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id", Username: "alex"}, Cred: &platform.Cred{Key: "alex-own-key"}}
	base := signedInServer(t, map[string]string{"PW_GATEWAY_URL": plat.URL + "/api/openai/v1"}, false, func() *platform.Viewer { return v }, nil).URL + pagePrefix
	create := base + "/machines/m1/sessions"
	creates := func() int {
		n := 0
		got.mu.Lock()
		defer got.mu.Unlock()
		for _, r := range got.reqs {
			if r == "POST /api/code/machines/m1/sessions" {
				n++
			}
		}
		return n
	}
	start := func(mode string) int {
		code, _ := send(t, "POST", create, `{"workspace":"/home/alex/solver","permissionMode":"`+mode+`","prompt":"go","model":"model-a","backend":"native","remoteControl":true}`, nil)
		return code
	}

	// No ceiling named: accept-edits.
	if code := start("auto"); code != 403 || creates() != 0 {
		t.Fatalf("auto above the default ceiling: %d, %d sent", code, creates())
	}
	if code := start("accept-edits"); code != 200 || creates() != 1 {
		t.Fatalf("accept-edits: %d", code)
	}
	if _, body := got.last(); body["backend"] != nil || body["remoteControl"] != nil || body["prompt"] != "go" || body["permissionMode"] != "accept-edits" || body["model"] != "model-a" {
		t.Fatalf("sent %v", body)
	}
	ceiling = "auto"
	if code := start("auto"); code != 200 || creates() != 2 {
		t.Fatalf("auto on a machine that allows it: %d", code)
	}
	if code := start("bypass-permissions"); code != 403 || creates() != 2 {
		t.Fatalf("bypass above auto: %d", code)
	}
	if code := start("plan-everything"); code != 400 || creates() != 2 {
		t.Fatalf("an unknown mode: %d", code)
	}
	// A ceiling the Studio does not rank, and a listing that is down.
	for _, c := range []string{"everything", "down"} {
		ceiling = c
		n := creates()
		if code := start("accept-edits"); code != 403 || creates() != n {
			t.Fatalf("ceiling %s: %d", c, code)
		}
		if code, out := send(t, "POST", create, `{"workspace":"/home/alex/solver","permissionMode":"read-only"}`, nil); code != 200 {
			t.Fatalf("read-only with ceiling %s: %d %v", c, code, out)
		}
	}
	n := creates()
	ceiling = ""
	for _, body := range []string{`{"workspace":"relative/dir","permissionMode":"read-only"}`, `{"workspace":"/a\nb"}`, `{"permissionMode":"read-only"}`, `{"workspace":7}`, `[1]`} {
		if code, _ := send(t, "POST", create, body, nil); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if creates() != n {
		t.Fatal("a bad body was sent")
	}
	// A plan's answer that moves the session to a mode.
	plan := base + "/machines/m1/sessions/" + pageSid + "/approvals/ap1"
	before := got.n()
	if code, _ := send(t, "POST", plan, `{"allowed":true,"mode":"bypass-permissions"}`, nil); code != 403 {
		t.Fatalf("plan answer above the ceiling: %d", code)
	}
	for _, r := range got.reqs[before:] {
		if strings.Contains(r, "/approvals/") {
			t.Fatalf("sent %s", r)
		}
	}
	if code, _ := send(t, "POST", plan, `{"allowed":true,"mode":"accept-edits"}`, nil); code != 200 {
		t.Fatalf("plan answer within the ceiling: %d", code)
	}
}

// A page of another site cannot start, change or delete a session with the
// person's sign-in; the Studio's own pages can.
func TestTheCodePageRefusesOtherSites(t *testing.T) {
	got := &asked{}
	ceiling := "auto"
	plat := pagePlatform(t, got, &ceiling)
	v := &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id", Username: "alex"}, Cred: &platform.Cred{Key: "alex-own-key"}}
	srv := signedInServer(t, map[string]string{"PW_GATEWAY_URL": plat.URL + "/api/openai/v1"}, false, func() *platform.Viewer { return v }, nil)
	base := srv.URL + pagePrefix
	for _, h := range []map[string]string{{"Origin": "https://elsewhere.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		if code, _ := send(t, "DELETE", base+"/machines/m1/sessions/"+pageSid, "", h); code != 403 {
			t.Fatalf("delete from %v: %d", h, code)
		}
		if code, _ := send(t, "POST", base+"/machines/m1/sessions", `{"workspace":"/w","permissionMode":"read-only"}`, h); code != 403 {
			t.Fatalf("start from %v: %d", h, code)
		}
	}
	if got.n() != 0 {
		t.Fatalf("sent %v", got.reqs)
	}
	if code, _ := send(t, "POST", base+"/machines/m1/sessions/"+pageSid+"/pin", "", map[string]string{"Origin": srv.URL, "Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Fatalf("from the Studio's own page: %d", code)
	}
}

// On a shared Studio whose pw code runs on this computer, every session
// there runs as the Studio's account: a person lists and changes only the
// sessions the Studio started for them, and starts none from the Code page.
func TestOnThisComputerAPersonManagesOnlyTheirOwn(t *testing.T) {
	const mine, theirs = "20261123-120000-aaaaaa", "20261123-120000-bbbbbb"
	got := &asked{}
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			json.NewEncoder(w).Encode(map[string]any{"protocolVersion": pwcode.ProtocolVersion, "machineId": "mach1", "hostname": "box.example"})
		case "/v1/sessions":
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode([]any{map[string]any{"id": mine, "name": "my summary"}, map[string]any{"id": theirs, "name": "sam's agent"}})
				return
			}
			got.take(r)
			json.NewEncoder(w).Encode(map[string]any{"id": "new"})
		default:
			got.take(r)
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	var v *platform.Viewer
	own := func(r *http.Request) []string {
		if pv, _ := platform.New(platform.Options{}).ViewerOf(r); pv != nil && pv.UserID() == "alex-id" {
			return []string{mine}
		}
		return nil
	}
	base := signedInServer(t, map[string]string{"PW_CODE_SOCKET": sock, "AGENT_LOCAL_SITE": "1"}, true, func() *platform.Viewer { return v }, own).URL + pagePrefix
	v = &platform.Viewer{AuthEnabled: true, User: &platform.User{ID: "alex-id", Username: "alex"}}

	code, out := send(t, "GET", base+"/sessions", "", nil)
	if code != 200 {
		t.Fatalf("listing: %d %v", code, out)
	}
	ss, ms := out["sessions"].([]any), out["machines"].([]any)
	if len(ss) != 1 || ss[0].(map[string]any)["id"] != mine || ss[0].(map[string]any)["machineId"] != "mach1" {
		t.Fatalf("alex sees %v", ss)
	}
	if len(ms) != 1 || ms[0].(map[string]any)["kind"] != "local" || ms[0].(map[string]any)["remoteStart"] != false {
		t.Fatalf("machines %v", ms)
	}
	if code, _ := send(t, "POST", base+"/machines/mach1/sessions/"+theirs+"/archive", "", nil); code != 404 {
		t.Fatalf("someone else's session archived: %d", code)
	}
	if code, _ := send(t, "DELETE", base+"/machines/mach1/sessions/"+theirs, "", nil); code != 404 {
		t.Fatalf("someone else's session deleted: %d", code)
	}
	if code, _ := send(t, "POST", base+"/machines/mach1/sessions", `{"workspace":"/w","permissionMode":"read-only"}`, nil); code != 403 {
		t.Fatalf("started a session as the Studio's account: %d", code)
	}
	if code, _ := send(t, "POST", base+"/machines/mach1/sessions/"+mine+"/fork", "", nil); code != 403 {
		t.Fatalf("forked: %d", code)
	}
	if got.n() != 0 {
		t.Fatalf("sent %v", got.reqs)
	}
	if code, _ := send(t, "POST", base+"/machines/mach1/sessions/"+mine+"/rename", `{"name":"pumps"}`, nil); code != 200 {
		t.Fatalf("their own: %d", code)
	}
	if sent, body := got.last(); sent != "POST /v1/sessions/"+mine+"/rename" || body["name"] != "pumps" {
		t.Fatalf("sent %q %v", sent, body)
	}
	if code, _ := send(t, "POST", base+"/machines/other/sessions/"+mine+"/rename", `{"name":"x"}`, nil); code != 404 {
		t.Fatalf("another machine: %d", code)
	}
}
