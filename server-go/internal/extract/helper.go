package extract

import (
	"context"
	"os/exec"
	"time"
)

// helperWaitDelay is how long Wait waits, after the helper is stopped, for
// pipes a leftover descendant still holds before closing them itself.
const helperWaitDelay = 5 * time.Second

// helper is a helper process (tesseract, pdftoppm, pdfinfo, pdftotext,
// soffice, a contributed extractor) bounded by timeout: it runs in a
// process group of its own, and on expiry, or when ctx ends, the whole
// group is killed, so a wrapper script or LibreOffice's soffice.bin goes
// with it. On Linux the helper is also killed if the pass itself dies.
// The returned context ends when the helper's time is up (or ctx ends);
// call cancel when done.
func helper(ctx context.Context, timeout time.Duration, bin string, args ...string) (*exec.Cmd, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = helperEnv()
	killGroup(cmd)
	cmd.WaitDelay = helperWaitDelay
	return cmd, ctx, cancel
}
