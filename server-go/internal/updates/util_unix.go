//go:build !windows

package updates

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// accessOK is access(2), as fs.accessSync does.
func accessOK(p string, mode uint32) bool { return unix.Access(p, mode) == nil }

// detach starts cmd in a process group of its own, so a restart that stops
// this process and its group does not stop it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
