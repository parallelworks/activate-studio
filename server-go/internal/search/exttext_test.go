package search

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

// A file whose folder has no Studio text but an external database in
// GUFI's common schema reads as its ext_bm25 chunks, in order of asset and
// sequence, and a file of the same name in a subfolder adds nothing; a
// file with no chunks there keeps the answer it had. Needs GUFI
// (STUDIO_GUFI_BIN, default /opt/gufi/bin).
func TestIndexedTextFromExternalChunks(t *testing.T) {
	bin := os.Getenv("STUDIO_GUFI_BIN")
	if bin == "" {
		bin = "/opt/gufi/bin"
	}
	for _, tool := range []string{"gufi_dir2index", gufi.ToolQuery, gufi.ToolSQLite3} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Skipf("no %s in %s", tool, bin)
		}
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	os.MkdirAll(src, 0o755)
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	for _, n := range []string{"poster.pptx", "plain.txt", "sub/poster.pptx"} {
		os.WriteFile(filepath.Join(src, n), []byte("x"), 0o644)
	}
	ix := filepath.Join(base, "ix")
	os.MkdirAll(ix, 0o755)
	if out, err := exec.Command(filepath.Join(bin, "gufi_dir2index"), src, ix).CombinedOutput(); err != nil {
		t.Fatalf("gufi_dir2index: %v %s", err, out)
	}
	root := filepath.Join(ix, "src")
	local := gufi.NewLocal(bin)
	ctx := context.Background()
	res, err := local.Exec(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Args: []string{"-d", "x", filepath.Join(root, "db.db"), "SELECT inode, mtime FROM entries WHERE name = 'poster.pptx';"}})
	if err != nil {
		t.Fatal(err)
	}
	row := gufi.ParseRows(string(res.Stdout), 2)
	if len(row) != 1 {
		t.Fatalf("poster.pptx not in the index: %q", res.Stdout)
	}
	const create = "CREATE VIRTUAL TABLE ext_bm25 USING fts5(inode UNINDEXED, mtime UNINDEXED, extra_info UNINDEXED, outer_file_uuid UNINDEXED, asset_id UNINDEXED, " +
		"chunk_sequence UNINDEXED, start_position UNINDEXED, end_position UNINDEXED, page_start UNINDEXED, page_end UNINDEXED, audio_start_sec UNINDEXED, " +
		"video_start_sec UNINDEXED, video_end_sec UNINDEXED, content);"
	chunk := func(r []string, asset string, seq int, text string) string {
		return fmt.Sprintf("INSERT INTO ext_bm25 VALUES (%s, %s, '', 'u', %s, %d, 0, %d, 0, 0, NULL, NULL, NULL, %s);", r[0], r[1], q(asset), seq, len(text), q(text))
	}
	write := func(dir string, sql ...string) {
		t.Helper()
		res, err := local.Exec(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Args: append([]string{filepath.Join(dir, "ext_all.db")}, sql...)})
		if err != nil || res.Stderr != "" {
			t.Fatalf("writing %s/ext_all.db: %v %s", dir, err, res.Stderr)
		}
	}
	write(root, create, chunk(row[0], "b-derived", 1, "second part"), chunk(row[0], "b-derived", 0, "first part"))
	res, err = local.Exec(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Args: []string{"-d", "x", filepath.Join(root, "sub", "db.db"), "SELECT inode, mtime FROM entries WHERE name = 'poster.pptx';"}})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "sub"), create, chunk(gufi.ParseRows(string(res.Stdout), 2)[0], "b-derived", 0, "the subfolder's poster"))
	e := &Engine{Local: gufi.NewRunner(local, nil, gufi.RunnerOptions{Concurrency: 2, Threads: 2}), GufiBin: bin, Getenv: func(string) string { return "" }}
	got := e.IndexedTextState(ctx, root, "", "poster.pptx")
	if got.State != "text" || got.Text != "first part\n\nsecond part" {
		t.Errorf("want the chunks in order, got %+v", got)
	}
	if other := e.IndexedTextState(ctx, root, "", "plain.txt"); other.State == "text" || strings.Contains(other.Text, "part") {
		t.Errorf("a file without chunks has no text, got %+v", other)
	}
}
