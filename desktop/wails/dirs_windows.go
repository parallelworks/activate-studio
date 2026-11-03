package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// defaultDataDir: %LOCALAPPDATA%\ACTIVATE Studio.
func defaultDataDir() string {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0); err == nil {
		return filepath.Join(d, "ACTIVATE Studio")
	}
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "ACTIVATE Studio")
}

// downloadsDir: the Downloads known folder (which a person can move).
func downloadsDir() string {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

// openFolder shows a folder in Explorer.
func openFolder(dir string) {
	if cmd := exec.Command("explorer.exe", dir); cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}
