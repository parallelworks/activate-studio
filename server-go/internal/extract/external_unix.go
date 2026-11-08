//go:build unix

package extract

import (
	"os/exec"
	"syscall"
)

// killGroup runs the command in a process group of its own and stops the
// whole group on cancellation, so a shell script's children stop with it.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
