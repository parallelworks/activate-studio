package kb

import (
	"os"
	"path/filepath"
	"testing"
)

// On a filesystem that ignores case (drvfs under WSL) the new name of a
// case-only rename finds the file itself. A hard link stands in for that
// here: two names, one file. A different file under the other case is
// still refused.
func TestRenameCaseOnly(t *testing.T) {
	kbRoot := t.TempDir()
	r := Roots{KBRoot: kbRoot, IndexBase: t.TempDir()}
	f := &Files{Roots: r, Previews: &Previews{Roots: r}}
	os.WriteFile(filepath.Join(kbRoot, "notes.md"), []byte("a"), 0o644)
	if err := os.Link(filepath.Join(kbRoot, "notes.md"), filepath.Join(kbRoot, "Notes.md")); err != nil {
		t.Skip("no hard links here")
	}
	out, err := f.Rename("notes.md", "Notes.md")
	if err != nil || len(out.Moved) != 1 || out.Moved[0].To != "Notes.md" {
		t.Fatalf("case-only rename: %v %+v", err, out)
	}
	os.WriteFile(filepath.Join(kbRoot, "other.md"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(kbRoot, "Other.md"), []byte("c"), 0o644)
	if _, err := f.Rename("other.md", "Other.md"); status(err) != 409 {
		t.Fatalf("two files: %v", err)
	}
	if caseOnly("a/x.md", "a/x.md", "", "") || caseOnly("x.md", "y.md", "", "") {
		t.Fatal("caseOnly")
	}
}
