package kb

import (
	"archive/zip"
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// A folder of a library downloads as a zip of what the Library shows in
// it: entries hidden by name (a dot first) and the machinery it leaves out
// are left out of the zip too, and a symbolic link is not followed, so the
// zip holds only files under the folder. Its size is checked before the
// first byte is sent, so a folder over the limits is refused with a status
// and not cut off partway.

const (
	// zipMaxFiles and zipMaxBytes bound a folder on this host.
	zipMaxFiles = 50000
	zipMaxBytes = 8 << 30
	// zipMaxRemoteFiles and zipMaxRemoteBytes bound a folder of a library
	// on another system, each of whose files is fetched in turn.
	zipMaxRemoteFiles = 500
	zipMaxRemoteBytes = 1 << 30
)

// zipFile is a file going into a folder's zip: its name inside the zip,
// and where it is read from.
type zipFile struct {
	name string
	abs  string // a file on this host
	rel  string // a file of a library elsewhere, by its path in the library
	size int64
	mod  time.Time
}

// zipName is the zip's name for the folder at rel: the folder's name, or
// the library's for its top.
func zipName(lib Library, rel string) string {
	base := path.Base(strings.Trim(filepath.ToSlash(rel), "/"))
	if base == "." || base == "/" || base == "" {
		base = lib.Label
	}
	if base == "" {
		base = "library"
	}
	return base
}

// localFolderFiles is what goes into the zip of the folder abs on this
// host, under name.
func localFolderFiles(abs, name string) ([]zipFile, error) {
	var out []zipFile
	var total int64
	err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A folder that cannot be read is left out, as the Library
			// leaves out what it cannot list.
			if d != nil && d.IsDir() && p != abs {
				return fs.SkipDir
			}
			return nil
		}
		if p == abs {
			return nil
		}
		if HiddenName(d.Name()) || Excluded(d.Name(), d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		total += info.Size()
		out = append(out, zipFile{name: name + "/" + filepath.ToSlash(rel), abs: p, size: info.Size(), mod: info.ModTime()})
		if len(out) > zipMaxFiles || total > zipMaxBytes {
			return errTooBig
		}
		return nil
	})
	return out, err
}

var errTooBig = Error(413, "this folder is over %d files or %d GB, too large to download as a zip; download the folders inside it one at a time", zipMaxFiles, zipMaxBytes>>30)

// remoteFolderFiles is what goes into the zip of the folder rel of a
// library on another system, from its index.
func (m *Module) remoteFolderFiles(r *http.Request, lib Library, rel, name string) ([]zipFile, error) {
	var out []zipFile
	var total int64
	var walk func(dir string) error
	walk = func(dir string) error {
		list, err := m.remoteTree(r, lib, dir, false)
		if err != nil {
			return err
		}
		for _, e := range list {
			isDir := e.Type == "dir"
			if HiddenName(e.Name) || Excluded(e.Name, isDir) {
				continue
			}
			child := strings.TrimPrefix(path.Join(dir, e.Name), "/")
			if isDir {
				if err := walk(child); err != nil {
					return err
				}
				continue
			}
			total += e.Size
			inside := strings.TrimPrefix(strings.TrimPrefix(child, strings.Trim(rel, "/")), "/")
			f := zipFile{name: name + "/" + inside, rel: child, size: e.Size}
			if e.Mtime > 0 {
				f.mod = time.Unix(e.Mtime, 0)
			}
			out = append(out, f)
			if len(out) > zipMaxRemoteFiles || total > zipMaxRemoteBytes {
				return Error(413, "this folder is over %d files or %d GB, too large to download as a zip from %s; download the folders inside it one at a time", zipMaxRemoteFiles, zipMaxRemoteBytes>>30, lib.Label)
			}
		}
		return nil
	}
	if err := walk(strings.Trim(rel, "/")); err != nil {
		return nil, err
	}
	return out, nil
}

// zipOrSize answers what a folder's zip would hold, for check=1, which the
// Library asks before it starts the download, so a folder over the limits
// is refused with a notice and not saved as a file holding the error; and
// otherwise sends the zip.
func (m *Module) zipOrSize(w http.ResponseWriter, r *http.Request, lib Library, name string, files []zipFile) error {
	if qs(r, "check") != "1" {
		return m.sendZip(w, r, lib, name, files)
	}
	var bytes int64
	for _, f := range files {
		bytes += f.size
	}
	writeJSON(w, 200, map[string]any{"name": name + ".zip", "files": len(files), "bytes": bytes})
	return nil
}

// sendZip writes files as a zip named name.zip. Once the first byte is
// sent the status stands, so a file that cannot be read then is left out
// and the zip is finished without it.
func (m *Module) sendZip(w http.ResponseWriter, r *http.Request, lib Library, name string, files []zipFile) error {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(name+".zip"))
	w.WriteHeader(200)
	z := zip.NewWriter(w)
	for _, f := range files {
		if r.Context().Err() != nil {
			break
		}
		var src io.ReadCloser
		if f.abs != "" {
			fh, err := os.Open(f.abs)
			if err != nil {
				continue
			}
			src = fh
		} else {
			_, data, err := LibraryResolver.Download(r, lib, f.rel)
			if err != nil {
				continue
			}
			src = io.NopCloser(bytes.NewReader(data))
		}
		hdr := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		if !f.mod.IsZero() {
			hdr.Modified = f.mod
		}
		dst, err := z.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(dst, src)
		}
		src.Close()
		if err != nil {
			break
		}
	}
	return z.Close()
}
