package extdb

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
)

// Rekey moves the records of a db (an artifact or a spread tree db) from
// one extra_info to another: each file's records take the new extra_info
// and the outer_file_uuid (and own asset_id) that key gives, so text read
// before keeps being found and is not read again. It reports how many
// files it moved.
func Rekey(file, from, to string) (int, error) {
	if from == to {
		return 0, nil
	}
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(60000)&_txlock=immediate")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	has := map[string]bool{}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		has[n] = true
	}
	rows.Close()
	if !has["ext_assets"] {
		return 0, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	type key struct {
		inode, mtime int64
		uuid         string
	}
	var keys []key
	rows, err = tx.Query("SELECT DISTINCT inode, mtime, outer_file_uuid FROM ext_assets WHERE extra_info IS ?", from)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.inode, &k.mtime, &k.uuid); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if _, err := tx.Exec("CREATE TEMP TABLE IF NOT EXISTS rekey (old TEXT PRIMARY KEY, new TEXT NOT NULL); DELETE FROM temp.rekey"); err != nil {
		return 0, err
	}
	for _, k := range keys {
		nu := Key{Inode: k.inode, Mtime: k.mtime, ExtraInfo: to}.FileUUID()
		if _, err := tx.Exec("INSERT OR REPLACE INTO temp.rekey VALUES (?, ?)", k.uuid, nu); err != nil {
			return 0, err
		}
	}
	// A file's own asset is named by its outer_file_uuid; other assets
	// keep their ids.
	set := func(table, extra string) string {
		s := "UPDATE " + table + " SET outer_file_uuid = (SELECT new FROM temp.rekey WHERE old = " + table + ".outer_file_uuid), " +
			"asset_id = CASE WHEN asset_id = outer_file_uuid THEN (SELECT new FROM temp.rekey WHERE old = " + table + ".outer_file_uuid) ELSE asset_id END"
		if extra != "" {
			s += ", extra_info = " + extra
		}
		return s + " WHERE outer_file_uuid IN (SELECT old FROM temp.rekey)"
	}
	stmts := []string{set("ext_assets", "?"), set("ext_bm25", "?")}
	args := [][]any{{to}, {to}}
	if has["studio_vectors"] {
		stmts = append(stmts, set("studio_vectors", ""))
		args = append(args, nil)
	}
	if has["studio_extract_files"] {
		stmts = append(stmts, "UPDATE OR REPLACE studio_extract_files SET extra_info = ? WHERE extra_info IS ?")
		args = append(args, []any{to, from})
	}
	for i, s := range stmts {
		if _, err := tx.Exec(s, args[i]...); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(keys), nil
}

// RekeySpread is Rekey of every db of a spread tree's file system.
func RekeySpread(s Spread, from, to string) (int, error) {
	n := 0
	dir := filepath.Join(s.Top, s.Prefix, s.Fsid)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".db") {
			return nil
		}
		k, err := Rekey(p, from, to)
		n += k
		return err
	})
	return n, err
}
