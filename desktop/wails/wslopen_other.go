//go:build windows || darwin

package main

// openInWindows: only a Linux shell under WSL opens things in Windows.
func openInWindows(string, bool) bool { return false }
