package kb

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Files are the operations on the main library's files (move.ts and the
// delete routes): each moves the caches keyed by path with the file and
// re-indexes what it touched.
type Files struct {
	Roots    Roots
	Previews *Previews
	Indexer  Indexer
}

// Moved is one item a move, copy or rename did.
type Moved struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Skipped is an item left alone, with why.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Outcome is move.ts's MoveOutcome. Roots are the top-level subtrees
// touched, in first-touched order; RootDB that the root's own files were.
type Outcome struct {
	Moved   []Moved
	Skipped []Skipped
	Roots   []string
	RootDB  bool
	IndexMS int64
}

func (o *Outcome) touch(rel string) {
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		r := rel[:i]
		for _, x := range o.Roots {
			if x == r {
				return
			}
		}
		o.Roots = append(o.Roots, r)
	} else {
		o.RootDB = true
	}
}

// Result is the outcome as an async job answers with it: the Set of roots
// serializes as {} in Node.
func (o *Outcome) Result() *Obj {
	return O("moved", nonNilMoved(o.Moved), "skipped", nonNilSkipped(o.Skipped), "roots", O(), "rootDb", o.RootDB, "indexMs", o.IndexMS)
}

func nonNilMoved(v []Moved) []Moved {
	if v == nil {
		return []Moved{}
	}
	return v
}

func nonNilSkipped(v []Skipped) []Skipped {
	if v == nil {
		return []Skipped{}
	}
	return v
}

func newOutcome() *Outcome { return &Outcome{Moved: []Moved{}, Skipped: []Skipped{}} }

// Progress is called before and after each item.
type ProgressFunc func(done int, current string)

func (f *Files) abs(rel string) (string, error) { return Resolve(rel, f.Roots.KBRoot) }

func statOK(p string) bool { _, err := os.Stat(p); return err == nil }

// caseOnly reports a target that is the source itself under another case:
// on a filesystem that ignores case (drvfs under WSL, a macOS volume) the
// name the person typed finds the file they are renaming.
func caseOnly(fromRel, toRel, fromAbs, toAbs string) bool {
	if fromRel == toRel || !strings.EqualFold(fromRel, toRel) {
		return false
	}
	a, err1 := os.Stat(fromAbs)
	b, err2 := os.Stat(toAbs)
	return err1 == nil && err2 == nil && os.SameFile(a, b)
}

// moveCaches carries a file's path-keyed caches to its new path: the
// extracted text, the PDF rendering, the page images, the model cache. A
// cache that is not there, or across devices, is rebuilt later.
func (f *Files) moveCaches(from, to string) {
	mv := func(a, b string) {
		os.MkdirAll(filepath.Dir(b), 0o777)
		os.Rename(a, b)
	}
	mv(f.Roots.ExtractPath(from), f.Roots.ExtractPath(to))
	ff, fp := f.Previews.PreviewPaths(from)
	tf, tp := f.Previews.PreviewPaths(to)
	mv(ff, tf)
	mv(fp, tp)
	mv(f.Roots.ModelCachePath(from), f.Roots.ModelCachePath(to))
}

func skipReason(err error) string { return errMessage(err) }

func (f *Files) checkDest(destRaw string) (string, error) {
	dest := trimSlashes(destRaw)
	for _, part := range strings.Split(dest, "/") {
		if part == "" {
			continue
		}
		if ExcludeDirs[part] || strings.HasPrefix(part, ".") {
			return "", Error(400, "protected destination")
		}
	}
	abs, err := f.abs(dest)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		name := dest
		if name == "" {
			name = "root"
		}
		return "", Error(400, "not a directory: %s", name)
	}
	return dest, nil
}

// Move is movePaths: each path into dest, skipping (with the reason) what
// cannot go.
func (f *Files) Move(paths []string, destRaw string, progress ProgressFunc) (*Outcome, error) {
	if len(paths) == 0 {
		return nil, Error(400, "paths required")
	}
	dest, err := f.checkDest(destRaw)
	if err != nil {
		return nil, err
	}
	out := newOutcome()
	for done, rel := range paths {
		cleaned := trimSlashes(rel)
		if progress != nil {
			progress(done, cleaned)
		}
		if err := f.moveOne(cleaned, dest, out); err != nil {
			out.Skipped = append(out.Skipped, Skipped{cleaned, skipReason(err)})
		}
		if progress != nil {
			progress(done+1, cleaned)
		}
	}
	return out, nil
}

func (f *Files) moveOne(cleaned, dest string, out *Outcome) error {
	if protected(cleaned) {
		return Error(400, "protected path")
	}
	abs, err := f.abs(cleaned)
	if err != nil {
		return err
	}
	name := jsBasename(cleaned)
	target := name
	if dest != "" {
		target = dest + "/" + name
	}
	if target == cleaned {
		return Error(400, "already there")
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if st.IsDir() && (dest == cleaned || strings.HasPrefix(dest, cleaned+"/")) {
		return Error(400, "cannot move a directory into itself")
	}
	targetAbs, err := f.abs(target)
	if err != nil {
		return err
	}
	if caseOnly(cleaned, target, abs, targetAbs) {
		return Error(400, "already there")
	}
	if statOK(targetAbs) {
		return Error(409, "name already exists there")
	}
	if err := os.Rename(abs, targetAbs); err != nil {
		return err
	}
	if st.Mode().IsRegular() {
		f.moveCaches(cleaned, target)
	}
	out.Moved = append(out.Moved, Moved{cleaned, target})
	out.touch(cleaned)
	out.touch(target)
	return nil
}

// Rename is renamePath: a file or folder renamed where it is.
func (f *Files) Rename(rel, nameRaw string) (*Outcome, error) {
	cleaned := trimSlashes(rel)
	trimmed := JSTrim(nameRaw)
	name := jsBasename(trimmed)
	if cleaned == "" {
		return nil, Error(400, "path required")
	}
	if name == "" || strings.HasPrefix(name, ".") || name != trimmed {
		return nil, Error(400, "unusable name: %s", nameRaw)
	}
	if strings.ContainsAny(name, `/\`) {
		return nil, Error(400, "a name cannot contain a path separator")
	}
	if protected(cleaned) {
		return nil, Error(400, "protected path")
	}
	parent := ""
	if i := strings.LastIndexByte(cleaned, '/'); i >= 0 {
		parent = cleaned[:i]
	}
	target := name
	if parent != "" {
		target = parent + "/" + name
	}
	out := newOutcome()
	if target == cleaned {
		return out, nil
	}
	abs, err := f.abs(cleaned)
	if err != nil {
		return nil, err
	}
	targetAbs, err := f.abs(target)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if statOK(targetAbs) && !caseOnly(cleaned, target, abs, targetAbs) {
		return nil, Error(409, "name already exists here")
	}
	if err := os.Rename(abs, targetAbs); err != nil {
		return nil, err
	}
	if st.Mode().IsRegular() {
		f.moveCaches(cleaned, target)
	}
	out.Moved = append(out.Moved, Moved{cleaned, target})
	if parent != "" {
		out.Roots = append(out.Roots, strings.SplitN(parent, "/", 2)[0])
	} else {
		out.RootDB = true
	}
	return out, nil
}

// jsBasename is path.basename: the last segment, trailing slashes ignored.
func jsBasename(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Copy is copyPaths: each path into dest, named "<stem> copy<ext>" (then
// " copy 2" and on) where the name is taken. Caches are not copied.
func (f *Files) Copy(paths []string, destRaw string, progress ProgressFunc) (*Outcome, error) {
	if len(paths) == 0 {
		return nil, Error(400, "paths required")
	}
	dest, err := f.checkDest(destRaw)
	if err != nil {
		return nil, err
	}
	out := newOutcome()
	for done, rel := range paths {
		cleaned := trimSlashes(rel)
		if progress != nil {
			progress(done, cleaned)
		}
		if err := f.copyOne(cleaned, dest, out); err != nil {
			out.Skipped = append(out.Skipped, Skipped{cleaned, skipReason(err)})
		}
		if progress != nil {
			progress(done+1, cleaned)
		}
	}
	return out, nil
}

func (f *Files) copyOne(cleaned, dest string, out *Outcome) error {
	if protected(cleaned) {
		return Error(400, "protected path")
	}
	abs, err := f.abs(cleaned)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if st.IsDir() && (dest == cleaned || strings.HasPrefix(dest, cleaned+"/")) {
		return Error(400, "cannot copy a directory into itself")
	}
	name := jsBasename(cleaned)
	ext := ""
	if st.Mode().IsRegular() {
		ext = ExtName(name)
	}
	stem := strings.TrimSuffix(name, ext)
	join := func(n string) string {
		if dest != "" {
			return dest + "/" + n
		}
		return n
	}
	target := join(name)
	for n := 2; ; n++ {
		ta, err := f.abs(target)
		if err != nil {
			return err
		}
		if !statOK(ta) {
			break
		}
		cand := fmt.Sprintf("%s copy %d%s", stem, n, ext)
		if n == 2 {
			cand = stem + " copy" + ext
		}
		target = join(cand)
		if n > 50 {
			return Error(409, "too many copies of that name")
		}
	}
	targetAbs, err := f.abs(target)
	if err != nil {
		return err
	}
	if err := copyTree(abs, targetAbs); err != nil {
		return err
	}
	out.Moved = append(out.Moved, Moved{cleaned, target})
	out.touch(target)
	return nil
}

// copyTree is fs.cp with recursive: folders with their mode, files with
// their mode and new times, links as links (a relative target made
// absolute, as cp does without verbatimSymlinks). A link given as the
// source is copied as a link too.
func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(src), target)
		}
		return os.Symlink(target, dst)
	}
	if !st.IsDir() {
		return copyFile(src, dst, st.Mode())
	}
	return copyDir(src, dst, st.Mode())
}

func copyDir(src, dst string, mode os.FileMode) error {
	if err := os.Mkdir(dst, mode.Perm()); err != nil && !os.IsExist(err) {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		info, err := os.Lstat(s)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(s)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(s), target)
			}
			if err := os.Symlink(target, d); err != nil {
				return err
			}
		case info.IsDir():
			if err := copyDir(s, d, info.Mode()); err != nil {
				return err
			}
		default:
			if err := copyFile(s, d, info.Mode()); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode.Perm())
}

// Deleted is what a bulk delete did.
type Deleted struct {
	Deleted []string
	Skipped []Skipped
	Roots   []string
	RootDB  bool
}

// DeletePaths is the routes' deletePaths: files only, with their caches.
func (f *Files) DeletePaths(paths []string, progress ProgressFunc) *Deleted {
	out := &Deleted{Deleted: []string{}, Skipped: []Skipped{}}
	o := &Outcome{}
	for i, rel := range paths {
		cleaned := trimSlashes(rel)
		if progress != nil {
			progress(i, cleaned)
		}
		reason, err := f.deleteOne(cleaned)
		if err != nil {
			out.Skipped = append(out.Skipped, Skipped{cleaned, reason})
			continue
		}
		out.Deleted = append(out.Deleted, cleaned)
		o.touch(cleaned)
	}
	out.Roots, out.RootDB = o.Roots, o.RootDB
	return out
}

func (f *Files) deleteOne(cleaned string) (string, error) {
	if protected(cleaned) {
		return "protected path", Error(400, "protected path")
	}
	abs, err := f.abs(cleaned)
	if err != nil {
		return errMessage(err), err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "not found", err
	}
	if !st.Mode().IsRegular() {
		e := Error(400, "directories are not deletable here")
		return errMessage(e), e
	}
	if err := os.Remove(abs); err != nil {
		return "not found", err
	}
	f.dropCaches(cleaned)
	return "", nil
}

func (f *Files) dropCaches(rel string) {
	os.Remove(f.Roots.ExtractPath(rel))
	f.Previews.RemovePreview(rel)
	os.Remove(f.Roots.ModelCachePath(rel))
}

// DeleteDir is DELETE /api/kb/dir's removal: the subtree, its caches and
// its slice of the index, then the caches built from the index are told.
func (f *Files) DeleteDir(ctx context.Context, cleaned, abs string) error {
	if err := os.RemoveAll(abs); err != nil {
		return err
	}
	for _, c := range []string{"extract", "pdf-preview", "pdf-pages", "model-cache"} {
		os.RemoveAll(filepath.Join(f.Roots.IndexBase, c, cleaned))
	}
	os.RemoveAll(filepath.Join(f.Roots.GufiIndex, cleaned))
	f.Indexer.Changed(ctx, "dbList", "context")
	return nil
}

// DeleteFile removes one file and its caches, then re-indexes what covers it.
func (f *Files) DeleteFile(ctx context.Context, rel, abs string) (int64, error) {
	if err := os.Remove(abs); err != nil {
		return 0, err
	}
	f.dropCaches(rel)
	return ReindexForFile(ctx, f.Indexer, rel)
}
