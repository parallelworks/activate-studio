package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if len(remote.settings) != 0 {
		t.Errorf("settings reached the session: %v", remote.settings)
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
	for _, u := range []string{"/api/pwcode-feed/machines/m2/sessions/s1/events?after=0", "/api/pwcode-feed/sessions"} {
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
