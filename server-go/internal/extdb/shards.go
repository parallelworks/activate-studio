package extdb

import (
	"context"
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

// Shards are named and classed as GUFI main's spread_to_external names
// them, judged against the folder's owner: <prefix>_ur.db when the
// folder's owner owns the file and may read it, <prefix>_gr.db when the
// folder's group is the file's and may read it, <prefix>_or.db when
// others may read it, and <prefix>_<uid>_<gid>.db, of the file's own
// owner and group, for anything else. Attaching a shard is then the
// permission check: its mode lets in those its name says.
const (
	ReadUser  = "ur"
	ReadGroup = "gr"
	ReadOther = "or"
	// ReadNone is the class of a file none of the others fit, whose
	// shard is named by its uid and gid.
	ReadNone = ""
)

// ShardClass is the class of a file of this owner and mode in a folder
// of this owner, in GUFI's order.
func ShardClass(mode, uid, gid, duid, dgid int64) string {
	switch {
	case uid == duid && mode&0o400 != 0:
		return ReadUser
	case gid == dgid && mode&0o040 != 0:
		return ReadGroup
	case mode&0o004 != 0:
		return ReadOther
	}
	return ReadNone
}

// ShardName is the shard of a prefix for a class, and for ReadNone the
// file's uid and gid.
func ShardName(prefix, class string, uid, gid int64) string {
	if class == ReadNone {
		return fmt.Sprintf("%s_%d_%d.db", prefix, uid, gid)
	}
	return prefix + "_" + class + ".db"
}

// shardMode is a shard's permissions: readable by those its class names,
// and for ReadNone the narrowest of its files' own. Unlike GUFI's, the
// owner may always read and write it, so a Studio that is not root (and
// cannot give a shard away) still reads the shards it writes.
func shardMode(class string, fileMode int64) os.FileMode {
	switch class {
	case ReadOther:
		return 0o644
	case ReadGroup:
		return 0o640
	case ReadUser:
		return 0o600
	}
	return os.FileMode(fileMode)&0o666 | 0o600
}

// legacyShard matches the shards the Studio wrote before
// (studio_UR_501_20.db), removed wherever a refill finds them.
var legacyShard = regexp.MustCompile(`^` + LegacyPrefix + `_(OR|GR|UR|NA)_[0-9]+_[0-9]+\.db$`)

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
	// Legacy counts the earlier Studio's shards removed (studio_*).
	Legacy int `json:"legacy,omitempty"`
	// Vectors counts the shards given ext_vectors, and VectorError is the
	// first failure to make one; such a shard keeps its text alone.
	Vectors     int    `json:"vectors,omitempty"`
	VectorError string `json:"vectorError,omitempty"`
}

// ShardOptions are FillShardsWith's options.
type ShardOptions struct {
	// GufiSqlite3, when set, gives each shard whose files have vectors in
	// the spread tree an ext_vectors (VecShards); without it shards hold
	// text alone.
	GufiSqlite3 string
	Ctx         context.Context
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
	return FillShardsWith(s, root, extraInfo, ShardOptions{})
}

// FillShardsWith is FillShards with options.
func FillShardsWith(s Spread, root, extraInfo string, o ShardOptions) (ShardStats, error) {
	var st ShardStats
	if err := s.Check(); err != nil {
		return st, err
	}
	ctx := o.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	vec := o.GufiSqlite3 != ""
	mine := regexp.MustCompile(`^` + regexp.QuoteMeta(s.Prefix) + `_(ur|gr|or|[0-9]+_[0-9]+)\.db$`)
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
		classes := map[string]string{}
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
			classes[name] = class
			// A ReadNone shard takes the narrowest mode of its files.
			modes[name] &= f.mode
			want[name] = append(want[name], f)
		}
		names := make([]string, 0, len(want))
		for n := range want {
			names = append(names, n)
		}
		sort.Strings(names)
		var tmps []pendingShard
		var withVecs []string
		for _, name := range names {
			ps, err := writeShard(filepath.Join(dir, name), want[name], spread, extraInfo, shardMode(classes[name], modes[name]), vec)
			if err != nil {
				for _, t := range tmps {
					os.Remove(t.tmp)
				}
				return err
			}
			tmps = append(tmps, ps)
			if ps.vectors {
				withVecs = append(withVecs, ps.tmp)
			}
		}
		// The folder's shards with vectors get ext_vectors in one
		// gufi_sqlite3; where that fails they keep their text alone.
		if len(withVecs) > 0 {
			if err := VecShards(ctx, o.GufiSqlite3, withVecs); err != nil {
				if st.VectorError == "" {
					st.VectorError = err.Error()
				}
				for _, t := range withVecs {
					dropVecRows(t)
				}
			} else {
				st.Vectors += len(withVecs)
			}
		}
		for _, ps := range tmps {
			unchowned, err := ps.finish()
			if err != nil {
				return err
			}
			if unchowned {
				st.Unchowned++
			}
			st.Shards++
			st.Files += ps.files
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			switch {
			case mine.MatchString(e.Name()) && want[e.Name()] == nil:
				if os.Remove(filepath.Join(dir, e.Name())) == nil {
					st.Removed++
				}
			case legacyShard.MatchString(e.Name()):
				if os.Remove(filepath.Join(dir, e.Name())) == nil {
					st.Legacy++
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

// pendingShard is a shard written to its temporary file, to be put in
// place by finish.
type pendingShard struct {
	tmp, file string
	files     int
	uid, gid  int64
	mode      os.FileMode
	// vectors says its studio_vectors has rows, for VecShards.
	vectors bool
}

// writeShard writes one shard whole from the spread tree to a temporary
// file beside it; with vec, the files' vectors go into its studio_vectors.
func writeShard(file string, files []indexFile, spread func(int64) *sql.DB, extraInfo string, mode os.FileMode, vec bool) (pendingShard, error) {
	ps := pendingShard{tmp: file + ".tmp", file: file, files: len(files), mode: mode}
	if len(files) > 0 {
		ps.uid, ps.gid = files[0].uid, files[0].gid
	}
	os.Remove(ps.tmp)
	db, err := openShard(ps.tmp, true, true)
	if err != nil {
		return ps, err
	}
	tx, err := begin(db, ps.tmp, vec)
	if err != nil {
		db.Close()
		return ps, err
	}
	fail := func(err error) (pendingShard, error) {
		tx.Rollback()
		db.Close()
		os.Remove(ps.tmp)
		return ps, fmt.Errorf("%s: %w", file, err)
	}
	nvec := 0
	for _, f := range files {
		src := spread(f.inode)
		n, err := copyKey(src, tx, f.inode, f.mtime, extraInfo, vec)
		if err != nil {
			return fail(err)
		}
		nvec += n
	}
	if vec && nvec == 0 {
		if _, err := tx.Exec("DROP TABLE studio_vectors"); err != nil {
			return fail(err)
		}
	}
	ps.vectors = nvec > 0
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	db.Close()
	return ps, nil
}

// finish syncs a written shard and renames it into place with its mode
// and owner, and reports whether it could not be given to the files'
// owner.
func (ps pendingShard) finish() (bool, error) {
	// Written with no syncs; synced once, so the rename puts a whole file
	// in place.
	if f, err := os.OpenFile(ps.tmp, os.O_RDWR, 0); err == nil {
		serr := f.Sync()
		f.Close()
		if serr != nil {
			os.Remove(ps.tmp)
			return false, serr
		}
	}
	if err := os.Chmod(ps.tmp, ps.mode); err != nil {
		os.Remove(ps.tmp)
		return false, err
	}
	unchowned := false
	if ps.files > 0 && (int64(os.Getuid()) != ps.uid || int64(os.Getgid()) != ps.gid) {
		if os.Chown(ps.tmp, int(ps.uid), int(ps.gid)) != nil {
			unchowned = true
		}
	}
	return unchowned, os.Rename(ps.tmp, ps.file)
}

// dropVecRows leaves a shard whose vectors could not be made with its
// text alone.
func dropVecRows(file string) {
	db, err := openShard(file, false, true)
	if err != nil {
		return
	}
	defer db.Close()
	db.Exec("DROP TABLE IF EXISTS studio_vectors; VACUUM;")
}

// copyKey copies one version of a file's records from a spread db, with
// vec its vectors too, and says how many vectors it copied.
func copyKey(src *sql.DB, tx *sql.Tx, inode, mtime int64, extra string, vec bool) (int, error) {
	rows, err := src.Query("SELECT "+assetCols+" FROM ext_assets WHERE inode = ? AND mtime = ? AND extra_info IS ?", inode, mtime, extra)
	if err != nil {
		return 0, err
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
			return 0, err
		}
		assets = append(assets, vals)
	}
	rows.Close()
	nvec := 0
	for _, a := range assets {
		if _, err := tx.Exec("INSERT INTO ext_assets ("+assetCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", a...); err != nil {
			return 0, err
		}
		crows, err := src.Query("SELECT "+bm25Cols+" FROM ext_bm25 WHERE asset_id = ?", a[4])
		if err != nil {
			return 0, err
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
				return 0, err
			}
			chunks = append(chunks, vals)
		}
		crows.Close()
		for _, c := range chunks {
			if _, err := tx.Exec("INSERT INTO ext_bm25 ("+bm25Cols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", c...); err != nil {
				return 0, err
			}
		}
		if vec {
			var has int
			src.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'studio_vectors'").Scan(&has)
			if has > 0 {
				n, err := copyVecRows(src, tx, a[4])
				if err != nil {
					return 0, err
				}
				nvec += n
			}
		}
	}
	return nvec, nil
}
