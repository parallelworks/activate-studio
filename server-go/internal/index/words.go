package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/parallelworks/activate-studio/server-go/internal/extract"
	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

// The text phase's writing step: enrich.py's write_dir. Every directory
// db.db of an index tree gets a words fts5 table (empty or not, so a
// MATCH never meets a missing table), holding one row per file with text:
// its inode as text, its name, and its text, read from the file itself
// for the text formats and from the extract cache for the rest (which the
// documents phase filled). The rows, and the table's definition, are
// those enrich.py writes.

// excludeDirs are enrich.py's EXCLUDE_DIRS: no words are read from a
// folder with one of these names in its path.
var excludeDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, "dist": true, "build": true,
	".cache": true, ".pytest_cache": true, "screenshots": true, ".ipynb_checkpoints": true,
}

// WordsOptions are enrich.py's arguments that bear on the words.
type WordsOptions struct {
	KBRoot, Index, Cache string
	Subdir               string
	NoRecurse            bool
	Jobs                 int
	// NoDocuments: PDFs and Office documents index by name only.
	NoDocuments bool
	// Soffice set: the formats LibreOffice reads are read from the cache.
	Soffice bool
	// VisionModel set: an empty image entry is a failed caption, not text.
	VisionModel bool
	// ImagesUseful: OCR or captions can read an image (enrich.py's
	// images_useful); without, an image with no current entry is passed by.
	ImagesUseful bool
	// Extractors are contributed extractors' suffixes, read from the cache.
	Extractors []string
	// Progress gets the directories written of those found.
	Progress func(done, total int)
	// Warn gets a line for a folder or file that cannot be read.
	Warn func(string)
}

// WordsResult is what the step wrote.
type WordsResult struct {
	Dirs  int
	Files int
	// Documents counts, per suffix of a document format, the files seen
	// and those with no text (enrich.py's --report).
	Documents map[string]*DocCount
}

// DocCount is one document type's count.
type DocCount struct {
	Files  int `json:"files"`
	NoText int `json:"noText"`
}

// pySuffix is os.path.splitext(name)[1].lower(): leading dots belong to
// the name, so ".bashrc" has no suffix.
func pySuffix(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return ""
	}
	if strings.TrimLeft(name[:i], ".") == "" {
		return ""
	}
	return strings.ToLower(name[i:])
}

type wordFile struct {
	name, suffix string
	ino          uint64
	text         bool // read from the file itself
}

type wordDir struct {
	db    string
	rel   string
	files []wordFile
}

// WriteWords writes the words tables under opts.Index.
func WriteWords(ctx context.Context, o WordsOptions) (WordsResult, error) {
	warn := o.Warn
	if warn == nil {
		warn = func(string) {}
	}
	cacheable := map[string]bool{}
	for s := range extract.ImageSuffixes {
		cacheable[s] = true
	}
	if !o.NoDocuments {
		for s := range extract.DownmarkSuffixes {
			cacheable[s] = true
		}
		for s := range extract.NativeSuffixes {
			cacheable[s] = true
		}
		if o.Soffice {
			for s := range extract.LegacySuffixes {
				cacheable[s] = true
			}
		}
	}
	for _, s := range o.Extractors {
		cacheable[s] = true
	}
	walkRoot := o.Index
	if o.Subdir != "" {
		walkRoot = filepath.Join(o.Index, o.Subdir)
	}
	var dirs []wordDir
	err := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && p != walkRoot {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if o.NoRecurse && p != walkRoot {
			return fs.SkipDir
		}
		dbfile := filepath.Join(p, "db.db")
		if _, err := os.Stat(dbfile); err != nil {
			return nil
		}
		rel, _ := filepath.Rel(o.Index, p)
		if rel == "." {
			rel = ""
		}
		wd := wordDir{db: dbfile, rel: rel}
		src := filepath.Join(o.KBRoot, rel)
		if st, err := os.Stat(src); err == nil && st.IsDir() && !excludedRel(rel) {
			wd.files = listWordFiles(src, rel, cacheable, o, warn)
		}
		dirs = append(dirs, wd)
		return nil
	})
	if err != nil {
		return WordsResult{}, err
	}
	res := WordsResult{Dirs: len(dirs), Documents: map[string]*DocCount{}}
	var mu sync.Mutex
	done := 0
	if o.Progress != nil {
		o.Progress(0, len(dirs))
	}
	jobs := max(1, min(o.Jobs, 8))
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	var firstErr error
	for _, wd := range dirs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			n, seen, err := writeDir(wd, o)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", wd.db, err)
			}
			res.Files += n
			for s, c := range seen {
				x := res.Documents[s]
				if x == nil {
					x = &DocCount{}
					res.Documents[s] = x
				}
				x.Files += c.Files
				x.NoText += c.NoText
			}
			done++
			if o.Progress != nil {
				o.Progress(done, len(dirs))
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, firstErr
}

func excludedRel(rel string) bool {
	if rel == "" {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if excludeDirs[part] {
			return true
		}
	}
	return false
}

// listWordFiles are the files of a source folder whose text is indexed,
// sorted by name, with their inodes (enrich.py's plan).
func listWordFiles(src, rel string, cacheable map[string]bool, o WordsOptions, warn func(string)) []wordFile {
	entries, err := os.ReadDir(src)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			warn(fmt.Sprintf("  cannot list %s: %s", src, pyErr(err)))
		}
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []wordFile
	for _, e := range entries {
		// DirEntry.is_file() follows a symbolic link, and so does stat().
		st, err := os.Stat(filepath.Join(src, e.Name()))
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				warn(fmt.Sprintf("  cannot read %s: %s", filepath.Join(src, e.Name()), pyErr(err)))
			}
			continue
		}
		if !st.Mode().IsRegular() {
			continue
		}
		suffix := pySuffix(e.Name())
		isText := extract.TextSuffixes[suffix]
		if !isText && !cacheable[suffix] {
			continue
		}
		if !isText && extract.ImageSuffixes[suffix] && !o.ImagesUseful {
			// Passed by unless its entry is current: nothing here reads it.
			cst, err := os.Stat(extract.CacheFile(o.Cache, filepath.ToSlash(filepath.Join(rel, e.Name()))))
			if err != nil || cst.ModTime().Before(st.ModTime()) {
				continue
			}
		}
		out = append(out, wordFile{name: e.Name(), suffix: suffix, ino: inodeOf(st), text: isText})
	}
	return out
}

// pyErr is the message Python's PermissionError prints.
func pyErr(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Sprintf("[Errno 13] Permission denied: '%s'", pe.Path)
	}
	return err.Error()
}

func writeDir(wd wordDir, o WordsOptions) (int, map[string]*DocCount, error) {
	type row struct{ ino, name, text string }
	var rows []row
	seen := map[string]*DocCount{}
	for _, f := range wd.files {
		var text string
		ok := false
		if f.text {
			if b, err := readHead(filepath.Join(o.KBRoot, wd.rel, f.name)); err == nil {
				text, ok = pytext.ReadText(b, extract.MaxText), true
			}
		} else {
			cf := extract.CacheFile(o.Cache, filepath.ToSlash(filepath.Join(wd.rel, f.name)))
			if b, err := os.ReadFile(cf); err == nil {
				text, ok = pytext.ReadText(b, extract.MaxText), true
				if extract.ImageSuffixes[f.suffix] && pytext.Strip(text) == "" && o.VisionModel {
					ok = false
				} else {
					text = extract.IndexText(text)
				}
			}
		}
		document := !extract.TextSuffixes[f.suffix] && !extract.ImageSuffixes[f.suffix]
		if document {
			c := seen[f.suffix]
			if c == nil {
				c = &DocCount{}
				seen[f.suffix] = c
			}
			c.Files++
		}
		if !ok || pytext.Strip(text) == "" {
			if document {
				seen[f.suffix].NoText++
			}
			continue
		}
		rows = append(rows, row{fmt.Sprint(f.ino), f.name, text})
	}
	db, err := sql.Open("sqlite", "file:"+wd.db+"?_pragma=busy_timeout(60000)")
	if err != nil {
		return 0, seen, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// The index is rebuilt from the files at will: no waiting for the disk.
	if _, err := db.Exec("PRAGMA synchronous=OFF"); err != nil {
		return 0, seen, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, seen, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DROP TABLE IF EXISTS words"); err != nil {
		return 0, seen, err
	}
	if _, err := tx.Exec("CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)"); err != nil {
		return 0, seen, err
	}
	if len(rows) > 0 {
		st, err := tx.Prepare("INSERT INTO words (tinode, fname, wordf) VALUES (?, ?, ?)")
		if err != nil {
			return 0, seen, err
		}
		for _, r := range rows {
			if _, err := st.Exec(r.ino, r.name, r.text); err != nil {
				st.Close()
				return 0, seen, err
			}
		}
		st.Close()
	}
	return len(rows), seen, tx.Commit()
}

// readHead reads a text file, enough of it for MaxText characters: four
// bytes each at most, and twice that for "\r\n" pairs newline
// translation joins.
func readHead(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(extract.MaxText)*8))
}
