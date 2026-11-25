package extdb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestSpreadPath(t *testing.T) {
	s := Spread{Top: "/sp", Prefix: "ext", Fsid: "store1"}
	if got := s.Path(123456789); got != filepath.FromSlash("/sp/ext/store1/89/67/45.db") {
		t.Errorf("got %s", got)
	}
	if got := s.Path(5); got != filepath.FromSlash("/sp/ext/store1/05/00/00.db") {
		t.Errorf("got %s", got)
	}
	for _, bad := range []Spread{{Top: "", Prefix: "p", Fsid: "f"}, {Top: "/t", Prefix: "a/b", Fsid: "f"}, {Top: "/t", Prefix: "p", Fsid: ".."}} {
		if bad.Check() == nil {
			t.Errorf("%+v passed", bad)
		}
	}
}

func TestShardClass(t *testing.T) {
	// GUFI's order: the folder's owner first, then its group, then others.
	cases := []struct {
		mode, uid, gid, duid, dgid int64
		want                       string
	}{
		{0o644, 1, 2, 1, 9, ReadUser},
		{0o644, 1, 2, 9, 9, ReadOther},
		{0o640, 1, 2, 9, 2, ReadGroup},
		{0o640, 1, 2, 9, 3, ReadNone},
		{0o600, 1, 2, 1, 9, ReadUser},
		{0o600, 1, 2, 7, 9, ReadNone},
		{0o000, 1, 2, 1, 2, ReadNone},
	}
	for _, c := range cases {
		if got := ShardClass(c.mode, c.uid, c.gid, c.duid, c.dgid); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
	for class, want := range map[string]string{ReadUser: "ext_ur.db", ReadGroup: "ext_gr.db", ReadOther: "ext_or.db", ReadNone: "ext_1000_100.db"} {
		if got := ShardName(Prefix, class, 1000, 100); got != want {
			t.Errorf("%q: %s, want %s", class, got, want)
		}
	}
}

func artifactWith(t *testing.T, files map[Key]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.db")
	a, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	for k, text := range files {
		if err := a.Put(File{Key: k, State: StateText, Reader: "text", Chunks: Split(text, 100)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeDir writes a db.db with GUFI's columns that FillShards reads.
func fakeDir(t *testing.T, dir string, duid, dgid int64, files [][5]int64) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	db, err := sql.Open("sqlite", filepath.Join(dir, "db.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []string{
		"DROP TABLE IF EXISTS summary", "DROP TABLE IF EXISTS entries",
		"CREATE TABLE summary (name TEXT, uid INT64, gid INT64, isroot INT64)",
		"CREATE TABLE entries (name TEXT, type TEXT, inode TEXT, mode INT64, uid INT64, gid INT64, mtime INT64, size INT64)",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("INSERT INTO summary VALUES ('d', ?, ?, 1)", duid, dgid)
	for _, f := range files { // inode, mtime, uid, gid, mode
		db.Exec("INSERT INTO entries VALUES (?, 'f', ?, ?, ?, ?, ?, 10)", fmt.Sprintf("f%d", f[0]), f[0], f[4], f[2], f[3], f[1])
	}
}

func shardsIn(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if e.Name() != "db.db" && !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func texts(t *testing.T, shard string) map[int64]string {
	t.Helper()
	db, err := sql.Open("sqlite", shard)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT b.inode, b.content FROM ext_bm25 b JOIN ext_assets a ON a.asset_id = b.asset_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var i int64
		var c string
		rows.Scan(&i, &c)
		out[i] += c
	}
	return out
}

func TestSpreadAndShards(t *testing.T) {
	s := Spread{Top: filepath.Join(t.TempDir(), "spread"), Prefix: Prefix, Fsid: "store1"}
	art := artifactWith(t, map[Key]string{
		{Inode: 101, Mtime: 10}: "alpha text",
		{Inode: 102, Mtime: 20}: "beta text",
		{Inode: 201, Mtime: 30}: "gamma text",
		{Inode: 999, Mtime: 40}: "not in the index",
	})
	st, err := LoadSpread(s, art)
	if err != nil || st.Files != 4 || st.Chunks != 4 || st.DBs != 4 {
		t.Fatalf("%+v %v", st, err)
	}
	for _, f := range []string{"fsid.db", "extdbprefix.db", "ext.sql"} {
		if _, err := os.Stat(filepath.Join(s.Top, f)); err != nil {
			t.Errorf("no %s at the spread top", f)
		}
	}
	// A new version of 101, and the old again: the old one is replaced,
	// not doubled, and the new one kept beside it.
	if _, err := LoadSpread(s, artifactWith(t, map[Key]string{{Inode: 101, Mtime: 11}: "alpha changed", {Inode: 101, Mtime: 10}: "alpha text"})); err != nil {
		t.Fatal(err)
	}
	if got := texts(t, s.Path(101)); len(got) != 1 || got[101] != "alpha textalpha changed" && got[101] != "alpha changedalpha text" {
		t.Errorf("spread db of 101: %v", got)
	}

	ix := t.TempDir()
	uid, gid := int64(os.Getuid()), int64(os.Getgid())
	// Another owner and group, so the classes past ur are reached.
	ou, og := uid+1000, gid+1000
	// a: 101 the folder owner's, 102 the folder group's; b: 201 another's
	// that all may read; 202 has no text.
	fakeDir(t, filepath.Join(ix, "a"), uid, gid, [][5]int64{{101, 10, uid, gid, 0o600}, {102, 20, ou, gid, 0o640}})
	fakeDir(t, filepath.Join(ix, "a", "b"), uid, gid, [][5]int64{{201, 30, ou, og, 0o644}, {202, 5, uid, gid, 0o644}})
	os.WriteFile(filepath.Join(ix, "a", ShardName(Prefix, ReadOther, 5, 5)), []byte("stale"), 0o644)
	os.WriteFile(filepath.Join(ix, "a", "other_UR_1_1.db"), []byte("not ours"), 0o644)
	// The earlier Studio's shard is removed wherever a refill finds it.
	os.WriteFile(filepath.Join(ix, "a", "studio_UR_1_1.db"), []byte("legacy"), 0o600)
	fs, err := FillShards(s, ix, "")
	if err != nil {
		t.Fatal(err)
	}
	if fs.Dirs != 2 || fs.Files != 3 || fs.Shards != 3 || fs.Removed != 1 || fs.Legacy != 1 {
		t.Errorf("%+v", fs)
	}
	ur, gr, or := ShardName(Prefix, ReadUser, uid, gid), ShardName(Prefix, ReadGroup, ou, gid), ShardName(Prefix, ReadOther, ou, og)
	if got := shardsIn(t, filepath.Join(ix, "a")); len(got) != 3 || got[0] != gr || got[1] != ur || got[2] != "other_UR_1_1.db" {
		t.Errorf("folder a: %v", got)
	}
	if got := texts(t, filepath.Join(ix, "a", ur)); len(got) != 1 || got[101] != "alpha text" {
		t.Errorf("the owner's shard: %v", got)
	}
	if st, _ := os.Stat(filepath.Join(ix, "a", ur)); st.Mode().Perm() != 0o600 {
		t.Errorf("UR mode %v", st.Mode())
	}
	if st, _ := os.Stat(filepath.Join(ix, "a", gr)); st.Mode().Perm() != 0o640 {
		t.Errorf("GR mode %v", st.Mode())
	}
	if got := texts(t, filepath.Join(ix, "a", "b", or)); len(got) != 1 || got[201] != "gamma text" {
		t.Errorf("folder b: %v", got)
	}

	// The index is rebuilt: 102 changed (no text for its new version), 101
	// is now another's that all may read. Its records move to or; ur and
	// gr go.
	fakeDir(t, filepath.Join(ix, "a"), uid, gid, [][5]int64{{101, 11, ou, og, 0o644}, {102, 21, ou, gid, 0o640}})
	if fs, err = FillShards(s, ix, ""); err != nil || fs.Removed != 2 {
		t.Fatalf("%+v %v", fs, err)
	}
	if got := shardsIn(t, filepath.Join(ix, "a")); len(got) != 2 || got[0] != or {
		t.Errorf("folder a after: %v", got)
	}
	if got := texts(t, filepath.Join(ix, "a", or)); got[101] != "alpha changed" || len(got) != 1 {
		t.Errorf("after: %v", got)
	}
}

func TestLoadSpreadSinceAndWalkIndex(t *testing.T) {
	s := Spread{Top: filepath.Join(t.TempDir(), "spread"), Prefix: Prefix, Fsid: "f"}
	p := filepath.Join(t.TempDir(), "a.db")
	a, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	a.Put(File{Key: Key{Inode: 1, Mtime: 1}, State: StateText, Chunks: Split("one", 100)})
	a.Close()
	if st, err := LoadSpreadSince(s, p, 1<<40); err != nil || st.Files != 0 {
		t.Fatalf("nothing is that new: %+v %v", st, err)
	}
	if st, err := LoadSpreadSince(s, p, 1); err != nil || st.Files != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	ix := t.TempDir()
	fakeDir(t, ix, 1, 1, [][5]int64{{7, 70, 1, 1, 0o644}})
	fakeDir(t, filepath.Join(ix, "x", "y"), 1, 1, [][5]int64{{8, 80, 1, 1, 0o644}})
	got := map[string]IndexEntry{}
	if err := WalkIndex(ix, func(dir string, fs []IndexEntry) error {
		for _, f := range fs {
			got[dir+"|"+f.Name] = f
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["|f7"].Inode != 7 || got["x/y|f8"].Mtime != 80 || got["x/y|f8"].Size != 10 {
		t.Errorf("%+v", got)
	}
}
