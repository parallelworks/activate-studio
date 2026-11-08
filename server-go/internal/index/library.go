package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LibraryPaths are where one library the Studio builds of a folder here
// lives (libraryBuild.ts): its folder, its index tree, the folder that
// holds the tree and its staging, its extract cache, its embedding cache
// and its skip file.
type LibraryPaths struct {
	Source, Index, IndexDir, Cache, EmbedCache, SkipFile string
}

// LibraryWork runs the passes of a library build or sync with the tools
// the main folder's jobs use (gufi_dir2index, `studio-server extract`, the
// words writer and `studio-server embed`), under the library's own paths
// and settings. It is not an index job of its own: the caller's job
// (StartOwnJob) shows its progress.
type LibraryWork struct {
	c *jobCtx
}

// LibraryWorkOptions are a LibraryWork's settings and hooks.
type LibraryWorkOptions struct {
	// JobID names the job in the index log.
	JobID    int
	Settings Settings
	Threads  int
	// Denied takes the folders gufi_dir2index could not read, or could not
	// make an index folder for (omissions.ts's record of the library).
	Denied func(paths []string)
	// Embedded is called after embeddings changed, with the delay the map
	// of the library is made again after (semanticMap.ts).
	Embedded func(delay time.Duration)
}

// LibraryWork is a LibraryWork over p, with this indexer's tools, log and
// configuration.
func (ix *Indexer) LibraryWork(ctx context.Context, p LibraryPaths, o LibraryWorkOptions) *LibraryWork {
	paths := ix.P
	paths.KBRoot, paths.GufiIndex, paths.IndexDir = p.Source, p.Index, p.IndexDir
	paths.ExtractCache, paths.EmbedCache, paths.SkipFile = p.Cache, p.EmbedCache, p.SkipFile
	sub := &Indexer{App: ix.App, P: paths, Settings: ix.Settings, Log: ix.Log, Hooks: ix.Hooks, recs: map[int]*jobRec{}, holdFile: ix.holdFile}
	sub.Hooks.ScheduleMapRebuild = func(d time.Duration) {
		if o.Embedded != nil {
			o.Embedded(d)
		}
	}
	sub.deniedTo = func(paths []string) {
		if o.Denied != nil {
			o.Denied(paths)
		}
	}
	threads := o.Threads
	if threads <= 0 {
		threads = 1
	}
	rec := &jobRec{pub: &IndexJob{ID: o.JobID, Kind: "folder", Phases: []*Phase{}}, ctx: ctx}
	return &LibraryWork{c: &jobCtx{ix: sub, rec: rec, ctx: ctx, threads: threads, settings: o.Settings}}
}

// Names is gufi_dir2index of src into stage; it returns the tree it wrote
// (stage/<base of src>). maxLevel0 indexes the top folder alone.
func (w *LibraryWork) Names(src, stage string, maxLevel0 bool) (string, error) {
	return w.c.names(src, stage, maxLevel0)
}

// Documents is the extraction pass over the library's folder, or one
// folder of it (subdir), or its top folder's own files (noRecurse).
func (w *LibraryWork) Documents(subdir string, noRecurse bool) error {
	return w.c.documents(scope{subdir: subdir, noRecurse: noRecurse})
}

// Text writes the words tables over index, a tree that mirrors kbRoot,
// from the files there and the extract cache folder cache.
func (w *LibraryWork) Text(kbRoot, index, cache string, noRecurse bool) error {
	return w.c.text(textScope{kbRoot: kbRoot, index: index, cache: cache, noRecurse: noRecurse})
}

// Meaning embeds index with the library's embedding cache; prune drops
// cache entries the index no longer uses (a whole build only).
func (w *LibraryWork) Meaning(index string, noRecurse, prune bool) error {
	return w.c.meaning(meaningScope{index: index, noRecurse: noRecurse, prune: prune})
}

// VerifyTree checks GUFI reads the tree a build wrote.
func (w *LibraryWork) VerifyTree(tree string) error { return w.c.ix.verifyTree(w.c.ctx, tree) }

// ---- indexModes.ts, for the library builds ----

// RmTree removes a tree as rm -rf does, opening its folders first when one
// keeps the removal out.
func RmTree(p string) error { return rmTree(p) }

// OpenDir gives a folder read, write and search permission for its owner.
func OpenDir(p string) { openDir(p) }

// OpenIndex opens the index folders of the locked folders, parents first.
func OpenIndex(built string, locked []Locked) { openIndex(built, locked) }

// RecoverSwap is recoverSwap: the newest of olds with a db.db put back at
// dest when a stop cut a swap off between its renames; "" otherwise.
func RecoverSwap(dest string, olds []string) string { return recoverSwap(dest, olds) }

// CountDbs counts the db.db files written so far under a tree.
func CountDbs(root string) int { return countDbs(root) }

// LockedUnder is lockedUnder: the locked folders at or below rel, with
// paths made relative to it.
func LockedUnder(locked []Locked, rel string) []Locked {
	if rel == "" {
		return locked
	}
	var out []Locked
	for _, l := range locked {
		switch {
		case l.Rel == rel:
			out = append(out, Locked{Rel: "", Dirs: l.Dirs})
		case strings.HasPrefix(l.Rel, rel+"/"):
			out = append(out, Locked{Rel: l.Rel[len(rel)+1:], Dirs: l.Dirs})
		}
	}
	return out
}

// MissingIndexDirs are the folders of scan, by their path below the root,
// that have no db.db in the tree built of it. gufi_dir2index, meeting a
// folder it may not open, sometimes stops before it has indexed the
// readable folders beside it; a build indexes these again.
func MissingIndexDirs(built string, scan Scan) []string {
	denied := map[string]bool{}
	for _, d := range scan.Denied {
		denied[d] = true
	}
	under := func(rel string) bool {
		for d := range denied {
			if d == "" || rel == d || strings.HasPrefix(rel, d+"/") {
				return true
			}
		}
		return false
	}
	var out []string
	for _, rel := range scan.Order {
		if under(rel) {
			continue
		}
		if _, err := os.Stat(filepath.Join(built, rel, "db.db")); err != nil {
			out = append(out, rel)
		}
	}
	return out
}

// SkipDirs are the folder names gufi_dir2index passes over: the lines of
// the skip file the main folder's index uses, which a library's own skip
// file repeats.
func (ix *Indexer) SkipDirs() []string {
	b, _ := os.ReadFile(ix.ensureSkipFile())
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
