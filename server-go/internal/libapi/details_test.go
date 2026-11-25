package libapi

import (
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// The details card says what its reader may do in the main library.
func TestDetailsSayWhatTheReaderMayDo(t *testing.T) {
	names := map[string]string{"PW_USER": "boss", "STUDIO_ADMINS": "cy"}
	defer func(g func(string) string) { mainlib.Getenv = g }(mainlib.Getenv)
	mainlib.Getenv = func(k string) string { return names[k] }
	for _, c := range []struct {
		name, who string
		signIn    bool
		primary   bool
		writable  bool
		want      bool
	}{
		{"a person", "bob", true, true, true, false},
		{"a visitor", "", true, true, true, false},
		{"the owner", "boss", true, true, true, true},
		{"an administrator", "cy", true, true, true, true},
		{"an administrator, a read-only deployment", "cy", true, true, false, false},
		{"a person, a library of their own", "bob", true, false, true, true},
		{"a Studio of one", "", false, true, true, true},
	} {
		dir := t.TempDir()
		env := map[string]string{"INDEX_BASE": dir, "STUDIO_NODE": "off", "KB_ROOT": filepath.Join(dir, "kb")}
		if c.signIn {
			env["AUTH_JWKS_URL"] = "http://127.0.0.1:9/keys"
		}
		cfg, _ := config.Load(func(k string) string { return env[k] })
		s := &Service{a: app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)}
		r := httptest.NewRequest("GET", "/api/kb/details", nil)
		if c.who != "" {
			r = auth.WithUser(r, &auth.User{ID: c.who, Username: c.who})
		}
		d := search.Details{Library: search.DetailsLibrary{Writable: c.writable}}
		s.asMayWrite(r, libraries.Library{Primary: c.primary}, &d)
		if d.Library.Writable != c.want {
			t.Errorf("%s: %v", c.name, d.Library.Writable)
		}
	}
}
