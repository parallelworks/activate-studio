//go:build !windows && !darwin

package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// defaultDataDir: $XDG_DATA_HOME/activate-studio-data, else
// ~/.local/share/activate-studio-data. A folder of its own, not
// activate-studio: that is where the per-user install puts the program.
func defaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "activate-studio-data")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "activate-studio-data")
}

// downloadsDir: XDG_DOWNLOAD_DIR from ~/.config/user-dirs.dirs, else
// ~/Downloads. A value of the home folder itself, "$HOME/" as xdg-user-dirs
// writes it, means the folder is not set, so ~/Downloads it is.
func downloadsDir() string {
	home, _ := os.UserHomeDir()
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	return xdgDownloadDir(filepath.Join(cfgHome, "user-dirs.dirs"), home)
}

func xdgDownloadDir(dirsFile, home string) string {
	if f, err := os.Open(dirsFile); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "XDG_DOWNLOAD_DIR="); ok {
				v = strings.Trim(v, `"`)
				v = strings.Replace(v, "$HOME", home, 1)
				if v != "" && filepath.Clean(v) != filepath.Clean(home) {
					return filepath.Clean(v)
				}
			}
		}
	}
	return filepath.Join(home, "Downloads")
}

// openFolder shows a folder in the file manager.
func openFolder(dir string) {
	if openInWindows(dir, false) {
		return
	}
	if cmd := exec.Command("xdg-open", dir); cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// revealDownload: nothing here; the Studio says where the file went.
func revealDownload(string) {}
