package chat

import (
	"encoding/json"
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

func TestPathArgs(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{[]any{"/a.md", " b/c.md ", "a.md", 3}, "a.md|b/c.md"},
		{"a.md, b.md", "a.md|b.md"},
		{"x, y.md\nz.md", "x, y.md|z.md"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := strings.Join(pathArgs(c.in), "|"); got != c.want {
			t.Errorf("pathArgs(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestKBFileTools moves, renames and copies within the main library at
// once, and holds a delete until the user allows it, then deletes once.
func TestKBFileTools(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(filepath.Join(kb, "drafts"), 0o755)
	for _, f := range []string{"drafts/a.md", "drafts/b.md", "drafts/c.md", "keep.md"} {
		os.WriteFile(filepath.Join(kb, f), []byte(f), 0o644)
	}
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
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(kb, p)); return err == nil }

	out, ok := m.executeInGo(r, "move_kb_files", `{"paths":["drafts/a.md","drafts/nope.md"],"dest":"archive/2026"}`, ToolCtx{})
	if !ok || out.Failed || !exists("archive/2026/a.md") || exists("drafts/a.md") || !strings.Contains(out.Result, "Skipped drafts/nope.md") {
		t.Errorf("move %+v", out)
	}
	out, _ = m.executeInGo(r, "move_kb_files", `{"paths":["drafts/b.md"],"name":"b-final.md"}`, ToolCtx{})
	if out.Failed || !exists("drafts/b-final.md") || out.Summary != "renamed drafts/b.md" {
		t.Errorf("rename %+v", out)
	}
	out, _ = m.executeInGo(r, "rename_kb_file", `{"path":"drafts/b-final.md","name":"b-v2.md"}`, ToolCtx{})
	if out.Failed || !exists("drafts/b-v2.md") || exists("drafts/b-final.md") || !strings.Contains(out.Result, "Renamed") {
		t.Errorf("rename_kb_file %+v", out)
	}
	out, _ = m.executeInGo(r, "rename_kb_file", `{"path":"drafts/b-v2.md","name":"../c.md"}`, ToolCtx{})
	if !out.Failed || !exists("drafts/b-v2.md") {
		t.Errorf("a name with a folder is refused: %+v", out)
	}
	if out, _ = m.executeInGo(r, "rename_kb_file", `{"path":"drafts/b-v2.md"}`, ToolCtx{}); !out.Failed {
		t.Errorf("a rename needs a name: %+v", out)
	}
	os.WriteFile(filepath.Join(kb, "archive/2026/c.md"), []byte("taken"), 0o644)
	out, _ = m.executeInGo(r, "move_kb_files", `{"paths":["drafts/c.md"],"dest":"archive/2026"}`, ToolCtx{})
	if !out.Failed || !exists("drafts/c.md") || !strings.Contains(out.Result, "name already exists there") {
		t.Errorf("a taken name is not replaced: %+v", out)
	}
	out, _ = m.executeInGo(r, "copy_kb_files", `{"paths":["keep.md"],"dest":"/"}`, ToolCtx{})
	if out.Failed || !exists("keep copy.md") || !exists("keep.md") {
		t.Errorf("copy %+v", out)
	}
	out, _ = m.executeInGo(r, "move_kb_files", `{"paths":["keep.md"],"dest":".hidden"}`, ToolCtx{})
	if !strings.Contains(out.Result, "excluded") || !exists("keep.md") {
		t.Errorf("hidden dest %+v", out)
	}

	// The delete waits for approval, lists the files on its card, and
	// runs once it is allowed.
	if !asksFirst("delete_kb_files") || asksFirst("move_kb_files") {
		t.Fatal("only the delete asks first")
	}
	tools := goTools{m: m}
	if p, _ := tools.Prepare(r, "delete_kb_files", `{"paths":["drafts/c.md","gone.md"]}`, ""); !strings.HasPrefix(p.Problem, "Not found in the library: gone.md") {
		t.Errorf("missing file %+v", p)
	}
	if p, _ := tools.Prepare(r, "delete_kb_files", `{"paths":["drafts"]}`, ""); !strings.HasPrefix(p.Problem, "Only files can be deleted") {
		t.Errorf("folder %+v", p)
	}
	prep, _ := tools.Prepare(r, "delete_kb_files", `{"paths":"drafts/c.md, keep copy.md"}`, "")
	if prep.Problem != "" || prep.Canonical != `{"paths":["drafts/c.md","keep copy.md"]}` {
		t.Fatalf("prepare %+v", prep)
	}
	if c := m.kbChangesAvailable(m.viewerOf(r), nil, nil); !c.OK {
		t.Fatalf("kb changes %+v", c)
	}
	approval, err := tools.Hold(r, map[string]any{"conversationId": "c1", "messageId": "srv-1", "toolCallId": "call_1", "tool": "delete_kb_files", "canonical": prep.Canonical, "args": prep.Args})
	if err != nil {
		t.Fatal(err)
	}
	var part map[string]any
	json.Unmarshal(approval, &part)
	if part["header"] != "Delete 2 files?" || part["command"] != "drafts/c.md\nkeep copy.md" || !strings.Contains(part["reason"].(string), "cannot undo") {
		t.Errorf("approval %s", approval)
	}
	if !exists("drafts/c.md") {
		t.Fatal("deleted before approval")
	}
	answer := func(allowed bool) *Answered {
		got, _ := tools.Answer(r, map[string]any{"conversationId": "c1", "messageId": "srv-1", "toolCallId": "call_1", "allowed": allowed}, nil)
		return got
	}
	got := answer(true)
	if !got.OK || got.Failed || got.Summary != "deleted 2 files" || exists("drafts/c.md") || exists("keep copy.md") || !exists("keep.md") {
		t.Errorf("allowed %+v", got)
	}
	if again := answer(true); again.OK {
		t.Errorf("ran twice: %+v", again)
	}

	// Declined, nothing goes.
	prep, _ = tools.Prepare(r, "delete_kb_files", `{"paths":["keep.md"]}`, "")
	tools.Hold(r, map[string]any{"conversationId": "c1", "messageId": "srv-2", "toolCallId": "call_2", "tool": "delete_kb_files", "canonical": prep.Canonical, "args": prep.Args})
	d, _ := tools.Answer(r, map[string]any{"conversationId": "c1", "messageId": "srv-2", "toolCallId": "call_2", "allowed": false}, nil)
	if !d.OK || d.Summary != "declined" || !exists("keep.md") {
		t.Errorf("declined %+v", d)
	}
}
