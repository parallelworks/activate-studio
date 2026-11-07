//go:build !windows

package nodeproc

import (
	"os"
	"syscall"
)

// terminate sends SIGTERM, which Node's stop() handles.
func terminate(p *os.Process) { p.Signal(syscall.SIGTERM) }
