//go:build !linux && !windows

package main

import "syscall"

// macOS has no parent-death signal.
func setParentDeathSignal(*syscall.SysProcAttr) {}
