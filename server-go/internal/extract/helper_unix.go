//go:build unix

package extract

import (
	"os/exec"
	"syscall"
)

// tree is a helper's process group.
type tree struct{}

// prepare runs the command in a process group of its own and stops the
// whole group on cancellation.
func (tree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	parentDeath(cmd.SysProcAttr)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (tree) attach(*exec.Cmd) error { return nil }
func (tree) release()               {}
