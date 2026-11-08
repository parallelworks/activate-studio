package extract

import (
	"bytes"
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

// TestBuiltinRender: without pdftoppm the pages are drawn by the built-in
// renderer and read by tesseract, in page order.
func TestBuiltinRender(t *testing.T) {
	tools, log := fakeTools(t, 3)
	tools.Pdftoppm = ""
	x := newX(t, Options{OCR: true, Tools: &tools})
	pdf := filepath.Join(t.TempDir(), "a.pdf")
	put(t, pdf, pdfDoc("", "", ""))
	got := x.pdfOCR(context.Background(), pdf, 3)
	want := OCRHeader + "\nink read from page-001.png\n\nink read from page-002.png\n\nink read from page-003.png"
	if got != want {
		t.Errorf("got %q", got)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "pdftoppm") {
		t.Error("pdftoppm ran")
	}
}

// TestBuiltinRenderTimeout: a page that takes too long is cut off at the
// page timeout and the render goes on.
func TestBuiltinRenderTimeout(t *testing.T) {
	defer func(d time.Duration) { renderPageTimeout = d }(renderPageTimeout)
	renderPageTimeout = 50 * time.Millisecond
	dir := t.TempDir()
	var c strings.Builder
	for i := 0; i < 300000; i++ {
		c.WriteString("0 0 612 792 re f\n")
	}
	doc := bytes.Replace(pdfDoc("", ""), []byte("/Length 0 >>\nstream\n"), []byte("/Length "+strconv.Itoa(c.Len())+" >>\nstream\n"+c.String()), 1)
	pdf := filepath.Join(dir, "a.pdf")
	put(t, pdf, doc)
	start := time.Now()
	if err := renderBuiltin(context.Background(), pdf, 2, filepath.Join(dir, "page")); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("render took %s", d)
	}
	pngs, _ := filepath.Glob(filepath.Join(dir, "page-*.png"))
	if len(pngs) != 2 {
		t.Errorf("pages written: %v", pngs)
	}
}
