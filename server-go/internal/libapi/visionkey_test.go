package libapi

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
)

// Where each person uses their own credential (REQUIRE_PERSONAL_KEY), a
// person's own library describes its images with its owner's key, or with
// none, saying what the Assistant says; the Studio's libraries, and every
// library elsewhere, with the Studio's.
func TestOwnLibraryDescribesImagesWithItsOwnersKey(t *testing.T) {
	for _, c := range []struct {
		name  string
		env   map[string]string
		owner string
		want  string // "studio", or the key, or the refusal
	}{
		{"required, ada's", map[string]string{"REQUIRE_PERSONAL_KEY": "1"}, "ada", "ada-key"},
		{"required, bob's without one", map[string]string{"REQUIRE_PERSONAL_KEY": "1"}, "bob", modelkeys.SignInFirst},
		{"required, the Studio's library", map[string]string{"REQUIRE_PERSONAL_KEY": "1"}, "", "studio"},
		{"not required", map[string]string{}, "bob", "studio"},
		{"personal keys off", map[string]string{"REQUIRE_PERSONAL_KEY": "1", "DISABLE_PERSONAL_KEYS": "1"}, "bob", "studio"},
		{"sign-in off", map[string]string{"REQUIRE_PERSONAL_KEY": "1", "AUTH_JWKS_URL": ""}, "bob", "studio"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			env := map[string]string{"KB_ROOT": filepath.Join(dir, "kb"), "INDEX_BASE": filepath.Join(dir, "ix"), "STUDIO_NODE": "off",
				"SWEEP_INTERVAL_SEC": "0", "AI_ENABLED": "1", "ADE_VISION_MODEL": "caption-model", "AUTH_JWKS_URL": "http://127.0.0.1:9/jwks", "HOME": dir}
			for k, v := range c.env {
				env[k] = v
			}
			for k, v := range env {
				t.Setenv(k, v)
			}
			cfg, err := config.Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
			a.Providers.SignInKey = func(sub string) string {
				if sub == "ada" {
					return "ada-key"
				}
				return ""
			}
			s := New(a)
			t.Cleanup(func() { s.builds.stop() })
			d := &libraries.Def{ID: "u-x", Owner: c.owner, IndexRoot: filepath.Join(dir, "lib"), Build: &libraries.LocalBuild{IndexDir: filepath.Join(dir, "lib-build"), CacheRoot: filepath.Join(dir, "lib-cache")}}
			f := s.builds.visionKey(d)
			got := "studio"
			if f != nil {
				k, why, ok := f()
				got = k
				if !ok {
					got = why
				}
			}
			if got != c.want {
				t.Errorf("%q", got)
			}
			// The build's documents pass takes it.
			m, k := s.builds.work(d, 0, index.Settings{Documents: true, OCR: true, ImageDescriptions: true}).Vision()
			want := map[string][2]string{"studio": {"caption-model", ""}, "ada-key": {"caption-model", "ada-key"}, modelkeys.SignInFirst: {"", ""}}[c.want]
			if m != want[0] || k != want[1] {
				t.Errorf("the build describes with %q %q", m, k)
			}
		})
	}
}
