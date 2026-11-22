package libapi

import (
	"bytes"
	"encoding/json"
	"io"
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
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
)

// On a Studio with sign-in, the main library is read only for each person
// who signs in, and its administrators (the deployment's owner, and those
// STUDIO_ADMINS names) keep changing it. Without sign-in nothing changes.
func TestTheMainLibraryIsReadOnlyForSignedInPeople(t *testing.T) {
	for _, signIn := range []bool{true, false} {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "kb"), 0o755)
		env := map[string]string{"KB_ROOT": filepath.Join(dir, "kb"), "INDEX_BASE": filepath.Join(dir, "ix"), "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0",
			"PW_USER": "owner", "STUDIO_ADMINS": "ada@platform.example"}
		if signIn {
			env["AUTH_JWKS_URL"] = "http://127.0.0.1:1/jwks"
		}
		defer func(g func(string) string) { mainlib.Getenv = g }(mainlib.Getenv)
		mainlib.Getenv = func(k string) string { return env[k] }
		cfg, err := config.Load(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
		mux := http.NewServeMux()
		Register(mux, a)
		as := func(r *http.Request, u *auth.User) *http.Request {
			if u == nil {
				return r
			}
			return auth.WithUser(r, u)
		}
		people := map[string]*auth.User{
			"eve":    {ID: "e1", Username: "eve"},
			"owner":  {ID: "o1", Username: "owner"},
			"ada":    {ID: "a1", Username: "ada", Claims: auth.Claims{"email": "ada@platform.example"}},
			"nobody": nil,
		}
		for name, u := range people {
			may := !signIn || name == "owner" || name == "ada"
			r := as(httptest.NewRequest("GET", "/api/libraries", nil), u)
			// The libraries the client lists, and Settings' list.
			raws, err := a.Providers.Libraries(r, "kb", "")
			if err != nil {
				t.Fatal(err)
			}
			var main libraries.PublicLibrary
			json.Unmarshal(raws[0], &main)
			if main.ID != libraries.PrimaryID || main.Writable != may || main.Access.Write != may || (main.ReadOnlyWhy != "") == may {
				t.Errorf("sign-in %v, %s: listed %+v", signIn, name, main)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, as(httptest.NewRequest("GET", "/api/libraries", nil), u))
			if !strings.Contains(w.Body.String(), `"writable":`+map[bool]string{true: "true", false: "false"}[may]) {
				t.Errorf("sign-in %v, %s: Settings list %s", signIn, name, w.Body.String()[:200])
			}
			// The file routes' library, and what they say when refused.
			lib := kb.ForRequest(as(httptest.NewRequest("POST", "/api/kb/mkdir", nil), u), kb.Library{ID: "kb", Label: "kb", Writable: true, Primary: true})
			err = kb.RequireWritable(lib)
			if (err == nil) != may || (err != nil && !strings.Contains(err.Error(), "only its administrators")) {
				t.Errorf("sign-in %v, %s: write %v", signIn, name, err)
			}
			// How the main library is indexed is its administrators' too.
			w = httptest.NewRecorder()
			dr := httptest.NewRequest("PUT", "/api/libraries/kb/depth", bytes.NewBufferString(`{"depth":"names"}`))
			dr.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(w, as(dr, u))
			if (w.Code == 403) == may || (may && w.Code != 200) {
				t.Errorf("sign-in %v, %s: depth %d %s", signIn, name, w.Code, w.Body.String())
			}
		}
	}
}
