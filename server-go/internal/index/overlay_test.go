package index

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// overlayGufi stands in for gufi_query over a one-directory index: the
// two reads ReapplyOverlay makes, answered from the fixture's inodes.
type overlayGufi struct {
	index        string
	dirIno, file string
	fileIno      string
	queries      int
}

func (g *overlayGufi) Exec(_ context.Context, c gufi.Command) (gufi.Result, error) {
	g.queries++
	sqlText := ""
	for i, a := range c.Args {
		if a == "-E" && i+1 < len(c.Args) {
			sqlText = c.Args[i+1]
		}
	}
	const d = "\x1e"
	switch {
	case strings.Contains(sqlText, "FROM vrsummary;"):
		return gufi.Result{Stdout: []byte(g.index + d + g.dirIno + "\n")}, nil
	case strings.Contains(sqlText, "FROM vrpentries;"):
		return gufi.Result{Stdout: []byte(g.index + "/" + g.file + d + g.fileIno + "\n")}, nil
	}
	return gufi.Result{}, nil
}

func statInode(t *testing.T, p string) string {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	n := inodeOf(st)
	if n == 0 {
		t.Skip("needs inodes")
	}
	return fmt.Sprint(n)
}

// A filesystem without xattrs keeps labels in the overlay only, so a
// rebuilt index carries none. The index's change hook and the start hook
// both put them back into the index's xattrs_pwd rows.
func TestOverlayReappliedAfterChangeAndAtStart(t *testing.T) {
	t.Run("Node-free", func(t *testing.T) { overlayCase(t, map[string]string{"STUDIO_NODE": "off"}) })
	t.Run("Go indexes beside Node", func(t *testing.T) { overlayCase(t, map[string]string{"STUDIO_GO_INDEX": "1"}) })
}

func overlayCase(t *testing.T, mode map[string]string) {
	dir := t.TempDir()
	kbRoot, base := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	write(t, filepath.Join(kbRoot, "note.md"), "a note\n")
	env := map[string]string{"KB_ROOT": kbRoot, "INDEX_BASE": base, "GUFI_BIN": filepath.Join(dir, "no-gufi"),
		"SWEEP_INTERVAL_SEC": "0"}
	for k, v := range mode {
		env[k] = v
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	// The rebuilt index: the file's row, and no label.
	if err := os.MkdirAll(cfg.GufiIndex, 0o755); err != nil {
		t.Fatal(err)
	}
	fileIno, dirIno := statInode(t, filepath.Join(kbRoot, "note.md")), statInode(t, kbRoot)
	db, err := sql.Open("sqlite", filepath.Join(cfg.GufiIndex, "db.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []string{
		"CREATE TABLE summary (name TEXT, inode TEXT, isroot INT)",
		"CREATE TABLE entries (name TEXT, inode TEXT)",
		"CREATE TABLE xattrs_pwd (inode TEXT, name TEXT, value TEXT)",
		"INSERT INTO summary VALUES ('kb', '" + dirIno + "', 1)",
		"INSERT INTO entries VALUES ('note.md', '" + fileIno + "')",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	labels := func() string {
		var v sql.NullString
		db.QueryRow("SELECT value FROM xattrs_pwd WHERE inode = ? AND name = 'user.studio.tags'", fileIno).Scan(&v)
		return v.String
	}

	g := &overlayGufi{index: cfg.GufiIndex, dirIno: dirIno, file: "note.md", fileIno: fileIno}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	a.Gufi = gufi.NewRunner(g, nil, gufi.RunnerOptions{})
	m := kb.New(a)
	m.Tags.GufiAvailable = func() bool { return true }
	prev := kb.Default
	kb.Default = m
	t.Cleanup(func() { kb.Default = prev })
	if err := m.Tags.Overlay.Set("note.md", []string{"keep"}); err != nil {
		t.Fatal(err)
	}

	Register(http.NewServeMux(), a)
	ix := Of(a)

	// An index change (afterChange of a build, sweep or folder pass).
	ix.afterChange("")
	if got := labels(); got != "keep" {
		t.Fatalf("after a change the label is %q", got)
	}

	// At start: the index rebuilt again, by a deploy, before this process.
	if _, err := db.Exec("DELETE FROM xattrs_pwd"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for labels() != "keep" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := labels(); got != "keep" {
		t.Fatalf("after start the label is %q", got)
	}
	cancel()
	a.Wait(2 * time.Second)

	// With no overlay entries nothing is read from the index.
	if err := m.Tags.Overlay.Set("note.md", nil); err != nil {
		t.Fatal(err)
	}
	before := g.queries
	ix.afterChange("")
	if g.queries != before {
		t.Fatalf("an empty overlay ran %d queries", g.queries-before)
	}
}
