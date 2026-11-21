package extdb

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Spread is a spread tree: the pristine copy of the records of one prefix
// family for one file system, sharded by inode so that no path or name is
// involved and an index can be rebuilt from the source and refilled from
// here. The db of inode 123456789 with two digits a level is
// <Top>/<Prefix>/<Fsid>/89/67/45.db, as in GUFI's plan for system-level
// external databases. Every db holds the prefix's tables (Schema).
type Spread struct {
	Top, Prefix, Fsid string
}

// spreadDigits are the digits of each level: level 1, level 2, and the db
// files within a level 2 folder.
var spreadDigits = [3]int{2, 2, 2}

// Check reports a spread whose names cannot be folder names.
func (s Spread) Check() error {
	if s.Top == "" {
		return errors.New("extdb: a spread tree needs its top folder")
	}
	for _, n := range []string{s.Prefix, s.Fsid} {
		if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\\\x00") {
			return fmt.Errorf("extdb: %q cannot name a spread tree folder", n)
		}
	}
	return nil
}

// Path is the db that holds an inode's records.
func (s Spread) Path(inode int64) string {
	parts := []string{s.Top, s.Prefix, s.Fsid}
	n := uint64(inode)
	for i, d := range spreadDigits {
		mod := uint64(1)
		for j := 0; j < d; j++ {
			mod *= 10
		}
		part := fmt.Sprintf("%0*d", d, n%mod)
		n /= mod
		if i == len(spreadDigits)-1 {
			part += ".db"
		}
		parts = append(parts, part)
	}
	return filepath.Join(parts...)
}

// configure writes the two configuration databases of GUFI's plan at the
// spread tree's top, fsid.db and extdbprefix.db, and the prefix's .sql,
// once.
func (s Spread) configure() error {
	if err := os.MkdirAll(s.Top, 0o755); err != nil {
		return err
	}
	sqlPath := filepath.Join(s.Top, s.Prefix+".sql")
	if _, err := os.Stat(sqlPath); err != nil {
		if err := os.WriteFile(sqlPath, []byte(Schema), 0o644); err != nil {
			return err
		}
	}
	exec := func(file string, stmts ...string) error {
		db, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(60000)")
		if err != nil {
			return err
		}
		defer db.Close()
		for _, st := range stmts {
			if _, err := db.Exec(st); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
		}
		return nil
	}
	if err := exec(filepath.Join(s.Top, "fsid.db"),
		"CREATE TABLE IF NOT EXISTS fsid (fsid TEXT PRIMARY KEY, path TEXT, type TEXT, comments TEXT)",
		"INSERT OR IGNORE INTO fsid (fsid) VALUES ("+quote(s.Fsid)+")"); err != nil {
		return err
	}
	stmts := []string{
		"CREATE TABLE IF NOT EXISTS prefix (prefix TEXT PRIMARY KEY, level1digits INT, level2digits INT, sublevel2digits INT, sqlpath TEXT, spreadtop TEXT)",
		"CREATE TABLE IF NOT EXISTS prefixtables (prefix TEXT, tablename TEXT, tabledesc TEXT, PRIMARY KEY (prefix, tablename))",
		fmt.Sprintf("INSERT OR IGNORE INTO prefix VALUES (%s, %d, %d, %d, %s, %s)", quote(s.Prefix), spreadDigits[0], spreadDigits[1], spreadDigits[2], quote(sqlPath), quote(s.Top)),
	}
	desc := map[string]string{"ext_assets": "one row per asset of a file", "ext_bm25": "the text of each asset in chunks (fts5)"}
	for _, t := range PrefixTables {
		stmts = append(stmts, fmt.Sprintf("INSERT OR IGNORE INTO prefixtables VALUES (%s, %s, %s)", quote(s.Prefix), quote(t), quote(desc[t])))
	}
	return exec(filepath.Join(s.Top, "extdbprefix.db"), stmts...)
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// openShard opens a db of the prefix's tables, taking the write lock at
// the start of each transaction. With create, its folder is made, and
// with temp the db is a file of its own being written (a shard before it
// is renamed into place): it is written with no journal and no syncs, and
// synced once by the caller. begin starts a transaction that first makes
// the prefix's tables where they are missing, so a new db costs one
// commit.
func openShard(file string, create, temp bool) (*sql.DB, error) {
	if !create {
		if _, err := os.Stat(file); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + file + "?_pragma=busy_timeout(60000)&_txlock=immediate&_pragma=synchronous(NORMAL)"
	if temp {
		dsn = "file:" + file + "?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// begin starts a write to a db of the prefix's tables, making them first
// where they are missing.
func begin(db *sql.DB, file string) (*sql.Tx, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(Schema); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return tx, nil
}

// SpreadStats is what loading artifacts into a spread tree did.
type SpreadStats struct {
	Files  int `json:"files"`
	Chunks int `json:"chunks"`
	DBs    int `json:"dbs"`
}

const bm25Cols = "inode, mtime, extra_info, outer_file_uuid, asset_id, chunk_sequence, start_position, end_position, page_start, page_end, audio_start_sec, video_start_sec, video_end_sec, content"
const assetCols = "inode, mtime, extra_info, outer_file_uuid, asset_id, component_type, member_path_json, file_metadata, indexing_provenance, mimetype, asset_route"

// LoadSpread puts the records of an extraction artifact into the spread
// tree: for each file with text, its records in the tree replace those of
// the same inode, mtime and extra_info, under the db's write lock. Other
// versions of a file (other mtimes) are kept.
func LoadSpread(s Spread, artifact string) (SpreadStats, error) {
	return LoadSpreadSince(s, artifact, 0)
}

// LoadSpreadSince is LoadSpread of only the files the artifact recorded at
// or after since (Unix seconds): what one run added to an artifact that
// runs keep adding to.
func LoadSpreadSince(s Spread, artifact string, since int64) (SpreadStats, error) {
	var st SpreadStats
	if err := s.Check(); err != nil {
		return st, err
	}
	if err := s.configure(); err != nil {
		return st, err
	}
	art, err := sql.Open("sqlite", "file:"+artifact+"?mode=ro&_pragma=busy_timeout(60000)")
	if err != nil {
		return st, err
	}
	defer art.Close()
	art.SetMaxOpenConns(1)
	// ext_bm25 is found by asset only through a scan; one scan here
	// gives every chunk's rowid by asset.
	if _, err := art.Exec("CREATE TEMP TABLE chunks AS SELECT asset_id AS a, rowid AS r FROM ext_bm25; CREATE INDEX temp.chunks_a ON chunks(a)"); err != nil {
		return st, fmt.Errorf("%s: %w", artifact, err)
	}
	type asset struct {
		inode int64
		vals  []any
	}
	q, args := "SELECT "+assetCols+" FROM ext_assets", []any{}
	if since > 0 {
		q += " a WHERE EXISTS (SELECT 1 FROM studio_extract_files f WHERE f.inode = a.inode AND f.mtime = a.mtime AND f.extra_info = a.extra_info AND f.at >= ?)"
		args = append(args, since)
	}
	rows, err := art.Query(q, args...)
	if err != nil {
		return st, fmt.Errorf("%s: %w", artifact, err)
	}
	byDB := map[string][]asset{}
	for rows.Next() {
		vals := make([]any, 11)
		ptrs := make([]any, 11)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return st, err
		}
		ino, ok := vals[0].(int64)
		if !ok {
			continue
		}
		p := s.Path(ino)
		byDB[p] = append(byDB[p], asset{ino, vals})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	paths := make([]string, 0, len(byDB))
	for p := range byDB {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	files := map[[3]any]bool{}
	for _, p := range paths {
		db, err := openShard(p, true, false)
		if err != nil {
			return st, err
		}
		tx, err := begin(db, p)
		if err != nil {
			db.Close()
			return st, err
		}
		for _, a := range byDB[p] {
			key := [3]any{a.vals[0], a.vals[1], a.vals[2]}
			if !files[key] {
				files[key] = true
				st.Files++
				if err := deleteKey(tx, a.vals[0], a.vals[1], a.vals[2]); err != nil {
					tx.Rollback()
					db.Close()
					return st, err
				}
			}
			if _, err := tx.Exec("INSERT INTO ext_assets ("+assetCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", a.vals...); err != nil {
				tx.Rollback()
				db.Close()
				return st, fmt.Errorf("%s: %w", p, err)
			}
			n, err := copyChunks(art, tx, a.vals[4])
			if err != nil {
				tx.Rollback()
				db.Close()
				return st, fmt.Errorf("%s: %w", p, err)
			}
			st.Chunks += n
		}
		if err := tx.Commit(); err != nil {
			db.Close()
			return st, err
		}
		db.Close()
		st.DBs++
	}
	return st, nil
}

// deleteKey removes one version of a file's records.
func deleteKey(tx *sql.Tx, inode, mtime, extra any) error {
	if _, err := tx.Exec("DELETE FROM ext_bm25 WHERE asset_id IN (SELECT asset_id FROM ext_assets WHERE inode = ? AND mtime = ? AND extra_info IS ?)", inode, mtime, extra); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM ext_assets WHERE inode = ? AND mtime = ? AND extra_info IS ?", inode, mtime, extra)
	return err
}

// copyChunks copies an asset's ext_bm25 rows from src into tx.
func copyChunks(src *sql.DB, tx *sql.Tx, assetID any) (int, error) {
	rows, err := src.Query("SELECT "+bm25Cols+" FROM ext_bm25 WHERE rowid IN (SELECT r FROM temp.chunks WHERE a = ?)", assetID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		vals := make([]any, 14)
		ptrs := make([]any, 14)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		if _, err := tx.Exec("INSERT INTO ext_bm25 ("+bm25Cols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", vals...); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}
