package extract

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/odf"
	"github.com/parallelworks/activate-studio/server-go/internal/extract/ppt"
	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
	"github.com/parallelworks/activate-studio/server-go/internal/extract/xls"
)

// Action is what a pass did with one file's cache entry.
type Action string

const (
	Reused  Action = "reused"  // the entry was current
	Written Action = "written" // a new entry was written
	Failed  Action = "failed"  // no text, or no reader could read it: no entry
	Skipped Action = "skipped" // nothing here reads it (an image with OCR and captions off)
)

// FileResult is one file's outcome.
type FileResult struct {
	Rel    string
	Kind   Kind
	Action Action
	// Converted is true when downmark converted the file in this pass.
	Converted bool
	// Warnings counts what downmark reported losing.
	Warnings int
	// Text is the file's text when it was read but could not be cached
	// (an image whose caption request failed, an entry that could not be
	// written), for an index writer to use in this pass.
	Text string
	Err  error
}

// Update brings the cache entry for one file up to date, as today's two
// steps do in turn: for a PDF or Office document, preextract.ts converts
// it with downmark unless its entry is current and from this downmark,
// then enrich.py reads it with its own readers if there is still no
// current entry, and reads a thin PDF's pages with OCR; for the other
// formats, enrich.py's step alone. src is the file, rel its path in the
// knowledge base (slash separated), cacheRoot the extract cache.
func (x *Extractor) Update(ctx context.Context, src, rel, cacheRoot string) (r FileResult) {
	r.Rel = rel
	name := path.Base(rel)
	r.Kind = x.KindOf(name)
	st, err := os.Stat(src)
	if err != nil {
		r.Action, r.Err = Failed, err
		return r
	}
	mtime := st.ModTime()
	cache := CacheFile(cacheRoot, rel)
	switch r.Kind {
	case KindNone, KindText:
		r.Action = Skipped
		return r
	case KindImage:
		return x.updateImage(ctx, src, cache, mtime, r)
	}
	if LockFile(name) {
		r.Action = Skipped
		return r
	}
	if r.Kind == KindDownmark {
		x.updateDownmark(ctx, src, cache, mtime, &r)
	}
	// enrich.py's step: a current entry is kept (and a thin PDF's pages
	// read again by OCR), anything else is read here.
	ok, size, _ := fresh(cache, mtime)
	if ok {
		if r.Action == "" {
			r.Action = Reused
		}
		if Suffix(name) == ".pdf" && x.ocr && size < ocrTextCeiling*4 {
			x.recheck(ctx, src, cache)
		}
		return r
	}
	var text string
	switch r.Kind {
	case KindExternal:
		e := x.external[Suffix(name)]
		text, err = x.RunExternal(ctx, e, src)
		if err != nil && !errors.Is(err, errNoText) {
			r.Err = err
		}
	case KindDownmark:
		text = x.fallback(ctx, src)
	case KindNative:
		text = x.native(src)
		if text == "" {
			// LibreOffice, where configured, for a file the reader here
			// could not read.
			text = x.Legacy(ctx, src)
		}
	}
	text = pytext.Head(text, MaxText)
	if pytext.Strip(text) == "" {
		// Documents do not cache empty: an empty result usually means the
		// reader was missing, and caching it would hide the file from
		// every later pass, including the one after it is installed.
		r.Action = Failed
		if r.Err == nil {
			r.Err = errNoText
		}
		return r
	}
	if err := writeEntry(cache, text); err != nil {
		x.log("cache write failed %s: %v", cache, err)
		r.Action, r.Text, r.Err = Failed, text, err
		return r
	}
	r.Action = Written
	return r
}

// updateDownmark is preextract.ts's step for one document.
func (x *Extractor) updateDownmark(ctx context.Context, src, cache string, mtime time.Time, r *FileResult) {
	if ok, size, _ := fresh(cache, mtime); ok && size > 0 {
		// Current, and written by this converter: nothing to redo. A
		// version mismatch reconverts this one file, which is what makes
		// an upgrade reach a deployment that only ever indexes subtrees.
		was := EntryVersion(cache)
		if was == x.version || (was == x.version+noOCR && !x.ocr) {
			r.Action = Reused
			return
		}
	}
	entry, warnings, err := x.Downmark(ctx, src)
	r.Warnings = len(warnings)
	if err != nil {
		x.log("preextract: %s: %.200s", r.Rel, err)
		r.Err = err
		return
	}
	for i, w := range warnings {
		if i == 5 {
			x.log("preextract: %s: %d more warnings", r.Rel, len(warnings)-5)
			break
		}
		loc := ""
		if w.Location != "" {
			loc = " (" + w.Location + ")"
		}
		x.log("preextract: %s: %s%s: %s", r.Rel, w.Code, loc, w.Error())
	}
	if err := writeEntry(cache, entry); err != nil {
		x.log("preextract: %s: %.200s", r.Rel, err)
		r.Err = err
		return
	}
	r.Action, r.Converted, r.Err = Written, true, nil
}

// recheck reads a current PDF entry whose text is thin for its page count
// again with OCR, keeping the result (enrich.py's 'recheck' task). This is
// how text drawn as vector outlines, which downmark's OCR cannot see (it
// reads the images a page paints), gets read: pdftoppm renders the page.
func (x *Extractor) recheck(ctx context.Context, src, cache string) {
	b, err := os.ReadFile(cache)
	if err != nil {
		return
	}
	text := pytext.ReadText(b, MaxText)
	thick := pytext.Head(x.ocrIfThin(ctx, src, text), MaxText)
	if thick != text {
		if err := writeEntry(cache, thick); err != nil {
			x.log("cache write failed %s: %v", cache, err)
		}
	}
}

// fallback is enrich.py's reader for a PDF or Office document downmark
// did not convert.
func (x *Extractor) fallback(ctx context.Context, src string) string {
	switch Suffix(src) {
	case ".pdf":
		return x.pdfText(ctx, src)
	case ".doc":
		return x.Legacy(ctx, src)
	}
	f, err := os.Open(src)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	switch Suffix(src) {
	case ".docx":
		return ooxmlDocx(f, st.Size())
	case ".pptx":
		return ooxmlPptx(f, st.Size())
	case ".xlsx":
		return ooxmlXlsx(f, st.Size())
	}
	return ""
}

// native reads an OpenDocument, .xls or .ppt file with the reader here.
func (x *Extractor) native(src string) string {
	f, err := os.Open(src)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	var text string
	switch Suffix(src) {
	case ".xls":
		text, _ = xls.Text(f, st.Size(), MaxText)
	case ".ppt":
		text, _ = ppt.Text(f, st.Size(), MaxText)
	default:
		text, _ = odf.Text(f, st.Size(), MaxText)
	}
	return text
}

func (x *Extractor) updateImage(ctx context.Context, src, cache string, mtime time.Time, r FileResult) FileResult {
	ok, size, _ := fresh(cache, mtime)
	if ok && x.opt.Vision.Model != "" {
		// An empty entry while a vision model is configured was written
		// by a failed request or before captions were switched on.
		ok = size > 0
	}
	if ok {
		r.Action = Reused
		return r
	}
	if !x.imagesUseful() {
		r.Action = Skipped
		return r
	}
	text, complete, err := x.Image(ctx, src)
	if err != nil {
		r.Action, r.Err = Failed, err
		return r
	}
	text = pytext.Head(text, MaxText)
	// Images cache even when empty: OCR and captions are expensive and a
	// picture with no text is a real answer, unless the caption request
	// failed, which the next pass retries.
	if !complete {
		r.Action, r.Text, r.Err = Failed, text, errCaption
		return r
	}
	if err := writeEntry(cache, text); err != nil {
		x.log("cache write failed %s: %v", cache, err)
		r.Action, r.Text, r.Err = Failed, text, err
		return r
	}
	r.Action = Written
	return r
}

// SkipDirs are the folder names a pass does not enter: enrich.py's
// EXCLUDE_DIRS and the two more the server's index skip file adds.
var SkipDirs = set(".git", "node_modules", ".venv", "venv", "__pycache__", "dist", "build",
	".cache", ".pytest_cache", "screenshots", ".ipynb_checkpoints", ".claude", ".github")

// PassOptions scope a pass.
type PassOptions struct {
	KBRoot    string
	CacheRoot string
	// Subdir restricts the pass to this KB-relative folder.
	Subdir string
	// NoRecurse reads only Subdir itself, not the folders in it.
	NoRecurse bool
	// Progress is called with the files done of those found.
	Progress func(done, total int)
}

// KindCount is a pass's count for one suffix.
type KindCount struct {
	Files   int `json:"files"`
	Written int `json:"written"`
	Reused  int `json:"reused"`
	Failed  int `json:"failed"`
}

// Summary is what a pass did. The first six fields are preextract.ts's
// summary, for the documents downmark reads.
type Summary struct {
	Scanned   int                   `json:"scanned"`
	Converted int                   `json:"converted"`
	Reused    int                   `json:"reused"`
	Warned    int                   `json:"warned"`
	Failed    int                   `json:"failed"`
	Ms        int64                 `json:"ms"`
	Files     int                   `json:"files"`
	Written   int                   `json:"written"`
	OCR       bool                  `json:"ocr"`
	Kinds     map[string]*KindCount `json:"kinds"`
}

// Run brings the extract cache up to date for every file in scope that
// is read into it, Jobs at a time.
func (x *Extractor) Run(ctx context.Context, p PassOptions) (Summary, error) {
	t0 := time.Now()
	s := Summary{OCR: x.ocr, Kinds: map[string]*KindCount{}}
	files, err := x.Plan(p)
	if err != nil {
		return s, err
	}
	total := len(files)
	if p.Progress != nil {
		p.Progress(0, total)
	}
	results := make([]FileResult, total)
	work := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for range min(x.jobs, max(total, 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				rel := files[i]
				results[i] = x.Update(ctx, filepath.Join(p.KBRoot, filepath.FromSlash(rel)), rel, p.CacheRoot)
				mu.Lock()
				done++
				if p.Progress != nil {
					p.Progress(done, total)
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for i := range files {
		select {
		case work <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	for _, r := range results {
		if r.Rel == "" || r.Action == Skipped {
			continue
		}
		suffix := Suffix(r.Rel)
		k := s.Kinds[suffix]
		if k == nil {
			k = &KindCount{}
			s.Kinds[suffix] = k
		}
		s.Files++
		k.Files++
		switch r.Action {
		case Written:
			s.Written++
			k.Written++
		case Reused:
			k.Reused++
		case Failed:
			k.Failed++
		}
		if r.Kind == KindDownmark {
			s.Scanned++
			switch {
			case r.Converted:
				s.Converted++
				if r.Warnings > 0 {
					s.Warned++
				}
			case r.Action == Reused && r.Err == nil:
				s.Reused++
			default:
				s.Failed++
			}
		}
	}
	s.Ms = time.Since(t0).Milliseconds()
	return s, ctx.Err()
}

// Plan lists the files in scope that the cache holds text for, as
// KB-relative slash-separated paths in sorted order. Folders named in
// SkipDirs are not entered, a symbolic link to a file is read as the
// file, and one to a folder is not followed.
func (x *Extractor) Plan(p PassOptions) ([]string, error) {
	sub := strings.Trim(filepath.ToSlash(p.Subdir), "/")
	start := p.KBRoot
	if sub != "" {
		for _, part := range strings.Split(sub, "/") {
			if part == ".." {
				return nil, errors.New("extract: subdir escapes the knowledge base")
			}
			if SkipDirs[part] {
				return nil, nil
			}
		}
		start = filepath.Join(p.KBRoot, filepath.FromSlash(sub))
	}
	var files []string
	err := filepath.WalkDir(start, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if abs == start {
				return err
			}
			x.log("cannot read %s: %v", abs, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if abs == start {
				return nil
			}
			if p.NoRecurse || SkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		k := x.KindOf(d.Name())
		if k == KindNone || k == KindText {
			return nil
		}
		if !d.Type().IsRegular() {
			if d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			st, err := os.Stat(abs)
			if err != nil || !st.Mode().IsRegular() {
				return nil
			}
		}
		rel, err := filepath.Rel(p.KBRoot, abs)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	sort.Strings(files)
	return files, nil
}
