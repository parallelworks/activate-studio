//go:build !windows

package nodeproc

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// readySignal gives Node the write end of a pipe as fd 3 and names it in
// STUDIO_READY_FD; Node writes a line to it once it listens (main.ts). The
// channel is closed when that line arrives. after is called once Node has
// started, to close the parent's copy of the write end. A Node build that
// does not know the variable never writes, and Start falls back to polling.
func readySignal(cmd *exec.Cmd) (ready <-chan struct{}, after func(), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, func() {}, err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, w)
	cmd.Env = append(cmd.Env, "STUDIO_READY_FD="+strconv.Itoa(2+len(cmd.ExtraFiles)))
	ch := make(chan struct{})
	go func() {
		defer r.Close()
		line, _ := bufio.NewReader(r).ReadString('\n')
		if strings.TrimSpace(line) == "ready" {
			close(ch)
		}
	}()
	return ch, func() { w.Close() }, nil
}
