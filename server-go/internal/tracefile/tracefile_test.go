package tracefile

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type entry struct {
	path        string
	size, mtime int64
	dir         bool
}

// listing is out of order, as a store may list: files before the folders
// that hold them, a folder with no files, a duplicate, and names that
// cannot be kept.
var listing = []entry{
	{"projects/beta/raw/run1.bin", 200000, 1700000300, false},
	{"readme.txt", 4, 1700000100, false},
	{"projects/alpha/summary.txt", 22, 1700000200, false},
	{"space name/a file.txt", 3, 1700000150, false},
	{"projects/beta/plan.md", 29, 1700000250, false},
	{"projects", -1, 1800000000, true},
	{"empty", -1, 1800000000, true},
	{"projects/alpha/summary.txt", 22, 1700000200, false},
	{"../escape.txt", 1, 1, false},
	{"a/b.txt", 7, 1700000400, false},
	{"a//b.txt", 70, 1700000400, false},
	{"/readme.txt", 70, 1700000400, false},
	{"notes\n.txt", 70, 1700000400, false},
	{"tab\there/x.txt", 70, 1700000400, false},
	{"cut\x1eoff.txt", 70, 1700000400, false},
	{"del\x7f.txt", 70, 1700000400, false},
	{"nul\x00.txt", 70, 1700000400, false},
	{"line\nbreak", -1, 1800000000, true},
	{"/", -1, 1800000000, true},
	{"a//", -1, 1800000000, true},
	{strings.Repeat("x", 300) + "/f.txt", 1, 1, false},
}

func write(t *testing.T, es []entry) (string, Stats) {
	return writeLayout(t, es, false)
}

func writeLayout(t *testing.T, es []entry, noDev bool) (string, Stats) {
	t.Helper()
	w, err := New(t.TempDir(), "files", 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	w.NoDev = noDev
	defer w.Cleanup()
	for _, e := range es {
		if err := w.Add(e.path, e.size, e.mtime, e.dir); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	st, err := w.Write(&b)
	if err != nil {
		t.Fatal(err)
	}
	return b.String(), st
}

type line struct {
	name, typ, ino, size, mtime, pino string
}

func parse(t *testing.T, trace string) []line {
	t.Helper()
	var out []line
	for _, l := range strings.Split(strings.TrimSuffix(trace, "\n"), "\n") {
		cols := strings.Split(l, string(Delim))
		if len(cols) != 25 || cols[24] != "" {
			t.Fatalf("want 24 columns and a closing delimiter, got %d: %q", len(cols)-1, l)
		}
		name := cols[0][4:]
		if len(name) != hexLen(t, cols[0][:4]) {
			t.Fatalf("name length %s does not match %q", cols[0][:4], name)
		}
		out = append(out, line{name, cols[1], cols[3], cols[8], cols[12], cols[23]})
	}
	return out
}

func hexLen(t *testing.T, h string) int {
	t.Helper()
	var n int
	for _, c := range h {
		n *= 16
		switch {
		case c >= '0' && c <= '9':
			n += int(c - '0')
		case c >= 'a' && c <= 'f':
			n += int(c-'a') + 10
		default:
			t.Fatalf("bad hex %q", h)
		}
	}
	return n
}

func TestStanzasOncePerFolder(t *testing.T) {
	trace, st := write(t, listing)
	ls := parse(t, trace)
	if st.Objects != 6 || st.Bytes != 200000+4+22+3+29+7 {
		t.Errorf("objects %d bytes %d", st.Objects, st.Bytes)
	}
	if st.Skipped != 13 {
		t.Errorf("want 13 skipped (escape, duplicate, long name, 4 with an empty name, 6 with a control character), got %d", st.Skipped)
	}
	seen := map[string]bool{}
	inos := map[string]string{}
	var cur string
	var dirs []string
	for _, l := range ls {
		switch l.typ {
		case "d":
			if seen[l.name] {
				t.Fatalf("folder %s written twice", l.name)
			}
			seen[l.name] = true
			cur = l.name
			inos[l.name] = l.ino
			dirs = append(dirs, l.name)
			if parent := filepath.Dir(l.name); l.name != "files" && inos[parent] != l.pino {
				t.Errorf("%s: parent inode %s, want %s", l.name, l.pino, inos[parent])
			}
		case "f":
			if filepath.Dir(l.name) != cur {
				t.Errorf("file %s in the stanza of %s", l.name, cur)
			}
			if l.pino != inos[cur] {
				t.Errorf("file %s: parent inode %s, want %s", l.name, l.pino, inos[cur])
			}
		default:
			t.Fatalf("type %q", l.typ)
		}
	}
	want := []string{"files", "files/a", "files/empty", "files/projects", "files/projects/alpha", "files/projects/beta", "files/projects/beta/raw", "files/space name"}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Errorf("folders %v, want %v", dirs, want)
	}
	if !sort.StringsAreSorted(dirs) {
		t.Errorf("folders not in order: %v", dirs)
	}
	for _, l := range ls {
		if l.name == "files/projects/beta" && l.mtime != "1700000250" {
			t.Errorf("a folder takes its newest file's time, got %s", l.mtime)
		}
		if l.name == "files/empty" && l.mtime != "1800000000" {
			t.Errorf("a folder with no files takes the listing's time, got %s", l.mtime)
		}
	}
}

// Runs on disk give the same trace as one run in memory.
func TestRunsMatchMemory(t *testing.T) {
	want, _ := write(t, listing)
	old := runSize
	runSize = 2
	defer func() { runSize = old }()
	got, st := write(t, listing)
	if got != want {
		t.Errorf("runs on disk wrote a different trace:\n%q\nwant\n%q", got, want)
	}
	if st.Objects != 6 {
		t.Errorf("objects %d", st.Objects)
	}
}

func gufiBin(t *testing.T) string {
	for _, d := range []string{os.Getenv("GUFI_BIN"), "/opt/gufi/bin"} {
		if d != "" {
			if _, err := os.Stat(filepath.Join(d, "gufi_trace2index")); err == nil {
				return d
			}
		}
	}
	t.Skip("no gufi_trace2index")
	return ""
}

// GUFI builds an index from the trace that answers for every file.
func TestGufiBuildsTheTrace(t *testing.T) {
	bin := gufiBin(t)
	noDev, err := Layout(bin, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trace, _ := writeLayout(t, listing, noDev)
	dir := t.TempDir()
	tf := filepath.Join(dir, "t.trace")
	os.WriteFile(tf, []byte(trace), 0o600)
	idx := filepath.Join(dir, "idx")
	if out, err := exec.Command(filepath.Join(bin, "gufi_trace2index"), "-n", "2", tf, idx).CombinedOutput(); err != nil {
		t.Fatalf("gufi_trace2index: %v\n%s", err, out)
	}
	out, err := exec.Command(filepath.Join(bin, "gufi_query"), "-d", "|", "-E", "SELECT rpath(sname, sroll, name), size FROM vrpentries;", filepath.Join(idx, "files")).CombinedOutput()
	if err != nil {
		t.Fatalf("gufi_query: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		got = append(got, strings.TrimPrefix(l, idx+"/"))
	}
	sort.Strings(got)
	want := []string{"files/a/b.txt|7", "files/projects/alpha/summary.txt|22", "files/projects/beta/plan.md|29", "files/projects/beta/raw/run1.bin|200000", "files/readme.txt|4", "files/space name/a file.txt|3"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("index holds\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if st, err := os.Stat(filepath.Join(idx, "files", "projects")); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("index folders should be the Studio account's alone: %v %v", st.Mode(), err)
	}
}

// inodes are the inodes of a trace's entries, by path.
func inodes(t *testing.T, trace string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, l := range parse(t, trace) {
		m[l.name] = l.ino
	}
	return m
}

// An entry keeps its inode from one listing to the next, whatever is
// added before it, after it or beside it, or taken away.
func TestInodesSurviveAListing(t *testing.T) {
	first := inodes(t, func() string { s, _ := write(t, listing); return s }())
	if again := inodes(t, func() string { s, _ := write(t, listing); return s }()); len(again) != len(first) {
		t.Fatal("the same listing gave other entries")
	} else {
		for p, ino := range first {
			if again[p] != ino {
				t.Errorf("%s: %s, then %s", p, ino, again[p])
			}
		}
	}
	changed := append([]entry{
		{"aaa-first.txt", 1, 1700000000, false},
		{"projects/alpha/0-new.txt", 1, 1700000000, false},
		{"projects/aaa/new.txt", 1, 1700000000, false},
	}, listing[1:]...) // projects/beta/raw/run1.bin is gone
	second := inodes(t, func() string { s, _ := write(t, changed); return s }())
	kept := 0
	for p, ino := range first {
		if strings.HasPrefix(p, "files/projects/beta/raw") {
			// Gone, and its folder with it.
			continue
		}
		if second[p] != ino {
			t.Errorf("%s changed inode: %s, then %s", p, ino, second[p])
		}
		kept++
	}
	if kept < 10 {
		t.Fatalf("compared only %d entries", kept)
	}
	seen := map[string]string{}
	for p, ino := range second {
		if q, dup := seen[ino]; dup {
			t.Errorf("%s and %s share inode %s", p, q, ino)
		}
		seen[ino] = p
	}
	if Inode("readme.txt", false, 0) == Inode("readme.txt", true, 0) || Inode("x", false, 0) == Inode("x", false, 1) {
		t.Error("the kind or the try does not change the hash")
	}
}

// With a hash that collides often, folders never share an inode, no file
// takes a folder's, files of one folder never share one, and the result
// is the same each time.
func TestInodeCollisionsProbedApart(t *testing.T) {
	old := inodeFunc
	inodeFunc = func(rel string, isDir bool, try int) uint64 { return 2 + Inode(rel, isDir, try)%13 }
	defer func() { inodeFunc = old }()
	var es []entry
	for i := 0; i < 4; i++ {
		es = append(es, entry{path: "d" + string(rune('0'+i)), size: -1, mtime: 1, dir: true})
	}
	for i := 0; i < 6; i++ {
		es = append(es, entry{path: "f" + string(rune('a'+i)), size: 1, mtime: 1})
	}
	trace, _ := write(t, es)
	again, _ := write(t, es)
	if trace != again {
		t.Error("probing is not deterministic")
	}
	dirs, files := map[string]string{}, map[string]string{}
	for _, l := range parse(t, trace) {
		if l.typ == "d" {
			if dirs[l.ino] != "" {
				t.Errorf("folders %s and %s share %s", l.name, dirs[l.ino], l.ino)
			}
			dirs[l.ino] = l.name
			continue
		}
		if dirs[l.ino] != "" || files[l.ino] != "" {
			t.Errorf("file %s takes inode %s of %s%s", l.name, l.ino, dirs[l.ino], files[l.ino])
		}
		files[l.ino] = l.name
	}
	if len(files) != 6 || len(dirs) != 5 {
		t.Fatalf("%d files, %d folders", len(files), len(dirs))
	}
}

// GUFI keeps the trace's inodes, which use all 63 bits.
func TestGufiKeepsTheInodes(t *testing.T) {
	bin := gufiBin(t)
	noDev, err := Layout(bin, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trace, _ := writeLayout(t, listing, noDev)
	dir := t.TempDir()
	tf := filepath.Join(dir, "t.trace")
	os.WriteFile(tf, []byte(trace), 0o600)
	idx := filepath.Join(dir, "idx")
	if out, err := exec.Command(filepath.Join(bin, "gufi_trace2index"), "-n", "2", tf, idx).CombinedOutput(); err != nil {
		t.Fatalf("gufi_trace2index: %v\n%s", err, out)
	}
	out, err := exec.Command(filepath.Join(bin, "gufi_query"), "-d", "|", "-E", "SELECT rpath(sname, sroll, name), inode FROM vrpentries;", filepath.Join(idx, "files")).CombinedOutput()
	if err != nil {
		t.Fatalf("gufi_query: %v\n%s", err, out)
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p, ino, _ := strings.Cut(strings.TrimPrefix(l, idx+"/"), "|")
		// No two of these paths collide, so each has its first try.
		if want := strconv.FormatUint(Inode(strings.TrimPrefix(p, "files/"), false, 0), 10); want != ino {
			t.Errorf("%s: index has inode %s, want %s", p, ino, want)
		}
		n++
	}
	if n != 6 {
		t.Errorf("checked %d files", n)
	}
}
