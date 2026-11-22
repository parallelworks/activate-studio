package extract

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func perm(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// A cache an earlier version made with open folders (0755) is closed: the
// top by the next pass, and each folder an entry is in as the entry is met
// or written, since a folder's listing names the files whose text it holds.
func TestOpenCacheFoldersAreClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX modes")
	}
	root := filepath.Join(t.TempDir(), "cache")
	old := CacheFile(root, "private/plan.pdf")
	put(t, old, []byte("secret text"))
	newer := CacheFile(root, "other/notes.docx")
	os.MkdirAll(filepath.Dir(newer), 0o755)
	for _, d := range []string{root, filepath.Dir(old), filepath.Dir(newer)} {
		os.Chmod(d, 0o755)
	}
	if _, _, exists := fresh(old, time.Time{}); !exists {
		t.Fatal("entry not found")
	}
	if p := perm(t, filepath.Dir(old)); p != 0o700 {
		t.Fatalf("an old entry's folder stayed %v", p)
	}
	if err := writeEntry(newer, "text"); err != nil {
		t.Fatal(err)
	}
	if p := perm(t, filepath.Dir(newer)); p != 0o700 {
		t.Fatalf("an open folder written into stayed %v", p)
	}
	x := newX(t, Options{})
	if _, err := x.Run(context.Background(), PassOptions{KBRoot: t.TempDir(), CacheRoot: root}); err != nil {
		t.Fatal(err)
	}
	if p := perm(t, root); p != 0o700 {
		t.Fatalf("the cache top stayed %v after a pass", p)
	}
}

// enrich.py's entries are made in folders of 0700 at every level, not only
// the last (Python's mkdir gives the parents it makes the default mode),
// and the folder it writes into is closed if it was open.
func TestEnrichCacheFoldersAreClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX modes")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	indexer, _ := filepath.Abs("../../../indexer")
	root := filepath.Join(t.TempDir(), "cache")
	os.MkdirAll(filepath.Join(root, "open"), 0o755)
	os.Chmod(filepath.Join(root, "open"), 0o755)
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	script := `import sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
import enrich
r = Path(sys.argv[2])
assert enrich.write_cache(r / "a" / "b" / "c" / "x.pdf.txt", "secret")
assert enrich.write_cache(r / "open" / "y.pdf.txt", "secret")
`
	if out, err := exec.Command(py, "-I", "-c", script, indexer, root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, d := range []string{"a", "a/b", "a/b/c", "open"} {
		if p := perm(t, filepath.Join(root, d)); p != 0o700 {
			t.Errorf("%s is %v, want 0700", d, p)
		}
	}
	if p := perm(t, filepath.Join(root, "a/b/c/x.pdf.txt")); p != 0o600 {
		t.Errorf("entry is %v, want 0600", p)
	}
}
