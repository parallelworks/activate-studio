package transfers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// LocalFS is a folder on this computer.
type LocalFS struct {
	Dir string // absolute
}

func (l *LocalFS) Where() string { return "this computer" }
func (l *LocalFS) Root() string  { return l.Dir }

func unixTime(s int64) time.Time { return time.Unix(s, 0) }

func errTooMany(limit int) error {
	return apierr.New(400, "more than %d files: transfer fewer at a time", limit)
}

// realRoot is the root's real path; a missing root is a 502, since the
// location itself cannot be reached.
func (l *LocalFS) realRoot() (string, error) {
	r, err := filepath.EvalSymlinks(l.Dir)
	if err != nil {
		return "", apierr.New(502, "the location's folder cannot be opened on this computer: %s", reason(err))
	}
	return r, nil
}

func reason(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "no such file or folder"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func insideReal(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// abs is rel's path on this computer, with the real folder holding it
// checked to be inside the real root. The last name is not followed.
func (l *LocalFS) abs(rel string) (string, error) {
	root, err := l.realRoot()
	if err != nil {
		return "", err
	}
	if rel == "" {
		return root, nil
	}
	parent := filepath.Join(root, filepath.FromSlash(parentOf(rel)))
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", apierr.New(404, "%s: no such file or folder", rel)
		}
		return "", apierr.New(502, "%s: %s", rel, reason(err))
	}
	if !insideReal(real, root) {
		return "", apierr.New(400, "%s leads outside the location", rel)
	}
	return filepath.Join(real, filepath.Base(filepath.FromSlash(rel))), nil
}

func infoOf(rel string, fi os.FileInfo) FileInfo {
	t := byte('o')
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		t = 'l'
	case fi.IsDir():
		t = 'd'
	case fi.Mode().IsRegular():
		t = 'f'
	}
	size := fi.Size()
	if t == 'd' {
		size = 0
	}
	return FileInfo{Rel: rel, Type: t, Size: size, Mtime: fi.ModTime().Unix(), Mode: uint32(fi.Mode().Perm())}
}

func (l *LocalFS) List(ctx context.Context, rel string, max int) ([]Entry, bool, error) {
	p, err := l.abs(rel)
	if err != nil {
		return nil, false, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, apierr.New(404, "%s: no such folder", orRoot(rel))
		}
		return nil, false, apierr.New(502, "%s: %s", orRoot(rel), reason(err))
	}
	if !fi.IsDir() {
		return nil, false, apierr.New(400, "%s is not a folder", orRoot(rel))
	}
	des, err := os.ReadDir(p)
	if err != nil {
		return nil, false, apierr.New(502, "%s: %s", orRoot(rel), reason(err))
	}
	var out []Entry
	for _, d := range des {
		if strings.HasPrefix(d.Name(), ".") {
			continue
		}
		fi, err := d.Info()
		if err != nil {
			continue
		}
		out = append(out, entryOf(infoOf("", fi), d.Name()))
	}
	es, more := sortEntries(out, max)
	return es, more, nil
}

func orRoot(rel string) string {
	if rel == "" {
		return "the top folder"
	}
	return rel
}

func (l *LocalFS) Stat(ctx context.Context, rels []string) (map[string]FileInfo, error) {
	out := map[string]FileInfo{}
	for _, rel := range rels {
		p, err := l.abs(rel)
		if err != nil {
			if statusOf(err) == 404 {
				continue
			}
			return nil, err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		out[rel] = infoOf(rel, fi)
	}
	return out, nil
}

func (l *LocalFS) Walk(ctx context.Context, rels []string, limit int) (Walked, error) {
	var raw []FileInfo
	for _, rel := range rels {
		p, err := l.abs(rel)
		if err != nil {
			return Walked{}, err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return Walked{}, apierr.New(404, "%s: no such file or folder", orRoot(rel))
			}
			return Walked{}, apierr.New(502, "%s: %s", orRoot(rel), reason(err))
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return Walked{}, apierr.New(400, "%s is a link; name what it points to", rel)
		}
		if !fi.IsDir() {
			raw = append(raw, infoOf(rel, fi))
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return apierr.New(502, "%s: %s", rel, reason(err))
			}
			sub, _ := filepath.Rel(p, path)
			r := join(rel, filepath.ToSlash(sub))
			if sub == "." {
				r = rel
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			raw = append(raw, infoOf(r, fi))
			if d.IsDir() && path != p && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if limit > 0 && len(raw) > 4*limit+1000 {
				return errTooMany(limit)
			}
			return nil
		})
		if err != nil {
			return Walked{}, err
		}
	}
	return collect(rels, raw, limit)
}

func (l *LocalFS) Sha256(ctx context.Context, rels []string) (map[string]string, error) {
	out := map[string]string{}
	for _, rel := range rels {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r, err := l.Read(ctx, rel)
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, r)
		r.Close()
		if err != nil {
			return nil, apierr.New(502, "%s: %s", rel, reason(err))
		}
		out[rel] = hex.EncodeToString(h.Sum(nil))
	}
	return out, nil
}

func (l *LocalFS) Read(ctx context.Context, rel string) (io.ReadCloser, error) {
	p, err := l.abs(rel)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(p)
	if err != nil {
		return nil, apierr.New(404, "%s: %s", rel, reason(err))
	}
	if !before.Mode().IsRegular() {
		return nil, apierr.New(400, "%s is no longer a plain file", rel)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, apierr.New(502, "%s: %s", rel, reason(err))
	}
	// What opened must be what was checked: a link put there between the
	// two steps is refused.
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		f.Close()
		return nil, apierr.New(400, "%s changed while it was opened", rel)
	}
	return f, nil
}

func (l *LocalFS) Write(ctx context.Context, rel string, r io.Reader, mtime int64, mode uint32) (int64, error) {
	if err := l.Mkdir(ctx, parentOf(rel)); err != nil {
		return 0, err
	}
	p, err := l.abs(rel)
	if err != nil {
		return 0, err
	}
	perm := os.FileMode(0o644)
	if mode&0o100 != 0 {
		perm = 0o755
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, apierr.New(502, "%s: %s", rel, reason(err))
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	if mtime > 0 {
		t := unixTime(mtime)
		os.Chtimes(p, t, t)
	}
	return n, nil
}

func (l *LocalFS) Mkdir(ctx context.Context, rel string) error {
	root, err := l.realRoot()
	if err != nil {
		return err
	}
	if rel == "" {
		return nil
	}
	// One level at a time, each checked, so no link on the way is followed
	// out of the root.
	cur := root
	for _, seg := range strings.Split(rel, "/") {
		next := filepath.Join(cur, seg)
		fi, err := os.Lstat(next)
		switch {
		case err == nil && fi.IsDir():
		case err == nil:
			return apierr.New(409, "%s is in the way of a folder", seg)
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(next, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return apierr.New(502, "%s: %s", rel, reason(err))
			}
		default:
			return apierr.New(502, "%s: %s", rel, reason(err))
		}
		cur = next
	}
	return nil
}

func (l *LocalFS) Land(ctx context.Context, pairs []LandPair) ([]string, error) {
	var done []string
	for _, p := range pairs {
		if err := l.Mkdir(ctx, parentOf(p.Final)); err != nil {
			return done, err
		}
		from, err := l.abs(p.Staged)
		if err != nil {
			return done, err
		}
		to, err := l.abs(p.Final)
		if err != nil {
			return done, err
		}
		if fi, err := os.Lstat(to); err == nil {
			if !p.Replace || !fi.Mode().IsRegular() {
				return done, apierr.New(409, "%s is already there and the plan did not list it", p.Final)
			}
			if err := os.Rename(from, to); err != nil {
				return done, apierr.New(502, "%s: %s", p.Final, reason(err))
			}
			done = append(done, p.Final)
			continue
		}
		// A hard link fails if anything took the name since; where the volume
		// has none, a rename after one more look.
		if err := os.Link(from, to); err == nil {
			os.Remove(from)
		} else if errors.Is(err, fs.ErrExist) {
			return done, apierr.New(409, "%s is already there and the plan did not list it", p.Final)
		} else {
			if _, err := os.Lstat(to); err == nil {
				return done, apierr.New(409, "%s is already there and the plan did not list it", p.Final)
			}
			if err := os.Rename(from, to); err != nil {
				return done, apierr.New(502, "%s: %s", p.Final, reason(err))
			}
		}
		done = append(done, p.Final)
	}
	return done, nil
}

func (l *LocalFS) Remove(ctx context.Context, files, dirs []string) ([]string, error) {
	var removed []string
	for _, f := range files {
		p, err := l.abs(f)
		if err != nil {
			return removed, err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(p); err != nil {
			return removed, apierr.New(502, "%s: %s", f, reason(err))
		}
		removed = append(removed, f)
	}
	for _, d := range deepestFirst(dirs) {
		if p, err := l.abs(d); err == nil && d != "" {
			os.Remove(p) // only when empty
		}
	}
	return removed, nil
}

func (l *LocalFS) RemoveStaging(ctx context.Context, rel string) error {
	if !strings.HasPrefix(baseName(rel), stagingPrefix) {
		return apierr.New(400, "not a staging folder")
	}
	p, err := l.abs(rel)
	if err != nil {
		if statusOf(err) == 404 {
			return nil
		}
		return err
	}
	return os.RemoveAll(p)
}

func baseName(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

// deepestFirst orders folders so each comes before those above it.
func deepestFirst(dirs []string) []string {
	out := append([]string(nil), dirs...)
	sortByDepth(out)
	return out
}

func sortByDepth(a []string) {
	sort.Slice(a, func(i, j int) bool {
		di, dj := strings.Count(a[i], "/"), strings.Count(a[j], "/")
		if di != dj {
			return di > dj
		}
		return a[i] > a[j]
	})
}

func (l *LocalFS) Free(ctx context.Context, rel string) (int64, bool) {
	p, err := l.abs(rel)
	if err != nil {
		return 0, false
	}
	return freeBytes(p)
}
