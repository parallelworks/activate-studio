//go:build !linux

package main

import "time"

// reapWebKit: WebKitGTK's helper processes exist on Linux only.
func reapWebKit(time.Duration) {}
