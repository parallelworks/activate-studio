package libraries_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// A connected index whose text is in an external database beside its
// db.db (GUFI's common schema's ext_bm25, or the earlier ext_bm25_by_file)
// has full text, as one with the Studio's words table does; an external
// database with neither, or none at all, leaves it without. Needs GUFI
// (STUDIO_GUFI_BIN, default /opt/gufi/bin).
func TestProbeFindsExternalText(t *testing.T) {
	bin := os.Getenv("STUDIO_GUFI_BIN")
	if bin == "" {
		bin = "/opt/gufi/bin"
	}
	if _, err := os.Stat(filepath.Join(bin, gufi.ToolSQLite3)); err != nil {
		t.Skipf("no %s in %s", gufi.ToolSQLite3, bin)
	}
	local := gufi.NewLocal(bin)
	ctx := context.Background()
	sqlite := func(db string, sql ...string) {
		t.Helper()
		res, err := local.Exec(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Args: append([]string{db}, sql...)})
		if err != nil || res.Stderr != "" {
			t.Fatalf("%s: %v %s", db, err, res.Stderr)
		}
	}
	dir := t.TempDir()
	index := func(name string, ext ...string) string {
		root := filepath.Join(dir, name)
		os.MkdirAll(root, 0o755)
		sqlite(filepath.Join(root, "db.db"), "CREATE TABLE entries (name TEXT, inode TEXT);")
		if len(ext) > 0 {
			sqlite(filepath.Join(root, "ext_all.db"), ext...)
		}
		return root
	}
	reg := libraries.New(libraries.Options{KBRoot: filepath.Join(dir, "kb"), IndexBase: filepath.Join(dir, "ix"),
		GufiIndex: filepath.Join(dir, "ix", "gufi", "kb"), GufiBin: bin,
		Runner: gufi.NewRunner(local, nil, gufi.RunnerOptions{Concurrency: 2, Threads: 2}),
		Getenv: func(string) string { return "" }})
	for _, c := range []struct {
		name string
		ext  []string
		want bool
	}{
		{"common", []string{"CREATE VIRTUAL TABLE ext_bm25 USING fts5(inode UNINDEXED, mtime UNINDEXED, content);"}, true},
		{"earlier", []string{"CREATE VIRTUAL TABLE ext_bm25_by_file USING fts5(binode UNINDEXED, bmtime UNINDEXED, content);"}, true},
		{"assets-only", []string{"CREATE TABLE ext_assets (inode INTEGER, mtime INTEGER);"}, false},
		{"none", nil, false},
	} {
		caps, err := reg.Probe(ctx, index(c.name, c.ext...))
		if err != nil {
			t.Fatal(err)
		}
		if !caps.Index || caps.FullText != c.want {
			t.Errorf("%s: want index and fullText %v, got %+v", c.name, c.want, caps)
		}
	}
	reg.CountedForTests()
}
