//go:build unix

package kb

import "golang.org/x/sys/unix"

// dirWritable: the Studio's account may make files in dir.
func dirWritable(dir string) bool { return unix.Access(dir, unix.W_OK|unix.X_OK) == nil }
