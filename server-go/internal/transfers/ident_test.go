package transfers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Ident tells a file from another at its path, through the shell helper
// and where a write helper is at hand, and says what the account can read.
func TestIdent(t *testing.T) {
	root := t.TempDir()
	w := func(rel, s string) {
		t.Helper()
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("a/-b c.txt", "1")
	w("a/closed.txt", "1")
	w("shut/x.txt", "1")
	os.Symlink("-b c.txt", filepath.Join(root, "a", "to"))
	fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "h"}
	ident := func(rels ...string) map[string]PathIdent {
		t.Helper()
		got, ok, err := fs.Ident(context.Background(), rels)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		return got
	}
	got := ident("a/-b c.txt", "a", "", "none/x", "none", "a/to")
	f, d, top := got["a/-b c.txt"], got["a"], got[""]
	if len(got) != 4 || f.Type != 'f' || d.Type != 'd' || top.Type != 'd' || !f.Readable || !d.Readable || f.Ino == "" || f.Ino == d.Ino {
		t.Fatalf("%+v", got)
	}
	if got["a/to"].Type != 'l' || got["a/to"].Ino == f.Ino {
		t.Errorf("a link: %+v", got["a/to"])
	}
	// Written in place it is the same; put in its place it is not; a move
	// takes it along; a link led elsewhere is another.
	w("a/-b c.txt", "2")
	if ident("a/-b c.txt")["a/-b c.txt"] != f {
		t.Error("changed in place")
	}
	os.Rename(filepath.Join(root, "a/-b c.txt"), filepath.Join(root, "moved.txt"))
	if ident("moved.txt")["moved.txt"] != f {
		t.Error("moved")
	}
	w("a/-b c.txt", "3")
	if n := ident("a/-b c.txt", "a/to"); n["a/-b c.txt"] == f || n["a/to"].Ino == got["a/to"].Ino {
		t.Errorf("another file: %+v", n)
	}
	if os.Geteuid() != 0 {
		os.Chmod(filepath.Join(root, "a/closed.txt"), 0)
		os.Chmod(filepath.Join(root, "shut"), 0o100)
		defer os.Chmod(filepath.Join(root, "shut"), 0o755)
		n := ident("a/closed.txt", "shut", "shut/x.txt")
		if n["a/closed.txt"].Readable || n["shut"].Readable || !n["shut/x.txt"].Readable {
			t.Errorf("%+v", n)
		}
	}
	// Never outside the folder.
	os.Symlink("/", filepath.Join(root, "out"))
	if _, _, err := fs.Ident(context.Background(), []string{"out/etc"}); err == nil {
		t.Error("looked outside the folder")
	}
}
