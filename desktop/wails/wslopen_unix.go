//go:build !windows && !darwin

package main

import (
	"os"
	"os/exec"
	"strings"
)

// Under WSL (Windows Subsystem for Linux, docs/WINDOWS.md) a web page or a
// folder opens in Windows: the Linux side usually has no browser or file
// manager, and xdg-open may not be installed.

// inWSL: WSL sets WSL_DISTRO_NAME for what it starts and registers the
// handler that runs Windows programs.
func inWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	_, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop")
	return err == nil
}

// wslOpenCommand is the command that opens target in Windows: wslview (from
// wslu) where installed; else rundll32 with url.dll for a web address,
// which hands it to the default browser without a shell parsing it; else
// explorer.exe with the folder's Windows path (wslpath -w). nil when none
// is found.
func wslOpenCommand(target string, isURL bool, lookPath func(string) (string, error), winPath func(string) (string, error)) []string {
	if p, err := lookPath("wslview"); err == nil {
		return []string{p, target}
	}
	if isURL {
		if p, err := lookPath("rundll32.exe"); err == nil {
			return []string{p, "url.dll,FileProtocolHandler", target}
		}
		if p, err := lookPath("explorer.exe"); err == nil {
			return []string{p, target}
		}
		return nil
	}
	p, err := lookPath("explorer.exe")
	if err != nil {
		return nil
	}
	w, err := winPath(target)
	if err != nil || w == "" {
		return nil
	}
	return []string{p, w}
}

func windowsPathOf(p string) (string, error) {
	out, err := exec.Command("wslpath", "-w", p).Output()
	return strings.TrimSpace(string(out)), err
}

// openInWindows opens target with wslOpenCommand under WSL, and
// reports whether it did.
func openInWindows(target string, isURL bool) bool {
	if !inWSL() {
		return false
	}
	argv := wslOpenCommand(target, isURL, exec.LookPath, windowsPathOf)
	if argv == nil {
		return false
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if cmd.Start() != nil {
		return false
	}
	go func() { _ = cmd.Wait() }()
	return true
}
