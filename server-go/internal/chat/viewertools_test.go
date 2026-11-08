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

// TestViewerAndLabelToolsRunInGo checks that apply_labels and
// show_in_viewer answer here for the main library (the golden harness
// compares their text with Node's), and that a library only Node knows is
// left to Node.
func TestViewerAndLabelToolsRunInGo(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(filepath.Join(kb, "notes"), 0o755)
	os.WriteFile(filepath.Join(kb, "notes", "a.md"), []byte("a"), 0o644)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	m := Default
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	out, ok := m.executeInGo(r, "apply_labels", `{"paths":"notes/a.md","add":"Alpha"}`, ToolCtx{})
	if !ok || !strings.HasPrefix(out.Result, "Labels updated (on the files, and re-indexed):\nnotes/a.md: [alpha]") {
		t.Errorf("apply_labels %v %+v", ok, out)
	}
	if _, ok := m.executeInGo(r, "apply_labels", `{"paths":"x","add":"a","library":"someone-elses"}`, ToolCtx{}); ok {
		t.Errorf("a library only Node knows ran here")
	}
	out, ok = m.executeInGo(r, "show_in_viewer", `{"target":"notes/a.md"}`, ToolCtx{})
	if !ok || out.Summary != "link for notes/a.md" {
		t.Errorf("show_in_viewer %+v", out)
	}
	out, _ = m.executeInGo(r, "show_in_viewer", `{"kind":"workflow_dag","target":"wf"}`, ToolCtx{})
	if out.Result != "Tool error: "+noCLIMessage {
		t.Errorf("no CLI: %q", out.Result)
	}
}
