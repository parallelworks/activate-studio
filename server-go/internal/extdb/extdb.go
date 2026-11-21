// Package extdb writes extracted text in GUFI's common external database
// schema (ext_assets and the fts5 table ext_bm25, with their views; see
// schema.sql), which GUFI's examples publish and the Studio's search
// already reads beside each directory's db.db.
//
// What it writes here is the extraction artifact: one SQLite file holding
// the text of a set of files, each keyed by inode, mtime and extra_info,
// with no paths in it, so it stays valid when files are renamed and can be
// loaded into any index of the same files. A loader puts its rows into the
// spread tree and from there into the index's permission shards
// (prefix_XX_uid_gid.db). Beside the schema's tables an artifact has
// studio_extract_files, one row per file looked at, with what came of it,
// so a run stopped part way, or a job of an array that ran again, goes on
// where it left off.
package extdb

import (
	"crypto/sha1"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// Prefix is the Studio's prefix family: its permission shards are named
// studio_XX_uid_gid.db.
const Prefix = "studio"

// Schema is the prefix's .sql: the tables and views of each spread tree
// db and permission shard.
//
//go:embed schema.sql
var Schema string

// PrefixTables are the tables of the prefix that carry records, as the
// prefixtables configuration lists them.
var PrefixTables = []string{"ext_assets", "ext_bm25"}

// filesTable is the artifact's own table of outcomes.
const filesTable = `CREATE TABLE IF NOT EXISTS studio_extract_files (inode INTEGER NOT NULL, mtime INTEGER NOT NULL, extra_info TEXT NOT NULL,
 size INTEGER, state TEXT NOT NULL, reader TEXT, chunks INTEGER NOT NULL DEFAULT 0, chars INTEGER NOT NULL DEFAULT 0, error TEXT, at INTEGER NOT NULL,
 PRIMARY KEY (inode, mtime, extra_info)) WITHOUT ROWID;`

// Key identifies one version of one file: its inode, its mtime in
// seconds, and the extra_info string that makes the pair unique.
type Key struct {
	Inode, Mtime int64
	ExtraInfo    string
}

// FileUUID is the file's outer_file_uuid: the same for the same key in
// every run, so a second extraction replaces the first and never adds to
// it. It has the form of a name-based (version 5) UUID.
func (k Key) FileUUID() string {
	h := sha1.Sum([]byte("studio\x00" + strconv.FormatInt(k.Inode, 10) + "\x00" + strconv.FormatInt(k.Mtime, 10) + "\x00" + k.ExtraInfo))
	h[6] = h[6]&0x0f | 0x50
	h[8] = h[8]&0x3f | 0x80
	s := hex.EncodeToString(h[:16])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// The states of studio_extract_files.
const (
	StateText    = "text"    // read; its text is in ext_bm25
	StateEmpty   = "empty"   // read, and no text came of it
	StateSkipped = "skipped" // nothing here reads files of its kind
	StateChanged = "changed" // the file is not the version listed
	StateMissing = "missing" // the file could not be opened
	StateFailed  = "failed"  // a reader failed on it
)

// Final reports whether a state stands until the file changes. Only
// text does: a file left empty, skipped or failed may give text once a
// helper is installed or a reader is fixed, so a run with retry reads
// every file in another state again.
func Final(state string) bool { return state == StateText }

// File is what one file's extraction gave.
type File struct {
	Key
	Size  int64
	State string
	// Reader is the kind of reader that read it (text, downmark, native,
	// image, external), and the asset's route.
	Reader   string
	Mimetype string
	// Provenance is the asset's indexing_provenance, JSON.
	Provenance string
	Chunks     []Chunk
	Err        string
}

// Artifact is an extraction artifact open for writing. It is not safe
// for concurrent use: one writer puts every file.
type Artifact struct {
	db      *sql.DB
	tx      *sql.Tx
	pending int
	last    time.Time
	// CommitEvery is how many files go into one transaction, and
	// CommitAfter the longest a transaction stays open; whichever comes
	// first. A stopped run loses at most that much work.
	CommitEvery int
	CommitAfter time.Duration
}

// Create opens the artifact at path, making it and its tables when
// missing.
func Create(path string) (*Artifact, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(60000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(Schema + "\n" + filesTable); err != nil {
		db.Close()
		return nil, fmt.Errorf("extdb: %s: %w", path, err)
	}
	return &Artifact{db: db, CommitEvery: 100, CommitAfter: 5 * time.Second}, nil
}

// States are the files recorded so far, with their states.
func (a *Artifact) States() (map[Key]string, error) {
	rows, err := a.db.Query("SELECT inode, mtime, extra_info, state FROM studio_extract_files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Key]string{}
	for rows.Next() {
		var k Key
		var s string
		if err := rows.Scan(&k.Inode, &k.Mtime, &k.ExtraInfo, &s); err != nil {
			return nil, err
		}
		out[k] = s
	}
	return out, rows.Err()
}

func (a *Artifact) begin() error {
	if a.tx != nil {
		return nil
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	a.tx, a.pending, a.last = tx, 0, time.Now()
	return nil
}

// Put records one file and, when it has text, its asset and chunks,
// replacing whatever the artifact held for its key.
func (a *Artifact) Put(f File) error {
	if f.State == "" {
		return errors.New("extdb: a file needs a state")
	}
	if err := a.begin(); err != nil {
		return err
	}
	tx := a.tx
	id := f.FileUUID()
	// A key put before (a retry) loses its old rows first. ext_bm25 is
	// matched by its asset, which the key alone decides.
	if _, err := tx.Exec("DELETE FROM ext_bm25 WHERE asset_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM ext_assets WHERE asset_id = ?", id); err != nil {
		return err
	}
	chars := 0
	if f.State == StateText {
		var prov any
		if f.Provenance != "" {
			prov = f.Provenance
		}
		var mime any
		if f.Mimetype != "" {
			mime = f.Mimetype
		}
		// One asset per file, the file itself: its asset_id is its
		// outer_file_uuid, as GUFI's example gives a file's own asset.
		if _, err := tx.Exec("INSERT INTO ext_assets (inode, mtime, extra_info, outer_file_uuid, asset_id, component_type, member_path_json, file_metadata, indexing_provenance, mimetype, asset_route) VALUES (?, ?, ?, ?, ?, 'file', '[]', NULL, ?, ?, ?)",
			f.Inode, f.Mtime, f.ExtraInfo, id, id, prov, mime, f.Reader); err != nil {
			return err
		}
		st, err := tx.Prepare("INSERT INTO ext_bm25 (inode, mtime, extra_info, outer_file_uuid, asset_id, chunk_sequence, start_position, end_position, content) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		for _, c := range f.Chunks {
			if _, err := st.Exec(f.Inode, f.Mtime, f.ExtraInfo, id, id, c.Seq, c.Start, c.End, c.Text); err != nil {
				st.Close()
				return err
			}
			chars += c.End - c.Start
		}
		st.Close()
	}
	var errText any
	if f.Err != "" {
		errText = f.Err
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO studio_extract_files (inode, mtime, extra_info, size, state, reader, chunks, chars, error, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		f.Inode, f.Mtime, f.ExtraInfo, f.Size, f.State, f.Reader, len(f.Chunks), chars, errText, time.Now().Unix()); err != nil {
		return err
	}
	a.pending++
	if a.pending >= a.CommitEvery || time.Since(a.last) >= a.CommitAfter {
		return a.Commit()
	}
	return nil
}

// Commit writes the files put so far.
func (a *Artifact) Commit() error {
	if a.tx == nil {
		return nil
	}
	err := a.tx.Commit()
	a.tx = nil
	return err
}

// Close commits and closes the artifact.
func (a *Artifact) Close() error {
	err := a.Commit()
	if cerr := a.db.Close(); err == nil {
		err = cerr
	}
	return err
}
