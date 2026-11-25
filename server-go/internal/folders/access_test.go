//go:build !windows

package folders

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func access(t *testing.T, root, rel string) (Access, bool) {
	t.Helper()
	out, err := exec.Command("sh", "-c", AccessScript, "studio-access", root, rel).Output()
	if err != nil {
		t.Fatal(err)
	}
	return ParseAccess(string(out))
}

func TestAccessScript(t *testing.T) {
	root, out := filepath.Join(t.TempDir(), "my library"), t.TempDir()
	os.MkdirAll(filepath.Join(root, "runs", "-n odd"), 0o755)
	os.WriteFile(filepath.Join(root, "runs", "a.md"), []byte("a"), 0o644)
	os.Symlink(out, filepath.Join(root, "away"))
	os.Symlink("runs", filepath.Join(root, "near"))
	os.Symlink(root, filepath.Join(out, "link"))
	// Beside the library, with its name as a start.
	os.MkdirAll(root+"-2", 0o755)
	os.Symlink(root+"-2", filepath.Join(root, "beside"))
	for _, c := range []struct {
		root, rel string
		ok, file  bool
	}{
		{root, "", true, false},
		{root, "runs", true, false},
		{root, "runs/-n odd", true, false},
		{root, "near", true, false},
		{filepath.Join(out, "link"), "runs", true, false},
		{root, "runs/a.md", true, true},
		{"/", "", true, false},
		{root, "gone", false, false},
		{root, "gone/a.md", false, false},
		{root, "away", false, false},
		{root, "beside", false, false},
		{root, "../" + filepath.Base(root) + "-2", false, false},
		{root, "$(touch " + out + "/ran)", false, false},
		{filepath.Join(root, "gone"), "", false, false},
	} {
		a, ok := access(t, c.root, c.rel)
		if ok != c.ok || a.File != c.file || ok && a.Ino == "" {
			t.Errorf("%s %q: %+v %v", c.root, c.rel, a, ok)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "ran")); err == nil {
		t.Error("a path ran")
	}
	one, _ := access(t, root, "runs")
	two, _ := access(t, filepath.Join(out, "link"), "runs/a.md")
	if one.Ino != two.Ino || !one.Write {
		t.Errorf("%+v, %+v", one, two)
	}
	if os.Geteuid() == 0 {
		return
	}
	os.Chmod(filepath.Join(root, "runs"), 0o555)
	if a, ok := access(t, root, "runs"); !ok || a.Write {
		t.Errorf("read only: %+v %v", a, ok)
	}
	for _, mode := range []os.FileMode{0o111, 0o444, 0} {
		os.Chmod(filepath.Join(root, "runs"), mode)
		for _, rel := range []string{"runs", "runs/a.md"} {
			if a, ok := access(t, root, rel); ok {
				t.Errorf("%o %s: %+v", mode, rel, a)
			}
		}
	}
	os.Chmod(filepath.Join(root, "runs"), 0o755)
}
