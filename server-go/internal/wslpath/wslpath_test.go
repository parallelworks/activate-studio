package wslpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFromWindows(t *testing.T) {
	Set(true, "/mnt/")
	t.Cleanup(func() { Set(false, "/mnt/") })
	for in, want := range map[string]string{
		`C:\Users\me\Documents`:               "/mnt/c/Users/me/Documents",
		`d:/data/projects/`:                   "/mnt/d/data/projects",
		`C:`:                                  "/mnt/c",
		`C:\`:                                 "/mnt/c",
		` E:\Shared Files\a b `:               "/mnt/e/Shared Files/a b",
		`\\wsl.localhost\Ubuntu-24.04\home\me`: "/home/me",
		`\\wsl$\Ubuntu\home\me\notes\`:        "/home/me/notes",
		`\\WSL.LOCALHOST\Ubuntu\`:             "/",
	} {
		if got, ok := FromWindows(in); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"/home/me", "~/docs", "notes", `\\server\share\x`, "C", "C:x", "1:/a"} {
		if got, ok := FromWindows(in); ok || got != in {
			t.Errorf("%q changed to %q", in, got)
		}
	}
	Set(true, "/win/")
	if got, _ := FromWindows(`C:\x`); got != "/win/c/x" {
		t.Errorf("automount root: %q", got)
	}
	Set(false, "/mnt/")
	if got, ok := FromWindows(`C:\Users`); ok || got != `C:\Users` {
		t.Errorf("outside WSL: %q %v", got, ok)
	}
}

func TestAutomountRoot(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "wsl.conf")
	os.WriteFile(conf, []byte("[boot]\nsystemd=true\n[automount]\nenabled = true\nroot = \"/drives\"\n"), 0o644)
	if r := automountRoot(conf); r != "/drives/" {
		t.Fatalf("%q", r)
	}
	if r := automountRoot(filepath.Join(dir, "none")); r != "/mnt/" {
		t.Fatalf("%q", r)
	}
}
