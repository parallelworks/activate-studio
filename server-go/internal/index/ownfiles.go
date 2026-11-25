package index

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/ownfile"
)

// LeaveOutOwn takes the AGENTS.md files the Studio keeps for pw code
// (internal/ownfile) out of a tree just built of the folder src: the row of
// each, and what it added to its folder's totals. gufi_dir2index passes
// over names it is given, and a person's AGENTS.md has the same one. One that cannot
// be taken out stays, with a line to warn.
func LeaveOutOwn(built, src string, warn func(string)) {
	filepath.WalkDir(built, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && p != built {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(built, p)
		if err != nil || !ownfile.At(filepath.Join(src, rel, ownfile.Name)) {
			return nil
		}
		db := filepath.Join(p, "db.db")
		if _, err := os.Stat(db); err != nil {
			return nil
		}
		if err := leaveOut(db, ownfile.Name); err != nil && warn != nil {
			warn(fmt.Sprintf("%s could not be left out of the index: %v", filepath.Join(src, rel, ownfile.Name), err))
		}
		return nil
	})
}

// folderTotals are a folder's totals over what is in it, as gufi_dir2index
// (sumit in GUFI's utils.c) makes them: the columns anything here reads,
// and the others of their kind. An empty folder's least and greatest are
// the ends of the range.
var folderTotals = strings.NewReplacer("LEAST", "9223372036854775807", "GREATEST", "(-9223372036854775807 - 1)").Replace(`UPDATE summary SET
 (totfiles, totsize, minsize, maxsize, totzero, totltk, totmtk, totltm, totmtm, totmtg, totmtt) =
 (SELECT count(*), coalesce(sum(size), 0), coalesce(min(size), LEAST), coalesce(max(size), GREATEST),
  coalesce(sum(size = 0), 0), coalesce(sum(size <= 1024), 0), coalesce(sum(size > 1024), 0),
  coalesce(sum(size <= 1048576), 0), coalesce(sum(size > 1048576), 0), coalesce(sum(size > 1073741824), 0), coalesce(sum(size > 1099511627776), 0)
  FROM entries WHERE type = 'f'),
 (minmtime, maxmtime, totmtime) =
 (SELECT coalesce(min(mtime), LEAST), coalesce(max(mtime), GREATEST), coalesce(sum(mtime), 0) FROM entries)
 WHERE isroot = 1`)

// leaveOut takes the file name out of one folder's database.
func leaveOut(file, name string) error {
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(60000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM entries WHERE name = ? AND type = 'f'", name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	if _, err := tx.Exec(folderTotals); err != nil {
		return err
	}
	return tx.Commit()
}
