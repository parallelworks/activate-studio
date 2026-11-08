//go:build !unix && !windows

package extract

import "os/exec"

// tree leaves the default: the command itself is killed.
type tree struct{}

func (tree) prepare(*exec.Cmd)      {}
func (tree) attach(*exec.Cmd) error { return nil }
func (tree) release()               {}
