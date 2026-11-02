//go:build !windows

package main

// holdFocus: only Windows needs the guard (focusguard_windows.go).
func holdFocus(bool) func() { return func() {} }
