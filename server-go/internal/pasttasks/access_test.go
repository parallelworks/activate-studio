package pasttasks

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
)

// The retired engine never recorded who started a task, so on a Studio
// with sign-in its tasks are read by the administrators only; on a Studio
// without sign-in, by its one person.
func TestEarlierTasksAreTheAdministrators(t *testing.T) {
	dir := t.TempDir()
	ix := filepath.Join(dir, "ix")
	seeded := seed(t)
	os.MkdirAll(ix, 0o755)
	os.Rename(seeded.Dir, filepath.Join(ix, "tasks"))
	env := map[string]string{"INDEX_BASE": ix, "KB_ROOT": filepath.Join(dir, "kb"), "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "STUDIO_NODE": "off"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	Register(mux, a)
	defer func(g func(string) string) { mainlib.Getenv = g }(mainlib.Getenv)
	mainlib.Getenv = func(k string) string {
		if k == "STUDIO_ADMINS" {
			return "cy"
		}
		return ""
	}
	get := func(u *auth.User, path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if u != nil {
			req = auth.WithUser(req, u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	const id = "2026-11-02-live-cd34"
	paths := []string{"/api/tasks", "/api/tasks/" + id, "/api/tasks/" + id + "/agents/w1/output"}

	// Sign-in off: its one person reads them.
	for _, p := range paths {
		if code, body := get(nil, p); code != 200 || strings.Contains(body, "adminsOnly") {
			t.Errorf("sign-in off %s: %d %s", p, code, body)
		}
	}

	a.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	bob, cy := &auth.User{ID: "bob", Username: "bob"}, &auth.User{ID: "cy", Username: "cy"}
	for _, u := range []*auth.User{bob, nil} {
		code, body := get(u, "/api/tasks")
		if code != 200 || strings.Contains(body, id) || !strings.Contains(body, `"adminsOnly"`) {
			t.Errorf("%v lists: %d %s", u, code, body)
		}
		for _, p := range paths[1:] {
			if code, _ := get(u, p); code != 404 {
				t.Errorf("%v reads %s: %d", u, p, code)
			}
		}
	}
	for _, p := range paths {
		if code, body := get(cy, p); code != 200 || strings.Contains(body, "adminsOnly") {
			t.Errorf("an administrator %s: %d %s", p, code, body)
		}
	}
}
