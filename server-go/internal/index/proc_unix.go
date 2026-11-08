//go:build !windows

package index

import (
	"os"
	"os/exec"
	"syscall"
)

// lockingUser is any user but root: one an index folder's mode can keep out.
func lockingUser() bool { return os.Getuid() != 0 }

// accessRX is access(p, R_OK|X_OK).
func accessRX(p string) bool { return syscall.Access(p, 0x4|0x1) == nil }

// ownGroup runs cmd in a process group of its own, so stopping a job
// stops the workers the program started too.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup signals cmd's process group.
func killGroup(cmd *exec.Cmd, hard bool) {
	if cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if hard {
		sig = syscall.SIGKILL
	}
	syscall.Kill(-cmd.Process.Pid, sig)
}
