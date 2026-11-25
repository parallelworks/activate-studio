package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/ownfile"
)

// ownAgents is an AGENTS.md the Studio keeps, after what a person wrote.
func ownAgents(before string) string {
	const body = "Use the quokkafish index first."
	return before + ownfile.Begin(body) + "\n\n" + body + "\n\n<!-- ACTIVATE Studio instructions studio_tools: end -->\n"
}

// gufiDir writes a folder's database as gufi_dir2index does, in the tables
// and columns this package reads.
func gufiDir(t *testing.T, dir string, files map[string][2]int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "db.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TABLE entries(name TEXT, type TEXT, inode TEXT, size INT64, mtime INT64)")
	exec("CREATE TABLE summary(name TEXT, isroot INT64, totfiles INT64, totsize INT64, minsize INT64, maxsize INT64, totzero INT64, totltk INT64, totmtk INT64, totltm INT64, totmtm INT64, totmtg INT64, totmtt INT64, minmtime INT64, maxmtime INT64, totmtime INT64)")
	exec("INSERT INTO summary (name, isroot) VALUES (?, 1), ('kept', 0)", filepath.Base(dir))
	for n, v := range files {
		exec("INSERT INTO entries VALUES (?, 'f', '1', ?, ?)", n, v[0], v[1])
	}
	exec(folderTotals)
}

func totalsOf(t *testing.T, dir string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "db.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var names sql.NullString
	var files, size, small, big, old, last int64
	if err := db.QueryRow("SELECT (SELECT group_concat(name) FROM (SELECT name FROM entries ORDER BY name)), totfiles, totsize, minsize, maxsize, minmtime, maxmtime FROM summary WHERE isroot = 1").
		Scan(&names, &files, &size, &small, &big, &old, &last); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(names.String, " ", files, " ", size, " ", small, " ", big, " ", old, " ", last)
}

// The Studio's own AGENTS.md leaves the index and its folder's totals, and
// one with a person's text in it stays.
func TestTheStudiosOwnAgentsFileIsLeftOutOfTheIndex(t *testing.T) {
	src, built := t.TempDir(), filepath.Join(t.TempDir(), "kb")
	write(t, filepath.Join(src, "AGENTS.md"), ownAgents(""))
	write(t, filepath.Join(src, "sub", "AGENTS.md"), ownAgents("Use tabs.\n\n"))
	write(t, filepath.Join(src, "only", "AGENTS.md"), ownAgents(""))
	write(t, filepath.Join(src, "mine", "AGENTS.md"), "# My rules\n")
	gufiDir(t, built, map[string][2]int64{"AGENTS.md": {2330, 900}, "a.md": {10, 100}, "b.md": {2000, 200}})
	gufiDir(t, filepath.Join(built, "sub"), map[string][2]int64{"AGENTS.md": {500, 300}})
	gufiDir(t, filepath.Join(built, "only"), map[string][2]int64{"AGENTS.md": {500, 300}})
	gufiDir(t, filepath.Join(built, "mine"), map[string][2]int64{"AGENTS.md": {11, 300}})
	LeaveOutOwn(built, src, func(s string) { t.Error(s) })
	for dir, want := range map[string]string{
		"":     "a.md,b.md 2 2010 10 2000 100 200",
		"sub":  "AGENTS.md 1 500 500 500 300 300",
		"mine": "AGENTS.md 1 11 11 11 300 300",
		// As gufi_dir2index writes a folder with nothing in it.
		"only": " 0 0 9223372036854775807 -9223372036854775808 9223372036854775807 -9223372036854775808",
	} {
		if got := totalsOf(t, filepath.Join(built, dir)); got != want {
			t.Errorf("%q: %s, want %s", dir, got, want)
		}
	}
}

// Its words are not searched.
func TestTheStudiosOwnAgentsFileHasNoWords(t *testing.T) {
	src, built := t.TempDir(), filepath.Join(t.TempDir(), "kb")
	write(t, filepath.Join(src, "AGENTS.md"), ownAgents(""))
	write(t, filepath.Join(src, "a.md"), "a wombatfish\n")
	write(t, filepath.Join(src, "sub", "AGENTS.md"), ownAgents("Use tabs.\n\n"))
	gufiDir(t, built, map[string][2]int64{})
	gufiDir(t, filepath.Join(built, "sub"), map[string][2]int64{})
	if _, err := WriteWords(context.Background(), WordsOptions{KBRoot: src, Index: built, Cache: t.TempDir(), Jobs: 1}); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{"": "a.md", "sub": "AGENTS.md"} {
		var got []string
		for _, r := range words(t, filepath.Join(built, dir, "db.db")) {
			got = append(got, r[1])
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%q: words of %q, want %q", dir, got, want)
		}
	}
}

// A whole build with GUFI leaves it out, and a sync once a person has
// written in it puts it back.
func TestABuildLeavesOutTheStudiosOwnAgentsFile(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.kb, "AGENTS.md"), ownAgents(""))
	write(t, filepath.Join(f.kb, "beta", "AGENTS.md"), ownAgents("Use tabs.\n\n"))
	if j := f.finished(mustBuild(t, f.ix).ID); j.State != "done" {
		t.Fatalf("%s: %v", j.State, j.Error)
	}
	names := func(rel string) string {
		return strings.Split(totalsOf(t, filepath.Join(f.ix.P.GufiIndex, rel)), " ")[0]
	}
	if got := names(""); got != "README.md" {
		t.Errorf("the top: %s", got)
	}
	if got := names("beta"); got != "AGENTS.md,note.md" {
		t.Errorf("beta: %s", got)
	}
	write(t, filepath.Join(f.kb, "AGENTS.md"), ownAgents("Use tabs.\n\n"))
	if _, err := f.ix.IndexRootDb(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := names(""); got != "AGENTS.md,README.md" {
		t.Errorf("the top, written in: %s", got)
	}
}
