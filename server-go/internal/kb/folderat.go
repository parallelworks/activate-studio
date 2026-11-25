package kb

import (
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// FolderAt is a folder of one of the viewer's libraries, on the system that
// holds it: where a pw code session started from the Library works.
type FolderAt struct {
	Lib Library
	// Rel is the folder's path in the library, File the file's that was
	// asked for, "" for none.
	Rel, File string
	// Site is the system, "" for the host the Studio runs on, and Root and
	// Dir the library's folder and this one there.
	Site, Root, Dir string
	// Ino is the folder's inode there, to tell it from another at its path.
	Ino string
	// SSH: the system is a host reached over ssh, whose pw code says it is
	// Daemon ("" where none runs), the id ACTIVATE lists it by.
	SSH    bool
	Daemon string
}

// FolderPlaces finds folders in people's own libraries, as the viewer. The
// libraries track sets it.
type FolderPlaces interface {
	Folder(r *http.Request, lib Library, rel string) (FolderAt, error)
}

// Folders is set by the libraries track; nil, and only this host's
// libraries have folders a session starts in.
var Folders FolderPlaces

// folderNone is the one answer for no such library, a path that is not
// there, and one the viewer cannot open.
const folderNone = "That folder is not there any more, or it is not open to you."

// ErrFolderNone is that answer.
func ErrFolderNone() error { return Error(404, folderNone) }

// ErrFolderReadOnly is why no session starts in a folder the viewer cannot
// write in.
func ErrFolderReadOnly(what string) error {
	return Error(403, "%s, so a pw code session cannot work here.", what)
}

// FolderFor is the folder at rel of the library named id, or the folder of
// the file there, when the viewer can open it and write in it.
func (m *Module) FolderFor(r *http.Request, id, rel string) (FolderAt, error) {
	if _, _, ok := m.linkUser(r); !ok {
		return FolderAt{}, Error(401, "Sign in to start a chat in a library's folder.")
	}
	rel, err := linkRel(rel)
	if err != nil {
		return FolderAt{}, err
	}
	if id == "" || strings.EqualFold(id, All) || strings.ContainsAny(rel, "\r\n") {
		return FolderAt{}, ErrFolderNone()
	}
	lib, err := m.libByID(r, id)
	if err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) && ae.Status != http.StatusNotFound {
			return FolderAt{}, err
		}
		return FolderAt{}, ErrFolderNone()
	}
	// What the Library does not list has no folder here either.
	if rel != "" {
		parts := strings.Split(rel, "/")
		for i, p := range parts {
			if Excluded(p, i < len(parts)-1) {
				return FolderAt{}, ErrFolderNone()
			}
		}
	}
	switch {
	case lib.Bucket:
		return FolderAt{}, Error(409, "%s is in a bucket, which has no folder a pw code session can work in.", lib.Label)
	case lib.ReadThrough:
		return FolderAt{}, Error(409, "The files of %s are read through another host, which the Studio starts no session on.", lib.Label)
	case lib.Elsewhere():
		if Folders == nil || !strings.HasPrefix(lib.IndexRoot, remotePrefix) {
			return FolderAt{}, Error(409, "The Studio does not know the folder %s is made from.", lib.Label)
		}
		return Folders.Folder(r, lib, rel)
	case lib.SourceRoot == "":
		return FolderAt{}, Error(409, "%s has no files on this host.", lib.Label)
	}
	// Where people sign in, a library here is read only to those the Studio
	// does not let change it, whatever their account on the host may do.
	if m.A.Cfg.AuthEnabled() && !lib.Writable {
		return FolderAt{}, ErrFolderReadOnly(lib.Label + " is read only for you")
	}
	st, ok, err := localLinkStat(lib.SourceRoot, rel)
	if err != nil || !ok || !st.Readable {
		return FolderAt{}, ErrFolderNone()
	}
	at := FolderAt{Lib: lib, Rel: rel, Root: lib.SourceRoot}
	if !st.Dir {
		at.File, at.Rel = rel, strings.Trim(path.Dir("/"+rel), "/")
		if st, ok, err = localLinkStat(lib.SourceRoot, at.Rel); err != nil || !ok || !st.Dir || !st.Readable {
			return FolderAt{}, ErrFolderNone()
		}
	}
	at.Dir, at.Ino = filepath.Join(lib.SourceRoot, filepath.FromSlash(at.Rel)), st.Same.Ino
	if !m.A.Cfg.AuthEnabled() && !dirWritable(at.Dir) {
		return FolderAt{}, ErrFolderReadOnly("Your account cannot write in this folder")
	}
	return at, nil
}
