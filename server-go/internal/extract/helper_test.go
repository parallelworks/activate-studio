package extract

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stubborn writes a helper that starts a grandchild, records both process
// ids in pids, and then outlives any timeout: what a wrapper script around
// pdftoppm, or soffice with its soffice.bin, looks like.
func stubborn(t *testing.T, dir string) (bin, pids string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-ins")
	}
	pids = filepath.Join(dir, "pids")
	bin = filepath.Join(dir, "helper")
	script := "#!/bin/sh\nsleep 300 &\necho $$ $! > " + pids + "\nsleep 300\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, pids
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// gone waits for the processes listed in pids to exit.
func gone(t *testing.T, pids string) {
	t.Helper()
	b, err := os.ReadFile(pids)
	if err != nil {
		t.Fatalf("the helper did not start: %v", err)
	}
	var ids []int
	for _, f := range strings.Fields(string(b)) {
		n, _ := strconv.Atoi(f)
		ids = append(ids, n)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, id := range ids {
		for alive(id) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(id) {
			syscall.Kill(id, syscall.SIGKILL)
			t.Errorf("process %d outlived the helper's timeout", id)
		}
	}
}

func TestHelperTimeoutKillsTheGroup(t *testing.T) {
	bin, pids := stubborn(t, t.TempDir())
	cmd, hctx, cancel := helper(context.Background(), 300*time.Millisecond, bin)
	defer cancel()
	start := time.Now()
	cmd.Run()
	if hctx.Err() == nil {
		t.Fatal("no timeout")
	}
	if d := time.Since(start); d > helperWaitDelay+2*time.Second {
		t.Fatalf("Run returned after %s", d)
	}
	gone(t, pids)
}

// TestRenderTimeout: a pdftoppm that does not finish is stopped at the
// render timeout with everything it started, and the document gets no OCR.
func TestRenderTimeout(t *testing.T) {
	dir := t.TempDir()
	bin, pids := stubborn(t, dir)
	defer func(d time.Duration) { ocrRenderTimeout = d }(ocrRenderTimeout)
	ocrRenderTimeout = 300 * time.Millisecond
	tools, _ := fakeTools(t, 2)
	tools.Pdftoppm = bin
	x := newX(t, Options{OCR: true, Tools: &tools})
	pdf := filepath.Join(dir, "a.pdf")
	put(t, pdf, pdfDoc("", ""))
	start := time.Now()
	if got := x.pdfOCR(context.Background(), pdf, 2); got != "" {
		t.Errorf("OCR text from a render that timed out: %q", got)
	}
	if d := time.Since(start); d > helperWaitDelay+2*time.Second {
		t.Fatalf("pdfOCR returned after %s", d)
	}
	gone(t, pids)
}

// TestTesseractTimeout: the same for one page's tesseract.
func TestTesseractTimeout(t *testing.T) {
	dir := t.TempDir()
	bin, pids := stubborn(t, dir)
	x := newX(t, Options{OCR: true, Tools: &Tools{Tesseract: bin}})
	if _, err := x.tesseract(context.Background(), filepath.Join(dir, "p.png"), 300*time.Millisecond); err == nil {
		t.Error("no error after the timeout")
	}
	gone(t, pids)
}

// TestPassCancelKillsHelpers: stopping the pass stops a helper running.
func TestPassCancelKillsHelpers(t *testing.T) {
	dir := t.TempDir()
	bin, pids := stubborn(t, dir)
	x := newX(t, Options{OCR: true, Tools: &Tools{Tesseract: bin}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(pids); err == nil {
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	x.tesseract(ctx, filepath.Join(dir, "p.png"), time.Hour)
	gone(t, pids)
}
