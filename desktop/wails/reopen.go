//go:build !windows

package main

// Relaunch after an update. The page asks (web/src/nativeShell.ts
// reopenApp) when an update changed the window's own program: the update
// put a new payload at the install's path, the server restarted from it, and
// this shell is still the old program in memory. If the program the install
// now holds under this one's name differs from the one running, the shell
// stops the server, starts the install's launcher again (which picks the new
// program) and quits; otherwise the server restarts and the page reloads.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// fileHash is the SHA-256 of a file, or "" when it cannot be read.
func fileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// installedShell is the file this program's name has in the install now.
func (a *App) installedShell() string {
	if a.cfg.Tree == "" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(a.cfg.Tree, "bin", filepath.Base(exe))
}

// shellChanged: the install holds a different program than the one running.
func (a *App) shellChanged() bool {
	inst := a.installedShell()
	if inst == "" {
		return false
	}
	now, was := fileHash(inst), fileHash("/proc/self/exe")
	return now != "" && was != "" && now != was
}

// relaunchAsked: the server installed a release that replaces the whole
// app (its manifest's requiresRelaunch, docs/UPDATES.md), and wrote
// updates/relaunch.json: the app is reopened even where this program is
// unchanged, never only the server restarted into the new copy.
func (a *App) relaunchAsked() bool {
	_, err := os.Stat(filepath.Join(a.cfg.DataDir, "updates", "relaunch.json"))
	return err == nil
}

// reopen starts the install's launcher in a session of its own, which waits
// for this process to end (STUDIO_WAIT_PID), and quits.
func (a *App) reopen(inst *instance) bool {
	launcher := filepath.Join(a.cfg.Tree, "bin", "activate-studio")
	if _, err := os.Stat(launcher); err != nil {
		return false
	}
	a.sup.Stop()
	inst.Release()
	attr := &syscall.SysProcAttr{Setsid: true}
	pid, err := syscall.ForkExec(launcher, []string{launcher}, &syscall.ProcAttr{
		Dir:   a.cfg.DataDir,
		Env:   append(os.Environ(), "STUDIO_WAIT_PID="+strconv.Itoa(os.Getpid())),
		Files: []uintptr{0, 1, 2},
		Sys:   attr,
	})
	if err != nil || pid <= 0 {
		return false
	}
	return true
}

// waitForPID waits (up to 30 s) for the process the previous window ran as.
func waitForPID(v string) {
	pid, err := strconv.Atoi(v)
	if err != nil || pid <= 1 {
		return
	}
	for i := 0; i < 300; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
