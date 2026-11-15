package transfers

import (
	"context"
	"io"
	"sort"
	"strings"
)

// FileInfo is one entry as a walk or a stat found it. Rel is relative to
// the location's root.
type FileInfo struct {
	Rel   string
	Type  byte // 'f' plain file, 'd' folder, 'l' link, 'o' anything else
	Size  int64
	Mtime int64 // seconds
	Mode  uint32
}

// Walked is what a walk of some paths found: the plain files and folders,
// with hidden entries and links left out and counted.
type Walked struct {
	Files   []FileInfo
	Dirs    []string
	Skipped Skipped
}

// LandPair is one staged file taking its name.
type LandPair struct {
	Staged, Final string
	// Replace: the plan listed Final as replaced, so a plain file there may
	// be written over; anything else there fails the landing.
	Replace bool
}

// FS is a location's files: on this computer, or on a host over ssh or
// pw ssh. Every path is relative to Root and already checked by CleanRel;
// each implementation also checks, on the host, that the real folder of
// what it touches is inside the real root, so a folder swapped for a link
// that leaves the root is refused.
type FS interface {
	// Where is "this computer" or the host's name, for messages.
	Where() string
	// Root is the location's folder on its host, absolute.
	Root() string
	// List is one folder's entries, hidden ones left out, the first max
	// by name and whether there were more. A missing folder is a 404.
	List(ctx context.Context, rel string, max int) ([]Entry, bool, error)
	// Stat is each path that exists, without following a last link.
	Stat(ctx context.Context, rels []string) (map[string]FileInfo, error)
	// Walk is every plain file and folder under each path (a file names
	// itself). A missing path is a 404, a path that is a link a 400.
	Walk(ctx context.Context, rels []string, limit int) (Walked, error)
	// Free is the bytes free for an unprivileged writer at rel; ok false
	// when the host cannot say.
	Free(ctx context.Context, rel string) (int64, bool)
	// Sha256 is each plain file's hash, hex.
	Sha256(ctx context.Context, rels []string) (map[string]string, error)
	// Read opens a plain file inside the root for reading.
	Read(ctx context.Context, rel string) (io.ReadCloser, error)
	// Write creates a new file (never over one) with what r gives, its
	// folders made as needed, its modification time set when mtime > 0, and
	// returns the bytes written.
	Write(ctx context.Context, rel string, r io.Reader, mtime int64, mode uint32) (int64, error)
	// Mkdir makes a folder and those above it.
	Mkdir(ctx context.Context, rel string) error
	// Land gives staged files their names, in order, and returns the names
	// that took theirs before any failure.
	Land(ctx context.Context, pairs []LandPair) ([]string, error)
	// Remove removes plain files, then folders that are empty, deepest
	// first, and returns the files removed.
	Remove(ctx context.Context, files, dirs []string) ([]string, error)
	// RemoveStaging removes a staging folder and all in it; rel's last name
	// must be a staging folder's.
	RemoveStaging(ctx context.Context, rel string) error
}

// Remote is an FS on a host, for movers that run a program there.
type Remote interface {
	FS
	Site() string
	Key() string
}

// typeOf is a listing's type word as a FileInfo type.
func typeOf(s string) byte {
	switch s {
	case "f", "Regular File":
		return 'f'
	case "d", "Directory":
		return 'd'
	case "l", "Symbolic Link":
		return 'l'
	}
	return 'o'
}

// collect turns raw entries of a walk into Walked: hidden entries and
// everything under them are left out and counted once, links and special
// files are left out and counted as links.
func collect(items []string, raw []FileInfo, limit int) (Walked, error) {
	var w Walked
	seenDir := map[string]bool{}
	for _, e := range raw {
		// The hidden part, if any, below the item named.
		item := ""
		for _, it := range items {
			if within(e.Rel, it) && len(it) >= len(item) {
				item = it
			}
		}
		below := strings.TrimPrefix(strings.TrimPrefix(e.Rel, item), "/")
		if below != "" && hiddenSeg(below) {
			// Count only the top-most hidden name: its own last segment hidden
			// and no hidden segment above it.
			segs := strings.Split(below, "/")
			top := true
			for _, s := range segs[:len(segs)-1] {
				if strings.HasPrefix(s, ".") {
					top = false
				}
			}
			if top && strings.HasPrefix(segs[len(segs)-1], ".") {
				w.Skipped.Hidden++
			}
			continue
		}
		switch e.Type {
		case 'f':
			w.Files = append(w.Files, e)
			if limit > 0 && len(w.Files) > limit {
				return w, errTooMany(limit)
			}
		case 'd':
			if !seenDir[e.Rel] {
				seenDir[e.Rel] = true
				w.Dirs = append(w.Dirs, e.Rel)
			}
		default:
			w.Skipped.Links++
		}
	}
	sort.Slice(w.Files, func(i, j int) bool { return w.Files[i].Rel < w.Files[j].Rel })
	sort.Strings(w.Dirs)
	return w, nil
}

func entryOf(e FileInfo, name string) Entry {
	t := "file"
	switch e.Type {
	case 'd':
		t = "dir"
	case 'l':
		t = "link"
	case 'o':
		t = "file"
	}
	return Entry{Name: name, Type: t, Size: e.Size, Mtime: iso(unixTime(e.Mtime))}
}

// sortEntries puts entries in name order and cuts them at max.
func sortEntries(es []Entry, max int) ([]Entry, bool) {
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	if max > 0 && len(es) > max {
		return es[:max], true
	}
	if es == nil {
		es = []Entry{}
	}
	return es, false
}
