package updates

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// newTestApp is an App for a clone at root, with no Node.
func newTestApp(t *testing.T, root string) *app.App {
	t.Helper()
	cfg := config.Config{ProjectRoot: root, IndexBase: t.TempDir(), KBRoot: t.TempDir(), Port: 6031}
	return app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
}

func TestRegisterServesRoutesAndAsksNodeLate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t, root)
	mux := http.NewServeMux()
	Register(mux, a)
	Register(http.NewServeMux(), newTestApp(t, root)) // a second server in one process registers its Node env once

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/updates", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mode":"clone"`) {
		t.Fatalf("GET /api/updates: %d %s", rec.Code, rec.Body.String())
	}
	if env := bridge.NodeEnv(map[string]string{}, "http://127.0.0.1:6031"); env["STUDIO_BEHIND_GO"] != "1" {
		t.Errorf("Node's environment has no STUDIO_BEHIND_GO=1")
	}

	// Without Node nothing is busy; Node attached after Register is asked.
	if a.Busy() {
		t.Fatal("busy with no Node")
	}
	busy := `{"running":false,"busy":true}`
	a.SetNodeHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/index/status" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, busy)
	}))
	if !a.Busy() {
		t.Error("Node's busy report is not counted")
	}
	busy = `{"running":false}`
	if a.Busy() {
		t.Error("idle Node counted as busy")
	}
}
