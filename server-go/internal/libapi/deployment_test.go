package libapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// With sign-in on, the libraries on the Studio's computer are the
// deployment's, read with its own account for everyone: nobody signed in
// adds one, or removes one, from the Studio.
func TestSignedInPeopleDoNotChangeTheDeploymentsLibraries(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"KB_ROOT": filepath.Join(dir, "kb"), "INDEX_BASE": filepath.Join(dir, "ix"), "STUDIO_NODE": "off",
		"SWEEP_INTERVAL_SEC": "0", "AUTH_JWKS_URL": "http://127.0.0.1:1/jwks"}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	mux := http.NewServeMux()
	Register(mux, a)
	for _, c := range []struct{ method, url, body string }{
		{"POST", "/api/libraries", `{"id":"theirs","indexRoot":"/home/someone/index"}`},
		{"DELETE", "/api/libraries/scratch", ""},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(c.method, c.url, bytes.NewBufferString(c.body)))
		var b map[string]any
		json.Unmarshal(w.Body.Bytes(), &b)
		if w.Code != 403 || !strings.Contains(b["error"].(string), "the deployment's") {
			t.Errorf("%s %s: %d %v", c.method, c.url, w.Code, b)
		}
	}
}
