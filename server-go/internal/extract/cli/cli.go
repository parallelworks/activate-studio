// Package cli is the command line of the extraction pass, the stand-in for
// `node server/dist/preextract.js` followed by the extraction step of
// `indexer/enrich.py`:
//
//	studio-server extract --kb-root DIR --extract-cache DIR [--subdir REL] [--no-recurse]
//	  [--jobs N] [--progress] [--no-ocr] [--no-documents] [--pdf-reader auto|pdftotext|pypdf]
//	  [--soffice PATH] [--extractors FILE] [--report FILE]
//
// It fills the extract cache and prints the pass's summary as one JSON
// line on stdout, after `@progress {...}` lines with --progress. Problems
// with single files go to stderr, one line each, and do not fail the
// pass. Contributed extractors (extract.External) are read from
// --extractors, else from the file STUDIO_EXTRACTORS names, else from
// INDEX_BASE/extractors.json when it exists. Images are described by a vision model when ADE_VISION_MODEL and
// a gateway key are set, as for enrich.py.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/extract"
)

// Main runs the command with args (without the program name) and returns
// its exit status: 0, 1 when the pass failed, 2 for a bad command line.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kb := fs.String("kb-root", "", "the knowledge base")
	cache := fs.String("extract-cache", "", "the extract cache")
	subdir := fs.String("subdir", "", "restrict to this KB-relative folder")
	noRecurse := fs.Bool("no-recurse", false, "read only the subdir itself, not the folders in it")
	jobs := fs.Int("jobs", 0, "documents read side by side, and the most OCR processes at once (default: the CPU count)")
	progress := fs.Bool("progress", false, "print @progress lines on stdout")
	noOCR := fs.Bool("no-ocr", false, "do not run tesseract on images or scanned PDFs")
	noDocs := fs.Bool("no-documents", false, "leave PDFs and Office documents unread (indexed by name)")
	pdfReader := fs.String("pdf-reader", "auto", "how a PDF downmark cannot open is read: auto, pdftotext, or pypdf (no reader here)")
	soffice := fs.String("soffice", "", "LibreOffice's soffice, for a legacy file the readers here get nothing from")
	extractors := fs.String("extractors", "", "a JSON file of contributed extractors (default: $STUDIO_EXTRACTORS, else INDEX_BASE/extractors.json if present)")
	report := fs.String("report", "", "also write the summary to this JSON file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *kb == "" || *cache == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: studio-server extract --kb-root DIR --extract-cache DIR [--subdir REL] [--no-recurse] [--jobs N] [--progress] [--no-ocr] [--no-documents] [--pdf-reader auto|pdftotext|pypdf] [--soffice PATH] [--extractors FILE] [--report FILE]")
		return 2
	}
	switch *pdfReader {
	case "auto", "pdftotext", "pypdf":
	default:
		fmt.Fprintf(stderr, "extract: --pdf-reader must be auto, pdftotext or pypdf\n")
		return 2
	}
	var ext []extract.External
	if path, required := ExtractorsFile(*extractors, os.Getenv); path != "" {
		var err error
		ext, err = extract.LoadExternal(path)
		switch {
		case err == nil:
		case required:
			fmt.Fprintln(stderr, err)
			return 2
		case !errors.Is(err, iofs.ErrNotExist):
			// A mistake in the default file leaves the contributed
			// extractors out; it does not stop the pass.
			fmt.Fprintf(stderr, "%v (contributed extractors not used)\n", err)
			ext = nil
		}
	}
	var logMu sync.Mutex
	logf := func(s string) {
		logMu.Lock()
		fmt.Fprintln(stderr, s)
		logMu.Unlock()
	}
	// downmark runs tesseract itself, with this process's environment:
	// one thread each, as the server sets it for preextract.ts.
	os.Setenv("OMP_THREAD_LIMIT", "1")
	x, err := extract.New(extract.Options{
		OCR:         !*noOCR,
		NoDocuments: *noDocs,
		PDFReader:   *pdfReader,
		Soffice:     *soffice,
		Jobs:        *jobs,
		Vision:      extract.VisionFromEnv(),
		External:    ext,
		Log:         logf,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var outMu sync.Mutex
	var last time.Time
	opts := extract.PassOptions{KBRoot: *kb, CacheRoot: *cache, Subdir: *subdir, NoRecurse: *noRecurse}
	if *progress {
		// At most a few lines a second, and always the first and last.
		opts.Progress = func(done, total int) {
			now := time.Now()
			if done != 0 && done != total && now.Sub(last) < 250*time.Millisecond {
				return
			}
			last = now
			b, _ := json.Marshal(map[string]any{"phase": "documents", "done": done, "total": total})
			outMu.Lock()
			fmt.Fprintf(stdout, "@progress %s\n", b)
			outMu.Unlock()
		}
	}
	s, err := x.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "extract: %v\n", err)
		return 1
	}
	b, _ := json.Marshal(s)
	fmt.Fprintf(stdout, "%s\n", b)
	if *report != "" {
		if err := os.WriteFile(*report, b, 0o644); err != nil {
			fmt.Fprintf(stderr, "extract: %v\n", err)
			return 1
		}
	}
	return 0
}

// ExtractorsFile is where the contributed extractors are configured: the
// flag's value, else STUDIO_EXTRACTORS, else extractors.json in INDEX_BASE
// (the Studio's state folder, as the server resolves it). required is
// false for the last, which need not exist.
func ExtractorsFile(flagValue string, getenv func(string) string) (path string, required bool) {
	if flagValue != "" {
		return flagValue, true
	}
	if p := getenv("STUDIO_EXTRACTORS"); p != "" {
		return p, true
	}
	cfg, err := config.Load(getenv)
	if err != nil || cfg.IndexBase == "" {
		return "", false
	}
	return filepath.Join(cfg.IndexBase, "extractors.json"), false
}
