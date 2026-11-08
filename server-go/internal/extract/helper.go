package extract

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// helperWaitDelay is how long Wait waits, after the helper is stopped, for
// pipes a leftover descendant still holds before closing them itself.
const helperWaitDelay = 5 * time.Second

// helperCmd is a helper process. Start, Run, Output and Wait are
// exec.Cmd's, with the platform's hold on the process tree taken right
// after the start and let go after the wait.
type helperCmd struct {
	*exec.Cmd
	tree tree
}

// helper is a helper process (tesseract, pdftoppm, pdfinfo, pdftotext,
// soffice, a contributed extractor) bounded by timeout: on expiry, or when
// ctx ends, it is killed with every process it started, so a wrapper
// script or LibreOffice's soffice.bin goes with it. On Unix the helper
// runs in a process group of its own, which is killed; on Linux it is
// also killed if the pass itself dies. On Windows it runs in a job object
// that kills the tree when stopped, or when the pass's handle to it
// closes, the pass dying included. The returned context ends when the
// helper's time is up (or ctx ends); call cancel when done.
func helper(ctx context.Context, timeout time.Duration, bin string, args ...string) (*helperCmd, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	h := &helperCmd{Cmd: exec.CommandContext(ctx, bin, args...)}
	h.Env = helperEnv()
	h.WaitDelay = helperWaitDelay
	h.tree.prepare(h.Cmd)
	return h, ctx, cancel
}

func (h *helperCmd) Start() error {
	if err := h.Cmd.Start(); err != nil {
		h.tree.release()
		return err
	}
	if err := h.tree.attach(h.Cmd); err != nil {
		h.Process.Kill()
		h.Cmd.Wait()
		h.tree.release()
		return err
	}
	return nil
}

func (h *helperCmd) Wait() error {
	err := h.Cmd.Wait()
	h.tree.release()
	return err
}

func (h *helperCmd) Run() error {
	if err := h.Start(); err != nil {
		return err
	}
	return h.Wait()
}

func (h *helperCmd) Output() ([]byte, error) {
	if h.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var out bytes.Buffer
	h.Stdout = &out
	err := h.Run()
	return out.Bytes(), err
}
