package chat

import (
	"errors"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// TestSuggestionsAreTheOwners answers a conversation's owner, whose id on
// a platform sign-in is another form of the username the conversation is
// recorded under, and refuses someone else.
func TestSuggestionsAreTheOwners(t *testing.T) {
	dir := t.TempDir()
	// Each person's conversations are their own.
	t.Setenv("CHAT_SHARED_HISTORY", "0")
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": dir, "KB_ROOT": filepath.Join(dir, "kb"), "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "CHAT_SHARED_HISTORY": "0"}[k]
	})
	m := New(app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	m.Conv.RecordUserTurn("conv-1", "u1", "a question", nil, "Pat.Lee")
	get := func(u *auth.User) int {
		r := httptest.NewRequest("GET", "/api/chat/suggestions?conversation=conv-1", nil)
		if u != nil {
			r = auth.WithUser(r, u)
		}
		w := httptest.NewRecorder()
		if err := m.suggestions(w, r); err != nil {
			var e *apierr.Error
			if errors.As(err, &e) {
				return e.Status
			}
			return 500
		}
		return w.Code
	}
	if code := get(&auth.User{ID: "user:Pat.Lee", Username: "Pat.Lee"}); code != 200 {
		t.Errorf("the owner: %d", code)
	}
	if code := get(&auth.User{ID: "user:Sam.Roe", Username: "Sam.Roe"}); code != 403 {
		t.Errorf("someone else: %d", code)
	}
}
