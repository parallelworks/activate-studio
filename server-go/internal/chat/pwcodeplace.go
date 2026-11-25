package chat

import (
	"net/http"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/folders"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/userlibs"
)

// New pw code chat here, in the Library: a chat whose session works in a
// library's folder, on the machine that holds it. The folder is found in
// the viewer's own libraries and looked at as them on its machine, when the
// Library offers it and again when the first message starts the session;
// what the page says of the machine and the folder is only compared. There
// is no other machine or folder it starts on instead.

// libraryStart is where a session started from the Library works.
type libraryStart struct {
	// machine is "" for the one the Studio drives for the viewer.
	machine, name, folder string
	// rel is the folder in its library, file the file asked for, if one.
	library, rel, file string
	claudeAllowed      any
	claudeInstalled    any
}

// libraryStart is where a session in the folder at path of the viewer's
// library named library starts, or why none does.
func (m *Module) libraryStart(r *http.Request, library, at string) (*libraryStart, error) {
	ctx := r.Context()
	if why := m.pwcodeUnavailable(ctx, r); why != "" {
		return nil, pwErr(409, why)
	}
	if kb.Default == nil {
		return nil, kb.ErrFolderNone()
	}
	f, err := kb.Default.FolderFor(r, library, at)
	if err != nil {
		return nil, err
	}
	out := &libraryStart{folder: f.Dir, library: f.Lib.ID, rel: f.Rel, file: f.File}
	home := userlibs.HomeCluster()
	switch {
	case f.Site == "" && !m.A.Cfg.AuthEnabled():
		return out, m.ownStart(r, out)
	case f.Site == "":
		if err := m.sameFolderThere(r, home, f); err != nil {
			return nil, err
		}
		return out, m.ownStart(r, out)
	// Named another way than the cluster the viewer's pw code runs on, it is
	// that cluster when the folder there is this one.
	case m.A.Cfg.AuthEnabled() && (f.Site == home || sameName(f.Site, home) && m.sameFolderThere(r, home, f) == nil):
		return out, m.ownStart(r, out)
	}
	return out, m.machineStart(r, f, out)
}

// ownStart: the session starts on the machine the Studio drives for the
// viewer, whose pw code answers.
func (m *Module) ownStart(r *http.Request, out *libraryStart) error {
	here, err := m.pwcodeHereCached(r.Context(), r)
	if err != nil {
		return pwErr(409, "pw code does not answer for you on this Studio's machine: "+firstLine(apiMessage(err), 300))
	}
	if st := jsonv.Obj(here.status); st != nil {
		if why := pwcode.UnusableReason(st); why != "" {
			return pwErr(409, why)
		}
	}
	mach := m.startMachine(r, here)
	out.machine, out.name = "", jsonv.String(jsonv.Path(mach, "name"))
	out.claudeAllowed, out.claudeInstalled = jsonv.Path(mach, "claudeCodeAllowed"), jsonv.Path(mach, "claudeInstalled")
	return nil
}

// sameFolderThere: the folder f is the one at its path on the cluster home,
// and the viewer's to write in there.
func (m *Module) sameFolderThere(r *http.Request, home string, f kb.FolderAt) error {
	key, name := m.viewerPlatformKey(r), agentSiteName(home)
	if key == "" {
		var err error
		if key, err = codesessions.ViewerKey(r, m.pf); err != nil {
			return err
		}
	}
	res, err := m.bridgeRun()(r.Context(), home, key, []string{"sh", "-c", folders.AccessScript, "studio-access", f.Root, f.Rel}, 30*time.Second)
	if err != nil {
		return pwErr(502, "The Studio could not reach "+name+" as you: "+firstLine(apiMessage(err), 300))
	}
	a, ok := folders.ParseAccess(res.Stdout)
	switch {
	case !ok || a.File:
		return pwErr(409, "Your account on "+name+" cannot open the folder of "+f.Lib.Label+".")
	case f.Ino == "" || a.Ino != f.Ino:
		return pwErr(409, "The folder of "+f.Lib.Label+" on this Studio is not the one at that path on "+name+".")
	case !a.Write:
		return kb.ErrFolderReadOnly("Your account on " + name + " cannot write in this folder")
	}
	return nil
}

// sameName: a and b may name one system. pw://<owner>/<name> names one
// person's, so two of one name and two owners do not.
func sameName(a, b string) bool {
	ao, an := siteParts(a)
	bo, bn := siteParts(b)
	return an != "" && an == bn && (ao == "" || bo == "" || ao == bo)
}

func siteParts(site string) (owner, name string) {
	site = strings.TrimSuffix(strings.TrimPrefix(site, "pw://"), "/")
	if i := strings.LastIndex(site, "/"); i >= 0 {
		return site[:i], site[i+1:]
	}
	return "", site
}

// machineStart: the session starts on the viewer's pw code machine that is
// the system f is on, through ACTIVATE.
func (m *Module) machineStart(r *http.Request, f kb.FolderAt, out *libraryStart) error {
	name := remote.ShortName(f.Site)
	if f.SSH {
		if f.Daemon == "" {
			return pwErr(409, name+" runs no pw code for your account now. Start it there (pw code, then /rc), then try again.")
		}
		if here, err := m.pwcodeHereCached(r.Context(), r); err == nil && here.daemonID == f.Daemon {
			return m.ownStart(r, out)
		}
	}
	if why := m.relayUnavailable(r); why != "" {
		return pwErr(409, why)
	}
	ms, _, err := codesessions.PlatformListing(r, m.pf)
	if err != nil {
		return pwErr(502, "ACTIVATE did not list your pw code machines: "+firstLine(apiMessage(err), 300))
	}
	var on []*jsonv.Object
	for _, mach := range ms {
		if is, err := m.machineIs(r, mach, f); err != nil {
			return err
		} else if is {
			on = append(on, mach)
		}
	}
	// One cluster may be listed by more than one daemon: the connected one.
	if len(on) > 1 {
		var up []*jsonv.Object
		for _, mach := range on {
			if jsonv.String(jsonv.Path(mach, "status")) == "connected" {
				up = append(up, mach)
			}
		}
		if len(up) > 0 {
			on = up
		}
	}
	switch {
	case len(on) == 0:
		return pwErr(409, name+" has no pw code connected to ACTIVATE as you. Start it there (pw code, then /rc), then try again.")
	case len(on) > 1:
		return pwErr(409, "ACTIVATE lists more than one pw code machine named "+name+" to you, and the Studio does not guess which one this library is on.")
	}
	mach := on[0]
	if here, err := m.pwcodeHereCached(r.Context(), r); err == nil && m.startIsOwn(mach, here) {
		return m.ownStart(r, out)
	}
	if started, _ := jsonv.Path(mach, "remoteStart").(bool); jsonv.String(jsonv.Path(mach, "status")) != "connected" || !started {
		return pwErr(409, name+" does not take new sessions from ACTIVATE now. Start pw code there, then try again.")
	}
	shown := codesessions.ClaudeCodeShown(r, mach)
	out.machine, out.name = jsonv.String(jsonv.Path(mach, "id")), firstNonEmpty(jsonv.String(jsonv.Path(mach, "name")), name)
	out.claudeAllowed, out.claudeInstalled = jsonv.Path(shown, "claudeCodeAllowed"), jsonv.Path(shown, "claudeInstalled")
	if !pwcode.IsRouteID(out.machine) {
		return pwErr(409, "The Studio cannot name the pw code machine on "+name+".")
	}
	return nil
}

// machineIs: the platform's machine is the system f is on. A computer that
// connected itself has no address to tell it by, whatever its name, unless
// the host itself said which daemon runs there.
func (m *Module) machineIs(r *http.Request, mach *jsonv.Object, f kb.FolderAt) (bool, error) {
	if f.SSH {
		return jsonv.String(jsonv.Path(mach, "daemonId")) == f.Daemon, nil
	}
	b := machOf(mach)
	owner, name := siteParts(f.Site)
	if b.Kind == "local" || b.Name == "" || b.Name != name {
		return false, nil
	}
	// The listing names no owner for a cluster that is not of the platform's
	// making: the viewer's own cluster listing does.
	if b.Owner == "" {
		key, err := codesessions.ViewerKey(r, m.pf)
		if err != nil {
			return false, err
		}
		cs, err := m.bridgeClusters()(r, key)
		if err != nil {
			return false, pwErr(502, firstLine(apiMessage(err), 300)+", so the Studio cannot tell which pw code machine this library is on.")
		}
		owners := map[string]bool{}
		for _, c := range cs {
			if c.Name != name {
				continue
			}
			o := c.User
			if o == "" {
				o, _ = siteParts(c.URI)
			}
			b.Owner = o
			owners[o] = true
		}
		if len(owners) != 1 || b.Owner == "" {
			return false, nil
		}
	}
	if owner != "" {
		return b.Owner == owner, nil
	}
	// Reached by its name alone, it is this machine when the folder is there.
	return m.sameFolderThere(r, "pw://"+b.Owner+"/"+name, f) == nil, nil
}

// pickInLibrary is p for a chat started from the Library: the machine and
// folder found now, as the viewer, which must be the ones the page showed.
func (m *Module) pickInLibrary(r *http.Request, p startPick, library, at string) (startPick, error) {
	s, err := m.libraryStart(r, library, at)
	if err != nil {
		return startPick{}, err
	}
	if p.machine != s.machine || p.folder != s.folder {
		return startPick{}, pwErr(409, "This folder is on "+firstNonEmpty(s.name, "another machine")+" at "+s.folder+", which is not where the chat was set to start. Start it again from the Library.")
	}
	return p, nil
}

// startInLibrary is GET /api/chat/pwcode/start/library?library=&path=: where
// a chat started at a folder of the Library works, or why it cannot.
func (m *Module) startInLibrary(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	if !q.Has("path") {
		return writeProblem(w, http.StatusBadRequest, "path: a file or folder in the library")
	}
	s, err := m.libraryStart(r, strings.TrimSpace(firstNonEmpty(q.Get("library"), libraries.PrimaryID)), q.Get("path"))
	if err != nil {
		return writeErr(w, err)
	}
	return reqbody.OK(w, reqbody.O("machine", firstNonEmpty(s.machine, directMachine), "machineName", s.name, "folder", s.folder,
		"library", s.library, "path", s.rel, "file", s.file,
		"claudeCodeAllowed", s.claudeAllowed, "claudeInstalled", s.claudeInstalled))
}
