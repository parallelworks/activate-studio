package agentsmd

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// CleanFolder is a folder named relative to a library's top: slashes only,
// no parent steps and no hidden or machinery folders. "" is the top.
func CleanFolder(rel string) (string, error) {
	c := strings.Trim(strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/"), "/")
	if c == "" {
		return "", nil
	}
	for _, part := range strings.Split(c, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || kb.ExcludeDirs[part] {
			return "", apierr.New(400, "folder: a folder in the library, relative to its top, with no .. or hidden folders")
		}
	}
	return path.Clean(c), nil
}

// Local is a folder in a library whose files are on this host, written as
// the Library writes there.
type Local struct {
	// Root is the library's top on this host; Rel the folder in it.
	Root, Rel string
	// Label names the library in messages.
	Label string
	// Index is told the library paths that changed; nil for none.
	Index func(rels []string)
}

func (l Local) abs(rel string) (string, error) {
	return kb.Resolve(path.Join(l.Rel, rel), l.Root)
}

// libRel is rel as a path from the library's top.
func (l Local) libRel(rel string) string { return path.Join(l.Rel, rel) }

func (l Local) Where() string {
	if l.Rel == "" {
		return "the top of " + l.Label
	}
	return l.Rel + " in " + l.Label
}

func (l Local) Read(rel string) ([]byte, bool, error) {
	abs, err := l.abs(rel)
	if err != nil {
		return nil, false, err
	}
	st, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	case st.Mode()&fs.ModeSymlink != 0:
		return nil, false, apierr.New(409, "%s in %s is a link; the Studio does not write through one. Replace it with a file, or apply the instructions to another folder.", rel, l.Where())
	case !st.Mode().IsRegular():
		return nil, false, apierr.New(409, "%s in %s is not a file", rel, l.Where())
	case st.Size() > MaxFile:
		return nil, false, apierr.New(413, "%s in %s is over %d bytes", rel, l.Where(), MaxFile)
	}
	b, err := os.ReadFile(abs)
	return b, err == nil, err
}

// Write puts data beside the file, then in its place in one rename, so a
// reader never sees half a file. A file that was there keeps its mode.
func (l Local) Write(rel string, data []byte) error {
	abs, err := l.abs(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Lstat(abs); err == nil {
		if st.Mode()&fs.ModeSymlink != 0 {
			return apierr.New(409, "%s in %s is a link; the Studio does not write through one", rel, l.Where())
		}
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".studio-agents-md-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), mode)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), abs)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// Delete removes the file, then the folders above it it left empty, up to
// the folder itself; a folder with anything in it stays.
func (l Local) Delete(rel string) error {
	abs, err := l.abs(rel)
	if err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	top, err := l.abs("")
	if err != nil {
		return nil
	}
	for dir := filepath.Dir(abs); dir != top && strings.HasPrefix(dir, top+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			break
		}
	}
	return nil
}

func (l Local) Dirs(rel string) ([]string, error) {
	abs, err := l.abs(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (l Local) Changed(rels []string) {
	if l.Index == nil {
		return
	}
	var out []string
	for _, r := range rels {
		out = append(out, l.libRel(r))
	}
	l.Index(out)
}

// Space is a folder in a person's own library (My library), read and
// written as them through kb.SpaceFiles.
type Space struct {
	R   *http.Request
	Lib kb.Library
	Rel string
}

func (s Space) libRel(rel string) string { return path.Join(s.Rel, rel) }

func (s Space) Where() string {
	if s.Rel == "" {
		return "the top of " + s.Lib.Label
	}
	return s.Rel + " in " + s.Lib.Label
}

func (s Space) files() (kb.SpaceFS, error) {
	if kb.SpaceFiles == nil {
		return nil, apierr.New(403, "%s is read only here", s.Lib.Label)
	}
	return kb.SpaceFiles, nil
}

func (s Space) Read(rel string) ([]byte, bool, error) {
	sf, err := s.files()
	if err != nil {
		return nil, false, err
	}
	b, err := sf.Get(s.R, s.Lib, s.libRel(rel), MaxFile)
	if err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) && ae.Status == 404 {
			return nil, false, nil
		}
		if errors.As(err, &ae) && ae.Status == 409 {
			return nil, false, apierr.New(409, "%s in %s is a link or not a file; the Studio does not write through one", rel, s.Where())
		}
		return nil, false, err
	}
	return b, true, nil
}

func (s Space) Write(rel string, data []byte) error {
	sf, err := s.files()
	if err != nil {
		return err
	}
	full := s.libRel(rel)
	if dir := path.Dir(full); dir != "." && dir != "" {
		// An error here is a folder already there; Put says if it is not.
		_ = sf.Mkdir(s.R, s.Lib, dir)
	}
	return sf.Put(s.R, s.Lib, full, bytes.NewReader(data), MaxFile)
}

// Delete removes the file only: a space's listing leaves hidden files out,
// so an empty-looking folder there may not be empty.
func (s Space) Delete(rel string) error {
	sf, err := s.files()
	if err != nil {
		return err
	}
	_, skipped, err := sf.Delete(s.R, s.Lib, []string{s.libRel(rel)})
	if err != nil {
		return err
	}
	if len(skipped) > 0 && skipped[0].Reason != "not found" {
		return fmt.Errorf("%s: %s", rel, skipped[0].Reason)
	}
	return nil
}

func (s Space) Dirs(rel string) ([]string, error) {
	sf, err := s.files()
	if err != nil {
		return nil, err
	}
	list, err := sf.List(s.R, s.Lib, s.libRel(rel))
	if err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) && ae.Status == 404 {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range list {
		if e.Type == "dir" {
			out = append(out, e.Name)
		}
	}
	return out, nil
}

func (s Space) Changed(rels []string) {
	sf, err := s.files()
	if err != nil {
		return
	}
	var out []string
	for _, r := range rels {
		out = append(out, s.libRel(r))
	}
	sf.Changed(s.R, s.Lib, out)
}
