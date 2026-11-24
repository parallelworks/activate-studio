package kb

import (
	"database/sql"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// LocalIndex is how a library on this computer is indexed now: Busy while
// a build or sync of it runs or waits to start, SyncEvery the background
// sync's interval in seconds (0 when it is paused), and CatchUp what
// indexes a change at once: "sync" (Sync now, the main library) or
// "rebuild" (a library the Studio built of a folder here).
type LocalIndex struct {
	Busy      bool
	SyncEvery float64
	CatchUp   string
}

// LocalIndexing is LocalIndex for a library on this computer; ok false
// for one the Studio does not index here itself. Set by the libraries
// track; nil says none is.
var LocalIndexing func(lib Library) (LocalIndex, bool)

// localIndexNote is why the Indexed text of a file of a library on this
// computer is not the file's, when the index is the reason, as indexNote
// says it of a library on another system. For a file with no text: the
// library is indexing now, has no index yet, or its index has no entry
// for the file or one older than the file. For a file whose text is shown
// (from the extract cache, which the index's passes write): the index's
// entry is older than the file, so the text is from before it changed.
// nil when the text, or the lack of it, is the file's as it is.
func localIndexNote(lib Library, rel string, out *Obj) *Obj {
	src, _ := out.Get("source")
	if (src != "none" && src != "extracted") || LocalIndexing == nil || lib.IndexRoot == "" {
		return nil
	}
	if k, _ := out.Get("kind"); k != KindExtracted && k != KindImage {
		return nil
	}
	rel = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(rel)), "/")
	if passedOver(rel) {
		return nil
	}
	mtime, _ := out.Get("mtime")
	m, _ := mtime.(int64)
	if src == "extracted" {
		if entryOf(lib.IndexRoot, rel, m) != entryOlder {
			return nil
		}
		return localNote(lib, "newer")
	}
	// A build writes a file's entry before its text, so while one runs a
	// file without text may be waiting for it whatever its entry says.
	if st, ok := LocalIndexing(lib); ok && st.Busy {
		return O("state", "indexing")
	}
	if _, err := os.Stat(filepath.Join(lib.IndexRoot, "db.db")); errors.Is(err, os.ErrNotExist) {
		// No index yet: the sync keeps an index in step, it does not make one.
		return localNote(lib, "unindexed")
	}
	if e := entryOf(lib.IndexRoot, rel, m); e != entryMissing && e != entryOlder {
		return nil
	}
	return localNote(lib, "newer")
}

// localNote is the note of a library whose index is behind a file: state
// says how, unless the library is indexing now. nil for a library the
// Studio does not index here.
func localNote(lib Library, state string) *Obj {
	st, ok := LocalIndexing(lib)
	switch {
	case !ok:
		return nil
	case st.Busy:
		return O("state", "indexing")
	case state == "unindexed":
		return O("state", "unindexed", "catchUp", st.CatchUp)
	}
	return O("state", "newer", "syncEvery", st.SyncEvery, "catchUp", st.CatchUp)
}

// passedOver: a file in a folder the index passes over (a dot folder,
// node_modules), or with a dot name.
func passedOver(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == "" || strings.HasPrefix(part, ".") || ExcludeDirs[part] {
			return true
		}
	}
	return false
}

// What the index holds of a file, against its time now.
const (
	entryUnknown = iota // the index could not be read
	entryMissing        // no entry: the index has not seen the file
	entryOlder          // an entry from before the file's time
	entryCurrent        // an entry as the file is
)

// entryOf is what the index tree at root holds of the file at rel (clean
// and relative) with mtime (seconds), read from the db.db of its folder.
func entryOf(root, rel string, mtime int64) int {
	dir := filepath.Dir(filepath.FromSlash(rel))
	if dir == "." {
		dir = ""
	}
	db := filepath.Join(root, dir, "db.db")
	if _, err := os.Stat(db); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return entryMissing
		}
		return entryUnknown
	}
	conn, err := sql.Open("sqlite", "file:"+db+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return entryUnknown
	}
	defer conn.Close()
	var at int64
	err = conn.QueryRow("SELECT mtime FROM entries WHERE name = ? LIMIT 1", filepath.Base(rel)).Scan(&at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return entryMissing
	case err != nil:
		return entryUnknown
	case at < mtime:
		return entryOlder
	}
	return entryCurrent
}
