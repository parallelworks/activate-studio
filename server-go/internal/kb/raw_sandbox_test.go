package kb_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// A library file opened at /api/kb/raw never runs script on the Studio's
// origin: HTML and SVG come with a script-free sandbox, and nothing is
// sniffed into a type that would.
func TestRawSandboxesActiveFiles(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "kb")
	os.MkdirAll(root, 0o755)
	for name, body := range map[string]string{
		"page.html": "<script>fetch('/api/kb/files')</script>",
		"pic.svg":   `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"doc.pdf":   "%PDF-1.4",
		"blob.bin":  "<html><script>alert(1)</script>",
		"shot.png":  "\x89PNG",
	} {
		os.WriteFile(filepath.Join(root, name), []byte(body), 0o644)
	}
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": root, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	mux := http.NewServeMux()
	kb.Register(mux, app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	get := func(p string) http.Header {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/kb/raw?path="+p, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
		return w.Header()
	}
	for _, p := range []string{"page.html", "pic.svg"} {
		h := get(p)
		csp := h.Get("Content-Security-Policy")
		if !strings.HasPrefix(csp, "sandbox;") || strings.Contains(csp, "allow-scripts") || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: csp %q nosniff %q", p, csp, h.Get("X-Content-Type-Options"))
		}
	}
	for _, p := range []string{"doc.pdf", "shot.png", "blob.bin"} {
		h := get(p)
		if h.Get("Content-Security-Policy") != "" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: csp %q nosniff %q", p, h.Get("Content-Security-Policy"), h.Get("X-Content-Type-Options"))
		}
	}
	if ct := get("blob.bin").Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("unknown type served as %q", ct)
	}
}
