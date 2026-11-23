package chat

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// twoLibraries is a module whose deployment has the main library and two
// more, Desktop and Projects, each with an index to answer from.
func twoLibraries(t *testing.T) *Module {
	t.Helper()
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	var defs []map[string]any
	for _, l := range []struct{ id, label string }{{"desktop", "Desktop"}, {"projects", "Projects"}} {
		ix := filepath.Join(dir, "idx", l.id)
		os.MkdirAll(ix, 0o755)
		os.WriteFile(filepath.Join(ix, "db.db"), nil, 0o644)
		defs = append(defs, map[string]any{"id": l.id, "label": l.label, "indexRoot": ix, "sourceRoot": filepath.Join(dir, "src", l.id)})
	}
	b, _ := json.Marshal(defs)
	t.Setenv("STUDIO_LIBRARIES", string(b))
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		// No GUFI: with one installed, the registry probes each library in
		// the background, and the probe wrote library-caps.json into the
		// folder after the test ended, failing its cleanup.
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "GUFI_BIN": filepath.Join(dir, "no-gufi")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	return Default
}

// TestChatLibrariesRequestShapes checks each shape the chat request's
// library field takes, and that ids no library has are dropped.
func TestChatLibrariesRequestShapes(t *testing.T) {
	m := twoLibraries(t)
	all := []string{"all"}
	for _, c := range []struct {
		name string
		in   any
		want []string
	}{
		{"absent", nil, nil},
		{"empty string", "", nil},
		{"empty list", []any{}, nil},
		{"one id", "desktop", []string{"desktop"}},
		{"the main one", "kb", []string{"kb"}},
		{"all", "all", all},
		{"all in capitals", "ALL", all},
		{"comma list", "desktop, projects", []string{"desktop", "projects"}},
		{"comma list naming each library", "kb,desktop,projects", all},
		{"array", []any{"projects", "desktop"}, []string{"projects", "desktop"}},
		{"array with all", []any{"desktop", "all"}, all},
		{"array with a repeat", []any{"desktop", "desktop"}, []string{"desktop"}},
		{"unknown id dropped", []any{"desktop", "gone"}, []string{"desktop"}},
		{"unknown id in a comma list", "gone,projects", []string{"projects"}},
		{"only unknown ids", "gone", all},
		{"not strings", []any{1.0, true}, nil},
	} {
		if got := m.chatLibraries(c.in, libraries.ViewerOf("")); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: chatLibraries(%#v) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestChatScopeSearchesTheChosenLibraries checks that search and query
// cover the libraries the chat selects, and a library the model names
// instead.
func TestChatScopeSearchesTheChosenLibraries(t *testing.T) {
	m := twoLibraries(t)
	ids := func(ctx ToolCtx, arg any) []string {
		libs, _, err := m.Corpus.Reg.Scope(scopeParam(arg, ctx), viewerOfCtx(ctx), m.primaryLabel())
		if err != nil {
			t.Fatalf("Scope: %v", err)
		}
		var out []string
		for _, l := range libs {
			out = append(out, l.ID)
		}
		return out
	}
	for _, c := range []struct {
		scope []string
		arg   any
		want  []string
	}{
		{nil, nil, []string{"kb"}},
		{[]string{"all"}, nil, []string{"kb", "desktop", "projects"}},
		{[]string{"desktop"}, nil, []string{"desktop"}},
		{[]string{"projects", "desktop"}, nil, []string{"projects", "desktop"}},
		{[]string{"kb", "projects"}, nil, []string{"kb", "projects"}},
		// The model's own library argument wins.
		{[]string{"projects", "desktop"}, "kb", []string{"kb"}},
		{[]string{"desktop"}, "all", []string{"kb", "desktop", "projects"}},
	} {
		if got := ids(ToolCtx{Library: c.scope}, c.arg); !reflect.DeepEqual(got, c.want) {
			t.Errorf("scope %q, library %v: searched %q, want %q", c.scope, c.arg, got, c.want)
		}
	}
	if got := queryLibrary([]string{"desktop", "projects"}); !reflect.DeepEqual(got, []string{"desktop", "projects"}) {
		t.Errorf("query over two: %#v", got)
	}
	if got := queryLibrary([]string{"desktop"}); got != "desktop" {
		t.Errorf("query over one: %#v", got)
	}
	if got := queryLibrary(nil); got != nil {
		t.Errorf("query over none: %#v", got)
	}
}

// TestReadsUnderSeveralLibraries checks which library a read or a listing
// works in when the model names none.
func TestReadsUnderSeveralLibraries(t *testing.T) {
	m := twoLibraries(t)
	for _, c := range []struct {
		scope []string
		want  string
	}{
		{nil, "kb"},
		{[]string{"all"}, "kb"},
		{[]string{"desktop"}, "desktop"},
		{[]string{"projects", "kb"}, "kb"},
	} {
		l, err := m.namedLibrary(nil, ToolCtx{Library: c.scope})
		if err != nil || l.ID != c.want {
			t.Errorf("scope %q: read in %q (%v), want %q", c.scope, l.ID, err, c.want)
		}
	}
	_, err := m.namedLibrary(nil, ToolCtx{Library: []string{"desktop", "projects"}})
	if err == nil || !strings.HasPrefix(err.Error(), "name one library (an id from list_libraries) to read or list; this chat searches desktop, projects") {
		t.Errorf("several without the main one: %v", err)
	}
	if l, err := m.namedLibrary("projects", ToolCtx{Library: []string{"desktop", "projects"}}); err != nil || l.ID != "projects" {
		t.Errorf("named among several: %q %v", l.ID, err)
	}
	if _, err := m.namedLibrary("all", ToolCtx{}); err == nil || err.Error() != errOneLibrary {
		t.Errorf("all named: %v", err)
	}
}

func TestLibraryScopeNoteAndCitations(t *testing.T) {
	if n := libraryScopeNote([]string{"desktop", "projects"}); !strings.Contains(n, "these libraries: desktop, projects.") {
		t.Errorf("note for two: %q", n)
	}
	for _, s := range [][]string{nil, {"all"}, {"desktop"}} {
		if n := libraryScopeNote(s); n != "" {
			t.Errorf("note for %q: %q", s, n)
		}
	}
	for _, c := range []struct {
		scope []string
		want  string
	}{{nil, ""}, {[]string{"all"}, ""}, {[]string{"desktop"}, "desktop"}, {[]string{"desktop", "projects"}, ""}} {
		if got := scopeOne(c.scope); got != c.want {
			t.Errorf("scopeOne(%q) = %q, want %q", c.scope, got, c.want)
		}
	}
	// A citation to a file no tool returned links into the chat's one
	// library, and into the main one when the chat searches several.
	ctx := &CitationContext{Known: map[string]string{}, Library: scopeOne([]string{"desktop", "projects"})}
	if got := ctx.libraryOf("notes/a.md"); got != "" {
		t.Errorf("several: %q", got)
	}
	ctx.Library = scopeOne([]string{"desktop"})
	if got := ctx.libraryOf("notes/a.md"); got != "desktop" {
		t.Errorf("one: %q", got)
	}
}
