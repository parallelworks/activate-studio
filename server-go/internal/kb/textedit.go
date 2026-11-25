package kb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

// Making and editing plain text files in the Library. A file is opened with
// a version, the sha256 of its bytes, and saved naming the version it was
// opened at, so a save never writes over a change made since. Uploads are
// apart from this (upload.go, spaces.go).

// MaxEditBytes is the largest text file the Library edits.
const MaxEditBytes = 1 << 20

// NoBase is the base of a save that makes a new file.
const NoBase = "none"

var versionRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TextVersion is the version of a file's bytes.
func TextVersion(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Why a save was not made (TextConflict.State).
const (
	TextChanged = "changed" // the file is not as it was opened
	TextMissing = "missing" // the file is not there any more
	TextExists  = "exists"  // a new file's name is taken
)

// TextConflict is a save refused so that nothing is written over: nothing
// was changed. Version is the file's now, where it was read.
type TextConflict struct {
	State   string
	Version string
}

func (e *TextConflict) Error() string {
	switch e.State {
	case TextMissing:
		return "This file was moved or deleted after you opened it."
	case TextExists:
		return "There is already something of that name here. Choose another name."
	}
	return "This file changed after you opened it."
}

// TextFS writes text files in a person's library, as them.
type TextFS interface {
	// OpenText is a file's bytes, at most max of them, refused where
	// SaveText would refuse the file.
	OpenText(r *http.Request, lib Library, rel string, max int) ([]byte, error)
	// CreateText makes a new file, a TextConflict when the name is taken.
	CreateText(r *http.Request, lib Library, rel string, data []byte) error
	// SaveText replaces a file's bytes when its version is still base, and
	// is a TextConflict otherwise.
	SaveText(r *http.Request, lib Library, rel string, data []byte, base string) error
}

// SpaceText is set by the libraries track; nil, and such libraries' text
// is not edited.
var SpaceText TextFS

// textLocks takes the saves of one file on this computer one at a time.
var textLocks sync.Map

// textTarget is the library and path a text request names, refused where
// the Library's other edits are.
func (m *Module) textTarget(r *http.Request) (Library, string, error) {
	var lib Library
	var err error
	rel := qs(r, "path")
	if inAll(r) {
		lib, rel, err = m.splitAll(r, rel)
	} else {
		lib, err = m.lib(r, body{})
	}
	if err != nil {
		return lib, "", err
	}
	switch {
	case lib.ReadOnlyWhy != "":
		return lib, "", Error(403, "%s", lib.ReadOnlyWhy)
	case !lib.Writable:
		return lib, "", Error(403, "%s is read only", lib.Label)
	case lib.AsViewer():
		if SpaceFiles == nil || SpaceText == nil {
			return lib, "", Error(403, "%s is read only here", lib.Label)
		}
	case !lib.Primary || lib.Elsewhere() || lib.SourceRoot == "":
		return lib, "", Error(403, "%s is read only", lib.Label)
	}
	return lib, rel, nil
}

// checkText is why bytes are not text the editor can hold and give back as
// they are, nil when they are.
func checkText(name string, b []byte) error {
	if len(b) > MaxEditBytes {
		return Error(413, "%s is larger than 1 MB, the most a file edited here may hold", name)
	}
	if !utf8.Valid(b) {
		return Error(415, "%s is not UTF-8 text, so it cannot be edited here", name)
	}
	for _, c := range b {
		if c == 0 {
			return Error(415, "%s is not a text file", name)
		}
	}
	return nil
}

// openText is GET /api/kb/text: a file's whole text, to edit, with its
// version. Only where the viewer may save it.
func (m *Module) openText(w http.ResponseWriter, r *http.Request) error {
	lib, rel, err := m.textTarget(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	if rel, err = spaceRel(rel); err != nil {
		return err
	}
	var b []byte
	if lib.AsViewer() {
		b, err = SpaceText.OpenText(r, lib, rel, MaxEditBytes+1)
	} else {
		b, err = readLocalText(lib.SourceRoot, rel)
	}
	if err != nil {
		return err
	}
	if err := checkText(path.Base(rel), b); err != nil {
		return err
	}
	writeJSON(w, 200, O("path", rel, "content", string(b), "version", TextVersion(b), "size", len(b)))
	return nil
}

// saveText is PUT /api/kb/text: the request's body becomes the file's
// bytes. base is the version it was opened at, or "none" to make a new
// file. Always a PUT of bytes, which a page of another site cannot send.
func (m *Module) saveText(w http.ResponseWriter, r *http.Request) error {
	lib, rel, err := m.textTarget(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	base := qs(r, "base")
	if base != NoBase && !versionRe.MatchString(base) {
		return Error(400, "a save names the version it was made from")
	}
	if base == NoBase {
		rel, err = newTextPath(rel)
	} else {
		rel, err = spaceRel(rel)
	}
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxEditBytes+1))
	if err != nil {
		return err
	}
	if err := checkText(path.Base(rel), b); err != nil {
		return err
	}
	switch {
	case lib.AsViewer() && base == NoBase:
		err = SpaceText.CreateText(r, lib, rel, b)
	case lib.AsViewer():
		err = SpaceText.SaveText(r, lib, rel, b, base)
	case base == NoBase:
		err = createLocalText(lib.SourceRoot, rel, b)
	default:
		err = saveLocalText(lib.SourceRoot, rel, b, base)
	}
	var conflict *TextConflict
	if errors.As(err, &conflict) {
		out := O("error", conflict.Error(), "conflict", conflict.State)
		if conflict.Version != "" {
			out.Set("version", conflict.Version)
		}
		writeJSON(w, 409, out)
		return nil
	}
	if err != nil {
		return err
	}
	// Indexed as an upload is: the main library's by the pass the client
	// asks for next, a person's own library by itself once changes settle.
	out := O("path", rel, "version", TextVersion(b), "size", len(b), "indexed", false, "deferred", true)
	if lib.AsViewer() {
		SpaceFiles.Changed(r, lib, []string{rel})
		out.Set("reindexing", true)
	}
	writeJSON(w, 200, out)
	return nil
}

// newTextPath is where a new file goes: the folder as it is, the name as
// an upload's and a new folder's are made usable.
func newTextPath(rel string) (string, error) {
	dir, name := path.Split(strings.TrimLeft(rel, "/"))
	dir = trimSlashes(dir)
	if strings.TrimSpace(name) == "" {
		return "", Error(400, "a file needs a name")
	}
	name, err := SanitizeName(name)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return spaceRel(name)
	}
	return spaceRel(dir + "/" + name)
}

// textRoot opens the library's folder so that no path in it, through a
// link or otherwise, leads outside it.
func textRoot(root string, do func(*os.Root) error) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	err = do(r)
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "path escapes"):
		return Error(403, "this path goes through a link that leads out of the library, so it cannot be edited here")
	case errors.Is(err, syscall.ENAMETOOLONG):
		return Error(400, "that name is too long")
	case errors.Is(err, syscall.ENOTDIR), errors.Is(err, fs.ErrExist):
		return Error(409, "a file is where its folder would be")
	}
	return err
}

// openLocal opens a plain file of the library, never through a last link.
func openLocal(r *os.Root, rel string) (*os.File, error) {
	name := filepath.FromSlash(rel)
	st, err := r.Lstat(name)
	switch {
	case missing(err):
		return nil, &TextConflict{State: TextMissing}
	case err != nil:
		return nil, err
	case st.Mode()&fs.ModeSymlink != 0:
		return nil, Error(409, "%s is a link", path.Base(rel))
	case !st.Mode().IsRegular():
		return nil, Error(409, "%s is not a file", path.Base(rel))
	case st.Size() > MaxEditBytes:
		return nil, Error(413, "%s is larger than 1 MB, the most a file edited here may hold", path.Base(rel))
	}
	f, err := r.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	// The file opened is the one looked at, not one put there since.
	if now, err := f.Stat(); err != nil || !os.SameFile(st, now) {
		f.Close()
		return nil, &TextConflict{State: TextChanged}
	}
	return f, nil
}

func readLocalText(root, rel string) (b []byte, err error) {
	err = textRoot(root, func(r *os.Root) error {
		f, err := openLocal(r, rel)
		var c *TextConflict
		if errors.As(err, &c) && c.State == TextMissing {
			return Error(404, "no such file")
		}
		if err != nil {
			return err
		}
		defer f.Close()
		b, err = io.ReadAll(io.LimitReader(f, MaxEditBytes+1))
		return err
	})
	return b, err
}

// createLocalText makes a new file, its folder first as an upload's is.
func createLocalText(root, rel string, b []byte) error {
	return textRoot(root, func(r *os.Root) error {
		name := filepath.FromSlash(rel)
		if dir := filepath.Dir(name); dir != "." {
			// Looked at first: for a link that leads out, MkdirAll answers
			// EEXIST on some Go versions (1.26.0) and says so on others.
			_, err := r.Stat(dir)
			if missing(err) {
				err = r.MkdirAll(dir, 0o777)
			}
			if err != nil {
				return err
			}
		}
		f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			return &TextConflict{State: TextExists}
		}
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			r.Remove(name)
		}
		return err
	})
}

// saveLocalText writes a file where it is, so its mode, owner, ACL and
// labels (kept on the file, or by its inode) stay. What was there is put
// back when the new bytes do not all go in.
func saveLocalText(root, rel string, b []byte, base string) error {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	l, _ := textLocks.LoadOrStore(abs, &sync.Mutex{})
	mu := l.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return textRoot(root, func(r *os.Root) error {
		f, err := openLocal(r, rel)
		if err != nil {
			return err
		}
		defer f.Close()
		was, err := io.ReadAll(io.LimitReader(f, MaxEditBytes+1))
		if err != nil {
			return err
		}
		if v := TextVersion(was); v != base {
			return &TextConflict{State: TextChanged, Version: v}
		}
		if err := writeWhole(f, b); err != nil {
			_ = writeWhole(f, was)
			return err
		}
		return f.Sync()
	})
}

func writeWhole(f *os.File, b []byte) error {
	if _, err := f.WriteAt(b, 0); err != nil {
		return err
	}
	return f.Truncate(int64(len(b)))
}
