package extractfiles

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/extdb"
	"github.com/parallelworks/activate-studio/server-go/internal/extract"
)

func TestParseLine(t *testing.T) {
	e, err := ParseLine("12\x1e1700000000\x1e30\x1eidx/kb/a\x1eb.txt\r", DefaultDelim)
	if err != nil || e.Inode != 12 || e.Mtime != 1700000000 || e.Size != 30 || e.Path != "idx/kb/a\x1eb.txt" {
		t.Fatalf("%+v %v", e, err)
	}
	if e, err := ParseLine("12\t5\t\tx", '\t'); err != nil || e.Size != -1 {
		t.Fatalf("an empty size: %+v %v", e, err)
	}
	for _, bad := range []string{"12\t5\tx", "a\t5\t1\tx", "-1\t5\t1\tx", "1\tb\t1\tx", "1\t5\t-2\tx", "1\t5\t1\t"} {
		if _, err := ParseLine(bad, '\t'); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestShardAndDelim(t *testing.T) {
	s, err := ParseShard("2/4")
	if err != nil || s != (Shard{2, 4}) || !s.Has(6) || s.Has(7) {
		t.Fatalf("%+v %v", s, err)
	}
	for _, bad := range []string{"4/4", "-1/4", "1", "a/b", "0/0"} {
		if _, err := ParseShard(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if !(Shard{}).Has(99) {
		t.Error("the zero shard leaves files out")
	}
	for in, want := range map[string]byte{`\x1e`: 0x1e, `\t`: '\t', "|": '|', ",": ','} {
		if d, err := ParseDelim(in); err != nil || d != want {
			t.Errorf("%q: %q %v", in, d, err)
		}
	}
	for _, bad := range []string{"", "ab", `\n`, `\q`} {
		if _, err := ParseDelim(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestPaths(t *testing.T) {
	cases := [][3]string{{"idx/kb/a/b.txt", "idx/kb", "a/b.txt"}, {"idx/kb/a/b.txt", "idx/kb/", "a/b.txt"}, {"idx/kbx/a", "idx/kb", "idx/kbx/a"}, {"a", "", "a"}}
	for _, c := range cases {
		if got := stripPath(c[0], c[1]); got != c[2] {
			t.Errorf("stripPath(%q, %q) = %q", c[0], c[1], got)
		}
	}
	for in, want := range map[string]string{"/abs/x.pdf": "abs/x.pdf", "../../etc/x": "etc/x", "a/./b": "a/b"} {
		if got := cacheRel(in); got != want {
			t.Errorf("cacheRel(%q) = %q", in, got)
		}
	}
}

func docx(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`,
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tree writes files under root and returns list lines for them, as
// gufi_query prints them for an index whose top folder is "ix/top". The
// inodes are made up (the local source does not read them), so the test
// runs the same on every system.
func tree(t *testing.T, root string, files map[string][]byte) []string {
	t.Helper()
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var lines []string
	for i, n := range names {
		p := filepath.Join(root, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, files[n], 0o644); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(p)
		ino := 1000 + i
		lines = append(lines, fmt.Sprintf("%d\x1e%d\x1e%d\x1eix/top/%s", ino, st.ModTime().Unix(), st.Size(), n))
	}
	return lines
}

func newX(t *testing.T) *extract.Extractor {
	t.Helper()
	x, err := extract.New(extract.Options{Tools: &extract.Tools{}, Jobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func run(t *testing.T, x *extract.Extractor, root, out string, lines []string, mod func(*Options)) Summary {
	t.Helper()
	o := Options{List: strings.NewReader(strings.Join(lines, "\n") + "\n"), Total: len(lines), Delim: DefaultDelim,
		Source: Local{Root: root, Strip: "ix/top"}, Strip: "ix/top", Out: out, Jobs: 2}
	if mod != nil {
		mod(&o)
	}
	s, err := Run(context.Background(), x, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func states(t *testing.T, art string) map[int64]string {
	t.Helper()
	db, err := sql.Open("sqlite", art)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT inode, state FROM studio_extract_files")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var i int64
		var s string
		rows.Scan(&i, &s)
		out[i] = s
	}
	return out
}

func inode(line string) int64 {
	e, _ := ParseLine(line, DefaultDelim)
	return e.Inode
}

func TestRun(t *testing.T) {
	root := t.TempDir()
	art := filepath.Join(t.TempDir(), "art.db")
	lines := tree(t, root, map[string][]byte{
		"notes/a.md":     []byte("# Field notes\n\nquartzite deposits near zanzibar\n"),
		"report.docx":    docx(t, "the budget for basalt sampling"),
		"blob.bin":       {0, 1, 2},
		"~$report.docx":  []byte("owner"),
		"changed.txt":    []byte("this file changes after it is listed"),
		"gone/x.txt":     []byte("removed before the run"),
		"empty/blank.md": []byte("   \n\n"),
	})
	os.Remove(filepath.Join(root, "gone/x.txt"))
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(root, "changed.txt"), later, later)
	lines = append(lines, "not a line")

	x := newX(t)
	var prog [][2]int
	s := run(t, x, root, art, lines, func(o *Options) {
		o.Progress = func(done, total int, _ int64) { prog = append(prog, [2]int{done, total}) }
	})
	want := map[string]int64{"text": 2, "skipped": 2, "changed": 1, "missing": 1, "empty": 1}
	for k, v := range want {
		if s.States[k] != v {
			t.Errorf("state %s: %d, want %d (%v)", k, s.States[k], v, s.States)
		}
	}
	if s.Lines != 8 || s.BadLines != 1 || s.Already != 0 || s.Chunks != 2 {
		t.Errorf("summary %+v", s)
	}
	if len(prog) == 0 || prog[len(prog)-1] != [2]int{8, 8} {
		t.Errorf("progress ended at %v", prog)
	}

	db, err := sql.Open("sqlite", art)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	var ino int64
	if err := db.QueryRow("SELECT inode, content FROM ext_bm25 WHERE ext_bm25 MATCH 'zanzibar'").Scan(&ino, &name); err != nil || !strings.Contains(name, "quartzite") || strings.Contains(name, "Field notes\n\n\n") {
		t.Fatalf("text by match: %q %v", name, err)
	}
	var mime, route, prov string
	if err := db.QueryRow("SELECT mimetype, asset_route, indexing_provenance FROM ext_assets a JOIN ext_bm25 b ON a.asset_id = b.asset_id WHERE ext_bm25 MATCH 'basalt'").Scan(&mime, &route, &prov); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	json.Unmarshal([]byte(prov), &p)
	if !strings.Contains(mime, "wordprocessingml") || route != "downmark" || p["extractor"] != "index-extract" {
		t.Errorf("docx asset: %s %s %s", mime, route, prov)
	}
	db.Close()

	// Again: everything recorded is passed by and nothing is read.
	s = run(t, x, root, art, lines, nil)
	if s.Already != 7 || len(s.States) != 0 {
		t.Errorf("second run %+v", s)
	}
	// With retry, the files without text are read again; put the
	// missing one back.
	os.MkdirAll(filepath.Join(root, "gone"), 0o755)
	gone := filepath.Join(root, "gone/x.txt")
	os.WriteFile(gone, []byte("removed before the run"), 0o644)
	e, _ := ParseLine(lines[3], DefaultDelim)
	mt := time.Unix(e.Mtime, 0)
	os.Chtimes(gone, mt, mt)
	s = run(t, x, root, art, lines, func(o *Options) { o.Retry = true })
	if s.Already != 2 || s.States["text"] != 1 || s.States["changed"] != 1 {
		t.Errorf("retry %+v", s)
	}
	if st := states(t, art); st[inode(lines[3])] != "text" {
		t.Errorf("the file put back is %q", st[inode(lines[3])])
	}
}

func TestShardsCoverTheList(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{}
	for i := 0; i < 23; i++ {
		files[fmt.Sprintf("d%d/f%02d.txt", i%3, i)] = []byte(fmt.Sprintf("file number %d", i))
	}
	lines := tree(t, root, files)
	x := newX(t)
	seen := map[int64]int{}
	for k := 0; k < 4; k++ {
		art := filepath.Join(t.TempDir(), "art.db")
		s := run(t, x, root, art, lines, func(o *Options) { o.Shard = Shard{k, 4} })
		if s.States["text"]+s.OtherShard != 23 {
			t.Errorf("shard %d: %+v", k, s)
		}
		for i := range states(t, art) {
			seen[i]++
		}
	}
	if len(seen) != 23 {
		t.Fatalf("shards covered %d of 23", len(seen))
	}
	for i, n := range seen {
		if n != 1 {
			t.Errorf("inode %d in %d shards", i, n)
		}
	}
}

func TestStopRecordsNothingHalfDone(t *testing.T) {
	root := t.TempDir()
	lines := tree(t, root, map[string][]byte{"a.txt": []byte("alpha")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	art := filepath.Join(t.TempDir(), "art.db")
	s, err := Run(ctx, newX(t), Options{List: strings.NewReader(strings.Join(lines, "\n")), Delim: DefaultDelim, Source: Local{Root: root, Strip: "ix/top"}, Strip: "ix/top", Out: art, Jobs: 1})
	if err != nil || !s.Stopped {
		t.Fatalf("%+v %v", s, err)
	}
	if st := states(t, art); len(st) != 0 {
		t.Errorf("a stopped run recorded %v", st)
	}
}

func TestMissingHelpersReported(t *testing.T) {
	root := t.TempDir()
	lines := tree(t, root, map[string][]byte{"scan.pdf": []byte("%PDF-1.4 not really"), "old.ppt": []byte("x")})
	s := run(t, newX(t), root, filepath.Join(t.TempDir(), "art.db"), lines, nil)
	got := strings.Join(s.Missing, "\n")
	if !strings.Contains(got, "tesseract: scanned pages of 1 PDFs") || !strings.Contains(got, "soffice: 1 legacy") {
		t.Errorf("missing: %q", s.Missing)
	}
	if s.Helpers["tesseract"] != "" {
		t.Errorf("helpers %v", s.Helpers)
	}
}

func TestMain_(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := Main(nil, nil, &out, &errb); rc != 2 {
		t.Errorf("no command: %d", rc)
	}
	if rc := Main([]string{"files"}, nil, &out, &errb); rc != 2 {
		t.Errorf("files without --out: %d", rc)
	}
	if rc := Main([]string{"files", "--out", "x.db", "--shard", "3/3"}, nil, &out, &errb); rc != 2 {
		t.Errorf("bad shard: %d", rc)
	}
	out.Reset()
	if rc := Main([]string{"schema"}, nil, &out, &errb); rc != 0 || !strings.Contains(out.String(), "CREATE VIRTUAL TABLE IF NOT EXISTS ext_bm25") {
		t.Errorf("schema: %d %s", rc, out.String())
	}

	root := t.TempDir()
	lines := tree(t, root, map[string][]byte{"a.txt": []byte("alpha beta")})
	list := filepath.Join(t.TempDir(), "list")
	os.WriteFile(list, []byte(strings.ReplaceAll(strings.Join(lines, "\n"), "\x1e", "\t")), 0o644)
	art, rep := filepath.Join(t.TempDir(), "a.db"), filepath.Join(t.TempDir(), "r.json")
	out.Reset()
	t.Setenv("STUDIO_EXTRACTORS", "")
	t.Setenv("INDEX_BASE", t.TempDir())
	rc := Main([]string{"files", "--list", list, "--delim", `\t`, "--out", art, "--root", root, "--strip", "ix/top", "--no-ocr", "--progress", "--report", rep}, nil, &out, &errb)
	if rc != 0 {
		t.Fatalf("rc %d: %s", rc, errb.String())
	}
	outLines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.HasPrefix(outLines[0], "@progress ") || !strings.Contains(outLines[0], `"total":1`) {
		t.Errorf("progress: %q", outLines[0])
	}
	var s Summary
	if err := json.Unmarshal([]byte(outLines[len(outLines)-1]), &s); err != nil || s.States["text"] != 1 {
		t.Errorf("summary %q %v", outLines[len(outLines)-1], err)
	}
	if b, err := os.ReadFile(rep); err != nil || !bytes.Contains(b, []byte(`"text":1`)) {
		t.Errorf("report %s %v", b, err)
	}
}

// TestGUFIRoundTrip lists a folder's index with gufi_query, extracts the
// list, loads the artifact into a spread tree and from there into the
// index's permission shards, where an --external-copy walk finds the file
// by its text.
func TestGUFIRoundTrip(t *testing.T) {
	bin := os.Getenv("STUDIO_GUFI_BIN")
	if bin == "" {
		bin = "/opt/gufi/bin"
	}
	for _, tool := range []string{"gufi_dir2index", "gufi_query"} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Skipf("no %s in %s", tool, bin)
		}
	}
	base := t.TempDir()
	src := filepath.Join(base, "top")
	tree(t, src, map[string][]byte{"a.md": []byte("quartzite deposits near zanzibar"), "b.txt": []byte("nothing to see")})
	ix := filepath.Join(base, "ix")
	os.MkdirAll(ix, 0o755)
	if out, err := exec.Command(filepath.Join(bin, "gufi_dir2index"), src, ix).CombinedOutput(); err != nil {
		t.Fatalf("dir2index: %v %s", err, out)
	}
	top := filepath.Join(ix, "top")
	list, err := exec.Command(filepath.Join(bin, "gufi_query"), "-E", "SELECT inode, mtime, size, rpath(sname, sroll)||'/'||name FROM vrpentries WHERE type = 'f';", top).Output()
	if err != nil {
		t.Fatal(err)
	}
	art := filepath.Join(base, "art.db")
	s, err := Run(context.Background(), newX(t), Options{List: bytes.NewReader(list), Total: -1, Delim: DefaultDelim, Source: Local{Root: src, Strip: top}, Strip: top, Out: art, Jobs: 2, ExtraInfo: "local"})
	if err != nil || s.States["text"] != 2 {
		t.Fatalf("%+v %v", s, err)
	}
	// Into a spread tree, and from it into the index's permission shards.
	spread := filepath.Join(base, "spread")
	var out, errb bytes.Buffer
	if rc := Main([]string{"spread", "--spread", spread, "--fsid", "local", art}, nil, &out, &errb); rc != 0 || !strings.Contains(out.String(), `"files":2`) {
		t.Fatalf("spread %d: %s %s", rc, out.String(), errb.String())
	}
	out.Reset()
	if rc := Main([]string{"shards", "--spread", spread, "--fsid", "local", "--index", top}, nil, &out, &errb); rc != 0 || !strings.Contains(out.String(), `"files":2`) {
		t.Fatalf("shards %d: %s %s", rc, out.String(), errb.String())
	}
	// The files are the folder owner's: ext_ur.db, as GUFI names it.
	shard := filepath.Join(top, extdb.ShardName(extdb.Prefix, extdb.ReadUser, int64(os.Getuid()), int64(os.Getgid())))
	if _, err := os.Stat(shard); err != nil {
		t.Fatalf("no shard %s: %v", shard, err)
	}
	got, err := exec.Command(filepath.Join(bin, "gufi_query"), "-I", "CREATE TABLE c (n TEXT);",
		"--external-copy", "ext", "INSERT INTO c SELECT e.name FROM extdb.ext_bm25 b JOIN vrpentries e ON CAST(b.inode AS INT64) = e.inode AND CAST(b.mtime AS INT64) = e.mtime WHERE ext_bm25 MATCH 'zanzibar';",
		"-E", "SELECT n FROM c;", top).Output()
	if err != nil || strings.TrimSpace(string(got)) != "a.md" {
		t.Fatalf("walk found %q %v", got, err)
	}
}
