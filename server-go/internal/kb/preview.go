package kb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MIME is preview.ts's table: what a file is served as.
var MIME = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
	".pdf": "application/pdf",
	".md":  "text/plain; charset=utf-8", ".txt": "text/plain; charset=utf-8",
	".html": "text/html; charset=utf-8", ".json": "application/json",
	".csv": "text/csv; charset=utf-8",
}

// MimeFor is preview.ts's mimeFor.
func MimeFor(p string) string {
	if t, ok := MIME[strings.ToLower(ExtName(p))]; ok {
		return t
	}
	return "application/octet-stream"
}

// Convertible are the office formats LibreOffice renders as PDF.
var Convertible = set(".docx", ".pptx", ".xlsx", ".doc", ".ppt", ".xls", ".odt", ".odp", ".ods")

// Previews renders office documents and PDF pages with the optional
// helpers: LibreOffice's soffice and poppler's pdfinfo and pdftoppm.
type Previews struct {
	Roots Roots
	// Getenv is os.Getenv; tests replace it.
	Getenv func(string) string
	// One LibreOffice at a time: instances fight over the user profile.
	convert sync.Mutex
}

func (p *Previews) env(k string) string {
	if p.Getenv != nil {
		return p.Getenv(k)
	}
	return os.Getenv(k)
}

// Soffice is extractors.ts's findSoffice: SOFFICE_BIN, the PATH, then the
// app on macOS. "" when there is none.
func (p *Previews) Soffice() string {
	if b := p.env("SOFFICE_BIN"); b != "" {
		return b
	}
	if b, err := exec.LookPath("soffice"); err == nil {
		return b
	}
	home, _ := os.UserHomeDir()
	for _, app := range []string{"/Applications/LibreOffice.app", filepath.Join(home, "Applications", "LibreOffice.app")} {
		if b := filepath.Join(app, "Contents", "MacOS", "soffice"); exists(b) {
			return b
		}
	}
	return ""
}

// pdfOut is where the PDF of an office document at rel is cached.
func (p *Previews) pdfOut(rel string) string {
	return filepath.Join(p.Roots.PDFCache(), filepath.Dir(rel), strings.TrimSuffix(filepath.Base(rel), ExtName(rel))+".pdf")
}

func (p *Previews) pageDir(rel string) string { return filepath.Join(p.Roots.PDFPages(), rel) }

// PreviewPaths are the cached renderings of a file, so a move can carry
// them: its PDF and its page images.
func (p *Previews) PreviewPaths(rel string) (file, pages string) {
	return p.pdfOut(rel), p.pageDir(rel)
}

// RemovePreview drops a file's cached renderings.
func (p *Previews) RemovePreview(rel string) {
	os.Remove(p.pdfOut(rel))
	os.RemoveAll(p.pageDir(rel))
}

// OfficeToPDF is preview.ts's officeToPdf: the document rendered by
// LibreOffice, cached by the source's modification time. A host without
// LibreOffice gets a 415 that says so.
func (p *Previews) OfficeToPDF(ctx context.Context, abs, rel string) (string, error) {
	p.convert.Lock()
	defer p.convert.Unlock()
	src, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	outFile := p.pdfOut(rel)
	if c, err := os.Stat(outFile); err == nil && mtimeMs(c) >= mtimeMs(src) {
		return outFile, nil
	}
	outDir := filepath.Dir(outFile)
	home := filepath.Join(p.Roots.IndexBase, ".soffice")
	if err := os.MkdirAll(outDir, 0o777); err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o777); err != nil {
		return "", err
	}
	bin := p.Soffice()
	shown := bin
	if bin == "" {
		bin, shown = "soffice", "soffice"
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "--headless", "--convert-to", "pdf", "--outdir", outDir, abs)
	cmd.Env = append(os.Environ(), "HOME="+home)
	var se strings.Builder
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			msg := fmt.Sprintf("LibreOffice is not available on this deployment (%s not found), so Word, PowerPoint and Excel files cannot be rendered here.", shown)
			if runtime.GOOS == "darwin" {
				msg += " Install LibreOffice in Applications to preview them; their text is still shown."
			}
			return "", Error(415, "%s", msg)
		}
		detail := se.String()
		if detail == "" {
			detail = "Command failed: " + strings.Join(cmd.Args, " ")
		}
		return "", Error(415, "conversion failed: %s", detail)
	}
	if !exists(outFile) {
		return "", errors.New("conversion produced no output")
	}
	return outFile, nil
}

// PreviewPDF is the PDF behind a preview: the file itself, or its
// LibreOffice rendering for an office document.
func (p *Previews) PreviewPDF(ctx context.Context, abs, rel string) (string, error) {
	if Convertible[strings.ToLower(ExtName(rel))] {
		return p.OfficeToPDF(ctx, abs, rel)
	}
	return abs, nil
}

var pagesLine = regexp.MustCompile(`(?m)^Pages:\s+(\d+)`)

// PageCount is pdfinfo's page count.
func (p *Previews) PageCount(ctx context.Context, pdf string) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "pdfinfo", pdf)
	var se strings.Builder
	cmd.Stderr = &se
	out, err := cmd.Output()
	if err != nil {
		if e := popplerMissing(err, "pdfinfo"); e != nil {
			return 0, e
		}
		return 0, errors.New(firstNonEmpty(se.String(), err.Error()))
	}
	m := pagesLine.FindSubmatch(out)
	if m == nil {
		return 0, errors.New("pdfinfo reported no page count")
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n, nil
}

var pageFile = regexp.MustCompile(`^p-?0*(\d+)\.png$`)

// PagePNG renders one page (1-based) at 130 dpi, cached by the PDF's
// modification time, and returns the image's path.
func (p *Previews) PagePNG(ctx context.Context, abs, rel string, page int) (string, error) {
	pdf, err := p.PreviewPDF(ctx, abs, rel)
	if err != nil {
		return "", err
	}
	src, err := os.Stat(pdf)
	if err != nil {
		return "", err
	}
	dir := p.pageDir(rel)
	outFile := filepath.Join(dir, fmt.Sprintf("p%d.png", page))
	if c, err := os.Stat(outFile); err == nil && mtimeMs(c) >= mtimeMs(src) {
		return outFile, nil
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	n := strconv.Itoa(page)
	cmd := exec.CommandContext(cctx, "pdftoppm", "-f", n, "-l", n, "-r", "130", "-png", pdf, filepath.Join(dir, "p"))
	var se strings.Builder
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		if e := popplerMissing(err, "pdftoppm"); e != nil {
			return "", e
		}
		return "", fmt.Errorf("page render failed: %s", firstNonEmpty(se.String(), err.Error()))
	}
	// pdftoppm writes <prefix>-<page>.png, zero-padded once the page count
	// needs it; whatever it wrote becomes pN.png.
	if !exists(outFile) {
		entries, _ := os.ReadDir(dir)
		made := ""
		for _, e := range entries {
			if m := pageFile.FindStringSubmatch(e.Name()); m != nil {
				if v, _ := strconv.Atoi(m[1]); v == page {
					made = e.Name()
					break
				}
			}
		}
		if made == "" {
			return "", errors.New("page render produced no output")
		}
		if err := os.Rename(filepath.Join(dir, made), outFile); err != nil {
			return "", err
		}
	}
	return outFile, nil
}

// popplerMissing is the 415 for a host without poppler, whose pdfinfo and
// pdftoppm draw PDF pages; the viewer shows a PDF's text instead and says
// why. The macOS app does not include poppler; its server's PATH has
// Homebrew's, so installing it there brings the page images. nil for any
// other failure.
func popplerMissing(err error, tool string) error {
	if !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	msg := fmt.Sprintf("poppler's %s is not installed", tool)
	if runtime.GOOS == "darwin" {
		msg += "; brew install poppler adds it"
	}
	return Error(415, "%s", msg)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
