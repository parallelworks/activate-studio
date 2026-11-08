package kb

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is kb.ts's KbEntry, with the labels a listing adds.
type Entry struct {
	Name  string   `json:"name"`
	Path  string   `json:"path"` // relative to the root listed, "" for the root
	Type  string   `json:"type"` // dir or file
	Size  int64    `json:"size"`
	Mtime int64    `json:"mtime"` // seconds
	Kind  string   `json:"kind"`
	Tags  []string `json:"tags,omitempty"`
}

// ListDir is kb.ts's listDir: one directory of base, directories first,
// each group in locale order. A directory that does not exist lists as
// empty. An entry is a directory by its directory entry, so a link to a
// directory is a file, sized and dated by what it points at; a broken
// link is left out.
func ListDir(rel, base string, showHidden bool) ([]Entry, error) {
	abs, err := Resolve(rel, base)
	if err != nil {
		return nil, err
	}
	dirents, err := os.ReadDir(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Entry{}, nil
		}
		return nil, err
	}
	out := make([]Entry, 0, len(dirents))
	for _, d := range dirents {
		name := d.Name()
		isDir := d.IsDir()
		if !showHidden && HiddenName(name) {
			continue
		}
		if Excluded(name, isDir) {
			continue
		}
		p := filepath.Join(abs, name)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		typ := "file"
		if isDir {
			typ = "dir"
		}
		out = append(out, Entry{
			Name: name, Path: relTo(base, p), Type: typ,
			Size: st.Size(), Mtime: mtimeSec(st), Kind: Classify(name, isDir),
		})
	}
	SortEntries(out)
	return out, nil
}

// SortEntries orders a listing as listDir does.
func SortEntries(out []Entry) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Type == b.Type {
			return LocaleCompare(a.Name, b.Name) < 0
		}
		return a.Type == "dir"
	})
}

// relTo is path.relative(base, p) for p inside base.
func relTo(base, p string) string {
	r, err := filepath.Rel(jsResolve(base, ""), p)
	if err != nil || r == "." {
		return ""
	}
	return r
}

// mtimeSec is Math.floor(st.mtimeMs / 1000).
func mtimeSec(st fs.FileInfo) int64 {
	t := st.ModTime()
	s := t.Unix()
	return s
}

// mtimeMs is st.mtimeMs, as a float of milliseconds.
func mtimeMs(st fs.FileInfo) float64 {
	return float64(st.ModTime().UnixNano()) / 1e6
}

// Dirs is GET /api/kb/dirs: every directory of the main library to eight
// levels, at most 2,001, depth first in locale order, skipping hidden and
// machinery directories. Links to directories are not followed.
func Dirs(kbRoot string) []string {
	out := []string{}
	// As Node's: past the cap a level stops, and each level above it adds
	// one more directory before it stops in turn.
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		if depth > 8 {
			return
		}
		abs, err := Resolve(rel, kbRoot)
		if err != nil {
			return
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		sort.SliceStable(entries, func(i, j int) bool { return LocaleCompare(entries[i].Name(), entries[j].Name()) < 0 })
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || ExcludeDirs[e.Name()] {
				continue
			}
			child := e.Name()
			if rel != "" {
				child = rel + "/" + e.Name()
			}
			out = append(out, child)
			if len(out) > 2000 {
				return
			}
			walk(child, depth+1)
		}
	}
	walk("", 0)
	return out
}
