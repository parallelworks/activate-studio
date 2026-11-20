package chat

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
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
)

func TestWritingCheckComesBackWithAReport(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	advice := "A check of the writing found:"

	// A report, and the documents made from it, come back with what the
	// check found.
	for _, path := range []string{"reports/plan.md", "reports/plan.pdf", "reports/plan.docx"} {
		out, ok := Default.executeInGo(r, "write_kb_file", `{"path":"`+path+`","content":"# Plan\n\nThe platform is truly ready — it runs today.\n"}`, ToolCtx{})
		if !ok || !strings.Contains(out.Result, advice) || !strings.Contains(out.Result, `"truly" 1`) || !strings.Contains(out.Result, "a dash used as punctuation") {
			t.Errorf("%s: %+v", path, out)
		}
	}
	// Plain writing, and a script, come back without it.
	for path, content := range map[string]string{"reports/clean.md": `# Plan\n\nThe platform runs today.\n`, "scripts/run.py": `print(\"truly — done\")\n`} {
		out, ok := Default.executeInGo(r, "write_kb_file", `{"path":"`+path+`","content":"`+content+`"}`, ToolCtx{})
		if !ok || strings.Contains(out.Result, advice) {
			t.Errorf("%s: %+v", path, out)
		}
	}
	// An edit is checked again.
	out, ok := Default.executeInGo(r, "edit_kb_file", `{"path":"reports/clean.md","edits":[{"find":"runs today","replace":"really runs today"}]}`, ToolCtx{})
	if !ok || !strings.Contains(out.Result, `"really" 1`) {
		t.Errorf("edit: %+v", out)
	}
}

func TestPWCodeReadsTheLibrarysConventions(t *testing.T) {
	m, _ := pwModule(t, &fakePW{}, nil)
	tools := m.chatTools(httptest.NewRequest(http.MethodGet, "/", nil))
	if p := m.pwcodePrompt(tools); strings.Contains(p, "This library's conventions") {
		t.Errorf("no conventions file, but: %s", p)
	}
	os.MkdirAll(m.A.Cfg.KBRoot, 0o755)
	if err := os.WriteFile(filepath.Join(m.A.Cfg.KBRoot, conventionsFile), []byte("# Conventions\n"), 0o644); err != nil {
		t.Fatalf("%q: %v", m.A.Cfg.KBRoot, err)
	}
	if p := m.pwcodePrompt(tools); !strings.Contains(p, "This library's conventions, "+conventionsFile+" at its top") || !strings.Contains(p, "a check of the writing") {
		t.Errorf("prompt: %s", p)
	}
}
