package extdb

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func join(cs []Chunk) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, "\n\n")
}

func TestSplitParagraphsComeBackWhole(t *testing.T) {
	var paras []string
	for i := 0; i < 40; i++ {
		paras = append(paras, strings.Repeat("word"+string(rune('a'+i%26))+" ", 10+(i%10)*7))
	}
	text := strings.Join(paras, "\n\n")
	cs := Split(text, 500)
	if len(cs) < 10 {
		t.Fatalf("want many chunks, got %d", len(cs))
	}
	if got := join(cs); got != text {
		t.Fatalf("rejoined text differs:\n%q\n%q", got[:100], text[:100])
	}
	r := []rune(text)
	for i, c := range cs {
		if c.Seq != i {
			t.Errorf("chunk %d has sequence %d", i, c.Seq)
		}
		if n := utf8.RuneCountInString(c.Text); n > 500 || n != c.End-c.Start {
			t.Errorf("chunk %d: %d characters, positions %d to %d", i, n, c.Start, c.End)
		}
		if string(r[c.Start:c.End]) != c.Text {
			t.Errorf("chunk %d's positions do not give its text", i)
		}
	}
}

func TestSplitLongLinesAndWhiteSpace(t *testing.T) {
	// No blank lines or line ends: cut at spaces, never mid-word.
	text := strings.Repeat("ünïcode ", 1000)
	for _, c := range Split(text, 300) {
		if utf8.RuneCountInString(c.Text) > 300 || strings.HasPrefix(c.Text, " ") {
			t.Fatalf("bad chunk %q", c.Text)
		}
	}
	// No spaces at all: hard cuts at the limit.
	if cs := Split(strings.Repeat("x", 1001), 500); len(cs) != 3 || cs[2].Text != "x" {
		t.Fatalf("hard cuts: %+v", cs)
	}
	// White space alone makes no chunk; short text is one.
	if cs := Split(" \n\n\t ", 500); len(cs) != 0 {
		t.Fatalf("white space gave %+v", cs)
	}
	if cs := Split("short", 0); len(cs) != 1 || cs[0].Text != "short" || cs[0].End != 5 {
		t.Fatalf("short: %+v", cs)
	}
}

func TestFileUUID(t *testing.T) {
	k := Key{Inode: 123456789, Mtime: 1700000000, ExtraInfo: ""}
	u := k.FileUUID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(u) {
		t.Fatalf("not a version 5 UUID: %s", u)
	}
	if u != k.FileUUID() {
		t.Fatal("not stable")
	}
	for _, o := range []Key{{123456789, 1700000001, ""}, {123456788, 1700000000, ""}, {123456789, 1700000000, "dev7"}} {
		if o.FileUUID() == u {
			t.Errorf("%+v has the same id as %+v", o, k)
		}
	}
}

func TestArtifact(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.db")
	a, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	k := Key{Inode: 42, Mtime: 1700000000, ExtraInfo: ""}
	text := "quartzite deposits\n\nnear zanzibar"
	if err := a.Put(File{Key: k, Size: 30, State: StateText, Reader: "text", Mimetype: "text/plain", Provenance: `{"schema_version":1}`, Chunks: Split(text, 20)}); err != nil {
		t.Fatal(err)
	}
	if err := a.Put(File{Key: Key{Inode: 43, Mtime: 5}, State: StateFailed, Reader: "downmark", Err: "broken"}); err != nil {
		t.Fatal(err)
	}
	// Put again: the old rows go.
	if err := a.Put(File{Key: k, Size: 30, State: StateText, Reader: "text", Chunks: Split("zanzibar only", 100)}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	a, err = Create(p)
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.States()
	if err != nil || len(st) != 2 || st[k] != StateText || st[Key{Inode: 43, Mtime: 5}] != StateFailed {
		t.Fatalf("states %v %v", st, err)
	}
	a.Close()

	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow("SELECT count(*) FROM ext_bm25").Scan(&n)
	if n != 1 {
		t.Errorf("want the second put's one chunk, got %d", n)
	}
	// The views read as GUFI's queries read them.
	var content, route string
	var rank float64
	if err := db.QueryRow("SELECT content, asset_route, rank FROM ext_bm25_v WHERE ext_bm25_v MATCH 'zanzibar'").Scan(&content, &route, &rank); err != nil {
		// A view over fts5 is matched through its table.
		if err := db.QueryRow("SELECT content, asset_route, rank FROM ext_bm25_v WHERE content IN (SELECT content FROM ext_bm25 WHERE ext_bm25 MATCH 'zanzibar')").Scan(&content, &route, &rank); err != nil {
			t.Fatal(err)
		}
	}
	if content != "zanzibar only" || route != "text" {
		t.Errorf("view gave %q %q", content, route)
	}
	// GUFI main's views: a nested assets_json per file, and the array and
	// flat views renamed _sv.
	var assets int
	var js string
	if err := db.QueryRow("SELECT asset_count, assets_json FROM ext_assets_by_file_v WHERE inode = 42").Scan(&assets, &js); err != nil || assets != 1 ||
		!strings.Contains(js, `"asset_id":"`+k.FileUUID()+`"`) || !strings.Contains(js, `"component_type":"file"`) {
		t.Errorf("by file view: %d %s %v", assets, js, err)
	}
	var ids string
	if err := db.QueryRow("SELECT asset_ids FROM ext_assets_by_file_sv WHERE inode = 42").Scan(&ids); err != nil || !strings.Contains(ids, k.FileUUID()) {
		t.Errorf("array view: %s %v", ids, err)
	}
	var flat string
	if err := db.QueryRow("SELECT component_types FROM ext_assets_by_file_flat_sv WHERE inode = 42").Scan(&flat); err != nil || flat != "file" {
		t.Errorf("flat view: %q %v", flat, err)
	}
}

// An artifact of the schema before GUFI main's gets the new views when
// it is opened again.
func TestOldViewsReplaced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"CREATE TABLE ext_assets (inode INTEGER NOT NULL,mtime INTEGER NOT NULL,extra_info TEXT,outer_file_uuid TEXT NOT NULL,asset_id TEXT PRIMARY KEY,component_type TEXT NOT NULL,member_path_json TEXT NOT NULL,file_metadata TEXT,indexing_provenance TEXT,mimetype TEXT,asset_route TEXT)",
		"CREATE VIEW ext_assets_by_file_v AS SELECT inode, json_group_array(asset_id) AS asset_ids FROM ext_assets GROUP BY inode",
		"CREATE VIEW ext_assets_by_file_flat_v AS SELECT inode, asset_ids FROM ext_assets_by_file_v",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	a, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	db, _ = sql.Open("sqlite", p)
	defer db.Close()
	var n int
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('ext_assets_by_file_flat_v')").Scan(&n)
	if n != 0 {
		t.Error("the old flat view is still there")
	}
	if _, err := db.Exec("SELECT assets_json FROM ext_assets_by_file_v"); err != nil {
		t.Errorf("the by file view is not GUFI main's: %v", err)
	}
}

func TestPutNeedsState(t *testing.T) {
	a, err := Create(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Put(File{Key: Key{Inode: 1}}); err == nil {
		t.Fatal("a file with no state was put")
	}
}
