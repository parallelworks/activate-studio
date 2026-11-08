// Package state is stateFormat.ts: the format of the Studio's own state,
// and the snapshots the updater takes of it before any change of version
// (docs/UPDATES.md, "State"). Files on disk are Node's, byte for byte in
// layout, so Node and Go read each other's markers and snapshots.
//
// The state is what a newer version might migrate: settings,
// conversations, the credential vault, the library list and labels,
// per-user data, tasks, the fleet, extensions, branding, and the
// launcher's studio.env. INDEX_BASE/state-format.json says which format
// the state is in. A version reads the formats up to its own Format; one
// that finds a newer format puts back the newest snapshot in a format it
// reads, keeping what it replaces as a snapshot too.
//
// The updates track uses TakeSnapshot, ListSnapshots and RestoreSnapshot.
package state

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// Format is the state format this version writes and reads. Raise it only
// with a migration, and only after a snapshot (docs/UPDATES.md).
const Format = 1

// MarkerName is the marker's file name in INDEX_BASE.
const MarkerName = "state-format.json"

// keep is how many snapshots are kept besides the newest of each format.
const keep = 3

// Directories of INDEX_BASE that are state, copied whole.
var stateDirs = map[string]bool{"users": true, "tasks": true, "fleet": true, "extensions": true, "branding": true, "ssh": true}

// Top-level files of INDEX_BASE that are caches, locks or logs.
var skipFiles = map[string]bool{".studio-instance": true, "embed-cache.db": true, "embed-cache.db-wal": true, "embed-cache.db-shm": true, "semantic-map.json": true, "runs.jsonl": true}

// Below users/<id>/, the semantic maps are caches.
var skipUnderUsers = map[string]bool{"maps": true}

const configName = "@config/studio.env"

// Marker is state-format.json.
type Marker struct {
	Format    int    `json:"format"`
	WrittenBy string `json:"writtenBy,omitempty"`
	At        string `json:"at,omitempty"`
}

// Snapshot is a snapshot's snapshot.json.
type Snapshot struct {
	ID string `json:"id"`
	// The version running when it was taken, and the one it was taken for.
	From   string  `json:"from"`
	To     *string `json:"to"`
	Reason string  `json:"reason"`
	// Format is the state format of what it holds.
	Format int      `json:"format"`
	At     string   `json:"at"`
	Files  []string `json:"files"`
}

// Dir is one state: the index base it lives in. Its methods are the
// functions of stateFormat.ts with INDEX_BASE as base.
type Dir struct {
	Base string
	// Getenv reads STUDIO_DATA_DIR and STUDIO_CONFIG_FILE; nil is os.Getenv.
	Getenv func(string) string
	// Now is time.Now; tests replace it.
	Now func() time.Time
}

func (d Dir) env(k string) string {
	if d.Getenv != nil {
		return d.Getenv(k)
	}
	return os.Getenv(k)
}

func (d Dir) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// ISO is Date.prototype.toISOString.
func ISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (d Dir) dataDir() string {
	if v := d.env("STUDIO_DATA_DIR"); v != "" {
		return v
	}
	if v := d.env("STUDIO_CONFIG_FILE"); v != "" {
		return filepath.Dir(v)
	}
	return ""
}

// SnapshotsDir is where snapshots are kept: the data folder's
// updates/state-snapshots, else INDEX_BASE's.
func (d Dir) SnapshotsDir() string {
	if dd := d.dataDir(); dd != "" {
		return filepath.Join(dd, "updates", "state-snapshots")
	}
	return filepath.Join(d.Base, "updates", "state-snapshots")
}

// configFile is the launcher's settings file, which is state too.
func (d Dir) configFile() string {
	if v := d.env("STUDIO_CONFIG_FILE"); v != "" {
		return v
	}
	if v := d.env("STUDIO_DATA_DIR"); v != "" {
		return filepath.Join(v, "studio.env")
	}
	return ""
}

// StateFormat is the format the state is in: the marker's, or 1 for state
// from before markers.
func (d Dir) StateFormat() int {
	b, err := os.ReadFile(filepath.Join(d.Base, MarkerName))
	if err != nil {
		return 1
	}
	var m struct {
		Format json.RawMessage `json:"format"`
	}
	if json.Unmarshal(b, &m) != nil || len(m.Format) == 0 || m.Format[0] == '"' {
		return 1
	}
	n, err := json.Number(m.Format).Int64()
	if err != nil || n < 1 || n > 1<<53-1 {
		return 1
	}
	return int(n)
}

// WriteMarker writes the marker for format, written by version.
func (d Dir) WriteMarker(format int, version string) error {
	if err := os.MkdirAll(d.Base, 0o755); err != nil {
		return err
	}
	file := filepath.Join(d.Base, MarkerName)
	b, _ := json.MarshalIndent(Marker{Format: format, WrittenBy: version, At: ISO(d.now())}, "", " ")
	return writeRename(file, append(b, '\n'), 0o644)
}

func writeRename(file string, b []byte, mode os.FileMode) error {
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func isFile(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

// Files is every file of the state, relative to INDEX_BASE, the
// launcher's file as @config/studio.env, sorted.
func (d Dir) Files() []string {
	var out []string
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		entries, err := os.ReadDir(filepath.Join(d.Base, rel))
		if err != nil {
			return
		}
		for _, e := range entries {
			r := filepath.Join(rel, e.Name())
			switch {
			case e.Type().IsRegular():
				out = append(out, r)
			case e.IsDir() && !(depth == 1 && strings.HasPrefix(rel, "users") && skipUnderUsers[e.Name()]):
				walk(r, depth+1)
			}
		}
	}
	top, _ := os.ReadDir(d.Base)
	for _, e := range top {
		switch {
		case e.Type().IsRegular() && !skipFiles[e.Name()] && !strings.HasSuffix(e.Name(), ".tmp"):
			out = append(out, e.Name())
		case e.IsDir() && stateDirs[e.Name()]:
			walk(e.Name(), 0)
		case e.IsDir() && e.Name() == "libraries":
			// A built library's own files (labels and settings), not its index.
			libs, _ := os.ReadDir(filepath.Join(d.Base, "libraries"))
			for _, lib := range libs {
				if !lib.IsDir() {
					continue
				}
				files, _ := os.ReadDir(filepath.Join(d.Base, "libraries", lib.Name()))
				for _, f := range files {
					if f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".json") {
						out = append(out, filepath.Join("libraries", lib.Name(), f.Name()))
					}
				}
			}
		}
	}
	if cfg := d.configFile(); cfg != "" && isFile(cfg) {
		out = append(out, configName)
	}
	sort.Strings(out)
	return out
}

func (d Dir) sourceOf(rel string) string {
	if rel == configName {
		return d.configFile()
	}
	return filepath.Join(d.Base, rel)
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

const base36 = "0123456789abcdefghijklmnopqrstuvwxyz"

func rand36(n int) string {
	b := make([]byte, n)
	for i := range b {
		v, _ := rand.Int(rand.Reader, big.NewInt(36))
		b[i] = base36[v.Int64()]
	}
	return string(b)
}

// SnapshotOpts is takeSnapshot's options.
type SnapshotOpts struct {
	From   string
	To     *string
	Reason string
	// Keep is a snapshot pruning must keep besides the usual ones.
	Keep string
}

// TakeSnapshot copies the state into a new snapshot and prunes old ones.
func (d Dir) TakeSnapshot(o SnapshotOpts) (Snapshot, error) {
	root := d.SnapshotsDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Snapshot{}, err
	}
	at := ISO(d.now())
	id := fmt.Sprintf("%s-%d-%s", strings.NewReplacer(":", "-", ".", "-").Replace(at), os.Getpid(), rand36(4))
	dir := filepath.Join(root, id)
	work := dir + ".part"
	if err := os.MkdirAll(filepath.Join(work, "files"), 0o700); err != nil {
		return Snapshot{}, err
	}
	files := []string{}
	for _, rel := range d.Files() {
		if copyFile(d.sourceOf(rel), filepath.Join(work, "files", rel)) == nil {
			files = append(files, rel)
		}
	}
	snap := Snapshot{ID: id, From: o.From, To: o.To, Reason: o.Reason, Format: d.StateFormat(), At: at, Files: files}
	b, _ := json.MarshalIndent(snap, "", " ")
	if err := os.WriteFile(filepath.Join(work, "snapshot.json"), append(b, '\n'), 0o600); err != nil {
		return Snapshot{}, err
	}
	if err := os.Rename(work, dir); err != nil {
		return Snapshot{}, err
	}
	d.prune(o.Keep)
	return snap, nil
}

// ListSnapshots is every snapshot, newest first.
func (d Dir) ListSnapshots() []Snapshot {
	root := d.SnapshotsDir()
	names, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Snapshot
	for _, n := range names {
		if strings.HasSuffix(n.Name(), ".part") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, n.Name(), "snapshot.json"))
		if err != nil {
			continue
		}
		var raw struct {
			ID     string          `json:"id"`
			Files  json.RawMessage `json:"files"`
			Format json.RawMessage `json:"format"`
		}
		var s Snapshot
		if json.Unmarshal(b, &raw) != nil || raw.ID != n.Name() || !isArray(raw.Files) || !safeInt(raw.Format) || json.Unmarshal(b, &s) != nil {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

func isArray(b json.RawMessage) bool {
	b = bytes.TrimSpace(b)
	return len(b) > 0 && b[0] == '['
}

func safeInt(raw json.RawMessage) bool {
	if len(raw) == 0 || raw[0] == '"' {
		return false
	}
	v, err := json.Number(raw).Int64()
	return err == nil && v >= -(1<<53-1) && v <= 1<<53-1
}

// CompatibleSnapshot is the newest snapshot a version reading formats up
// to format can use, or nil.
func (d Dir) CompatibleSnapshot(format int) *Snapshot {
	for _, s := range d.ListSnapshots() {
		if s.Format <= format {
			s := s
			return &s
		}
	}
	return nil
}

// prune keeps the newest few, and the newest of each format, so the
// snapshot taken before a first move to a newer format survives later
// updates.
func (d Dir) prune(also string) {
	all := d.ListSnapshots()
	kept := map[string]bool{}
	for i, s := range all {
		if i < keep {
			kept[s.ID] = true
		}
	}
	if also != "" {
		kept[also] = true
	}
	formats := map[int]bool{}
	for _, s := range all {
		if !formats[s.Format] {
			formats[s.Format] = true
			kept[s.ID] = true
		}
	}
	for _, s := range all {
		if !kept[s.ID] {
			os.RemoveAll(filepath.Join(d.SnapshotsDir(), s.ID))
		}
	}
}

// RestoreSnapshot puts a snapshot's state back: what is there now is kept
// as a snapshot first, then the state's files are replaced by the
// snapshot's, and the marker says the snapshot's format. The update policy
// (settings.json's updates) stays as it is now.
func (d Dir) RestoreSnapshot(id, running string) (Snapshot, error) {
	var snap *Snapshot
	for _, s := range d.ListSnapshots() {
		if s.ID == id {
			s := s
			snap = &s
			break
		}
	}
	if snap == nil {
		return Snapshot{}, fmt.Errorf("no state snapshot %s", id)
	}
	r := running
	if _, err := d.TakeSnapshot(SnapshotOpts{From: running, To: &r, Reason: "replaced by the snapshot of " + snap.At, Keep: snap.ID}); err != nil {
		return Snapshot{}, err
	}
	src := filepath.Join(d.SnapshotsDir(), id, "files")
	settingsFile := filepath.Join(d.Base, "settings.json")
	var updates json.RawMessage
	if b, err := os.ReadFile(settingsFile); err == nil {
		if obj, err := jsonobj.Parse(b); err == nil {
			updates = obj.Get("updates")
		}
	}
	in := map[string]bool{}
	for _, f := range snap.Files {
		in[f] = true
	}
	for _, rel := range d.Files() {
		if !in[rel] {
			os.Remove(d.sourceOf(rel))
		}
	}
	for _, rel := range snap.Files {
		dest := d.sourceOf(rel)
		if dest == "" {
			continue
		}
		tmp := fmt.Sprintf("%s.%d.restore", dest, os.Getpid())
		if err := copyFile(filepath.Join(src, rel), tmp); err != nil {
			return Snapshot{}, err
		}
		if err := os.Rename(tmp, dest); err != nil {
			return Snapshot{}, err
		}
	}
	if updates != nil {
		if b, err := os.ReadFile(settingsFile); err == nil {
			if obj, err := jsonobj.Parse(b); err == nil {
				obj.Set("updates", updates)
				if out, err := obj.Indent("  "); err == nil {
					os.WriteFile(settingsFile, append(out, '\n'), 0o644)
				}
			}
		}
	}
	if err := d.WriteMarker(snap.Format, running); err != nil {
		return Snapshot{}, err
	}
	return *snap, nil
}

// Reconciled is what Reconcile did.
type Reconciled string

const (
	OK         Reconciled = "ok"
	Marked     Reconciled = "marked"
	Restored   Reconciled = "restored"
	Unreadable Reconciled = "unreadable"
)

// Reconcile is reconcileState, at start: a missing marker is written; a
// state in a newer format than this version reads is replaced by the
// newest snapshot it can read.
func (d Dir) Reconcile(version string, log func(string)) (Reconciled, error) {
	if log == nil {
		log = func(string) {}
	}
	if _, err := os.Stat(filepath.Join(d.Base, MarkerName)); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(d.Base); err != nil {
			return OK, nil
		}
		if err := d.WriteMarker(Format, version); err != nil {
			return "", err
		}
		return Marked, nil
	}
	have := d.StateFormat()
	if have <= Format {
		return OK, nil
	}
	snap := d.CompatibleSnapshot(Format)
	if snap == nil {
		log(fmt.Sprintf("state: the state is in format %d, newer than this version reads (%d), and no snapshot in a readable format is kept", have, Format))
		return Unreadable, nil
	}
	if _, err := d.RestoreSnapshot(snap.ID, version); err != nil {
		return "", err
	}
	to := "another version"
	if snap.To != nil {
		to = *snap.To
	}
	log(fmt.Sprintf("state: format %d is newer than this version reads; the snapshot taken %s (before %s changed to %s) was put back", have, snap.At, snap.From, to))
	return Restored, nil
}
