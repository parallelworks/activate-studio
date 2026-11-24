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

// newOneBed is a Studio one person uses, with no sign-in and no spaces:
// its main library is My library, or label when one is set.
func newOneBed(t *testing.T, label string) *whereBed {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(filepath.Join(kb, "notes"), 0o755)
	os.WriteFile(filepath.Join(kb, "notes", "sample.md"), []byte("# Sample\n"), 0o644)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	t.Setenv("KB_LABEL", label)
	if label == "" {
		os.Unsetenv("KB_LABEL")
	}
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "STUDIO_NODE": "off"}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	prevR, prevS, prevW := kbpkg.LibraryResolver, kbpkg.SpaceFiles, kbpkg.MainWriter
	kbpkg.LibraryResolver, kbpkg.SpaceFiles, kbpkg.MainWriter = nil, nil, nil
	t.Cleanup(func() { kbpkg.LibraryResolver, kbpkg.SpaceFiles, kbpkg.MainWriter = prevR, prevS, prevW })
	return &whereBed{m: Default, kb: kb}
}

func (b *whereBed) mustRun(t *testing.T, r *http.Request, tool, args string) {
	t.Helper()
	if out := b.call(r, "c-name", tool, args); out.Failed || strings.HasPrefix(out.Result, "Tool error") {
		t.Fatalf("%s %s: %+v", tool, args, out)
	}
}

// On a Studio one person uses, a path that starts with the main library's
// name is a path in it: no folder of that name is made, by any tool.
func TestTheLibrarysNameAtAPathsFrontIsNotAFolder(t *testing.T) {
	b := newOneBed(t, "")
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	b.mustRun(t, r, "write_kb_file", `{"path":"My library/notes/Example note.md","content":"# Note\n"}`)
	b.mustRun(t, r, "write_kb_file", `{"path":"my library/notes/second.md","content":"x"}`)
	b.mustRun(t, r, "edit_kb_file", `{"path":"My library/notes/second.md","append":"y"}`)
	b.mustRun(t, r, "copy_kb_files", `{"paths":["My library/notes/sample.md"],"dest":"My library/archive"}`)
	b.mustRun(t, r, "move_kb_files", `{"paths":["My library/notes/second.md"],"dest":"my library/archive"}`)
	b.mustRun(t, r, "rename_kb_file", `{"path":"My library/archive/second.md","name":"third.md"}`)
	for _, rel := range []string{"notes/Example note.md", "archive/sample.md", "archive/third.md"} {
		if !b.inMain(rel) {
			t.Errorf("%s was not written", rel)
		}
	}
	if b.inMain("My library") || b.inMain("my library") {
		t.Error("a folder named after the library was made")
	}
	if got := b.m.libraryFromPath(r, map[string]any{"path": "My library/notes/sample.md"}); got["path"] != "notes/sample.md" || got["library"] != kbpkg.PrimaryID {
		t.Errorf("a read: %v", got)
	}
	// A delete's card lists the paths in the library, and the call
	// deletes them there.
	p, err := goTools{b.m}.Prepare(r, "delete_kb_files", `{"paths":["My library/archive/third.md"]}`, "")
	if err != nil || p.Problem != "" {
		t.Fatalf("%+v %v", p, err)
	}
	var args struct{ Paths []string }
	json.Unmarshal(p.Args, &args)
	if len(args.Paths) != 1 || args.Paths[0] != "archive/third.md" {
		t.Errorf("the card lists %q", args.Paths)
	}
	b.mustRun(t, r, "delete_kb_files", `{"paths":["My library/archive/sample.md"]}`)
	if b.inMain("archive/sample.md") {
		t.Error("archive/sample.md was not deleted")
	}
}

// The main library's own name is taken off too, where it was given one;
// "My library" still means it on a Studio one person uses.
func TestTheMainLibraryByItsGivenName(t *testing.T) {
	b := newOneBed(t, "Example docs")
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	b.mustRun(t, r, "write_kb_file", `{"path":"Example docs/notes/a.md","content":"x"}`)
	b.mustRun(t, r, "write_kb_file", `{"path":"My library/notes/b.md","content":"x"}`)
	if !b.inMain("notes/a.md") || !b.inMain("notes/b.md") || b.inMain("Example docs") || b.inMain("My library") {
		t.Error("a library's name became a folder")
	}
}

// A folder that really has the library's name, at its top, is that
// folder: the path is kept whole, with or without spaces.
func TestARealFolderNamedLikeTheLibraryIsKept(t *testing.T) {
	b := newOneBed(t, "")
	os.MkdirAll(filepath.Join(b.kb, "My library", "notes"), 0o755)
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	b.mustRun(t, r, "write_kb_file", `{"path":"My library/notes/kept.md","content":"x"}`)
	if !b.inMain("My library/notes/kept.md") || b.inMain("notes/kept.md") {
		t.Error("the real folder was not used")
	}

	s := newWhereBed(t)
	s.space.dirs["space-ada:My library"] = true
	s.mustRun(t, s.req(false), "write_kb_file", `{"path":"My library/plan.md","content":"x"}`)
	if _, ok := s.space.files["space-ada:My library/plan.md"]; !ok {
		t.Errorf("the space's own My library folder was not used: %v", s.space.handed)
	}
}

// Where people sign in, a person with no space of their own names the
// shared main library by its name, which is taken off as well.
func TestTheSharedLibrarysNameWithoutASpace(t *testing.T) {
	b := newWhereBed(t)
	b.space.libs = b.space.libs[:1]
	r := b.req(true)
	b.mustRun(t, r, "write_kb_file", `{"path":"Team library/team/new.md","content":"x"}`)
	if !b.inMain("team/new.md") || b.inMain("Team library") {
		t.Error("the shared library's name became a folder")
	}
}
