package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// asWho serves mux with the person the X-Who header names signed in.
func asWho(t *testing.T, srv *httptest.Server) func(who, method, path, body string) (int, string) {
	h := srv.Config.Handler
	return func(who, method, path, body string) (int, string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if who != "" {
			r = auth.WithUser(r, &auth.User{ID: who, Username: who})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
}

// TestAnotherPersonsChatIsAnsweredAsNone: with shared history off, each of
// the chat's routes answers another person, an administrator and a visitor
// as for a chat that is not there, and changes nothing.
func TestAnotherPersonsChatIsAnsweredAsNone(t *testing.T) {
	m, srv, gw, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks", "STUDIO_ADMINS": "cy"})
	do := asWho(t, srv)
	title := "Budget of the project"
	const there, none = "7f1c2b9e-0000-4000-8000-000000000001", "7f1c2b9e-0000-4000-8000-00000000000f"
	m.Conv.RecordUserTurn(there, "u1", "what is in my notes?", nil, "ada")
	m.Conv.RecordAssistantTurn(there, conversations.Message{ID: "a1", Role: "assistant", Content: sp("QUOTED-FROM-MY-LIBRARY"), ParentID: sp("u1")})
	m.Conv.SetTitle(there, title)
	pw := m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: "local", Session: "s9", CreatedAt: m.Conv.Now()}).ID

	if m.eff().ChatSharedHistory {
		t.Fatal("shared history is on with sign-in and nothing set")
	}
	stream := func(id string) string {
		b, _ := json.Marshal(map[string]any{"conversationId": id, "userMessageId": "u-x", "model": "m", "engine": "studio", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
		return string(b)
	}
	routes := []struct{ method, path, body string }{
		{"GET", "/api/chat/conversations/ID", ""},
		{"PATCH", "/api/chat/conversations/ID", `{"title":"taken"}`},
		{"POST", "/api/chat/steer", `{"conversationId":"ID","id":"s","text":"x"}`},
		{"GET", "/api/chat/conversations/ID/generating", ""},
		{"POST", "/api/chat/conversations/ID/stop", ""},
		{"GET", "/api/chat/conversations/ID/save", ""},
		{"POST", "/api/chat/conversations/ID/save", ""},
		{"PUT", "/api/chat/conversations/ID/engine", `{"engine":"studio"}`},
		{"GET", "/api/chat/suggestions?conversation=ID", ""},
		{"GET", "/api/chat/engine?conversation=ID", ""},
		{"GET", "/api/chat/pwcode/ID", ""},
		{"POST", "/api/chat/pwcode/ID/interrupt", ""},
		{"POST", "/api/chat/pwcode/ID/new-session", ""},
		{"POST", "/api/chat/pwcode/ID/follow", `{"session":"s2"}`},
		{"POST", "/api/chat/pwcode/ID/approvals/a1", `{"allowed":true}`},
		{"GET", "/api/chat/pwcode/ID/machine", ""},
		{"POST", "/api/chat/stream", "STREAM"},
		{"POST", "/api/chat/conversations/delete", `{"ids":["ID"]}`},
		{"DELETE", "/api/chat/conversations/ID", ""},
	}
	for _, who := range []string{"bob", "cy", ""} {
		for _, id := range []string{there, pw} {
			for _, c := range routes {
				ask := func(id string) (int, string) {
					body := strings.ReplaceAll(c.body, "ID", id)
					if body == "STREAM" {
						body = stream(id)
					}
					code, out := do(who, c.method, strings.ReplaceAll(c.path, "ID", id), body)
					return code, strings.ReplaceAll(out, id, "ID")
				}
				code, got := ask(id)
				wantCode, want := ask(none)
				if code != wantCode || got != want {
					t.Errorf("%q %s %s: %d %s, and for none %d %s", who, c.method, c.path, code, got, wantCode, want)
				}
				if strings.Contains(got, "QUOTED") || strings.Contains(got, title) {
					t.Errorf("%q %s %s: %s", who, c.method, c.path, got)
				}
			}
		}
		if _, out := do(who, "GET", "/api/chat/conversations", ""); out != "[]" {
			t.Errorf("%q lists %s", who, out)
		}
		if _, out := do(who, "GET", "/api/chat/pwcode/here", ""); strings.Contains(out, pw) || strings.Contains(out, "s9") {
			t.Errorf("%q: the Code page's chats are %s", who, out)
		}
		r := httptest.NewRequest("GET", "/api/notices", nil)
		if who != "" {
			r = auth.WithUser(r, &auth.User{ID: who, Username: who})
		}
		if s := m.ChatStates(r); len(s) != 0 {
			t.Errorf("%q: notices of %v", who, s)
		}
	}
	if len(gw.requests) != 0 {
		t.Errorf("the model was asked %d times", len(gw.requests))
	}
	if c := m.Conv.Get(there); c == nil || len(c.Messages) != 2 || deref(c.Title) != title || c.OwnerName() != "ada" {
		t.Errorf("ada's chat is now %+v", c)
	}
	if m.Conv.Get(none) != nil {
		t.Error("a chat was made under an id of the page's own")
	}
	// Ada's own, as ever.
	if code, out := do("ada", "GET", "/api/chat/conversations/"+there, ""); code != http.StatusOK || !strings.Contains(out, "QUOTED") {
		t.Errorf("ada: %d %s", code, out)
	}
	// Turned on, the others see it.
	if err := m.A.Settings.Update(map[string]any{"chatSharedHistory": true, "chatSharedHistoryChosen": true}); err != nil {
		t.Fatal(err)
	}
	// They read it and do nothing else, an administrator too.
	m.Conv.CreateWith(&title, "", nil, nil)
	for _, who := range []string{"bob", "cy", ""} {
		if code, out := do(who, "GET", "/api/chat/conversations/"+there, ""); code != http.StatusOK || !strings.Contains(out, "QUOTED") || !strings.Contains(out, `"isOwner":false,"canCollaborate":false`) {
			t.Errorf("shared, %q: %d %s", who, code, out)
		}
		his := map[bool]int{true: 1}[who == "cy"] // the one with no owner
		if _, out := do(who, "GET", "/api/chat/conversations", ""); strings.Count(out, `"isOwner":false,"canCollaborate":false`) != 3-his || strings.Count(out, `"isOwner":true,"canCollaborate":true`) != his {
			t.Errorf("shared, %q lists %s", who, out)
		}
		for _, id := range []string{there, pw} {
			for _, c := range routes {
				body := strings.ReplaceAll(c.body, "ID", id)
				if body == "STREAM" {
					body = stream(id)
				}
				code, out := do(who, c.method, strings.ReplaceAll(c.path, "ID", id), body)
				read := c.method == "GET" && !strings.HasSuffix(c.path, "/save")
				switch {
				case strings.HasSuffix(c.path, "conversations/delete"):
					if code != 200 || !strings.Contains(out, `"deleted":[]`) {
						t.Errorf("shared, %q deletes with others: %d %s", who, code, out)
					}
				case strings.Contains(c.path, "/suggestions"):
					if code != 200 || out != `{"after":null,"suggestions":[]}` {
						t.Errorf("shared, %q suggestions: %d %s", who, code, out)
					}
				case read || strings.Contains(c.path, "/pwcode/"):
					if code == 200 && !read {
						t.Errorf("shared, %q %s %s: %d %s", who, c.method, c.path, code, out)
					}
				case code != http.StatusForbidden || !strings.Contains(out, conversations.OnlyOwner):
					t.Errorf("shared, %q %s %s: %d %s", who, c.method, c.path, code, out)
				}
			}
		}
	}
	if len(gw.requests) != 0 {
		t.Errorf("shared: the model was asked %d times", len(gw.requests))
	}
	if c := m.Conv.Get(there); c == nil || len(c.Messages) != 2 || deref(c.Title) != title || c.OwnerName() != "ada" || c.Engine != "" {
		t.Errorf("shared: ada's chat is now %+v", c)
	}
	if c := m.Conv.Get(pw); c == nil || len(c.Messages) != 0 || deref(c.Title) != title || c.Engine != "" {
		t.Errorf("shared: ada's pw code chat is now %+v", c)
	}
	// Hers to change.
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/api/chat/conversations/ID", `{"title":"Budget"}`},
		{"POST", "/api/chat/steer", `{"conversationId":"ID","id":"s","text":"x"}`},
		{"POST", "/api/chat/conversations/ID/stop", ""},
		{"PUT", "/api/chat/conversations/ID/engine", `{"engine":"studio"}`},
	} {
		if code, out := do("ada", c.method, strings.ReplaceAll(c.path, "ID", there), strings.ReplaceAll(c.body, "ID", there)); code != 200 {
			t.Errorf("ada %s %s: %d %s", c.method, c.path, code, out)
		}
	}
	if code, _ := do("ada", "DELETE", "/api/chat/conversations/"+there, ""); code != http.StatusOK || m.Conv.Get(there) != nil {
		t.Errorf("ada deletes her chat: %d", code)
	}
}

// A session that another person's chat holds is answered on the Code page's
// routes as one its daemon does not have.
func TestASessionInAnothersChatIsAnsweredAsNone(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, _, _, _ := relayModule(t, local, remote)
	title := "Tune the solver"
	m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: "local", Session: "s9", CreatedAt: m.Conv.Now()})
	m.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	m.A.Cfg.FrontAuth = true
	m.Convs.AuthEnabled = func() bool { return true }
	m.Convs.SharedHistory = func() bool { return false }
	m.pf.OwnKeys = func(*http.Request, string) (*platform.Cred, *platform.Cred) {
		return &platform.Cred{Key: "k"}, nil
	}
	ask := func(id, method, rest string) (int, string) {
		path := "/api/code/machines/m2/sessions/" + id
		r := auth.WithUser(httptest.NewRequest(method, path, strings.NewReader("{}")), &auth.User{ID: "bob", Username: "bob"})
		r.SetPathValue("machine", "m2")
		r.SetPathValue("id", id)
		r.SetPathValue("rest", rest)
		w := httptest.NewRecorder()
		if err := m.codeProxy(w, r); err != nil {
			t.Fatal(err)
		}
		return w.Code, w.Body.String()
	}
	// One that bob's own sign-in does reach is refused while a chat of ada's
	// holds it, and nothing reaches it.
	m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: pwcode.RelaySite("m2"), Relay: true, Session: "s1", Machine: "m2", MachineName: "other-box", CreatedAt: m.Conv.Now()})
	for _, c := range [][2]string{{"GET", ""}, {"POST", "messages"}, {"POST", "interrupt"}} {
		if code, out := ask("s1", c[0], c[1]); code != 404 || !strings.Contains(out, noPageSession) {
			t.Errorf("s1 %s %s: %d %s", c[0], c[1], code, out)
		}
	}
	if remote.calls+local.calls != 0 || remote.interrupts != 0 || len(remote.messages) != 0 {
		t.Error("ada's session was reached")
	}
	for _, c := range [][2]string{{"GET", ""}, {"GET", "transcript"}, {"POST", "archive"}} {
		code, got := ask("s9", c[0], c[1])
		wantCode, want := ask("s-none", c[0], c[1])
		if code != 404 || code != wantCode || got != want {
			t.Errorf("%s %s: %d %s, and for none %d %s", c[0], c[1], code, got, wantCode, want)
		}
	}
}
