//go:build windows

package nodeproc

import "os/exec"

// readySignal is not available on Windows (no inherited file descriptors
// beyond stdio); Start polls /healthz alone.
func readySignal(cmd *exec.Cmd) (ready <-chan struct{}, after func(), err error) {
	return nil, func() {}, nil
}
