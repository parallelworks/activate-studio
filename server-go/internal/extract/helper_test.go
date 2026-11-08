package extract

import (
	"bytes"
	"context"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
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
	deadline := time.Now().Add(30 * time.Second) // returns as soon as they are gone
	for _, id := range ids {
		for alive(id) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(id) {
			killHard(id)
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
	if d := time.Since(start); d > helperWaitDelay+60*time.Second {
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
	if d := time.Since(start); d > helperWaitDelay+60*time.Second {
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

// TestBuiltinRenderTimeout: each page gets renderPageTimeout and the
// render goes on after a page that runs out of it, writing what the page
// drew by then. With a timeout already past when a page starts, nothing
// is drawn and every page is still written: the page's fill shows only
// with the usual timeout. No wall-clock bound, so a loaded host cannot
// fail it.
func TestBuiltinRenderTimeout(t *testing.T) {
	defer func(d time.Duration) { renderPageTimeout = d }(renderPageTimeout)
	dir := t.TempDir()
	c := "0 0 612 792 re f"
	doc := bytes.Replace(pdfDoc("", ""), []byte("/Length 0 >>\nstream\n"), []byte("/Length "+strconv.Itoa(len(c))+" >>\nstream\n"+c), 1)
	pdf := filepath.Join(dir, "a.pdf")
	put(t, pdf, doc)
	centre := func(prefix string) []uint8 {
		t.Helper()
		pngs, _ := filepath.Glob(prefix + "-*.png")
		sort.Strings(pngs)
		var out []uint8
		for _, p := range pngs {
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			b := img.Bounds()
			out = append(out, color.GrayModel.Convert(img.At(b.Dx()/2, b.Dy()/2)).(color.Gray).Y)
		}
		return out
	}
	for _, tc := range []struct {
		timeout time.Duration
		want    []uint8 // centre grey of pages 1 and 2
	}{{time.Nanosecond, []uint8{255, 255}}, {time.Minute, []uint8{0, 255}}} {
		renderPageTimeout = tc.timeout
		prefix := filepath.Join(dir, fmt.Sprint(tc.timeout.Nanoseconds()))
		if err := renderBuiltin(context.Background(), pdf, 2, prefix); err != nil {
			t.Fatal(err)
		}
		if got := centre(prefix); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("timeout %s: page centres %v, want %v", tc.timeout, got, tc.want)
		}
	}
}
