//go:build !windows

package app

import (
	"errors"
	"syscall"
)

// alive is process.kill(pid, 0): the process exists, ours to signal or not.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
