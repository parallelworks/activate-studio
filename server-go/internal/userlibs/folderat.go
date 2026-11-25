package userlibs

import (
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/folders"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// pwcodeWho asks the pw code that runs for the account, if one does, for
// its status. It starts none.
const pwcodeWho = `p=$(command -v pw 2>/dev/null)
if [ -z "$p" ]; then for c in "$HOME/pw/pw" "$HOME/.local/bin/pw" "$HOME/bin/pw" /usr/local/bin/pw /opt/homebrew/bin/pw; do if [ -x "$c" ]; then p=$c; break; fi; done; fi
[ -n "$p" ] || exit 0
printf 'GET /v1/status HTTP/1.1\r\nHost: daemon\r\nConnection: close\r\n\r\n' | "$p" code daemon proxy 2>/dev/null
exit 0
`

var machineID = regexp.MustCompile(`"machineId"\s*:\s*"([A-Za-z0-9._:-]{1,128})"`)

// folderWait is how long finding a folder waits for a system.
const folderWait = 60 * time.Second

// Folder is kb.FolderPlaces': a folder of one of the viewer's own
// libraries, as their own session finds it on the library's system.
func (s spaceFiles) Folder(r *http.Request, lib kb.Library, rel string) (kb.FolderAt, error) {
	m := s.m
	w, err := m.whoOf(r)
	if err != nil {
		return kb.FolderAt{}, err
	}
	o, d, err := m.Users.FindRoot(lib.IndexRoot)
	if err != nil {
		return kb.FolderAt{}, err
	}
	if d == nil || o == "" || o != w.owner() {
		return kb.FolderAt{}, kb.ErrFolderNone()
	}
	name, root := d.ShownLabel(m.Users.AuthEnabled), spaceRoot(d)
	switch {
	case d.ReadThrough != nil:
		return kb.FolderAt{}, apierr.New(409, "The files of %s are read through another host, which the Studio starts no session on.", name)
	case !strings.HasPrefix(root, "/"):
		return kb.FolderAt{}, apierr.New(409, "The Studio does not know the folder %s is made from.", name)
	case (d.Shell != nil && !*d.Shell) || (d.OS != nil && strings.EqualFold(*d.OS, "windows")):
		return kb.FolderAt{}, apierr.New(409, "The Studio cannot look at the folder of %s there.", name)
	case d.Shared != nil && d.Shared.Access != "write":
		return kb.FolderAt{}, kb.ErrFolderReadOnly(name + " is shared with you to read")
	case d.ReadOnly != nil:
		return kb.FolderAt{}, apierr.New(403, "%s", *d.ReadOnly)
	}
	site, key, err := m.writeSession(w, o, d)
	if err != nil {
		return kb.FolderAt{}, err
	}
	if _, uid, ok := d.Account(); ok {
		if err := m.sameAccount(r.Context(), site, key, uid); err != nil {
			return kb.FolderAt{}, err
		}
	}
	res, err := m.Pool.Run(r.Context(), site, key, []string{"sh", "-c", folders.AccessScript, "studio-access", root, rel}, folderWait)
	if err != nil {
		return kb.FolderAt{}, m.sessionErr(remote.ShortName(site), key, err)
	}
	a, ok := folders.ParseAccess(res.Stdout)
	if !ok {
		return kb.FolderAt{}, kb.ErrFolderNone()
	}
	if !a.Write {
		return kb.FolderAt{}, kb.ErrFolderReadOnly("Your account on " + remote.ShortName(site) + " cannot write in this folder")
	}
	at := kb.FolderAt{Lib: lib, Rel: rel, Site: site, Root: root, Ino: a.Ino}
	if a.File {
		at.File, at.Rel = rel, joinRel(path.Dir("/"+rel), "")
	}
	at.Dir = path.Join(root, at.Rel)
	if d.IsSSH() {
		// No name says which of the person's pw code machines a host reached
		// over ssh is, so its own pw code is asked.
		at.SSH = true
		res, err := m.Pool.Run(r.Context(), site, key, []string{"sh", "-c", pwcodeWho}, folderWait)
		if err != nil {
			return kb.FolderAt{}, m.sessionErr(remote.ShortName(site), key, err)
		}
		if id := machineID.FindStringSubmatch(res.Stdout); id != nil {
			at.Daemon = id[1]
		}
	}
	return at, nil
}
