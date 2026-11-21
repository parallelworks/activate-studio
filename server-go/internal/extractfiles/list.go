// Package extractfiles is the extractor that works from a list of files:
// it reads each listed file with the Studio's extractor (internal/extract)
// and writes the text to an extraction artifact in GUFI's common external
// database schema (internal/extdb), keyed by each file's inode and mtime.
//
// The list says which files and which version of each: one line per file,
// its inode, mtime and size and then its path, separated by GUFI's
// delimiter, which is what
//
//	gufi_query -E "SELECT inode, mtime, size, rpath(sname, sroll)||'/'||name FROM vrpentries WHERE type = 'f';" INDEX
//
// prints. Any walk that knows those four things can make one, so the same
// program serves an index of a local folder, an object store, or a
// parallel file system, and runs wherever the files can be read: on the
// workstation, on a node next to the storage, or as a job of a scheduler
// array, each job taking its own shard of the list (--shard K/N).
package extractfiles

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// DefaultDelim is the column separator gufi_query prints by default.
const DefaultDelim = '\x1e'

// Entry is one line of a file list.
type Entry struct {
	Inode, Mtime int64
	// Size is -1 when the line leaves it empty.
	Size int64
	Path string
	Line int
}

// ParseLine reads "inode D mtime D size D path". The path is last, so it
// may hold the delimiter itself; a size may be left empty.
func ParseLine(line string, delim byte) (Entry, error) {
	line = strings.TrimSuffix(line, "\r")
	f := strings.SplitN(line, string(delim), 4)
	if len(f) != 4 {
		return Entry{}, errors.New("want inode, mtime, size and path")
	}
	var e Entry
	var err error
	if e.Inode, err = strconv.ParseInt(strings.TrimSpace(f[0]), 10, 64); err != nil || e.Inode < 0 {
		return Entry{}, fmt.Errorf("bad inode %q", f[0])
	}
	if e.Mtime, err = strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64); err != nil {
		return Entry{}, fmt.Errorf("bad mtime %q", f[1])
	}
	e.Size = -1
	if s := strings.TrimSpace(f[2]); s != "" {
		if e.Size, err = strconv.ParseInt(s, 10, 64); err != nil || e.Size < 0 {
			return Entry{}, fmt.Errorf("bad size %q", f[2])
		}
	}
	if f[3] == "" {
		return Entry{}, errors.New("no path")
	}
	e.Path = f[3]
	return e, nil
}

// Shard is one part of a list split by inode: the files whose inode
// modulo Of is Index.
type Shard struct{ Index, Of int }

// ParseShard reads "K/N", 0 <= K < N, as a scheduler's array task id and
// the array's size give it.
func ParseShard(s string) (Shard, error) {
	k, n, ok := strings.Cut(s, "/")
	if !ok {
		return Shard{}, fmt.Errorf("shard %q: want K/N", s)
	}
	ki, err1 := strconv.Atoi(k)
	ni, err2 := strconv.Atoi(n)
	if err1 != nil || err2 != nil || ni < 1 || ki < 0 || ki >= ni {
		return Shard{}, fmt.Errorf("shard %q: want K/N with 0 <= K < N", s)
	}
	return Shard{ki, ni}, nil
}

// Has reports whether the shard takes a file of this inode. The zero
// Shard takes every file.
func (s Shard) Has(inode int64) bool {
	return s.Of <= 1 || uint64(inode)%uint64(s.Of) == uint64(s.Index)
}

// ParseDelim reads a delimiter given as one character or as an escape
// such as \t or \x1e.
func ParseDelim(s string) (byte, error) {
	if len(s) > 1 && s[0] == '\\' {
		u, err := strconv.Unquote(`"` + s + `"`)
		if err != nil {
			return 0, fmt.Errorf("delimiter %q: %v", s, err)
		}
		s = u
	}
	if len(s) != 1 || s[0] == '\n' {
		return 0, fmt.Errorf("delimiter %q: want one byte other than a line end", s)
	}
	return s[0], nil
}

// stripPath is p without the leading path prefix (which the list's
// walk added, such as the index's top folder), cleaned.
func stripPath(p, prefix string) string {
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if p == prefix {
			p = ""
		} else if strings.HasPrefix(p, prefix+"/") {
			p = p[len(prefix)+1:]
		}
	}
	return p
}

// cacheRel is the path an entry's text is cached under: relative, with
// no step out of the cache.
func cacheRel(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}
