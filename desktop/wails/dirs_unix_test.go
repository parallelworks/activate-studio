//go:build !windows && !darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestXDGDownloadDir(t *testing.T) {
	home := "/home/someone"
	dir := t.TempDir()
	file := filepath.Join(dir, "user-dirs.dirs")
	for _, c := range []struct{ line, want string }{
		{`XDG_DOWNLOAD_DIR="$HOME/Downloads"`, "/home/someone/Downloads"},
		{`XDG_DOWNLOAD_DIR="$HOME/Téléchargements/"`, "/home/someone/Téléchargements"},
		{`XDG_DOWNLOAD_DIR="/data/dl"`, "/data/dl"},
		// The home folder itself, as xdg-user-dirs writes a folder not set.
		{`XDG_DOWNLOAD_DIR="$HOME/"`, "/home/someone/Downloads"},
		{`XDG_DOWNLOAD_DIR="$HOME"`, "/home/someone/Downloads"},
		{`XDG_DOWNLOAD_DIR=""`, "/home/someone/Downloads"},
		{`XDG_MUSIC_DIR="$HOME/Music"`, "/home/someone/Downloads"},
	} {
		if err := os.WriteFile(file, []byte("# written by xdg-user-dirs-update\n"+c.line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := xdgDownloadDir(file, home); got != c.want {
			t.Errorf("%s: got %s, want %s", c.line, got, c.want)
		}
	}
	if got := xdgDownloadDir(filepath.Join(dir, "missing"), home); got != "/home/someone/Downloads" {
		t.Errorf("no file: %s", got)
	}
}
