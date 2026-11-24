package ragendpoint

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// Starting or stopping the model endpoint registers the Studio's model on
// the platform for everyone: where people sign in, only its
// administrators do.
func TestOnlyAdministratorsStartOrStopTheEndpoint(t *testing.T) {
	t.Setenv("PW_USER", "boss")
	t.Setenv("STUDIO_ADMINS", "")
	ix, kb := t.TempDir(), t.TempDir()
	cfg, err := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": ix, "KB_ROOT": kb, "PORT": "5999", "STUDIO_NODE": "off", "AUTH_JWKS_URL": "http://127.0.0.1:9/keys"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	p := platform.New(platform.Options{IndexBase: ix, KBRoot: kb, Home: t.TempDir(), LookupEnv: func(string) (string, bool) { return "", false }})
	mux := http.NewServeMux()
	Register(mux, a, p)
	do := func(who, path string) (int, string) {
		r := httptest.NewRequest("POST", path, nil)
		if who != "" {
			r = auth.WithUser(r, &auth.User{ID: who, Username: who})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	for _, path := range []string{"/api/rag/endpoint/start", "/api/rag/endpoint/stop"} {
		for _, who := range []string{"sam", ""} {
			if code, body := do(who, path); code != 403 || !strings.Contains(body, "administrators") {
				t.Errorf("%q %s: %d %s", who, path, code, body)
			}
		}
		// The owner gets as far as the endpoint's own checks.
		if code, body := do("boss", path); code != 200 {
			t.Errorf("owner %s: %d %s", path, code, body)
		}
	}
}
