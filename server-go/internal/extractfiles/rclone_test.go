package extractfiles

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/extdb"
)

// fakeRcloneCat is a shell rclone whose cat reads "remote:path" from
// dir/remote/path and logs each command line.
func fakeRcloneCat(t *testing.T) (bin, dir, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for rclone")
	}
	dir = t.TempDir()
	log = filepath.Join(dir, "log")
	bin = filepath.Join(dir, "rclone")
	script := "#!/bin/sh\necho \"$@\" >> '" + log + "'\n" +
		"[ \"$1\" = cat ] || exit 1\nfor a; do spec=$a; done\n" +
		"f='" + dir + "'/$(echo \"$spec\" | sed 's#:#/#')\n" +
		"[ -f \"$f\" ] || { echo 'ERROR : object not found' >&2; exit 4; }\ncat \"$f\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir, log
}

func TestRcloneSource(t *testing.T) {
	bin, dir, log := fakeRcloneCat(t)
	put := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	put("store/bkt/pre/a.md", "# The heron notes")
	put("store/bkt/pre/b.txt", "changed since the listing")
	put("store/bkt/pre/c.txt", "too big")
	out := filepath.Join(t.TempDir(), "a.db")
	list := strings.Join([]string{
		"1\x1e10\x1e17\x1ea.md",
		"2\x1e20\x1e3\x1eb.txt",
		"3\x1e30\x1e7000\x1ec.txt",
		"4\x1e40\x1e5\x1emissing.txt",
		"5\x1e50\x1e5\x1epic.bin",
	}, "\n")
	for _, bad := range []string{"bkt/pre", ":x", "a/b:c"} {
		if _, err := ParseRcloneRoot(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
	var stdout, stderr bytes.Buffer
	rc := Main([]string{"files", "--out", out, "--rclone", "store:bkt/pre", "--rclone-bin", bin, "--max-size", "1000", "--no-ocr"}, strings.NewReader(list), &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("%d %s %s", rc, stdout.String(), stderr.String())
	}
	a, err := extdb.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	states, _ := a.States()
	a.Close()
	want := map[int64]string{1: extdb.StateText, 2: extdb.StateChanged, 3: extdb.StateSkipped, 4: extdb.StateMissing, 5: extdb.StateSkipped}
	for ino, st := range want {
		// With no --extra-info, an rclone source's records carry its remote.
		if got := states[extdb.Key{Inode: ino, Mtime: ino * 10, ExtraInfo: "rclone:store:bkt/pre"}]; got != st {
			t.Errorf("inode %d: %s, want %s", ino, got, st)
		}
	}
	raw, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 || !strings.Contains(string(raw), "cat -- store:bkt/pre/a.md") {
		t.Errorf("one cat for each object read, none for one too big or not read: %q", lines)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(out), "object-*")); len(left) != 0 {
		t.Errorf("objects left behind: %v", left)
	}
	if rc := Main([]string{"files", "--out", out, "--rclone", "store:x", "--root", "/tmp"}, strings.NewReader(""), &stdout, &stderr); rc != 2 {
		t.Errorf("--root with --rclone: %d", rc)
	}
}
