//go:build windows

package updates

import (
	"os"
	"os/exec"
)

func accessOK(p string, mode uint32) bool { _, err := os.Stat(p); return err == nil }

func detach(cmd *exec.Cmd) {}
