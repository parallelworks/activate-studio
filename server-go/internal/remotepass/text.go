// Package remotepass is what the Studio's helper (cmd/studio-remote) runs
// for a library's build on another system, in place of the indexer's
// Python steps there: the text pass, enrich.py's command line over the
// Go extraction and words writer, so the build's script and the Studio
// read what it leaves as they read enrich.py's.
package remotepass

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"sync"
	"syscall"

	"github.com/parallelworks/activate-studio/server-go/internal/extract"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
)

// TextMain is `studio-remote text`, enrich.py's arguments:
//
//	--kb-root DIR --index DIR --extract-cache DIR [--subdir REL] [--no-recurse] [--jobs N]
//	[--no-ocr] [--pdf-reader auto|pdftotext|pypdf] [--soffice PATH] [--no-documents] [--report FILE]
//
// It fills the extract cache, writes the words table of every db.db under
// --index, and writes the report enrich.py writes: per document type the
// files and those with no text, the PDF reader, whether OCR ran, and the
// files with text. It returns 0, 1 when the pass failed, 2 for a bad
// command line.
func TextMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("text", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kb := fs.String("kb-root", "", "the folder indexed")
	ix := fs.String("index", "", "GUFI index tree top (mirrors kb-root)")
	cache := fs.String("extract-cache", "", "the extract cache")
	subdir := fs.String("subdir", "", "restrict to this KB-relative subtree")
	noRecurse := fs.Bool("no-recurse", false, "process only the subdir itself, not its children")
	jobs := fs.Int("jobs", 0, "documents read side by side (default: the CPU count)")
	fs.Bool("progress", false, "accepted for enrich.py's command line; no lines are printed")
	noOCR := fs.Bool("no-ocr", false, "do not run tesseract on images or scanned PDFs")
	pdfReader := fs.String("pdf-reader", "auto", "auto, pdftotext, or pypdf (the built-in reader)")
	soffice := fs.String("soffice", "", "LibreOffice's soffice, for .ppt, .xls, .doc and OpenDocument files")
	noDocs := fs.Bool("no-documents", false, "index PDFs and Office documents by name only")
	report := fs.String("report", "", "write what was read to this JSON file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *kb == "" || *ix == "" || *cache == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: studio-remote text --kb-root DIR --index DIR --extract-cache DIR [options]")
		return 2
	}
	switch *pdfReader {
	case "auto", "pdftotext", "pypdf":
	default:
		fmt.Fprintln(stderr, "text: --pdf-reader must be auto, pdftotext or pypdf")
		return 2
	}
	if *jobs <= 0 {
		*jobs = runtime.NumCPU()
	}
	if *soffice != "" {
		if _, err := os.Stat(*soffice); err != nil {
			*soffice = ""
		}
	}
	var mu sync.Mutex
	logf := func(s string) {
		mu.Lock()
		fmt.Fprintln(stderr, "  "+s)
		mu.Unlock()
	}
	os.Setenv("OMP_THREAD_LIMIT", "1")
	tools := extract.FindTools()
	x, err := extract.New(extract.Options{OCR: !*noOCR, NoDocuments: *noDocs, PDFReader: *pdfReader, Soffice: *soffice,
		Jobs: *jobs, Tools: &tools, Log: logf})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sum, err := x.Run(ctx, extract.PassOptions{KBRoot: *kb, CacheRoot: *cache, Subdir: *subdir, NoRecurse: *noRecurse})
	if err != nil {
		fmt.Fprintf(stderr, "text: %v\n", err)
		return 1
	}
	ocr := !*noOCR && tools.Tesseract != ""
	res, err := index.WriteWords(ctx, index.WordsOptions{KBRoot: *kb, Index: *ix, Cache: *cache, Subdir: *subdir, NoRecurse: *noRecurse,
		Jobs: *jobs, NoDocuments: *noDocs, Soffice: *soffice != "", ImagesUseful: ocr, Warn: logf})
	if err != nil {
		fmt.Fprintf(stderr, "text: %v\n", err)
		return 1
	}
	if *report != "" {
		b := ReportJSON(res.Documents, PDFReaderHere(*pdfReader, tools.Pdftotext != ""), ocr, res.Files)
		if err := os.WriteFile(*report, b, 0o644); err != nil {
			fmt.Fprintf(stderr, "text: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "enriched %d directories: %d files indexed, %d extract-cache entries\n", res.Dirs, res.Files, sum.Converted)
	return 0
}

// BuiltInPDFReader is what the report names the reader in pypdf's place.
const BuiltInPDFReader = "the built-in PDF reader"

// PDFReaderHere is pdf_reader_here: pdftotext where it is allowed and
// installed, else the built-in reader where pypdf would have been allowed,
// else "" (no reader, so every PDF comes out with no text).
func PDFReaderHere(reader string, pdftotext bool) string {
	if (reader == "auto" || reader == "pdftotext") && pdftotext {
		return "pdftotext"
	}
	if reader == "auto" || reader == "pypdf" {
		return BuiltInPDFReader
	}
	return ""
}

// ReportJSON is enrich.py's report, as json.dumps writes it, the document
// types in order.
func ReportJSON(docs map[string]*index.DocCount, pdfReader string, ocr bool, textFiles int) []byte {
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b := []byte(`{"documents": {`)
	for i, k := range keys {
		if i > 0 {
			b = append(b, ", "...)
		}
		kb, _ := json.Marshal(k)
		b = append(b, kb...)
		b = append(b, fmt.Sprintf(`: {"files": %d, "noText": %d}`, docs[k].Files, docs[k].NoText)...)
	}
	rb, _ := json.Marshal(pdfReader)
	b = append(b, `}, "pdfReader": `...)
	b = append(b, rb...)
	b = append(b, fmt.Sprintf(`, "ocr": %t, "textFiles": %d}`, ocr, textFiles)...)
	return b
}
