//go:build !windows

package posixaccess

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"
)

type info struct {
	mode     fs.FileMode
	uid, gid uint32
}

func (i info) Name() string       { return "x" }
func (i info) Size() int64        { return 0 }
func (i info) Mode() fs.FileMode  { return i.mode }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return i.mode.IsDir() }
func (i info) Sys() any           { return &syscall.Stat_t{Uid: i.uid, Gid: i.gid} }

func TestShared(t *testing.T) {
	dir := func(m fs.FileMode, uid, gid uint32) info { return info{fs.ModeDir | m, uid, gid} }
	file := func(m fs.FileMode, uid, gid uint32) info { return info{m, uid, gid} }
	cases := []struct {
		name string
		d, f info
		want bool
	}{
		{"open folder, open file", dir(0o755, 0, 0), file(0o644, 0, 0), true},
		{"open folder, root only file", dir(0o755, 0, 0), file(0o600, 0, 0), false},
		{"open folder, group file", dir(0o755, 0, 0), file(0o640, 0, 100), false},
		{"open folder, owner class cannot read", dir(0o755, 0, 0), file(0o044, 5, 0), false},
		{"group folder, same group file", dir(0o750, 0, 100), file(0o640, 0, 100), true},
		{"group folder, other group file", dir(0o750, 0, 100), file(0o640, 0, 200), false},
		{"group folder, owner only file", dir(0o750, 0, 100), file(0o600, 0, 100), false},
		{"group folder, world readable file", dir(0o750, 0, 100), file(0o644, 7, 200), true},
		{"group folder owned by a user who cannot read", dir(0o750, 5, 100), file(0o440, 6, 100), false},
		{"group folder owned by the file owner", dir(0o750, 5, 100), file(0o440, 5, 100), true},
		{"owner folder, owner file", dir(0o700, 5, 5), file(0o600, 5, 5), true},
		{"owner folder, someone else's file", dir(0o700, 5, 5), file(0o600, 6, 5), false},
		{"root folder", dir(0o700, 0, 0), file(0o600, 6, 6), true},
		{"nobody enters", dir(0o000, 5, 5), file(0o600, 6, 6), true},
	}
	for _, c := range cases {
		if got := Shared(c.d, c.f); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFileLevelSetting(t *testing.T) {
	t.Setenv(Env, "file")
	if !FileLevel() {
		t.Fatal("file: want true")
	}
	t.Setenv(Env, "folder")
	if FileLevel() {
		t.Fatal("folder: want false")
	}
	t.Setenv(Env, "")
	if FileLevel() != (os.Geteuid() == 0) {
		t.Fatal("unset: want true only as root")
	}
}

func TestPrivateDir(t *testing.T) {
	d := t.TempDir() + "/cache"
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := PrivateDir(d); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(d)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
}
