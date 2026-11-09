package index

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// indexModes.ts: index folders of a source whose folders their owner may
// not write. gufi_dir2index gives each index folder the mode of the folder
// it indexes, as it finishes it, before the folders inside it have made
// theirs; run by a user other than root, a source folder of mode 0555
// gets an index folder this process may not write in. So the index
// folders inside each such folder are made before it runs, and opened for
// their owner after.

// Locked is a source folder whose owner may not write it, by its path
// below the root ("" for the root), and the folders in it
// gufi_dir2index enters.
type Locked struct {
	Rel  string
	Dirs []string
}

// locks reports whether gufi_dir2index, run here, makes an index folder of
// this mode that this process may not write in.
func locks(mode fs.FileMode) bool {
	return lockingUser() && mode.Perm()&0o700 != 0o700
}

// lockedEntry is the entry for a folder the walk visits: nil unless its
// mode locks it. Only the folders this process may list are named.
func lockedEntry(abs, rel string, mode fs.FileMode, entries []fs.DirEntry, skip map[string]bool) *Locked {
	if !locks(mode) {
		return nil
	}
	l := &Locked{Rel: rel, Dirs: []string{}}
	for _, e := range entries {
		if !e.IsDir() || skip[e.Name()] {
			continue
		}
		if accessRX(filepath.Join(abs, e.Name())) {
			l.Dirs = append(l.Dirs, e.Name())
		}
	}
	return l
}

// prepareIndex makes the index folders inside each locked folder, under
// built, before gufi_dir2index runs.
func prepareIndex(built string, locked []Locked) error {
	for _, l := range locked {
		for _, d := range l.Dirs {
			if err := os.MkdirAll(filepath.Join(built, l.Rel, d), 0o755); err != nil {
				return err
			}
		}
	}
	return nil
}

// openDir gives a folder read, write and search permission for its owner,
// keeping the rest of its mode.
func openDir(p string) {
	st, err := os.Stat(p)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0o700 == 0o700 {
		return
	}
	os.Chmod(p, st.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)|0o700)
}

// openIndex opens the index folders of the locked folders, parents first.
func openIndex(built string, locked []Locked) {
	order := append([]Locked(nil), locked...)
	depth := func(rel string) int {
		if rel == "" {
			return 0
		}
		return len(strings.Split(rel, "/"))
	}
	sort.SliceStable(order, func(i, j int) bool { return depth(order[i].Rel) < depth(order[j].Rel) })
	for _, l := range order {
		openDir(filepath.Join(built, l.Rel))
	}
}

func openTree(root string) {
	openDir(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			openTree(filepath.Join(root, e.Name()))
		}
	}
}

// rmTree removes a tree as rm -rf does, opening its folders first when
// one keeps the removal out.
func rmTree(p string) error {
	err := os.RemoveAll(p)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrPermission) && !strings.Contains(err.Error(), "directory not empty") {
		return err
	}
	openDir(filepath.Dir(p))
	openTree(p)
	return os.RemoveAll(p)
}

var unmadeRe = regexp.MustCompile(`(?m)^mkdir (/.*)/db\.db failure: \d+`)

// unmadeIndexDirs are the folders gufi_dir2index could not make an index
// folder for, as it says, by their index folders.
func unmadeIndexDirs(stderr string) []string {
	var out []string
	for _, m := range unmadeRe.FindAllStringSubmatch(stderr, -1) {
		out = append(out, m[1])
	}
	return out
}

// sourceOf is an index folder under built named again as the source
// folder it is of.
func sourceOf(indexDir, built, src string) string {
	if indexDir == built {
		return src
	}
	if strings.HasPrefix(indexDir, built+"/") {
		return filepath.Join(src, indexDir[len(built)+1:])
	}
	return ""
}

// recoverSwap puts the newest of olds that holds a db.db back at dest,
// when a stop cut a swap off between its two renames; "" when dest is a
// tree already, or nothing is to restore.
func recoverSwap(dest string, olds []string) string {
	if exists(filepath.Join(dest, "db.db")) {
		return ""
	}
	var found []string
	for _, o := range olds {
		if exists(filepath.Join(o, "db.db")) {
			found = append(found, o)
		}
	}
	if len(found) == 0 {
		return ""
	}
	mt := func(p string) int64 {
		if st, err := os.Stat(p); err == nil {
			return st.ModTime().UnixNano()
		}
		return 0
	}
	sort.SliceStable(found, func(i, j int) bool { return mt(found[i]) > mt(found[j]) })
	f := found[0]
	if exists(dest) {
		rmTree(dest)
	}
	os.MkdirAll(filepath.Dir(dest), 0o755)
	openDir(f)
	if os.Rename(f, dest) != nil {
		return ""
	}
	return f
}

// swapLeftovers are the folders beside dest an earlier swap moved the old
// tree to (<dest>.old-<pid>-<time>).
func swapLeftovers(dest string) []string {
	dir, prefix := filepath.Dir(dest), filepath.Base(dest)+".old-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
