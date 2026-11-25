package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

func asUser(who string) *http.Request {
	return auth.WithUser(httptest.NewRequest(http.MethodPost, "/x", nil), &auth.User{ID: who, Username: who})
}

// The Code page's pane reads and directs a session no chat holds, as the
// viewer: through the platform with their credential on another machine,
// and at the daemon the Studio reaches for them on its own.
func TestTheCodePagesPaneReachesTheViewersOwnSession(t *testing.T) {
	local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn}
	m, base, plat, _ := relayModule(t, local, remote)
	for _, machine := range []string{"m2", "studio"} {
		s := base + "/api/code/machines/" + machine + "/sessions/s1"
		if code, out, _ := send(t, http.MethodGet, s, ""); code != 200 || out["id"] != "s1" {
			t.Errorf("%s: %d %v", machine, code, out)
		}
		if code, out, _ := send(t, http.MethodPost, s+"/messages", `{"text":"go on"}`); code != 200 {
			t.Errorf("%s message: %d %v", machine, code, out)
		}
		// What a chat's view may send is all the pane may.
		if code, _, _ := send(t, http.MethodPost, s+"/archive", `{}`); code != 404 {
			t.Errorf("%s archive: %d", machine, code)
		}
		if code, _, _ := send(t, http.MethodPatch, s+"/settings", `{"name":"x"}`); code != 400 {
			t.Errorf("%s settings: %d", machine, code)
		}
	}
	if len(local.messages) != 1 || len(remote.messages) != 1 {
		t.Errorf("messages: here %v, there %v", local.messages, remote.messages)
	}
	// A mode change is held to the machine's ceiling, as a chat's is.
	if code, out, _ := send(t, http.MethodPatch, base+"/api/code/machines/m2/sessions/s1/settings", `{"permissionMode":"auto"}`); code != 403 || !strings.Contains(out["detail"].(string), "at most accept-edits") {
		t.Errorf("mode: %d %v", code, out)
	}
	for _, r := range plat.relayed {
		if !strings.HasSuffix(r, " Bearer k") {
			t.Errorf("not as the viewer: %s", r)
		}
	}
	// Nothing of it is kept here.
	if l := m.Conv.List(); len(l) != 0 {
		t.Errorf("chats made: %d", len(l))
	}
	if code, _, _ := send(t, http.MethodGet, base+"/api/code/machines/m2/sessions/..%2Fs1", ""); code != 404 {
		t.Errorf("an id that is not one: %d", code)
	}
	// Another site's page directs nothing.
	req, _ := http.NewRequest(http.MethodPost, base+"/api/code/machines/m2/sessions/s1/messages", strings.NewReader(`{"text":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://elsewhere.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 || len(remote.messages) != 1 {
		t.Errorf("from another site: %d, messages %v", res.StatusCode, remote.messages)
	}
}

// With sign-in on, the pane is the person's own sign-in or nothing, and a
// session another person's chat runs in is not theirs, and is answered as any.
func TestTheCodePagesPaneIsNeverTheStudiosAccount(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, _, _, _ := relayModule(t, local, remote)
	title := "Ada's"
	m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: "local", Session: "s9", CreatedAt: m.Conv.Now()})
	m.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	m.A.Cfg.FrontAuth = true
	m.Convs.AuthEnabled = func() bool { return true }
	calls := local.calls + remote.calls
	for _, c := range []struct {
		machine, id string
		want        int
	}{{"studio", "s9", 409}, {"m2", "s9", 409}, {"studio", "s1", 409}, {"m2", "s1", 409}} {
		r := asUser("bob")
		r.SetPathValue("machine", c.machine)
		r.SetPathValue("id", c.id)
		r.SetPathValue("rest", "messages")
		rec := httptest.NewRecorder()
		if err := m.codeProxy(rec, r); err != nil {
			t.Fatal(err)
		}
		if rec.Code != c.want {
			t.Errorf("bob %s/%s: %d, want %d: %s", c.machine, c.id, rec.Code, c.want, rec.Body)
		}
	}
	if local.calls+remote.calls != calls {
		t.Error("a session was reached without the person's own sign-in")
	}
}

// The view's listing has the session the pane has open, and no other that
// no chat holds.
func TestTheListingHasTheSessionThePaneHasOpen(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, plat, _ := relayModule(t, local, remote)
	plat.mu.Lock()
	plat.sessions = append(plat.sessions, map[string]any{"machineId": "m2", "id": "s7", "name": "Another", "status": "idle", "messages": 1})
	plat.mu.Unlock()
	ids := func(q string) string {
		_, out, _ := send(t, http.MethodGet, base+"/api/code/sessions"+q, "")
		var got []string
		for _, s := range out["sessions"].([]any) {
			o := s.(map[string]any)
			got = append(got, o["machineId"].(string)+"/"+o["id"].(string))
		}
		return strings.Join(got, " ")
	}
	if got := ids(""); got != "" {
		t.Errorf("no pane, no chat: %q", got)
	}
	if got := ids("?open=m2/s7"); got != "m2/s7" {
		t.Errorf("open: %q", got)
	}
}

// A plan is answered in the pane as in a chat, into a mode the machine
// takes.
func TestAPlanIsAnsweredInThePane(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, _, _ := relayModule(t, local, remote)
	if code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/page/m2/s1/approvals/p1", `{"kind":"plan","allowed":true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if got := strings.Join(remote.answers, "|"); !strings.Contains(got, `"mode":"accept-edits"`) {
		t.Errorf("answered %s", got)
	}
}

// Continuing takes the daemon found for the person within the minute.
func TestContinueDoesNotAskTheDaemonAgain(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	_, base, _, _ := relayModule(t, local, remote)
	send(t, http.MethodGet, base+"/api/chat/pwcode/here", "")
	calls := local.calls
	relayedChat(t, base)
	if local.calls != calls {
		t.Errorf("the daemon here was asked %d more times", local.calls-calls)
	}
}
