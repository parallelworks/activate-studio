//go:build windows

package search

import "os/exec"

func ownGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) { cmd.Process.Kill() }
