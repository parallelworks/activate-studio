//go:build unix

package extract

import (
	"os/exec"
	"syscall"
)

// killGroup runs the command in a process group of its own and stops the
// whole group on cancellation, so a wrapper script's children, or the
// processes soffice starts, stop with it.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	parentDeath(cmd.SysProcAttr)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
