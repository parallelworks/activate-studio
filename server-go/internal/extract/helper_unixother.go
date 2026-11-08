//go:build unix && !linux

package extract

import "syscall"

// parentDeath has no counterpart outside Linux.
func parentDeath(*syscall.SysProcAttr) {}
