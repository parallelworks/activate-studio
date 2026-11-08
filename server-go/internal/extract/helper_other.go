//go:build !unix

package extract

import "os/exec"

// killGroup leaves the default: the command itself is killed.
func killGroup(cmd *exec.Cmd) {}
