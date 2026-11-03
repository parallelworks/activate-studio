//go:build !linux && !windows

package main

import (
	"os/exec"
	"syscall"
)

// macOS has no parent-death signal.
func setParentDeathSignal(*syscall.SysProcAttr) {}

func guardCommand(*exec.Cmd) {}

func maybeRunGuard() bool { return false }
