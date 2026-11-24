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

// localIndexNote is why a file of a library on this computer has no text
// in its index when that is the index and not the file, as indexNote says
// it of a library on another system: the library is indexing now, has no
// index yet, or its index has no entry for the file or one older than the
// file. nil when the file is in the index as it is and has no text.
func localIndexNote(lib Library, rel string, out *Obj) *Obj {
	if src, _ := out.Get("source"); src != "none" || LocalIndexing == nil || lib.IndexRoot == "" {
		return nil
	}
	if k, _ := out.Get("kind"); k != KindExtracted && k != KindImage {
		return nil
	}
	st, ok := LocalIndexing(lib)
	if !ok {
		return nil
	}
	if st.Busy {
		return O("state", "indexing")
	}
	rel = strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(rel)), "/")
	if passedOver(rel) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(lib.IndexRoot, "db.db")); errors.Is(err, os.ErrNotExist) {
		// No index yet: the sync keeps an index in step, it does not make one.
		return O("state", "unindexed", "catchUp", st.CatchUp)
	}
	mtime, _ := out.Get("mtime")
	m, _ := mtime.(int64)
	if !behindFile(lib.IndexRoot, rel, m) {
		return nil
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

// behindFile says whether the index tree at root has no entry for the
// file at rel, or one from before mtime (seconds), read from the db.db of
// its folder (rel is clean and relative). An index that cannot be read
// says nothing.
func behindFile(root, rel string, mtime int64) bool {
	dir := filepath.Dir(filepath.FromSlash(rel))
	if dir == "." {
		dir = ""
	}
	db := filepath.Join(root, dir, "db.db")
	if _, err := os.Stat(db); err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	conn, err := sql.Open("sqlite", "file:"+db+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return false
	}
	defer conn.Close()
	var at int64
	err = conn.QueryRow("SELECT mtime FROM entries WHERE name = ? LIMIT 1", filepath.Base(rel)).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return err == nil && at < mtime
}
