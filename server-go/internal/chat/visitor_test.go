package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

// TestAVisitorSeesNoChats: on a Studio where sign-in is optional, a visitor
// who has not signed in sees no chat, one with an owner or one with none,
// whether chats are shared or not. The list is empty, notices name none,
// and every route of a chat asks them to sign in or answers as for a chat
// that is not there, never with its title or words. Signed in, others read
// the shared ones as before.
func TestAVisitorSeesNoChats(t *testing.T) {
	const none = "7f1c2b9e-0000-4000-8000-00000000000f"
	stream := func(id string) string {
		b, _ := json.Marshal(map[string]any{"conversationId": id, "userMessageId": "u-x", "model": "m", "engine": "studio", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
		return string(b)
	}
	routes := []struct{ method, path, body string }{
		{"GET", "/api/chat/conversations/ID", ""},
		{"GET", "/api/chat/conversations/ID/generating", ""},
		{"GET", "/api/chat/suggestions?conversation=ID", ""},
		{"GET", "/api/chat/engine?conversation=ID", ""},
		{"GET", "/api/chat/pwcode/ID", ""},
		{"POST", "/api/chat/pwcode/ID/follow", `{}`},
		{"PATCH", "/api/chat/conversations/ID", `{"title":"taken"}`},
		{"POST", "/api/chat/steer", `{"conversationId":"ID","id":"s","text":"x"}`},
		{"POST", "/api/chat/conversations/ID/stop", ""},
		{"GET", "/api/chat/conversations/ID/save", ""},
		{"POST", "/api/chat/conversations/ID/save", ""},
		{"PUT", "/api/chat/conversations/ID/engine", `{"engine":"studio"}`},
		{"POST", "/api/chat/stream", "STREAM"},
		{"POST", "/api/chat/conversations/delete", `{"ids":["ID"]}`},
		{"DELETE", "/api/chat/conversations/ID", ""},
	}
	// Opening one, as a link to it does, and what follows it.
	asked := map[string]bool{"/api/chat/conversations/ID": true, "/api/chat/conversations/ID/generating": true}
	for _, shared := range []bool{true, false} {
		t.Run(map[bool]string{true: "shared", false: "not shared"}[shared], func(t *testing.T) {
			m, srv, gw, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks", "STUDIO_ADMINS": "cy"})
			do := asWho(t, srv)
			if err := m.A.Settings.Update(map[string]any{"chatSharedHistory": shared, "chatSharedHistoryChosen": true}); err != nil {
				t.Fatal(err)
			}
			title := "SECRET-TITLE budget"
			msgs := []conversations.Message{{ID: "u1", Role: "user", Content: sp("SECRET-WORDS")}}
			ada := m.Conv.CreateWith(&title, "ada", msgs, nil).ID
			unowned := m.Conv.CreateWith(&title, "", msgs, nil).ID
			pw := m.Conv.CreateWith(&title, "ada", msgs, &conversations.PWCodeLink{Site: "pw://ada/box", Session: "s1", CreatedAt: m.Conv.Now()}).ID

			if code, out := do("", "GET", "/api/chat/conversations", ""); code != http.StatusOK || strings.TrimSpace(out) != "[]" {
				t.Errorf("the visitor lists %d %s", code, out)
			}
			if s := m.ChatStates(httptest.NewRequest("GET", "/api/notices", nil)); len(s) != 0 {
				t.Errorf("notices of %v", s)
			}
			for _, c := range routes {
				ask := func(id string) (int, string) {
					body := strings.ReplaceAll(c.body, "ID", id)
					if body == "STREAM" {
						body = stream(id)
					}
					code, out := do("", c.method, strings.ReplaceAll(c.path, "ID", id), body)
					return code, strings.ReplaceAll(out, id, "ID")
				}
				wantCode, want := ask(none)
				if asked[c.path] && (wantCode != http.StatusUnauthorized || !strings.Contains(want, conversations.SignInToChat)) {
					t.Errorf("%s %s, none: %d %s", c.method, c.path, wantCode, want)
				}
				for _, id := range []string{ada, unowned, pw} {
					if code, got := ask(id); code != wantCode || got != want || strings.Contains(got, "SECRET") {
						t.Errorf("%s %s: %d %s, and for none %d %s", c.method, c.path, code, got, wantCode, want)
					}
				}
			}
			for _, id := range []string{ada, unowned, pw} {
				if c := m.Conv.Get(id); c == nil || len(c.Messages) != 1 || deref(c.Title) != title {
					t.Errorf("changed: %+v", c)
				}
			}
			if len(gw.requests) != 0 {
				t.Errorf("the model was asked %d times", len(gw.requests))
			}
			// Signed in, bob reads ada's where chats are shared, as before.
			code, out := do("bob", "GET", "/api/chat/conversations/"+ada, "")
			if shared != (code == http.StatusOK && strings.Contains(out, "SECRET-WORDS")) {
				t.Errorf("bob: %d %s", code, out)
			}
			if _, out := do("bob", "GET", "/api/chat/conversations", ""); shared != strings.Contains(out, ada) {
				t.Errorf("bob lists %s", out)
			}
		})
	}
}
