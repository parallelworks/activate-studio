//go:build windows

package app

import "os"

// alive: on Windows FindProcess opens the process, which fails once it is gone.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}
