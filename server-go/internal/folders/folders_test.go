//go:build !windows

package folders

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tree(t *testing.T) string {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.PDF"), 10)
	write(t, filepath.Join(dir, "b/c.pdf"), 20)
	write(t, filepath.Join(dir, "b/photo.jpeg"), 5)
	write(t, filepath.Join(dir, "b/photo.one.jpg"), 5)
	write(t, filepath.Join(dir, "b/deep/README"), 1)
	write(t, filepath.Join(dir, ".hidden"), 1)
	write(t, filepath.Join(dir, "a name\twith a tab.md"), 2)
	if err := os.Symlink(filepath.Join(dir, "a.PDF"), filepath.Join(dir, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestCount: a folder's files, size and main kinds read the same here and
// by the script another computer runs.
func TestCount(t *testing.T) {
	dir := tree(t)
	want := &Count{Files: 7, Bytes: 44, Kinds: []Kind{{"jpg", 2}, {"pdf", 2}, {"md", 1}}}
	if got := CountLocal(context.Background(), dir); !reflect.DeepEqual(got, want) {
		t.Errorf("here: %+v", got)
	}
	out, err := exec.Command("sh", "-c", CountScript, "_", dir, "5").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := ParseCount(string(out))
	// A tab in a name ends it for awk: the file is counted, and not its kind.
	want.Kinds = want.Kinds[:2]
	if !reflect.DeepEqual(got, want) {
		t.Errorf("there: %+v\n%s", got, out)
	}
	if CountLocal(context.Background(), filepath.Join(dir, "none")) != nil || ParseCount("") != nil {
		t.Error("a folder that is not there is not counted")
	}
}

// TestCountStops: a folder that takes longer than the count has is what was
// seen so far, and says so.
func TestCountStops(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "find"), []byte("#!/bin/sh\ncase \"$*\" in *-prune*) exit 0;; esac\nprintf '7\\tx.csv\\n'\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", CountScript, "_", t.TempDir(), "1")
	c.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ParseCount(string(out)), (&Count{Files: 1, Bytes: 7, Kinds: []Kind{{"csv", 1}}, Partial: true}); !reflect.DeepEqual(got, want) {
		t.Errorf("%+v\n%s", got, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := CountLocal(ctx, tree(t)); got == nil || !got.Partial {
		t.Errorf("stopped here: %+v", got)
	}
}

// TestHoldsHome: what macOS is not asked to count.
func TestHoldsHome(t *testing.T) {
	for dir, want := range map[string]bool{"/Users/alex": true, "/Users": true, "/": true, "/Users/alex/Documents": false, "/data": false} {
		if holdsHome(dir, "/Users/alex") != want {
			t.Errorf("%s: %v", dir, !want)
		}
	}
}

// TestListing: a directory's folders read the same here and by the script.
func TestListing(t *testing.T) {
	dir := tree(t)
	if err := os.MkdirAll(filepath.Join(dir, "repo/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []Entry{{Name: "b"}, {Name: "repo", Repository: true}}
	if l := Local(dir).Filter(""); l.Path != dir || !reflect.DeepEqual(l.Entries, want) {
		t.Errorf("here: %+v", l)
	}
	out, _ := exec.Command("sh", "-c", Script, "_", dir).Output()
	if l := Parse(string(out)).Filter("RE"); l.Path != dir || !reflect.DeepEqual(l.Entries, want[1:]) {
		t.Errorf("there: %+v", l)
	}
	if Local(filepath.Join(dir, "none")) != nil || Local("relative") != nil || Parse("") != nil {
		t.Error("what cannot be listed is nil")
	}
}
