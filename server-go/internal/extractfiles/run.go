package extractfiles

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/extdb"
	"github.com/parallelworks/activate-studio/server-go/internal/extract"
	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

// Source makes a listed file readable at a path on this computer.
type Source interface {
	// Fetch gives the path to read e's file at, its size and mtime (in
	// seconds) as found now, and a function to call when done with it.
	Fetch(ctx context.Context, e Entry) (local string, size, mtime int64, release func(), err error)
}

// Local reads the listed files where they are. A path relative after
// Strip is found under Root.
type Local struct {
	Root, Strip string
}

// Fetch is Source.Fetch.
func (l Local) Fetch(_ context.Context, e Entry) (string, int64, int64, func(), error) {
	p := stripPath(e.Path, l.Strip)
	if !filepath.IsAbs(p) && l.Root != "" {
		p = filepath.Join(l.Root, filepath.FromSlash(p))
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", 0, 0, nil, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, 0, nil, errNotRegular
	}
	return p, st.Size(), st.ModTime().Unix(), func() {}, nil
}

var errNotRegular = fmt.Errorf("not a regular file")

// Options configure a run.
type Options struct {
	// List is the file list; Total its line count, or -1 when unknown.
	List  io.Reader
	Total int
	Delim byte
	// Source reads the files; Strip is the leading path removed from
	// each listed path before it names the file's cache entry.
	Source Source
	Strip  string
	// ExtraInfo is written as every record's extra_info.
	ExtraInfo string
	Shard     Shard
	// Out is the artifact. Files it already holds are passed by; with
	// Retry, those it holds without text are read again.
	Out   string
	Retry bool
	// Cache is an extract cache to use and keep; empty for a temporary
	// one beside Out, removed at the end.
	Cache string
	Jobs  int
	// Progress gets the list lines handled, of Total, and the bytes read.
	Progress func(done, total int, bytes int64)
	// Log gets one line per file that could not be read.
	Log func(string)
}

// Summary is what a run did.
type Summary struct {
	Artifact string `json:"artifact"`
	Shard    string `json:"shard,omitempty"`
	// Lines counts the list's lines, BadLines those that could not be
	// read, OtherShard those left to other shards, Already those the
	// artifact held.
	Lines      int64 `json:"lines"`
	BadLines   int64 `json:"badLines"`
	OtherShard int64 `json:"otherShard"`
	Already    int64 `json:"already"`
	// States counts the files looked at in this run, by outcome.
	States map[string]int64 `json:"states"`
	Chunks int64            `json:"chunks"`
	// Bytes is the size of the files read.
	Bytes   int64   `json:"bytes"`
	Seconds float64 `json:"seconds"`
	Stopped bool    `json:"stopped,omitempty"`
	// Helpers are the helper programs found, by name ("" for none), and
	// Missing says what the files of this run lost for want of one.
	Helpers map[string]string `json:"helpers"`
	Missing []string          `json:"missing"`
}

type result struct {
	file  extdb.File
	kind  extract.Kind
	ext   string
	bytes int64
}

// Run extracts the listed files into the artifact.
func Run(ctx context.Context, x *extract.Extractor, o Options) (Summary, error) {
	start := time.Now()
	logf := o.Log
	if logf == nil {
		logf = func(string) {}
	}
	s := Summary{Artifact: o.Out, States: map[string]int64{}}
	if o.Shard.Of > 1 {
		s.Shard = fmt.Sprintf("%d/%d", o.Shard.Index, o.Shard.Of)
	}
	art, err := extdb.Create(o.Out)
	if err != nil {
		return s, err
	}
	states, err := art.States()
	if err != nil {
		art.Close()
		return s, err
	}
	cache, tmpCache := o.Cache, o.Cache == ""
	if tmpCache {
		if cache, err = os.MkdirTemp(filepath.Dir(o.Out), ".index-extract-cache-*"); err != nil {
			art.Close()
			return s, err
		}
		defer os.RemoveAll(cache)
	}
	jobs := o.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var lines, bad, other, already atomic.Int64
	work := make(chan Entry)
	var readErr error
	go func() {
		defer close(work)
		sc := bufio.NewScanner(o.List)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		n := 0
		for sc.Scan() {
			n++
			line := sc.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines.Add(1)
			e, err := ParseLine(line, o.Delim)
			if err != nil {
				bad.Add(1)
				logf(fmt.Sprintf("list line %d: %v", n, err))
				continue
			}
			e.Line = n
			if !o.Shard.Has(e.Inode) {
				other.Add(1)
				continue
			}
			if st, ok := states[extdb.Key{Inode: e.Inode, Mtime: e.Mtime, ExtraInfo: o.ExtraInfo}]; ok && (extdb.Final(st) || !o.Retry) {
				already.Add(1)
				continue
			}
			select {
			case work <- e:
			case <-ctx.Done():
				return
			}
		}
		readErr = sc.Err()
	}()

	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range work {
				r := one(ctx, x, o, cache, tmpCache, e)
				if ctx.Err() != nil {
					// Cut off part way: nothing is recorded, so the
					// next run reads it again.
					continue
				}
				if r.file.Err != "" {
					logf(fmt.Sprintf("%s: %s: %s", e.Path, r.file.State, r.file.Err))
				}
				results <- r
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	kinds := map[extract.Kind]int64{}
	exts := map[string]int64{}
	var done int64
	var last time.Time
	progress := func(force bool) {
		if o.Progress == nil {
			return
		}
		now := time.Now()
		if !force && now.Sub(last) < 250*time.Millisecond {
			return
		}
		last = now
		o.Progress(int(done+bad.Load()+other.Load()+already.Load()), o.Total, s.Bytes)
	}
	progress(true)
	var putErr error
	for r := range results {
		if putErr != nil {
			continue
		}
		if err := art.Put(r.file); err != nil {
			putErr = err
			cancel()
			continue
		}
		done++
		s.States[r.file.State]++
		s.Chunks += int64(len(r.file.Chunks))
		s.Bytes += r.bytes
		kinds[r.kind]++
		exts[r.ext]++
		progress(false)
	}
	stopped := ctx.Err() != nil && putErr == nil
	if err := art.Close(); err != nil && putErr == nil {
		putErr = err
	}
	s.Lines, s.BadLines, s.OtherShard, s.Already = lines.Load(), bad.Load(), other.Load(), already.Load()
	s.Stopped = stopped
	s.Helpers, s.Missing = helpers(x, kinds, exts)
	s.Seconds = time.Since(start).Seconds()
	progress(true)
	if putErr != nil {
		return s, fmt.Errorf("writing %s: %w", o.Out, putErr)
	}
	if readErr != nil {
		return s, fmt.Errorf("reading the list: %w", readErr)
	}
	return s, nil
}

// one extracts one file.
func one(ctx context.Context, x *extract.Extractor, o Options, cache string, tmpCache bool, e Entry) result {
	rel := cacheRel(stripPath(e.Path, o.Strip))
	name := path.Base(rel)
	kind := x.KindOf(name)
	r := result{kind: kind, ext: extract.Suffix(name)}
	f := &r.file
	f.Key = extdb.Key{Inode: e.Inode, Mtime: e.Mtime, ExtraInfo: o.ExtraInfo}
	f.Size = e.Size
	f.Reader = kind.String()
	if kind == extract.KindNone || extract.LockFile(name) {
		// Nothing reads it: the file is not opened at all.
		f.State = extdb.StateSkipped
		return r
	}
	local, size, mtime, release, err := o.Source.Fetch(ctx, e)
	if err != nil {
		f.State, f.Err = extdb.StateMissing, err.Error()
		return r
	}
	defer release()
	if mtime != e.Mtime || (e.Size >= 0 && size != e.Size) {
		f.State = extdb.StateChanged
		f.Err = fmt.Sprintf("listed with mtime %d and size %d, found with mtime %d and size %d", e.Mtime, e.Size, mtime, size)
		return r
	}
	f.Size, r.bytes = size, size
	text, state, err := readText(ctx, x, kind, local, rel, cache, tmpCache)
	if err != nil {
		f.Err = err.Error()
	}
	f.State = state
	if state != extdb.StateText {
		return r
	}
	f.Chunks = extdb.Split(text, extdb.ChunkChars)
	if len(f.Chunks) == 0 {
		f.State = extdb.StateEmpty
		return r
	}
	f.Mimetype = mimeType(r.ext)
	prov, _ := json.Marshal(map[string]any{
		"schema_version": 1,
		"extractor":      "index-extract",
		"reader":         kind.String(),
		"downmark":       x.Version(),
		"chars":          len([]rune(text)),
		"bm25":           map[string]any{"engine": "sqlite_fts5", "tokenize": "unicode61", "chunk_chars": extdb.ChunkChars},
	})
	f.Provenance = string(prov)
	return r
}

// readText is a file's text as the Studio indexes it, and the state that
// leaves the file in.
func readText(ctx context.Context, x *extract.Extractor, kind extract.Kind, local, rel, cache string, tmpCache bool) (string, string, error) {
	if kind == extract.KindText {
		b, err := readHead(local)
		if err != nil {
			return "", extdb.StateMissing, err
		}
		return textOrEmpty(pytext.ReadText(b, extract.MaxText))
	}
	u := x.Update(ctx, local, rel, cache)
	cf := extract.CacheFile(cache, rel)
	if tmpCache {
		defer os.Remove(cf)
	}
	switch u.Action {
	case extract.Skipped:
		return "", extdb.StateSkipped, nil
	case extract.Failed:
		if u.Text != "" {
			return textOrEmpty(extract.IndexText(u.Text))
		}
		if u.Err == nil || extract.IsNoText(u.Err) {
			return "", extdb.StateEmpty, nil
		}
		return "", extdb.StateFailed, u.Err
	}
	b, err := os.ReadFile(cf)
	if err != nil {
		return "", extdb.StateFailed, err
	}
	return textOrEmpty(extract.IndexText(pytext.ReadText(b, extract.MaxText)))
}

func textOrEmpty(text string) (string, string, error) {
	if pytext.Strip(text) == "" {
		return "", extdb.StateEmpty, nil
	}
	return text, extdb.StateText, nil
}

// readHead is as much of a text file as the index keeps.
func readHead(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(extract.MaxText)*8))
}

var mimeTypes = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".doc":  "application/msword",
	".ppt":  "application/vnd.ms-powerpoint",
	".xls":  "application/vnd.ms-excel",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",
	".odg":  "application/vnd.oasis.opendocument.graphics",
	".eml":  "message/rfc822",
	".emlx": "message/rfc822",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".md":   "text/markdown",
	".html": "text/html",
	".csv":  "text/csv",
	".json": "application/json",
	".xml":  "application/xml",
	".svg":  "image/svg+xml",
}

// mimeType is the type written for a suffix: a fixed table, so the same
// file gets the same type on every system; text/plain for the other
// text formats, and none for the rest.
func mimeType(ext string) string {
	if t, ok := mimeTypes[ext]; ok {
		return t
	}
	if extract.TextSuffixes[ext] {
		return "text/plain"
	}
	return ""
}

// helpers are the helper programs found and what this run's files lost
// for want of one. A missing helper never fails a run.
func helpers(x *extract.Extractor, kinds map[extract.Kind]int64, exts map[string]int64) (map[string]string, []string) {
	t := x.Tools()
	h := map[string]string{"tesseract": t.Tesseract, "pdftoppm": t.Pdftoppm, "pdftotext": t.Pdftotext, "pdfinfo": t.Pdfinfo, "soffice": x.Soffice()}
	var missing []string
	if t.Tesseract == "" {
		if kinds[extract.KindImage] > 0 {
			missing = append(missing, fmt.Sprintf("tesseract: %d images could be read only by a vision model, where one is set", kinds[extract.KindImage]))
		}
		if exts[".pdf"] > 0 {
			missing = append(missing, fmt.Sprintf("tesseract: scanned pages of %d PDFs were not read", exts[".pdf"]))
		}
	}
	if x.Soffice() == "" {
		var n int64
		for ext := range extract.LegacySuffixes {
			n += exts[ext]
		}
		if n > 0 {
			missing = append(missing, fmt.Sprintf("soffice: %d legacy Office or OpenDocument files the built-in readers get nothing from stay without text", n))
		}
	}
	sort.Strings(missing)
	if missing == nil {
		missing = []string{}
	}
	return h, missing
}
