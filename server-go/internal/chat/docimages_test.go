package chat

import (
	"bytes"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
)

func TestLibraryImages(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "uploads/chat"), 0o755)
	os.MkdirAll(filepath.Join(root, "reports/figs"), 0o755)
	os.WriteFile(filepath.Join(root, "uploads/chat/shot.png"), []byte("shot"), 0o644)
	os.WriteFile(filepath.Join(root, "reports/figs/a b.png"), []byte("fig"), 0o644)
	src := libraryImages(root, "reports")
	for ref, want := range map[string]string{
		"figs/a b.png":                           "fig",
		"figs/a%20b.png":                         "fig",
		"<figs/a b.png>":                         "fig",
		"../uploads/chat/shot.png":               "shot",
		"uploads/chat/shot.png":                  "shot", // from the library's top
		"/uploads/chat/shot.png":                 "shot",
		"#open=file:uploads/chat/shot.png":       "shot",
		"/api/kb/raw?path=uploads/chat/shot.png": "shot",
		"data:image/png;base64,c2hvdA==":         "shot",
	} {
		got, err := src(ref)
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", ref, got, err)
		}
	}
	for ref, why := range map[string]string{
		"https://example.org/a.png": "web images are not fetched",
		"missing.png":               "not found",
		"../../etc/passwd":          "escapes",
		"data:image/png,abc":        "base64",
	} {
		if _, err := src(ref); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v, want %q", ref, err, why)
		}
	}
}

func TestWriteKBFileEmbedsImages(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(filepath.Join(kb, "uploads/chat"), 0o755)
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 40, 30)))
	os.WriteFile(filepath.Join(kb, "uploads/chat/shot.png"), b.Bytes(), 0o644)
	os.WriteFile(filepath.Join(kb, "uploads/chat/Screenshot 2026-11-13 at 9.01.28 AM-x1.png"), b.Bytes(), 0o644)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)

	out, ok := Default.executeInGo(r, "write_kb_file", `{"path":"requirements/req.pdf","content":"# Req\n\n![screen](uploads/chat/shot.png)\n\n![spaced](../uploads/chat/Screenshot 2026-11-13 at 9.01.28 AM-x1.png)\n\n![web](https://example.org/x.png)\n"}`, ToolCtx{})
	if !ok || !strings.Contains(out.Result, "with 2 images") || !strings.Contains(out.Result, "https://example.org/x.png (web images are not fetched") {
		t.Fatalf("%+v", out)
	}
	pdf, _ := os.ReadFile(filepath.Join(kb, "requirements/req.pdf"))
	if !bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Error("the PDF holds no image")
	}
}
