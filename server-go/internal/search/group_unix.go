//go:build !windows

package search

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, so killGroup ends
// whatever it started too.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killGroup(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
