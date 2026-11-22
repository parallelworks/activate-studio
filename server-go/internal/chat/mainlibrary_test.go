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

// The assistant's file tools follow the main library's rule for the person
// who drives the chat: one who may not change it (on a Studio with
// sign-in, everyone but its administrators) changes nothing there.
func TestTheAssistantChangesTheMainLibraryOnlyForThoseWhoMay(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(filepath.Join(kb, "notes"), 0o755)
	os.WriteFile(filepath.Join(kb, "notes", "a.md"), []byte("# A\n"), 0o644)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	why := "On a Studio with sign-in, the main library is read only: only its administrators change it."
	defer func(f func(*http.Request) (bool, string)) { kbpkg.MainWriter = f }(kbpkg.MainWriter)
	kbpkg.MainWriter = func(r *http.Request) (bool, string) { return r.Header.Get("X-Admin") == "1", why }
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	for name, args := range map[string]string{
		"write_kb_file":  `{"path":"notes/new.md","content":"# New\n"}`,
		"edit_kb_file":   `{"path":"notes/a.md","edits":[{"find":"# A","replace":"# B"}]}`,
		"move_kb_files":  `{"paths":["notes/a.md"],"to":"moved"}`,
		"copy_kb_files":  `{"paths":["notes/a.md"],"dest":"copied"}`,
		"rename_kb_file": `{"path":"notes/a.md","name":"b.md"}`,
		"apply_labels":   `{"paths":["notes/a.md"],"add":["x"]}`,
	} {
		out, ok := Default.executeInGo(r, name, args, ToolCtx{})
		if !ok || out == nil || !strings.Contains(out.Result, "only its administrators") {
			t.Errorf("%s: %+v", name, out)
		}
	}
	// The delete, which asks first, is not offered for approval.
	if p, _ := (goTools{m: Default}).Prepare(r, "delete_kb_files", `{"paths":["notes/a.md"]}`, ""); p == nil || !strings.Contains(p.Problem, "only its administrators") {
		t.Errorf("delete: %+v", p)
	}
	if b, _ := os.ReadFile(filepath.Join(kb, "notes", "a.md")); string(b) != "# A\n" {
		t.Errorf("a.md changed: %q", b)
	}
	for _, p := range []string{"notes/new.md", "moved", "copied", "notes/b.md"} {
		if _, err := os.Stat(filepath.Join(kb, p)); err == nil {
			t.Errorf("%s was made", p)
		}
	}
	// An administrator's chat writes as before.
	r.Header.Set("X-Admin", "1")
	if out, ok := Default.executeInGo(r, "write_kb_file", `{"path":"notes/new.md","content":"# New\n"}`, ToolCtx{}); !ok || strings.Contains(out.Result, "administrators") {
		t.Errorf("admin write: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(kb, "notes", "new.md")); err != nil {
		t.Error("the administrator's file was not written")
	}
}
