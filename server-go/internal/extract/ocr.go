package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	gpdf "github.com/giraffesyo/pdf"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

// OCR budgets, as enrich.py and preextract.ts set them.
const (
	ocrMaxPages     = 30  // pages OCR'd per document
	ocrCharsPerPage = 120 // below this per page a PDF's text layer is thin
	ocrTextCeiling  = ocrCharsPerPage * ocrMaxPages
	ocrRenderDPI    = "200" // pdftoppm -r
	dmMinGlyphs     = 50    // downmark's "thin" policy floor
)

// Helper timeouts, variables so the tests can shorten them.
var (
	ocrRenderTimeout = 600 * time.Second // pdftoppm, the whole document
	ocrPageTimeout   = 180 * time.Second // tesseract, one rendered page
	ocrImageTimeout  = 120 * time.Second // tesseract, one image
	dmPageTimeout    = 120 * time.Second // downmark's OCR, one page
	dmTotalTimeout   = 300 * time.Second // downmark's OCR, one document
	pdftotextTimeout = 180 * time.Second
	pdfinfoTimeout   = 60 * time.Second
)

// downmarkOCRMarker is what downmark writes on a page its OCR filled in;
// a document carrying it has already been read as ink.
const downmarkOCRMarker = "includes OCR text"

// OCRHeader opens the text enrich.py's OCR pass appends to a thin PDF.
const OCRHeader = "Text read from scanned pages (OCR):"

// acquire takes an OCR slot, or fails when ctx ends first.
func (x *Extractor) acquire(ctx context.Context) error {
	select {
	case x.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (x *Extractor) release() { <-x.sem }

// boundedOCR holds an OCR slot for each page downmark asks about, so the
// documents of a pass together run at most Jobs tesseract processes.
// downmark reads a document's pages on a worker per CPU, and without this
// each of Jobs documents could start that many at once.
type boundedOCR struct {
	x     *Extractor
	inner gpdf.OCR
}

func (b boundedOCR) ExtractPage(ctx context.Context, req gpdf.OCRRequest) ([]gpdf.Glyph, error) {
	if err := b.x.acquire(ctx); err != nil {
		return nil, err
	}
	defer b.x.release()
	return b.inner.ExtractPage(ctx, req)
}

// tesseract reads one image file: `tesseract <path> stdout --psm 3`, its
// output stripped. Python reads the output as strict UTF-8 and treats
// output that is not as a failure; so does this.
func (x *Extractor) tesseract(ctx context.Context, path string, timeout time.Duration) (string, error) {
	if err := x.acquire(ctx); err != nil {
		return "", err
	}
	defer x.release()
	cmd, hctx, cancel := helper(ctx, timeout, x.tools.Tesseract, path, "stdout", "--psm", "3")
	defer cancel()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil && hctx.Err() != nil {
		return "", hctx.Err()
	}
	// enrich.py keeps whatever tesseract printed, whatever its exit status.
	if !utf8.Valid(out.Bytes()) {
		return "", errors.New("tesseract: output is not UTF-8")
	}
	return pytext.Newlines(out.String()), nil
}

// ocrIfThin appends OCR text to a PDF's text when its text layer is thin
// for its page count (enrich.py's ocr_if_thin): fewer than 120 characters
// a page, on at most 30 pages, read by rendering each page with pdftoppm
// and running tesseract on it. Text downmark's OCR or an earlier pass
// already read as ink is left as it is.
func (x *Extractor) ocrIfThin(ctx context.Context, path, text string) string {
	if strings.Contains(text, downmarkOCRMarker) || strings.Contains(text, OCRHeader) {
		return text
	}
	n := pytext.Len(pytext.Strip(text))
	if n >= ocrTextCeiling {
		return text
	}
	pages := x.pageCount(ctx, path)
	if n < ocrCharsPerPage*pages {
		if ocr := x.pdfOCR(ctx, path, pages); ocr != "" {
			// Keep both: the text layer may hold a header the scan blurs.
			if pytext.Strip(text) != "" {
				return text + "\n\n" + ocr
			}
			return ocr
		}
	}
	return text
}

// pageCount is a PDF's page count: pdfinfo's where poppler is installed,
// as enrich.py asks it first, else the PDF reader's here, else 1.
func (x *Extractor) pageCount(ctx context.Context, path string) int {
	if x.tools.Pdfinfo != "" {
		if n, ok := pdfinfoPages(ctx, x.tools.Pdfinfo, path); ok {
			return n
		}
	}
	if n, err := PDFPageCount(ctx, path); err == nil && n > 0 {
		return n
	}
	return 1
}

func pdfinfoPages(ctx context.Context, bin, path string) (int, bool) {
	cmd, _, cancel := helper(ctx, pdfinfoTimeout, bin, path)
	defer cancel()
	out, _ := cmd.Output()
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "Pages:"); ok {
			return pytext.ParseInt(v)
		}
	}
	return 0, false
}

// pdfOCR renders the first pages of a PDF and reads them with tesseract
// (enrich.py's _pdf_ocr). The pages are read side by side within the
// pass's OCR slots and joined in page order.
func (x *Extractor) pdfOCR(ctx context.Context, path string, pages int) string {
	if !x.ocr || x.tools.Pdftoppm == "" {
		return ""
	}
	limit := min(pages, ocrMaxPages)
	tmp, err := os.MkdirTemp("", "studio-ocr-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(tmp)
	if err := x.render(ctx, path, limit, filepath.Join(tmp, "page")); err != nil {
		return ""
	}
	pngs, _ := filepath.Glob(filepath.Join(tmp, "page*.png"))
	sort.Strings(pngs)
	parts := make([]string, len(pngs))
	var wg sync.WaitGroup
	for i, png := range pngs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if t, err := x.tesseract(ctx, png, ocrPageTimeout); err == nil {
				parts[i] = pytext.Strip(t)
			}
		}()
	}
	wg.Wait()
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		return ""
	}
	return OCRHeader + "\n" + strings.Join(keep, "\n\n")
}

// render writes pages 1 to last of a PDF as PNGs at 200 dpi, prefix-NN.png.
// pdftoppm's exit status does not matter: enrich.py reads whatever pages it
// wrote. It takes an OCR slot, since it is as heavy as a tesseract.
func (x *Extractor) render(ctx context.Context, path string, last int, prefix string) error {
	if err := x.acquire(ctx); err != nil {
		return err
	}
	defer x.release()
	cmd, hctx, cancel := helper(ctx, ocrRenderTimeout, x.tools.Pdftoppm, "-r", ocrRenderDPI, "-f", "1", "-l", fmt.Sprint(last), "-png", path, prefix)
	defer cancel()
	_ = cmd.Run()
	if hctx.Err() != nil {
		return hctx.Err() // enrich.py gives up on the document after a timeout
	}
	return nil
}
