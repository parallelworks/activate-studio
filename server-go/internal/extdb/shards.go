package extdb

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Shard names: prefix_XX_uid_gid.db, where XX says who may read the
// files whose records it holds, judged as GUFI's plan judges them against
// the folder: OR when others may read the file, GR when the folder's
// group may, UR when the folder's owner may, and NA for none of those.
// uid and gid are the files' own. Attaching a shard is then the
// permission check: its mode lets in exactly those who may read the
// files.
const (
	ReadOther = "OR"
	ReadGroup = "GR"
	ReadUser  = "UR"
	ReadNone  = "NA"
)

// ShardClass is the class of a file of this owner and mode in a folder
// of this owner.
func ShardClass(mode, uid, gid, duid, dgid int64) string {
	switch {
	case mode&0o004 != 0:
		return ReadOther
	case mode&0o040 != 0 && gid == dgid:
		return ReadGroup
	case mode&0o400 != 0 && uid == duid:
		return ReadUser
	}
	return ReadNone
}

// ShardName is the shard of a prefix for a class, uid and gid.
func ShardName(prefix, class string, uid, gid int64) string {
	return fmt.Sprintf("%s_%s_%d_%d.db", prefix, class, uid, gid)
}

// shardMode is a shard's permissions: readable by those its class names,
// and for NA the file's own.
func shardMode(class string, fileMode int64) os.FileMode {
	switch class {
	case ReadOther:
		return 0o644
	case ReadGroup:
		return 0o640
	case ReadUser:
		return 0o600
	}
	return os.FileMode(fileMode) & 0o666
}

// ShardStats is what refilling an index's shards did.
type ShardStats struct {
	Dirs   int `json:"dirs"`
	Files  int `json:"files"`
	Shards int `json:"shards"`
	// Removed counts shards of the prefix that no file needs any more.
	Removed int `json:"removed"`
	// Unchowned counts shards left owned by this account because only
	// root may give a file to another.
	Unchowned int `json:"unchowned,omitempty"`
}

type indexFile struct {
	inode, mtime, uid, gid, mode int64
}

// IndexEntry is a file of an index folder's db.db.
type IndexEntry struct {
	Name                               string
	Inode, Mtime, Size, UID, GID, Mode int64
}

// WalkIndex calls fn with each folder of the index at root that has a
// db.db, as a path relative to root ("" for root itself, with slashes),
// and the files its db.db lists.
func WalkIndex(root string, fn func(dir string, files []IndexEntry) error) error {
	return filepath.WalkDir(root, func(dir string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		dbFile := filepath.Join(dir, "db.db")
		if _, err := os.Stat(dbFile); err != nil {
			return nil
		}
		files, _, _, err := readEntries(dbFile)
		if err != nil {
			return fmt.Errorf("%s: %w", dbFile, err)
		}
		rel, _ := filepath.Rel(root, dir)
		if rel == "." {
			rel = ""
		}
		return fn(filepath.ToSlash(rel), files)
	})
}

// FillShards writes the permission shards of every folder of the index
// at root from the spread tree: each file in a folder's db.db whose inode
// and mtime have records there (with this extra_info) gets them in the
// shard of its class, beside db.db. A folder's shards are written whole
// each time, through a temporary file renamed into place, so records of
// files that changed or went are not kept, and a shard no file needs is
// removed.
func FillShards(s Spread, root, extraInfo string) (ShardStats, error) {
	var st ShardStats
	if err := s.Check(); err != nil {
		return st, err
	}
	mine := regexp.MustCompile(`^` + regexp.QuoteMeta(s.Prefix) + `_(OR|GR|UR|NA)_[0-9]+_[0-9]+\.db$`)
	spreads := map[string]*sql.DB{}
	defer func() {
		for _, db := range spreads {
			if db != nil {
				db.Close()
			}
		}
	}()
	spread := func(inode int64) *sql.DB {
		p := s.Path(inode)
		db, ok := spreads[p]
		if !ok {
			db, _ = openShard(p, false, false)
			spreads[p] = db
		}
		return db
	}
	err := filepath.WalkDir(root, func(dir string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		dbFile := filepath.Join(dir, "db.db")
		if _, err := os.Stat(dbFile); err != nil {
			return nil
		}
		st.Dirs++
		files, duid, dgid, err := readDir(dbFile)
		if err != nil {
			return fmt.Errorf("%s: %w", dbFile, err)
		}
		want := map[string][]indexFile{}
		modes := map[string]int64{}
		for _, f := range files {
			src := spread(f.inode)
			if src == nil {
				continue
			}
			var n int
			if err := src.QueryRow("SELECT count(*) FROM ext_assets WHERE inode = ? AND mtime = ? AND extra_info IS ?", f.inode, f.mtime, extraInfo).Scan(&n); err != nil || n == 0 {
				continue
			}
			class := ShardClass(f.mode, f.uid, f.gid, duid, dgid)
			name := ShardName(s.Prefix, class, f.uid, f.gid)
			if _, ok := modes[name]; !ok {
				modes[name] = f.mode
			}
			// An NA shard takes the narrowest mode of its files.
			modes[name] &= f.mode
			want[name] = append(want[name], f)
		}
		names := make([]string, 0, len(want))
		for n := range want {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			unchowned, err := writeShard(filepath.Join(dir, name), want[name], spread, extraInfo, shardMode(name[len(s.Prefix)+1:len(s.Prefix)+3], modes[name]))
			if err != nil {
				return err
			}
			if unchowned {
				st.Unchowned++
			}
			st.Shards++
			st.Files += len(want[name])
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if mine.MatchString(e.Name()) && want[e.Name()] == nil {
				if os.Remove(filepath.Join(dir, e.Name())) == nil {
					st.Removed++
				}
			}
		}
		return nil
	})
	return st, err
}

// readDir is a folder's files from its db.db, and the folder's owner.
func readDir(dbFile string) ([]indexFile, int64, int64, error) {
	es, duid, dgid, err := readEntries(dbFile)
	out := make([]indexFile, len(es))
	for i, e := range es {
		out[i] = indexFile{inode: e.Inode, mtime: e.Mtime, uid: e.UID, gid: e.GID, mode: e.Mode}
	}
	return out, duid, dgid, err
}

// readEntries is a folder's files from its db.db, and the folder's owner.
func readEntries(dbFile string) ([]IndexEntry, int64, int64, error) {
	db, err := sql.Open("sqlite", "file:"+dbFile+"?mode=ro&_pragma=busy_timeout(60000)")
	if err != nil {
		return nil, 0, 0, err
	}
	defer db.Close()
	var duid, dgid int64
	if err := db.QueryRow("SELECT uid, gid FROM summary WHERE isroot = 1 LIMIT 1").Scan(&duid, &dgid); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, 0, 0, err
		}
	}
	rows, err := db.Query("SELECT name, inode, mtime, size, uid, gid, mode FROM entries WHERE type = 'f'")
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var out []IndexEntry
	for rows.Next() {
		var ino string
		var f IndexEntry
		var size sql.NullInt64
		if err := rows.Scan(&f.Name, &ino, &f.Mtime, &size, &f.UID, &f.GID, &f.Mode); err != nil {
			return nil, 0, 0, err
		}
		f.Size = size.Int64
		// GUFI keeps the inode as text.
		if f.Inode, err = strconv.ParseInt(strings.TrimSpace(ino), 10, 64); err != nil {
			continue
		}
		out = append(out, f)
	}
	return out, duid, dgid, rows.Err()
}

// writeShard writes one shard whole from the spread tree, and reports
// whether it could not be given to the files' owner.
func writeShard(file string, files []indexFile, spread func(int64) *sql.DB, extraInfo string, mode os.FileMode) (bool, error) {
	tmp := file + ".tmp"
	os.Remove(tmp)
	db, err := openShard(tmp, true, true)
	if err != nil {
		return false, err
	}
	tx, err := begin(db, tmp)
	if err != nil {
		db.Close()
		return false, err
	}
	fail := func(err error) (bool, error) {
		tx.Rollback()
		db.Close()
		os.Remove(tmp)
		return false, fmt.Errorf("%s: %w", file, err)
	}
	for _, f := range files {
		src := spread(f.inode)
		if err := copyKey(src, tx, f.inode, f.mtime, extraInfo); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	db.Close()
	// Written with no syncs; synced once, so the rename puts a whole file
	// in place.
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0); err == nil {
		serr := f.Sync()
		f.Close()
		if serr != nil {
			os.Remove(tmp)
			return false, serr
		}
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return false, err
	}
	unchowned := false
	if len(files) > 0 && (int64(os.Getuid()) != files[0].uid || int64(os.Getgid()) != files[0].gid) {
		if os.Chown(tmp, int(files[0].uid), int(files[0].gid)) != nil {
			unchowned = true
		}
	}
	return unchowned, os.Rename(tmp, file)
}

// copyKey copies one version of a file's records from a spread db.
func copyKey(src *sql.DB, tx *sql.Tx, inode, mtime int64, extra string) error {
	rows, err := src.Query("SELECT "+assetCols+" FROM ext_assets WHERE inode = ? AND mtime = ? AND extra_info IS ?", inode, mtime, extra)
	if err != nil {
		return err
	}
	var assets [][]any
	for rows.Next() {
		vals := make([]any, 11)
		ptrs := make([]any, 11)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return err
		}
		assets = append(assets, vals)
	}
	rows.Close()
	for _, a := range assets {
		if _, err := tx.Exec("INSERT INTO ext_assets ("+assetCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", a...); err != nil {
			return err
		}
		crows, err := src.Query("SELECT "+bm25Cols+" FROM ext_bm25 WHERE asset_id = ?", a[4])
		if err != nil {
			return err
		}
		var chunks [][]any
		for crows.Next() {
			vals := make([]any, 14)
			ptrs := make([]any, 14)
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := crows.Scan(ptrs...); err != nil {
				crows.Close()
				return err
			}
			chunks = append(chunks, vals)
		}
		crows.Close()
		for _, c := range chunks {
			if _, err := tx.Exec("INSERT INTO ext_bm25 ("+bm25Cols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", c...); err != nil {
				return err
			}
		}
	}
	return nil
}
