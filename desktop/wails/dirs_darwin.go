//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// defaultDataDir is the macOS app's data folder, the launcher's
// (deploy/mac/launcher.sh): ~/Library/Application Support/ACTIVATE Studio.
func defaultDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "ACTIVATE Studio")
}

// downloadsDir is ~/Downloads, where the Swift window saved downloads.
func downloadsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

// openFolder shows a folder in the Finder.
func openFolder(dir string) {
	if cmd := exec.Command("/usr/bin/open", dir); cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// revealDownload shows a file selected in the Finder, as the Swift window did
// after a download.
func revealDownload(path string) {
	if cmd := exec.Command("/usr/bin/open", "-R", path); cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}
