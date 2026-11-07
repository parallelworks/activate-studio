//go:build windows

package nodeproc

import "os"

// terminate kills the process: Windows has no SIGTERM to send it.
func terminate(p *os.Process) { p.Kill() }
