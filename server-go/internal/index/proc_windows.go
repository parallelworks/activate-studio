//go:build windows

package index

import (
	"os"
	"os/exec"
)

// lockingUser: Windows has no POSIX modes to keep a folder's owner out.
func lockingUser() bool { return false }

func accessRX(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func ownGroup(cmd *exec.Cmd) {}

// killGroup ends the process; Windows has no process groups to signal.
func killGroup(cmd *exec.Cmd, hard bool) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
