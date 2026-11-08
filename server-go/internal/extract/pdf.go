package extract

import (
	"bytes"
	"context"
	"errors"
	"os"

	"github.com/giraffesyo/downmark"
	"github.com/giraffesyo/downmark/all"
	dmpdf "github.com/giraffesyo/downmark/convert/pdf"
	dmocr "github.com/giraffesyo/downmark/ocr"
	gpdf "github.com/giraffesyo/pdf"
	"github.com/giraffesyo/pdf/ocr/tesseract"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

// engineFor is the downmark engine for one document. A PDF read with OCR
// gets an engine of its own, because downmark's OCR budget (ocr.Limit)
// counts every page the engine reads: shared by a pass, it would stop
// reading scans 30 pages into the pass rather than into each document.
// The options are the ones preextract.ts gives the binary: tesseract,
// the "thin" policy (pages with fewer than 50 glyphs that paint an
// image, as well as pages with none), at most 30 pages, 120 s a page and
// 300 s a document.
func (x *Extractor) engineFor(pdf bool) *downmark.Engine {
	if !pdf || !x.ocr {
		return x.shared
	}
	tess := &tesseract.Engine{Command: x.tools.Tesseract}
	limited := dmocr.Limit(tess, dmocr.Limits{MaxPages: ocrMaxPages, PerPage: dmPageTimeout, Total: dmTotalTimeout})
	return all.New(all.Options{PDF: dmpdf.Options{
		OCR:          boundedOCR{x: x, inner: limited},
		OCRPolicy:    gpdf.OCRTextlessPages,
		OCRMinGlyphs: dmMinGlyphs,
	}})
}

// entryVersion is what this extractor writes after the provenance prefix
// for a document of this kind.
func (x *Extractor) entryVersion(pdf bool) string {
	if pdf && !x.ocr {
		return x.version + noOCR
	}
	return x.version
}

// Downmark converts one document as preextract.ts does and returns its
// cache entry: the provenance line, a blank line and the Markdown, cut to
// MaxText UTF-16 units as JavaScript cuts it. An empty result is
// errNoText. warnings are what the conversion reported losing.
func (x *Extractor) Downmark(ctx context.Context, path string) (entry string, warnings []downmark.Warning, err error) {
	ext := Suffix(path)
	st, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	if st.Size() > MaxInputBytes {
		return "", nil, errTooLarge
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	pdf := ext == ".pdf"
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	var res *downmark.Result
	err = guard(func() error {
		var err error
		// The npm package hands the binary the document on stdin with the
		// extension as its only hint, so no file name reaches downmark.
		res, err = x.engineFor(pdf).Convert(ctx, bytes.NewReader(data), downmark.StreamInfo{Extension: ext})
		return err
	})
	if err != nil {
		return "", nil, err
	}
	// The binary writes its result as JSON, which replaces invalid UTF-8.
	md := pytext.JSONSafe(res.Markdown)
	if pytext.TrimJS(md) == "" {
		return "", res.Warnings, errNoText
	}
	entry = pytext.HeadUTF16(provenanceLine(x.entryVersion(pdf))+"\n\n"+md, MaxText)
	return entry, res.Warnings, nil
}

var errTooLarge = errors.New("exceeds the converter's input limit")

// pdfText is enrich.py's extract_pdf for a PDF downmark did not convert:
// pdftotext -layout where installed and allowed, then the OCR of a thin
// text layer. enrich.py's last resort, pypdf, has no counterpart here.
func (x *Extractor) pdfText(ctx context.Context, path string) string {
	text := ""
	if x.tools.Pdftotext != "" && (x.opt.PDFReader == "auto" || x.opt.PDFReader == "pdftotext") {
		// -layout keeps columns and table cells apart.
		cmd, _, cancel := helper(ctx, pdftotextTimeout, x.tools.Pdftotext, "-layout", "-q", path, "-")
		var out bytes.Buffer
		cmd.Stdout = &out
		if cmd.Run() == nil {
			text = pytext.Newlines(pytext.Decode(out.Bytes()))
		}
		cancel()
	}
	return x.ocrIfThin(ctx, path, text)
}

// PDFPageCount is a PDF's page count by the PDF reader downmark uses.
func PDFPageCount(ctx context.Context, path string) (n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	err = guard(func() error {
		doc, err := gpdf.ExtractWithOptions(ctx, f, st.Size(), gpdf.Options{Pages: []gpdf.PageRange{{First: 1, Last: 1}}, Concurrency: 1})
		if err != nil {
			return err
		}
		n = doc.PageCount
		return nil
	})
	return n, err
}
