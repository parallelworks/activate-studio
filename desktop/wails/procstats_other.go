//go:build !linux && !windows

package main

import "syscall"

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func memoryStats(map[int]bool) map[string]any { return map[string]any{} }
