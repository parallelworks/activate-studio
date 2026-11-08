package libapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/userlibs"
)

// Reading a library's original files (originals.ts): at the library's own
// location (the folder on this computer, or the host of a library over
// ssh or pw ssh), then, when that cannot give it, on the host the library
// names for reading originals (readThrough), over ssh as the user.

// ReadThroughSingleUser is why setting the host is refused where the
// Studio signs people in.
const ReadThroughSingleUser = "Reading originals through another host uses the ssh keys of the account the Studio runs as, so it is offered only on a Studio with sign-in off."

const readOriginalTimeout = 60 * time.Second

// Attempt is one place an original was looked for.
type Attempt struct {
	Where string `json:"where"`
	Error string `json:"error"`
	// Missing: the place answered and does not have the file; false when
	// it could not be reached.
	Missing bool `json:"missing"`
}

// OriginalUnreachable is an original no place could give: the message
// names every place tried.
type OriginalUnreachable struct {
	Err      *apierr.Error
	Attempts []Attempt
}

func (e *OriginalUnreachable) Error() string { return e.Err.Error() }
func (e *OriginalUnreachable) Unwrap() error { return e.Err }

// Opened is an original as it was read.
type Opened struct {
	File *kb.Obj
	// Where: "this computer", or the host's name.
	Where string
	// Via: "library" for its own location, "read-through" for the host
	// named for reading originals.
	Via string
}

func hostName(rt *libraries.ReadThrough) string {
	if rt.Port != nil && *rt.Port != 0 {
		return fmt.Sprintf("%s:%d", rt.Destination, *rt.Port)
	}
	return rt.Destination
}

// sshAllowed is userLibraries.ts's sshAllowed: the account's ssh keys are
// lent only where nobody signs in.
func (s *Service) sshAllowed(viewer libraries.Viewer) bool {
	if s.a.Cfg.AuthEnabled() {
		return false
	}
	id := ""
	if viewer != nil {
		id = *viewer
	}
	return s.Reg.Users().OwnerOf(id) == "single"
}

// ownRootOf is a library's folder where its own location holds it: on
// this computer, or on its own host; "" when none is known.
func (s *Service) ownRootOf(l libraries.Library) string {
	if l.SourceRoot != "" {
		return l.SourceRoot
	}
	if l.Site == "" {
		return ""
	}
	t, err := s.Engine.Target(l.IndexRoot)
	if err != nil || t == nil {
		return ""
	}
	return t.SourceRoot
}

func (s *Service) cacheRootOf(l libraries.Library) string {
	if l.CacheRoot != "" {
		return l.CacheRoot
	}
	return filepath.Join(s.a.Cfg.IndexDir, "extract")
}

// failReason is why a read failed, in a few words and without this
// computer's paths.
func failReason(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return "no such file"
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.EPERM):
		return "permission denied"
	}
	msg := err.Error()
	var ae *apierr.Error
	if errors.As(err, &ae) {
		msg = ae.Message
	}
	msg, _, _ = strings.Cut(msg, "\n")
	return truncRunes(msg, 300)
}

// isMissing says whether a failure means the place was reached and does
// not have the file.
func isMissing(err error) bool {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return true
	}
	var ae *apierr.Error
	return errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 409)
}

func statusOf(err error) int {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

var trailingStop = regexp.MustCompile(`[.\s]+$`)

// without is a remote error without the "host: " it begins with, which
// where says, and without its last full stop.
func without(where, msg string) string {
	msg = strings.TrimPrefix(msg, where+": ")
	return trailingStop.ReplaceAllString(msg, "")
}

func (s *Service) readThroughSite(rt *libraries.ReadThrough) (string, error) {
	port := 0
	if rt.Port != nil {
		port = *rt.Port
	}
	return remote.SSHSite(rt.Destination, port, rt.SSHOptions)
}

// OpenOriginal is openOriginal: a library's original file, from its own
// location and, if that cannot give it, from the host the library names
// for reading originals. A failure that says the path itself is wrong (400)
// is final. When no place has the file, the error names each one tried.
func (s *Service) OpenOriginal(ctx context.Context, l libraries.Library, rel string, viewer libraries.Viewer) (Opened, error) {
	var tried []Attempt
	switch {
	case l.Site != "":
		where := l.Site
		f, err := s.readOwnRemote(ctx, l, rel)
		if err == nil {
			return Opened{f, where, "library"}, nil
		}
		if statusOf(err) == 400 {
			return Opened{}, err
		}
		tried = append(tried, Attempt{where, without(where, failReason(err)), isMissing(err)})
	case l.SourceRoot != "":
		f, err := kb.ReadFileContent(rel, l.SourceRoot, s.cacheRootOf(l))
		if err == nil {
			return Opened{f, "this computer", "library"}, nil
		}
		if statusOf(err) == 400 {
			return Opened{}, err
		}
		tried = append(tried, Attempt{"this computer", failReason(err), isMissing(err)})
	default:
		tried = append(tried, Attempt{"this computer", "this library has no files here", true})
	}
	if rt := l.ReadThrough; rt != nil {
		where := hostName(rt)
		f, err := func() (*kb.Obj, error) {
			if !s.sshAllowed(viewer) {
				return nil, apierr.New(403, ReadThroughSingleUser)
			}
			root := deref(rt.Root)
			if root == "" {
				root = s.ownRootOf(l)
			}
			if root == "" {
				return nil, apierr.New(409, "the library's folder on that host is not known: name it in Settings, Libraries")
			}
			site, err := s.readThroughSite(rt)
			if err != nil {
				return nil, err
			}
			return s.Engine.ReadRemoteFile(ctx, site, "", root, rel, readOriginalTimeout)
		}()
		if err == nil {
			return Opened{f, where, "read-through"}, nil
		}
		if statusOf(err) == 400 {
			return Opened{}, err
		}
		tried = append(tried, Attempt{where, without(where, failReason(err)), isMissing(err)})
	}
	return Opened{}, unreachable(tried, rel+" could not be opened. Tried ")
}

func unreachable(tried []Attempt, lead string) error {
	parts := make([]string, len(tried))
	missing := true
	for i, t := range tried {
		parts[i] = t.Where + ": " + t.Error
		missing = missing && t.Missing
	}
	status := 502
	if missing {
		status = 404
	}
	only := ""
	if strings.HasSuffix(lead, "opened. Tried ") && len(tried) == 1 {
		only = "only "
	}
	return &OriginalUnreachable{apierr.New(status, "%s", lead+only+strings.Join(parts, "; ")+"."), tried}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Service) readOwnRemote(ctx context.Context, l libraries.Library, rel string) (*kb.Obj, error) {
	t, err := s.Engine.Target(l.IndexRoot)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, apierr.New(404, "no such library")
	}
	return s.Engine.ReadRemoteFile(ctx, t.Site, t.Key, t.SourceRoot, rel, readOriginalTimeout)
}

// DownloadOriginal is downloadOriginal: a whole original for download,
// from the same places in the same order.
func (s *Service) DownloadOriginal(ctx context.Context, l libraries.Library, rel string, viewer libraries.Viewer) (string, []byte, error) {
	var tried []Attempt
	final := func(err error) bool { st := statusOf(err); return st == 400 || st == 413 }
	if l.Site != "" {
		name, body, err := s.Engine.RemoteDownload(ctx, l.IndexRoot, rel)
		if err == nil {
			return name, body, nil
		}
		if final(err) {
			return "", nil, err
		}
		tried = append(tried, Attempt{l.Site, without(l.Site, failReason(err)), isMissing(err)})
	} else {
		tried = append(tried, Attempt{"this computer", "this library has no files here", true})
	}
	if rt := l.ReadThrough; rt != nil {
		where := hostName(rt)
		name, body, err := func() (string, []byte, error) {
			if !s.sshAllowed(viewer) {
				return "", nil, apierr.New(403, ReadThroughSingleUser)
			}
			root := deref(rt.Root)
			if root == "" {
				root = s.ownRootOf(l)
			}
			if root == "" {
				return "", nil, apierr.New(409, "the library's folder on that host is not known: name it in Settings, Libraries")
			}
			site, err := s.readThroughSite(rt)
			if err != nil {
				return "", nil, err
			}
			return s.Engine.DownloadRemoteFile(ctx, site, "", root, rel)
		}()
		if err == nil {
			return name, body, nil
		}
		if final(err) {
			return "", nil, err
		}
		tried = append(tried, Attempt{where, without(where, failReason(err)), isMissing(err)})
	}
	return "", nil, unreachable(tried, rel+" could not be downloaded. Tried ")
}

// ---- the setting, and its connection test ----

type readThroughProbe struct {
	OK    bool                `json:"ok"`
	Steps []userlibs.HostStep `json:"steps"`
	Root  *string             `json:"root"`
}

// probeReadThrough is the connection test for the host: ssh reaches it and
// it runs a shell, and the library's folder there, the one given or the
// library's own path, is a directory the login can read.
func (s *Service) probeReadThrough(ctx context.Context, rt *libraries.ReadThrough, ownRoot string) readThroughProbe {
	port := 0
	if rt.Port != nil {
		port = *rt.Port
	}
	steps, run, ok, onlyQuery := userlibs.Of(s.a).ConnectSSH(ctx, rt.Destination, port, rt.SSHOptions)
	if !ok {
		// A login that runs only gufi_query reaches the host but opens no files.
		if n := len(steps); n > 0 && steps[n-1].Detail == onlyQuery {
			steps[n-1].Detail = rt.Destination + " runs only gufi_query for this login (no shell), so it cannot open files. Name a host whose login gives a shell, such as a file transfer host."
		}
		return readThroughProbe{Steps: steps}
	}
	root := deref(rt.Root)
	if root == "" {
		root = ownRoot
	}
	if root == "" {
		steps = append(steps, userlibs.HostStep{Name: "source", Detail: "The library has no folder of its own to look for there: give the folder on that host where its files are."})
		return readThroughProbe{Steps: steps}
	}
	r := run([]string{"sh", "-c", `if [ ! -d "$1" ]; then exit 3; elif [ ! -r "$1" ] || [ ! -x "$1" ]; then exit 4; fi; ls -A -- "$1" 2>/dev/null | head -n 1 | wc -l`, "_", root}, 45*time.Second)
	switch r.Code {
	case 3:
		steps = append(steps, userlibs.HostStep{Name: "source", Detail: root + " is not a directory on " + rt.Destination + ". Give the folder where this library's files are on that host."})
	case 4:
		steps = append(steps, userlibs.HostStep{Name: "source", Detail: "The login on " + rt.Destination + " cannot read " + root + "."})
	case 0:
		empty := strings.TrimSpace(r.Stdout) == "0"
		same := ""
		if deref(rt.Root) == "" {
			same = " (the same path as the library's own)"
		}
		note := ""
		if empty {
			note = ", but it is empty there: check that the file systems are mounted on that host"
		}
		steps = append(steps, userlibs.HostStep{Name: "source", OK: true, Warn: empty, Detail: root + " is readable on " + rt.Destination + same + note + "."})
	default:
		why := lastLine(r.Stderr)
		if why == "" {
			why = fmt.Sprintf("exit %d", r.Code)
		}
		steps = append(steps, userlibs.HostStep{Name: "source", Detail: root + " could not be checked on " + rt.Destination + ": " + why})
	}
	ok = true
	for _, st := range steps {
		ok = ok && st.OK
	}
	return readThroughProbe{OK: ok, Steps: steps, Root: &root}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// settable is the library a request names, checked for a read-through
// host: not the main folder, not one the deployment pinned, and not an
// index of a folder built on this computer.
func (s *Service) settable(r *http.Request) (libraries.Library, libraries.Viewer, error) {
	viewer, err := s.viewer(r)
	if err != nil {
		return libraries.Library{}, nil, err
	}
	if !s.sshAllowed(viewer) {
		return libraries.Library{}, nil, apierr.New(403, ReadThroughSingleUser)
	}
	l, err := s.Reg.Get(r.PathValue("id"), "", viewer)
	if err != nil {
		return libraries.Library{}, nil, err
	}
	switch {
	case l.Primary:
		return l, nil, apierr.New(409, "The main folder is on this computer: there is nothing to read through.")
	case l.Build != nil && l.Site == "":
		return l, nil, apierr.New(409, "%s is an index of a folder on this computer, kept in step with it; its files are here.", l.Label)
	case l.Pinned:
		return l, nil, apierr.New(403, "%s is set by the deployment: add readThrough to its entry in STUDIO_LIBRARIES and restart.", l.Label)
	}
	return l, viewer, nil
}

func (s *Service) readThroughTest(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	l, _, err := s.settable(r)
	if err != nil {
		return err
	}
	rt, err := libraries.ParseReadThrough(body.M())
	if err != nil {
		return err
	}
	if rt == nil {
		return apierr.New(400, "destination: user@host, or a Host alias from your ~/.ssh/config")
	}
	p := s.probeReadThrough(r.Context(), rt, s.ownRootOf(l))
	apierr.JSON(w, 200, struct {
		OK    bool                `json:"ok"`
		Steps []userlibs.HostStep `json:"steps"`
	}{p.OK, p.Steps})
	return nil
}

func (s *Service) readThroughSave(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	l, viewer, err := s.settable(r)
	if err != nil {
		return err
	}
	rt, err := libraries.ParseReadThrough(body.M())
	if err != nil {
		return err
	}
	var probe *readThroughProbe
	if rt != nil {
		p := s.probeReadThrough(r.Context(), rt, s.ownRootOf(l))
		if !p.OK {
			detail := ""
			for _, st := range p.Steps {
				if !st.OK {
					detail = st.Detail
					break
				}
			}
			return apierr.New(400, "%s", jsTrim("The connection test did not pass. "+detail))
		}
		probe = &p
	}
	id := r.PathValue("id")
	saved := false
	if l.Site != "" {
		owner := s.Reg.Users().OwnerOf(deref(viewer))
		saved = owner != "" && s.Reg.Users().Update(owner, id, func(d *libraries.UserDef) { d.ReadThrough = rt }) != nil
	} else {
		saved = s.Reg.SetAddedReadThrough(id, rt)
	}
	if !saved {
		return apierr.New(404, "no library named %s", id)
	}
	out := struct {
		ReadThrough *libraries.PublicReadThroughView `json:"readThrough"`
		Probe       *readThroughProbe                `json:"probe,omitempty"`
	}{libraries.PublicReadThrough(rt), probe}
	apierr.JSON(w, 200, out)
	return nil
}
