package kb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

func status(err error) int {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestExtNameMatchesPathExtname(t *testing.T) {
	// path.extname's answers.
	for in, want := range map[string]string{
		"a.txt": ".txt", "a.tar.gz": ".gz", ".bashrc": "", "a.": ".", "a": "",
		"dir.d/file": "", "..": "", "x/.hidden": "", "x/..y": ".y", "...": ".", "a.b/": ".b", "/": "",
	} {
		if got := ExtName(in); got != want {
			t.Errorf("ExtName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeUTF8IsWHATWG(t *testing.T) {
	// TextDecoder's maximal-subpart replacement.
	for in, want := range map[string]string{
		"ok":             "ok",
		"\xc0\x80":       "��",
		"\xe2\x82":       "�",
		"\xed\xa0\x80":   "���",
		"a\xf0\x9f\x98x": "a�x",
	} {
		if got := DecodeUTF8([]byte(in)); got != want {
			t.Errorf("DecodeUTF8(%q) = %q, want %q", in, got, want)
		}
	}
	if UTF16Len("a\U0001F600") != 3 || SliceUTF16("ab\U0001F600", 3) != "ab" {
		t.Error("UTF-16 length or slice")
	}
}

func TestSanitizeName(t *testing.T) {
	for in, want := range map[string]string{
		"report (1).pdf": "report (1).pdf",
		"a/b/c d.txt":    "c d.txt",
		"café.md":        "caf_.md",
		"\U0001F600.md":  "__.md",
		"a   b":          "a b",
	} {
		if got, err := SanitizeName(in); err != nil || got != want {
			t.Errorf("SanitizeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{".env", "   ", "/"} {
		if _, err := SanitizeName(in); status(err) != 400 {
			t.Errorf("SanitizeName(%q) should be a 400, got %v", in, err)
		}
	}
	if _, err := SanitizeRelPath(strings.Repeat("a/", 33)); status(err) != 400 {
		t.Error("33 segments should be refused")
	}
}

func TestResolveRefusesTraversal(t *testing.T) {
	base := t.TempDir()
	for _, rel := range []string{"../x", "a/../../x", "/../../etc/passwd"} {
		if _, err := Resolve(rel, base); status(err) != 400 {
			t.Errorf("Resolve(%q): %v, want a 400", rel, err)
		}
	}
	for _, rel := range []string{"", "a/b", "a/../b", "/a"} {
		if _, err := Resolve(rel, base); err != nil {
			t.Errorf("Resolve(%q): %v", rel, err)
		}
	}
}

func TestListDirSkipsBrokenLinksAndSortsUnicode(t *testing.T) {
	base := t.TempDir()
	os.Mkdir(filepath.Join(base, "z dir"), 0o755)
	for _, n := range []string{"été.md", "b.txt", "A.txt"} {
		os.WriteFile(filepath.Join(base, n), []byte("x"), 0o644)
	}
	os.Symlink(filepath.Join(base, "gone"), filepath.Join(base, "broken"))
	os.Symlink(filepath.Join(base, "b.txt"), filepath.Join(base, "link.txt"))
	got, err := ListDir("", base, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "z dir,A.txt,b.txt,été.md,link.txt" {
		t.Fatalf("entries: %v", names)
	}
	if missing, err := ListDir("nope", base, false); err != nil || len(missing) != 0 {
		t.Fatalf("a missing folder lists as empty: %v %v", missing, err)
	}
}

func TestContentDisposition(t *testing.T) {
	if got := contentDisposition(`a"b.pdf`); got != `attachment; filename="ab.pdf"` {
		t.Error(got)
	}
	got := contentDisposition("résumé.pdf")
	if got != `attachment; filename="r_sum_.pdf"; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf` {
		t.Error(got)
	}
}

// The labels-save fix: a missing path is a 404, and the index row is
// written under the inode the index has for the path, not the one on disk.
func TestApplyLabelsWritesTheIndexInode(t *testing.T) {
	kbRoot, base := t.TempDir(), t.TempDir()
	r := Roots{KBRoot: kbRoot, IndexBase: base, IndexDir: filepath.Join(base, "idx"), GufiIndex: filepath.Join(base, "gufi")}
	tags := &Tags{Roots: r, Overlay: &Overlay{Roots: r}, Indexer: NoIndexer{}}
	ctx := context.Background()
	if _, err := tags.Apply(ctx, []string{"no/such.md"}, []string{"x"}, nil); status(err) != 404 {
		t.Fatalf("missing path: %v", err)
	}
	if _, err := tags.Apply(ctx, []string{".git/config"}, []string{"x"}, nil); status(err) != 400 {
		t.Fatalf("hidden path: %v", err)
	}
	os.MkdirAll(filepath.Join(kbRoot, "docs"), 0o755)
	os.WriteFile(filepath.Join(kbRoot, "docs", "a.md"), []byte("a"), 0o644)
	os.MkdirAll(filepath.Join(r.GufiIndex, "docs"), 0o755)
	db, err := sql.Open("sqlite", filepath.Join(r.GufiIndex, "docs", "db.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []string{
		"CREATE TABLE entries (name TEXT, inode TEXT)",
		"CREATE TABLE summary (inode TEXT, isroot INT)",
		"CREATE TABLE xattrs_pwd (inode TEXT, name TEXT, value TEXT)",
		"INSERT INTO entries VALUES ('a.md', '4242')",
		"INSERT INTO summary VALUES ('77', 1)",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	out, err := tags.Apply(ctx, []string{"/docs/a.md"}, []string{"Alpha", "beta"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := out.MarshalJSON(); string(b) != `{"docs/a.md":["alpha","beta"]}` {
		t.Fatalf("updated: %s", b)
	}
	var inode, value string
	if err := db.QueryRow("SELECT inode, value FROM xattrs_pwd").Scan(&inode, &value); err != nil {
		t.Fatal(err)
	}
	if inode != "4242" || value != "alpha,beta" {
		t.Fatalf("row %s=%s", inode, value)
	}
	if got, ok := tags.Overlay.Get("docs/a.md"); xattrsSupported && ok {
		t.Fatalf("the xattr holds the labels, the overlay should not: %v", got)
	}
	tags.Apply(ctx, []string{"docs"}, []string{"folder"}, nil)
	if db.QueryRow("SELECT value FROM xattrs_pwd WHERE inode = '77'").Scan(&value); value != "folder" {
		t.Fatalf("folder row: %q", value)
	}
}

// Archives are listed as other files are; what compilers leave and what is
// still arriving are not, whether hidden files are shown or not.
func TestListDirShowsArchives(t *testing.T) {
	base := t.TempDir()
	for _, n := range []string{"a.tar", "b.tar.gz", "c.TGZ", "d.zip", "e.gz", "f.sif", "g.bin", "mod.pyc", "lib.so", "x.o", ".env", ".studio-upload-0a1b"} {
		os.WriteFile(filepath.Join(base, n), []byte("x"), 0o644)
	}
	for _, d := range []string{"sub", ".studio-upload-123", ".studio-transfer-x", ".Studio-Upload-9"} {
		os.Mkdir(filepath.Join(base, d), 0o755)
	}
	list := func(hidden bool) string {
		got, err := ListDir("", base, hidden)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range got {
			names = append(names, e.Name)
			if e.Type == "file" && e.Name != ".env" && e.Kind != KindBinary {
				t.Errorf("%s is %s", e.Name, e.Kind)
			}
		}
		return strings.Join(names, " ")
	}
	const shown = "a.tar b.tar.gz c.TGZ d.zip e.gz f.sif g.bin"
	if got := list(false); got != "sub "+shown {
		t.Errorf("listed %s", got)
	}
	if got := list(true); got != "sub .env "+shown {
		t.Errorf("with hidden files, listed %s", got)
	}
	for _, p := range []string{".studio-upload-123/data", "sub/.studio-transfer-x/a.txt", ".STUDIO-transfer-x"} {
		if _, err := Resolve(p, base); err == nil {
			t.Errorf("%s resolved", p)
		}
	}
}

func TestACopyLeavesOutWhatIsArriving(t *testing.T) {
	base := t.TempDir()
	for _, p := range []string{"run/a.txt", "run/.studio-upload-1/data", "run/deep/.studio-transfer-1/b.txt"} {
		os.MkdirAll(filepath.Dir(filepath.Join(base, p)), 0o755)
		os.WriteFile(filepath.Join(base, p), []byte("x"), 0o644)
	}
	if err := copyTree(filepath.Join(base, "run"), filepath.Join(base, "run copy")); err != nil {
		t.Fatal(err)
	}
	var got []string
	filepath.WalkDir(filepath.Join(base, "run copy"), func(p string, d os.DirEntry, err error) error {
		got = append(got, relTo(base, p))
		return err
	})
	if strings.Join(got, ",") != "run copy,run copy/a.txt,run copy/deep" {
		t.Errorf("copied %v", got)
	}
}
