package chat

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

// TestAChatWithNoOwner: with sign-in, a chat with no owner is its
// administrators'. Others who have signed in read it where chats are
// shared, and nothing else; where they are not, it is answered as one that
// is not there. A visitor reads none (TestAVisitorSeesNoChats).
func TestAChatWithNoOwner(t *testing.T) {
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
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{true: "shared", false: "not shared"}[shared], func(t *testing.T) {
			m, srv, gw, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks", "STUDIO_ADMINS": "cy,di"})
			do := asWho(t, srv)
			if err := m.A.Settings.Update(map[string]any{"chatSharedHistory": shared, "chatSharedHistoryChosen": true}); err != nil {
				t.Fatal(err)
			}
			title := "From before sign-in"
			msgs := []conversations.Message{{ID: "u1", Role: "user", Content: sp("OLD-QUESTION")}}
			id := m.Conv.CreateWith(&title, "", msgs, nil).ID
			relay := m.Conv.CreateWith(&title, "", msgs, &conversations.PWCodeLink{Relay: true, Site: "pw://me/box", Session: "s9", CreatedAt: m.Conv.Now()}).ID

			for _, who := range []string{"bob"} {
				for _, c := range routes {
					ask := func(id string) (int, string) {
						body := strings.ReplaceAll(c.body, "ID", id)
						if body == "STREAM" {
							body = stream(id)
						}
						code, out := do(who, c.method, strings.ReplaceAll(c.path, "ID", id), body)
						return code, strings.ReplaceAll(out, id, "ID")
					}
					wantCode, want := ask(none)
					// A chat that copies another machine's session is no
					// one else's to read, shared or not.
					if code, got := ask(relay); code != wantCode || got != want {
						t.Errorf("relay, %q %s %s: %d %s, and for none %d %s", who, c.method, c.path, code, got, wantCode, want)
					}
					code, got := ask(id)
					read := c.method == "GET" && !strings.HasSuffix(c.path, "/save") && !strings.Contains(c.path, "/suggestions")
					switch {
					case !shared || strings.Contains(c.path, "/suggestions"):
						if code != wantCode || got != want || strings.Contains(got, "OLD") || strings.Contains(got, title) {
							t.Errorf("%q %s %s: %d %s, and for none %d %s", who, c.method, c.path, code, got, wantCode, want)
						}
					case read:
						if code != http.StatusOK {
							t.Errorf("%q %s %s: %d %s", who, c.method, c.path, code, got)
						}
					case strings.HasSuffix(c.path, "conversations/delete"):
						if code != http.StatusOK || !strings.Contains(got, `"deleted":[]`) {
							t.Errorf("%q deletes with others: %d %s", who, code, got)
						}
					case code != http.StatusForbidden || !strings.Contains(got, conversations.OnlyAdmins):
						t.Errorf("%q %s %s: %d %s", who, c.method, c.path, code, got)
					}
				}
				_, out := do(who, "GET", "/api/chat/conversations", "")
				if strings.Contains(out, id) != shared || strings.Contains(out, relay) || shared && !strings.Contains(out, `"isOwner":false,"canCollaborate":false`) {
					t.Errorf("%q lists %s", who, out)
				}
			}
			if c := m.Conv.Get(id); c == nil || len(c.Messages) != 1 || deref(c.Title) != title || c.OwnerName() != "" || c.Engine != "" || len(gw.requests) != 0 {
				t.Fatalf("changed: %+v, the model asked %d times", c, len(gw.requests))
			}

			// Each administrator's.
			for _, who := range []string{"cy", "di"} {
				if _, out := do(who, "GET", "/api/chat/conversations", ""); !strings.Contains(out, id) || !strings.Contains(out, relay) || strings.Count(out, `"isOwner":true,"canCollaborate":true`) != 2 {
					t.Errorf("%q lists %s", who, out)
				}
				if code, out := do(who, "GET", "/api/chat/conversations/"+id, ""); code != http.StatusOK || !strings.Contains(out, "OLD-QUESTION") || !strings.Contains(out, `"isOwner":true,"canCollaborate":true`) {
					t.Errorf("%q: %d %s", who, code, out)
				}
				for _, c := range routes[4:10] {
					if strings.HasSuffix(c.path, "/save") {
						continue
					}
					if code, out := do(who, c.method, strings.ReplaceAll(c.path, "ID", id), strings.ReplaceAll(c.body, "ID", id)); code != http.StatusOK {
						t.Errorf("%q %s %s: %d %s", who, c.method, c.path, code, out)
					}
				}
				// The session it drives is not theirs.
				if code, _ := do(who, "POST", "/api/chat/pwcode/"+relay+"/interrupt", ""); code != http.StatusForbidden {
					t.Errorf("%q interrupts its session: %d", who, code)
				}
			}
			if c := m.Conv.Get(id); deref(c.Title) != "taken" || c.OwnerName() != "" {
				t.Errorf("renamed: %+v", c)
			}
			if _, out := do("cy", "POST", "/api/chat/conversations/delete", `{"ids":["`+id+`"]}`); !strings.Contains(out, `"deleted":["`+id+`"]`) {
				t.Errorf("deleted with others: %s", out)
			}
			if code, _ := do("di", "DELETE", "/api/chat/conversations/"+relay, ""); code != http.StatusOK || m.Conv.Get(relay) != nil {
				t.Errorf("deleted: %d", code)
			}

			// An administrator's message reaches it, and makes it theirs.
			taken := m.Conv.CreateWith(&title, "", msgs, nil).ID
			if code, out := do("cy", "POST", "/api/chat/stream", stream(taken)); code != http.StatusOK || strings.Contains(out, "administrator") {
				t.Errorf("cy sends: %d %s", code, out)
			}
			m.Conv.RecordUserTurn(taken, "u2", "more", nil, "cy")
			if c := m.Conv.Get(taken); c == nil || c.OwnerName() != "cy" {
				t.Errorf("after a message: %+v", c)
			}
			if code, _ := do("di", "PATCH", "/api/chat/conversations/"+taken, `{"title":"x"}`); code == http.StatusOK {
				t.Error("another administrator renamed it then")
			}
		})
	}
}

// TestAVisitorIsAskedToSignIn: a chat a visitor started would have no owner,
// so none is made.
func TestAVisitorIsAskedToSignIn(t *testing.T) {
	m, srv, _, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks"})
	do := asWho(t, srv)
	if code, out := do("", "POST", "/api/chat/conversations", `{"title":"x"}`); code != http.StatusUnauthorized || !strings.Contains(out, "Sign in to chat") || len(m.Conv.List()) != 0 {
		t.Errorf("%d %s, %d chats", code, out, len(m.Conv.List()))
	}
	if code, out := do("ada", "POST", "/api/chat/conversations", `{"title":"x"}`); code != http.StatusOK || len(m.Conv.List()) != 1 || m.Conv.List()[0].OwnerName() != "ada" {
		t.Errorf("ada: %d %s", code, out)
	}
	m2, srv2, _, _ := newTestModule(t, nil)
	if code, out := asWho(t, srv2)("", "POST", "/api/chat/conversations", `{"title":"x"}`); code != http.StatusOK || len(m2.Conv.List()) != 1 {
		t.Errorf("no sign-in: %d %s", code, out)
	}
}
