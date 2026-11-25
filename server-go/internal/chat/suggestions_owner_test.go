package chat

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
)

// TestSuggestionsAreTheOwners answers a conversation's owner, whose id on
// a platform sign-in is another form of the username the conversation is
// recorded under, and answers someone
// else, and a visitor, as for a conversation that is not there.
func TestSuggestionsAreTheOwners(t *testing.T) {
	dir := t.TempDir()
	// Each person's conversations are their own.
	t.Setenv("CHAT_SHARED_HISTORY", "0")
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": dir, "KB_ROOT": filepath.Join(dir, "kb"), "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "CHAT_SHARED_HISTORY": "0"}[k]
	})
	Register(http.NewServeMux(), app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	m := Default
	m.Convs.AuthEnabled = func() bool { return true }
	m.Convs.SharedHistory = func() bool { return false }
	m.Conv.RecordUserTurn("conv-1", "u1", "a question", nil, "Pat.Lee")
	m.Conv.RecordAssistantTurn("conv-1", conversations.Message{ID: "a1", Role: "assistant", Content: sp("an answer"), ParentID: sp("u1")})
	get := func(u *auth.User, id string) string {
		r := httptest.NewRequest("GET", "/api/chat/suggestions?conversation="+id, nil)
		if u != nil {
			r = auth.WithUser(r, u)
		}
		w := httptest.NewRecorder()
		if err := m.suggestions(w, r); err != nil || w.Code != 200 {
			t.Fatal(w.Code, err)
		}
		return w.Body.String()
	}
	none := get(&auth.User{ID: "user:Pat.Lee", Username: "Pat.Lee"}, "no-such")
	if got := get(&auth.User{ID: "user:Pat.Lee", Username: "Pat.Lee"}, "conv-1"); got == none || !strings.Contains(got, "a1") {
		t.Errorf("the owner: %s", got)
	}
	for _, u := range []*auth.User{{ID: "user:Sam.Roe", Username: "Sam.Roe"}, nil} {
		if got := get(u, "conv-1"); got != none {
			t.Errorf("%v: %s", u, got)
		}
	}
}
