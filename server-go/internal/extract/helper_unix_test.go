//go:build unix

package extract

import "syscall"

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func killHard(pid int) { syscall.Kill(pid, syscall.SIGKILL) }
