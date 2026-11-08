// Package extract reads the text of the documents in a knowledge base into
// the extract cache, in process, for the Studio's index. It stands in for
// two steps of today's pipeline: server/src/preextract.ts, which runs the
// downmark binary once per PDF and Office document, and the extraction
// step of indexer/enrich.py, which reads OpenDocument, .xls and .ppt
// files, images, and whatever downmark could not convert. The cache it
// writes is the one they write, entry for entry (see cache.go), so a
// cache either of them filled stays valid and the words tables built from
// it come out the same.
//
// What runs where:
//
//   - In the binary: downmark (github.com/giraffesyo/downmark) for .pdf,
//     .docx, .pptx, .xlsx and .doc; the readers in odf, ppt and xls for
//     OpenDocument, PowerPoint 97-2003 and Excel 97-2003; enrich.py's
//     flat OOXML readers for a docx, pptx or xlsx downmark rejects; the
//     PDF page count; and the image captioner's HTTP call.
//   - Helper processes, where there is no reasonable Go option:
//     tesseract for OCR (downmark's scanned pages, thin PDFs, images);
//     pdftoppm to render the pages of a PDF whose text layer is thin and
//     that downmark's own OCR could not read (text drawn as outlines);
//     pdftotext for a PDF downmark cannot open; and LibreOffice, when it
//     is configured, for a legacy file the readers here get nothing from.
//   - Contributed extractors: a command per suffix (see External).
//
// A pass (Run) runs a bounded pool of workers, Jobs of them, and at most
// Jobs OCR processes at a time across all of them.
package extract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/giraffesyo/downmark"
	"github.com/giraffesyo/downmark/all"
)

// MaxText is the most text kept per document, in characters (enrich.py
// and preextract.ts: MAX_TEXT).
const MaxText = 500_000

// MaxInputBytes is the largest document handed to downmark, which refuses
// OOXML archives above it anyway (preextract.ts: MAX_INPUT_BYTES).
const MaxInputBytes = 64 << 20

// DocumentTimeout bounds one document's conversion. downmark's own OCR
// budget is 300 s; nothing else in a conversion should come near this,
// so it only stops a converter that has gone wrong.
const DocumentTimeout = 15 * time.Minute

// The suffix sets of enrich.py and preextract.ts.
var (
	// TextSuffixes are read as text, by the index writer, not cached.
	TextSuffixes = set(".md", ".txt", ".py", ".sh", ".yaml", ".yml", ".json", ".csv", ".tsv", ".xml",
		".html", ".css", ".js", ".ts", ".tsx", ".svg", ".toml", ".cfg", ".ini", ".def", ".mjs", ".sql")
	// DownmarkSuffixes go through downmark.
	DownmarkSuffixes = set(".pdf", ".docx", ".pptx", ".xlsx", ".doc")
	// NativeSuffixes have a reader of their own in this module.
	NativeSuffixes = set(".odt", ".ods", ".odp", ".odg", ".xls", ".ppt")
	// LegacySuffixes are what LibreOffice converts each format to, for a
	// file its own reader gets nothing from.
	LegacySuffixes = map[string]string{".ppt": "pptx", ".odp": "pptx", ".xls": "xlsx", ".ods": "xlsx",
		".odt": "docx", ".doc": "docx"}
	// ImageSuffixes are read by OCR and the captioner.
	ImageSuffixes = set(".png", ".jpg", ".jpeg", ".gif", ".webp")
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Tools are the helper programs, by path; empty means not available.
type Tools struct {
	Tesseract string
	Pdftoppm  string
	Pdftotext string
	Pdfinfo   string
}

// FindTools looks the helpers up on PATH. tesseract counts only when
// `tesseract --version` runs, as preextract.ts checks.
func FindTools() Tools {
	var t Tools
	if p, err := exec.LookPath("tesseract"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if exec.CommandContext(ctx, p, "--version").Run() == nil {
			t.Tesseract = p
		}
		cancel()
	}
	t.Pdftoppm, _ = exec.LookPath("pdftoppm")
	t.Pdftotext, _ = exec.LookPath("pdftotext")
	t.Pdfinfo, _ = exec.LookPath("pdfinfo")
	return t
}

// Options configure an Extractor.
type Options struct {
	// OCR reads scanned PDF pages and images with tesseract, where
	// Tools.Tesseract is set (enrich.py and preextract.ts: not --no-ocr).
	OCR bool
	// NoDocuments leaves PDFs and Office documents unread: they index by
	// name. Images are still read (enrich.py's --no-documents).
	NoDocuments bool
	// PDFReader is how a PDF downmark cannot open is read: "auto" or
	// "pdftotext" (pdftotext where installed), or "pypdf", for which
	// there is no reader here, so such a PDF has no text.
	PDFReader string
	// Soffice is LibreOffice's soffice, for a legacy file the readers
	// here get nothing from; empty for none.
	Soffice string
	// Jobs is the number of documents read side by side and the most OCR
	// processes at once; the CPU count when zero.
	Jobs int
	// Vision describes images with a vision model when Model and Key are
	// set (enrich.py's ADE_VISION_MODEL and gateway key).
	Vision Vision
	// External are the contributed extractors, by suffix.
	External []External
	// Tools are the helpers; FindTools() when nil.
	Tools *Tools
	// Log receives one line per problem worth reporting.
	Log func(string)
}

// Extractor reads documents. It is safe for concurrent use.
type Extractor struct {
	opt      Options
	tools    Tools
	ocr      bool
	jobs     int
	sem      chan struct{} // OCR processes
	shared   *downmark.Engine
	version  string
	external map[string]*External
	profiles chan string // LibreOffice profiles, one per conversion at a time
	tmpRoot  string
}

// New returns an Extractor for opt.
func New(opt Options) (*Extractor, error) {
	x := &Extractor{opt: opt}
	if opt.Tools != nil {
		x.tools = *opt.Tools
	} else {
		x.tools = FindTools()
	}
	x.ocr = opt.OCR && x.tools.Tesseract != ""
	x.jobs = opt.Jobs
	if x.jobs <= 0 {
		x.jobs = runtime.NumCPU()
	}
	x.sem = make(chan struct{}, x.jobs)
	x.shared = all.New(all.Options{})
	x.version = DownmarkVersion()
	if opt.PDFReader == "" {
		x.opt.PDFReader = "auto"
	}
	x.external = map[string]*External{}
	for i := range opt.External {
		e := &opt.External[i]
		if err := e.check(); err != nil {
			return nil, err
		}
		for _, s := range e.Suffixes {
			s = normSuffix(s)
			if TextSuffixes[s] {
				return nil, fmt.Errorf("extract: extractor %s: %s is read as text and cannot be given an extractor", e.label(), s)
			}
			if _, dup := x.external[s]; dup {
				return nil, fmt.Errorf("extract: two extractors for %s", s)
			}
			x.external[s] = e
		}
	}
	if opt.Soffice != "" {
		if _, err := os.Stat(opt.Soffice); err != nil {
			x.opt.Soffice = ""
		}
	}
	x.profiles = make(chan string, x.jobs)
	for i := 0; i < x.jobs; i++ {
		x.profiles <- filepath.Join(os.TempDir(), fmt.Sprintf("studio-soffice-%d-%d", os.Getpid(), i))
	}
	return x, nil
}

// OCR reports whether this extractor reads scanned pages and images.
func (x *Extractor) OCR() bool { return x.ocr }

// Version is the downmark version this extractor names in its entries.
func (x *Extractor) Version() string { return x.version }

func (x *Extractor) log(format string, args ...any) {
	if x.opt.Log != nil {
		x.opt.Log(fmt.Sprintf(format, args...))
	}
}

// fallbackVersion is the downmark version in go.mod, for a binary built
// without module information (go test sets it).
const fallbackVersion = "0.10.0"

// DownmarkVersion is the version of the downmark module linked in, as
// the npm package's package.json gives it to preextract.ts ("0.10.0").
// The two must match for a cache written by either to stay current for
// the other; TestDownmarkVersionMatchesNPM checks that they do.
func DownmarkVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, m := range bi.Deps {
			if m.Path == "github.com/giraffesyo/downmark" {
				v := m.Version
				if m.Replace != nil && m.Replace.Version != "" {
					v = m.Replace.Version
				}
				return strings.TrimPrefix(v, "v")
			}
		}
	}
	return fallbackVersion
}

// Kind is how a file is read.
type Kind int

const (
	KindNone     Kind = iota // not read: indexed by name
	KindText                 // read as text by the index writer
	KindDownmark             // downmark, then enrich.py's readers
	KindNative               // a reader here, then LibreOffice
	KindImage                // OCR and the captioner
	KindExternal             // a contributed extractor
)

func (k Kind) String() string {
	return [...]string{"none", "text", "downmark", "native", "image", "external"}[k]
}

// LockFile reports whether name is an Office (~$name) or LibreOffice
// (.~lock.name#) lock file: a few bytes saying who has a document open,
// never a document itself.
func LockFile(name string) bool {
	return strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".~lock.")
}

func normSuffix(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s != "" && !strings.HasPrefix(s, ".") {
		s = "." + s
	}
	return s
}

// Suffix is a file name's lower-cased extension, as enrich.py and
// preextract.ts take it.
func Suffix(name string) string {
	return strings.ToLower(filepath.Ext(name))
}

// KindOf is how this extractor reads a file of this name.
func (x *Extractor) KindOf(name string) Kind {
	s := Suffix(name)
	if TextSuffixes[s] {
		return KindText
	}
	if _, ok := x.external[s]; ok {
		return KindExternal
	}
	if ImageSuffixes[s] {
		return KindImage
	}
	if x.opt.NoDocuments {
		return KindNone
	}
	switch {
	case DownmarkSuffixes[s]:
		return KindDownmark
	case NativeSuffixes[s]:
		// Every suffix LibreOffice is asked about has a reader here
		// first, so there is no kind for LibreOffice alone.
		return KindNative
	}
	return KindNone
}

// imagesUseful reports whether reading an image can give text at all.
func (x *Extractor) imagesUseful() bool {
	return x.ocr || x.opt.Vision.enabled()
}

// errNoText is a document a reader read and found no text in.
var errNoText = errors.New("no text")

// guard runs f, turning a panic into an error: a converter that panics
// on a hostile document must not take the pass down.
func guard(f func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("converter panicked: %v", p)
		}
	}()
	return f()
}

// Every helper process gets this environment: many tesseract processes
// side by side each run best on one thread, and scanned pages only come
// out with today's line breaks that way.
var helperEnv = sync.OnceValue(func() []string {
	return append(os.Environ(), "OMP_THREAD_LIMIT=1")
})
