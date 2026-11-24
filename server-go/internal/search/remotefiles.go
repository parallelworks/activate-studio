package search

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// A library's files on another system (userLibraries.ts): its folders
// listed from its index there, a file previewed or fetched whole over the
// host's runner. Nothing here is read by a shell on either side: a path is
// an argument of a fixed script, joined to the folder after CleanRel.

// PreviewBytes is how much of a text file on another system is shown.
const PreviewBytes = 512 * 1024

// DownloadMax is the largest file fetched from another system for download.
const DownloadMax = 50 * 1024 * 1024

// stat's format option differs: GNU and busybox take -c, macOS and BSD -f.
// Defines `st <gnu format> <bsd format> <file>` in a bash script.
const statFn = `st() { if stat -c %s / >/dev/null 2>&1; then stat -c "$1" -- "$3"; else stat -f "$2" -- "$3"; fi; }; st`

func lastStderrLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// RemoteTree is remoteTree: one folder of a library on another system,
// from its index there, subfolders first, then files, each by name.
func (e *Engine) RemoteTree(ctx context.Context, root, rel string) ([]kb.Entry, error) {
	t, err := e.Target(root)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, apierr.New(404, "no such library")
	}
	sub, err := CleanRel(rel)
	if err != nil {
		return nil, err
	}
	target := t.IndexRoot
	if sub != "" {
		target = path.Join(t.IndexRoot, sub)
	}
	q := libraries.GufiTool(t.GufiBin, "")
	// The folder's files and its subfolders: two gufi_query calls, side by
	// side, and nothing but gufi_query, so a host whose login runs only
	// gufi_query lists too.
	type res struct {
		r   remote.Result
		err error
	}
	// In a rolled-up index (gufi_rollup) a folder's database also holds
	// everything under it: its vrpentries has the files of its subfolders
	// too, and its children's vrsummary the folders below them, named by
	// their path from the folder ("AI/aix"). atroot marks the folder's own
	// files; folders are kept to direct children by name. A GUFI too old
	// to know atroot answers "no such column" (with exit 0), and the
	// listing is asked again without it, as before rollups were known.
	files, dirs := make(chan res, 1), make(chan res, 1)
	go func() {
		ask := func(sql string) (remote.Result, error) {
			return e.RemoteGufi(ctx, t.Site, t.Key, []string{q, "-d", "\x1e", "-n", "1", "--min-level", "0", "--max-level", "0", "-E", sql, target}, GufiOpts{Source: "library"})
		}
		r, err := ask("SELECT name, type, size, mtime, atroot FROM vrpentries;")
		if err == nil && r.Stdout == "" && strings.Contains(r.Stderr, "no such column") {
			r, err = ask("SELECT name, type, size, mtime FROM vrpentries;")
		}
		files <- res{r, err}
	}()
	go func() {
		r, err := e.RemoteGufi(ctx, t.Site, t.Key, []string{q, "-d", "\x1e", "-n", "1", "--min-level", "1", "--max-level", "1", "-S", "SELECT name, mtime FROM vrsummary;", target}, GufiOpts{Source: "library"})
		dirs <- res{r, err}
	}()
	fr, dr := <-files, <-dirs
	if fr.err != nil {
		return nil, fr.err
	}
	if dr.err != nil {
		return nil, dr.err
	}
	return treeEntries(sub, fr.r.Stdout, dr.r.Stdout), nil
}

// LocalTree is one folder of a library whose index is on this host and
// whose files are not (a bucket library), from its index, as RemoteTree
// lists one on another system.
func (e *Engine) LocalTree(ctx context.Context, indexRoot, rel string) ([]kb.Entry, error) {
	sub, err := CleanRel(rel)
	if err != nil {
		return nil, err
	}
	target := indexRoot
	if sub != "" {
		target = filepath.Join(indexRoot, filepath.FromSlash(sub))
	}
	if st, err := os.Stat(filepath.Join(target, "db.db")); err != nil || st.IsDir() {
		return nil, apierr.New(404, "no such folder: %s", sub)
	}
	type res struct {
		out string
		err error
	}
	files, dirs := make(chan res, 1), make(chan res, 1)
	go func() {
		out, err := e.runLocal(ctx, []string{"-d", "\x1e", "-n", "1", "--min-level", "0", "--max-level", "0", "-E", "SELECT name, type, size, mtime FROM vrpentries;", target}, 60*time.Second)
		files <- res{out, err}
	}()
	go func() {
		out, err := e.runLocal(ctx, []string{"-d", "\x1e", "-n", "1", "--min-level", "1", "--max-level", "1", "-S", "SELECT name, mtime FROM vrsummary;", target}, 60*time.Second)
		dirs <- res{out, err}
	}()
	fr, dr := <-files, <-dirs
	if fr.err != nil {
		return nil, fr.err
	}
	if dr.err != nil {
		return nil, dr.err
	}
	return treeEntries(sub, fr.out, dr.out), nil
}

// treeEntries is a folder's listing from its two gufi_query answers: its
// subfolders (name, mtime) and its files (name, type, size, mtime[,
// atroot]), subfolders first, then files, each by name.
func treeEntries(sub, filesOut, dirsOut string) []kb.Entry {
	rows := func(s string) [][]string {
		var out [][]string
		for _, l := range strings.Split(s, "\n") {
			if l != "" {
				out = append(out, strings.Split(l, "\x1e"))
			}
		}
		return out
	}
	at := func(name string) string {
		if sub != "" {
			return sub + "/" + name
		}
		return name
	}
	num := func(x []string, i int) int64 {
		if i >= len(x) {
			return 0
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(x[i]), 64)
		if err != nil {
			return 0
		}
		return int64(f)
	}
	var ds, fs []kb.Entry
	seen := map[string]int{}
	for _, x := range rows(dirsOut) {
		// A rolled-up child's descendants come as "child/sub": each stands
		// for its first part, which is listed once.
		name, deeper := x[0], false
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name, deeper = name[:i], true
		}
		if name == "" {
			continue
		}
		if j, ok := seen[name]; ok {
			if !deeper {
				ds[j].Mtime = num(x, 1)
			}
			continue
		}
		var mtime int64
		if !deeper {
			mtime = num(x, 1)
		}
		seen[name] = len(ds)
		ds = append(ds, kb.Entry{Name: name, Path: at(name), Type: "dir", Mtime: mtime, Kind: kb.KindDir})
	}
	for _, x := range rows(filesOut) {
		if len(x) >= 5 && strings.TrimSpace(x[4]) == "0" {
			continue // a file of a subfolder, in a rolled-up folder's database
		}
		if x[0] != "" && (len(x) < 2 || x[1] != "d") {
			fs = append(fs, kb.Entry{Name: x[0], Path: at(x[0]), Type: "file", Size: num(x, 2), Mtime: num(x, 3), Kind: kb.Classify(x[0], false)})
		}
	}
	byName := func(es []kb.Entry) {
		sort.SliceStable(es, func(i, j int) bool { return kb.LocaleCompare(es[i].Name, es[j].Name) < 0 })
	}
	byName(ds)
	byName(fs)
	return append(append([]kb.Entry{}, ds...), fs...)
}

// noFilesThere is why a file cannot be opened from a host whose login
// runs only gufi_query.
func noFilesThere(site string) string {
	return remote.SiteName(site) + " runs only gufi_query for this login, so its files cannot be opened from the Studio; open them on that host."
}

// RemoteFile is remoteFile: a file of a library on another system.
func (e *Engine) RemoteFile(ctx context.Context, root, rel string) (*kb.Obj, error) {
	t, err := e.Target(root)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, apierr.New(404, "no such library")
	}
	o, err := e.ReadRemoteFile(ctx, t.Site, t.Key, t.SourceRoot, rel, 120*time.Second)
	if err != nil {
		return o, err
	}
	if src, _ := o.Get("source"); src != "none" {
		return o, nil
	}
	// A PDF, an Office file or an image: the text its index there holds,
	// as the file view shows a local file's extracted text.
	sub, _ := CleanRel(rel)
	dir, name := path.Split(sub)
	if it := e.IndexedTextState(ctx, root, strings.TrimSuffix(dir, "/"), name); it.State == "text" {
		o = o.Set("truncated", kb.UTF16Len(it.Text) > PreviewBytes).Set("content", kb.SliceUTF16(it.Text, PreviewBytes)).Set("format", "text").Set("source", "extracted")
		if note := staleNote(t, root, o); note != nil {
			o = o.Set("index", note)
		}
		return o, nil
	}
	if note := indexNote(t, root, o); note != nil {
		o = o.Set("index", note)
	}
	return o, nil
}

// IndexQueued says whether a build of the library at indexRoot is waiting
// to start (a space waits for its changes to settle). Set by the
// libraries track; nil says none is.
var IndexQueued func(indexRoot string) bool

// indexNote is why a file of a library on another system has no text in
// its index when that is the index and not the file: the library is
// indexing now, or the file changed after its last pass. nil when the
// file was indexed and has no text.
func indexNote(t *libraries.Target, root string, o *kb.Obj) *kb.Obj {
	if t.Def.State == "building" || t.Def.State == "updating" || (IndexQueued != nil && IndexQueued(root)) {
		return kb.O("state", "indexing")
	}
	built, err := time.Parse(time.RFC3339, t.BuiltAt)
	if err != nil {
		return nil
	}
	mtime, _ := o.Get("mtime")
	if m, ok := mtime.(int64); ok && m > built.Unix() {
		return kb.O("state", "newer", "indexedAt", t.BuiltAt)
	}
	return nil
}

// staleNote is why the indexed text shown for a file of a library on
// another system is not the file's: the file changed after the library's
// last pass, so its text is from before; the library may be indexing now.
// nil when the file is no newer than that pass, or its time is unknown.
func staleNote(t *libraries.Target, root string, o *kb.Obj) *kb.Obj {
	built, err := time.Parse(time.RFC3339, t.BuiltAt)
	if err != nil {
		return nil
	}
	mtime, _ := o.Get("mtime")
	if m, ok := mtime.(int64); !ok || m <= built.Unix() {
		return nil
	}
	if t.Def.State == "building" || t.Def.State == "updating" || (IndexQueued != nil && IndexQueued(root)) {
		return kb.O("state", "indexing")
	}
	return kb.O("state", "newer", "indexedAt", t.BuiltAt)
}

// noSourceRoot is why a library on another system opens no file: it was
// connected without the folder its index was built from.
const noSourceRoot = "the library's folder on that host is not known: remove the library and add it again with its source directory, in Settings, Indexing, Libraries"

// ReadRemoteFile is readRemoteFile: one file under sourceRoot on a host,
// its size and time from the file itself, and its text when it is a text
// file, in one round trip.
func (e *Engine) ReadRemoteFile(ctx context.Context, site, key, sourceRoot, rel string, timeout time.Duration) (*kb.Obj, error) {
	if e.Pool.IsDirect(site) {
		return nil, apierr.New(409, "%s", noFilesThere(site))
	}
	if sourceRoot == "" {
		return nil, apierr.New(409, "%s", noSourceRoot)
	}
	sub, err := CleanRel(rel)
	if err != nil {
		return nil, err
	}
	abs := path.Join(sourceRoot, sub)
	kind := kb.Classify(path.Base(sub), false)
	text := kb.TextSuffixes[strings.ToLower(path.Ext(sub))]
	flag := "0"
	if text {
		flag = "1"
	}
	r, err := e.Pool.Run(ctx, site, key, []string{"bash", "-c", statFn + ` "%s %Y" "%z %m" "$1" || exit 3; [ "$2" = 1 ] && head -c "$3" -- "$1"; exit 0`, "_", abs, flag, strconv.Itoa(PreviewBytes + 1)}, timeout)
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, apierr.New(404, "%s: %s", remote.SiteName(site), lastStderrLine(r.Stderr))
	}
	out := kb.DecodeUTF8(r.Raw)
	head, body, _ := strings.Cut(out, "\n")
	parts := strings.Split(head, " ")
	num := func(i int) int64 {
		if i >= len(parts) {
			return 0
		}
		f, err := strconv.ParseFloat(parts[i], 64)
		if err != nil {
			return 0
		}
		return int64(f)
	}
	o := kb.O("path", sub, "kind", kind, "size", num(0), "mtime", num(1))
	if !text {
		return o.Set("truncated", false).Set("content", nil).Set("source", "none"), nil
	}
	return o.Set("truncated", kb.UTF16Len(body) > PreviewBytes).Set("content", kb.SliceUTF16(body, PreviewBytes)).Set("source", "raw"), nil
}

// RemoteStat is a file's size and modification time on a host.
type RemoteStat struct{ Size, Mtime int64 }

// StatRemoteFile is the size and time of one file under sourceRoot on a
// host, in one round trip.
func (e *Engine) StatRemoteFile(ctx context.Context, site, key, sourceRoot, rel string, timeout time.Duration) (RemoteStat, error) {
	if e.Pool.IsDirect(site) {
		return RemoteStat{}, apierr.New(409, "%s", noFilesThere(site))
	}
	if sourceRoot == "" {
		return RemoteStat{}, apierr.New(409, "%s", noSourceRoot)
	}
	sub, err := CleanRel(rel)
	if err != nil {
		return RemoteStat{}, err
	}
	r, err := e.Pool.Run(ctx, site, key, []string{"bash", "-c", statFn + ` "%s %Y" "%z %m" "$1"`, "_", path.Join(sourceRoot, sub)}, timeout)
	if err != nil {
		return RemoteStat{}, err
	}
	if r.Code != 0 {
		return RemoteStat{}, apierr.New(404, "%s: %s", remote.SiteName(site), lastStderrLine(r.Stderr))
	}
	var st RemoteStat
	if _, err := fmt.Sscanf(strings.TrimSpace(r.Stdout), "%d %d", &st.Size, &st.Mtime); err != nil {
		return RemoteStat{}, apierr.New(502, "%s did not say the file's size", remote.SiteName(site))
	}
	return st, nil
}

// RemoteDownload is remoteDownload: a file of a library on another
// system, whole.
func (e *Engine) RemoteDownload(ctx context.Context, root, rel string) (string, []byte, error) {
	t, err := e.Target(root)
	if err != nil {
		return "", nil, err
	}
	if t == nil {
		return "", nil, apierr.New(404, "no such library")
	}
	return e.DownloadRemoteFile(ctx, t.Site, t.Key, t.SourceRoot, rel)
}

// DownloadRemoteFile is downloadRemoteFile: a file under sourceRoot on a
// host, whole, up to DownloadMax.
func (e *Engine) DownloadRemoteFile(ctx context.Context, site, key, sourceRoot, rel string) (string, []byte, error) {
	if e.Pool.IsDirect(site) {
		return "", nil, apierr.New(409, "%s", noFilesThere(site))
	}
	if sourceRoot == "" {
		return "", nil, apierr.New(409, "%s", noSourceRoot)
	}
	sub, err := CleanRel(rel)
	if err != nil {
		return "", nil, err
	}
	abs := path.Join(sourceRoot, sub)
	r, err := e.Pool.Run(ctx, site, key, []string{"bash", "-c", `s=$(` + statFn + ` %s %z "$1") || exit 3; [ "$s" -le "$2" ] || exit 4; cat -- "$1"`, "_", abs, strconv.Itoa(DownloadMax)}, 300*time.Second)
	if err != nil {
		return "", nil, err
	}
	if r.Code == 4 {
		return "", nil, apierr.New(413, "This file is over %d MB; copy it from %s directly.", DownloadMax/1024/1024, remote.SiteName(site))
	}
	if r.Code != 0 {
		return "", nil, apierr.New(404, "%s: %s", remote.SiteName(site), lastStderrLine(r.Stderr))
	}
	return path.Base(sub), r.Raw, nil
}
