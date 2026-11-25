package notices

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/delegation"
)

// Delegated work opens on the Code page, which shows only with ACTIVATE: the
// Studio's notices hold none of it for a person without, and hold it again
// once they sign in.
func TestDelegatedNoticesNeedActivate(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": dir, "KB_ROOT": filepath.Join(dir, "kb"), "STUDIO_NODE": "off"}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	had := delegation.Default
	t.Cleanup(func() { delegation.Default = had })
	delegation.Default = delegation.New(a)
	if err := os.MkdirAll(filepath.Join(dir, "delegation"), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]delegation.Record{{Owner: "single", Session: "s1", Objective: "Run the sweep", State: "finished"}})
	if err := os.WriteFile(filepath.Join(dir, "delegation", "sessions.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	platform, local := false, false
	a.Providers.PlatformAvailable = func(*http.Request, string) bool { return platform }
	a.Providers.LocalCode = func(*http.Request) bool { return local }
	got := func() int { return len(sources(a).Delegated(httptest.NewRequest("GET", "/api/notices", nil))) }
	if n := got(); n != 0 {
		t.Fatal("without ACTIVATE:", n)
	}
	platform = true
	if n := got(); n != 1 {
		t.Fatal("with ACTIVATE:", n)
	}
	platform, local = false, true
	if n := got(); n != 1 {
		t.Fatal("with this computer's pw code:", n)
	}
}
