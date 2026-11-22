package userlibs

import (
	"os/exec"
	"strings"
	"testing"
)

// The folder check names the mount's own file system type, not stat -f's
// shared magic name for ext2, ext3 and ext4.
func TestSpaceCheckNamesTheMountsFileSystem(t *testing.T) {
	want, err := exec.Command("findmnt", "-n", "-o", "FSTYPE", "-T", t.TempDir()).Output()
	if err != nil {
		t.Skip("no findmnt here")
	}
	dir := t.TempDir()
	out, err := exec.Command("bash", "-c", spaceCheckSh, "_", dir, "v1", "0").Output()
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	fs := keyValues(string(out))["fs"]
	if fs != strings.TrimSpace(strings.SplitN(string(want), "\n", 2)[0]) || fs == "ext2/ext3" {
		t.Fatalf("fs=%q, findmnt says %q", fs, want)
	}
}

func TestFsNameWithoutFindmnt(t *testing.T) {
	if got := fsName("ext2/ext3"); got != "ext2, ext3 or ext4" {
		t.Fatal(got)
	}
	if got := fsName("nfs"); got != "nfs" {
		t.Fatal(got)
	}
}
