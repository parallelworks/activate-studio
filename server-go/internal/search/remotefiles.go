package search

import (
	"context"
	"path"
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
	files, dirs := make(chan res, 1), make(chan res, 1)
	go func() {
		r, err := e.RemoteGufi(ctx, t.Site, t.Key, []string{q, "-d", "\x1e", "-n", "1", "--min-level", "0", "--max-level", "0", "-E", "SELECT name, type, size, mtime FROM vrpentries;", target}, GufiOpts{Source: "library"})
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
	for _, x := range rows(dr.r.Stdout) {
		if x[0] != "" {
			ds = append(ds, kb.Entry{Name: x[0], Path: at(x[0]), Type: "dir", Mtime: num(x, 1), Kind: kb.KindDir})
		}
	}
	for _, x := range rows(fr.r.Stdout) {
		if x[0] != "" && (len(x) < 2 || x[1] != "d") {
			fs = append(fs, kb.Entry{Name: x[0], Path: at(x[0]), Type: "file", Size: num(x, 2), Mtime: num(x, 3), Kind: kb.Classify(x[0], false)})
		}
	}
	byName := func(es []kb.Entry) {
		sort.SliceStable(es, func(i, j int) bool { return kb.LocaleCompare(es[i].Name, es[j].Name) < 0 })
	}
	byName(ds)
	byName(fs)
	return append(append([]kb.Entry{}, ds...), fs...), nil
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
	}
	return o, nil
}

// ReadRemoteFile is readRemoteFile: one file under sourceRoot on a host,
// its size and time from the file itself, and its text when it is a text
// file, in one round trip.
func (e *Engine) ReadRemoteFile(ctx context.Context, site, key, sourceRoot, rel string, timeout time.Duration) (*kb.Obj, error) {
	if e.Pool.IsDirect(site) {
		return nil, apierr.New(409, "%s", noFilesThere(site))
	}
	if sourceRoot == "" {
		return nil, apierr.New(409, "the source directory of this library is not known")
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
		return "", nil, apierr.New(409, "the source directory of this library is not known")
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
