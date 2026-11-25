package libapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// Copy libraries (docs/LIBRARIES.md, Copies of a location): a folder in a
// Transfers location (an SFTP or ssh host, an rclone remote, a cluster) is
// copied into the library's own folder in the Studio's data and built as a
// folder on this computer is, so search, the Library tree, previews and the
// assistant read it as they read any built library. Transfers brings the
// copy in step (transfers.Pull) when the library is built, on its interval
// and when asked; the library's sync then indexes what changed. The copy is
// read only: a transfer into it would be removed by the next look.

const singleUserCopy = "Copies of a location are kept only on a Studio with sign-in off."

// copyKinds are the locations a copy library may come from: those Studio
// reaches with an account's own access. Libraries and this computer's
// folders are built in place, and CONDUIT paths cannot be listed.
var copyKinds = []string{transfers.KindSaved, transfers.KindHost, transfers.KindCluster, transfers.KindRclone}

// copyTick is how often the background loop sees which copies are due.
var copyTick = time.Minute

// copyLook is the look Update now asks for (a variable for the tests).
var copyLook = (*builds).update

// copies is what runs for copy libraries: one look at a time per library,
// the looks asked for or running, and the transfer of a look that has one.
type copies struct {
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	running map[string]string
	looking map[string]int
}

// look counts a look for changes asked for or running (d 1) and one that
// has ended (d -1). Update now counts its look before it answers, so the
// answer and every read after it say a look is on until it ends.
func (c *copies) look(id string, d int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.looking == nil {
		c.looking = map[string]int{}
	}
	if c.looking[id] += d; c.looking[id] <= 0 {
		delete(c.looking, id)
	}
}

func (c *copies) lockOf(id string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	m := c.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		c.locks[id] = m
	}
	return m
}

func (c *copies) setRunning(id, transfer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running == nil {
		c.running = map[string]string{}
	}
	if transfer == "" {
		delete(c.running, id)
	} else {
		c.running[id] = transfer
	}
}

func (c *copies) runningOf(id string) (bool, *string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.running[id]; ok {
		return true, &t
	}
	return c.looking[id] > 0, nil
}

// copyWhere names a copy's source for messages: the location's label and
// the folder in it.
func copyWhere(c *libraries.CopyOf) string {
	where := c.Label
	if where == "" {
		where = c.Location
	}
	if c.Path != "" {
		where += ", " + c.Path
	}
	return where
}

// pull brings a copy library's folder in step with its location and
// records what it found. With wait it takes its turn behind a look that is
// running (a build); without, it returns ran false when one is running.
func (b *builds) pull(ctx context.Context, id string, wait bool, progress progressFn) (res transfers.PullResult, ran bool, err error) {
	d := b.s.Reg.AddedDef(id)
	if d == nil || d.CopyOf == nil || d.SourceRoot == nil {
		return res, false, fmt.Errorf("%s is not a copy of a location", id)
	}
	m := b.copies.lockOf(id)
	if wait {
		m.Lock()
	} else if !m.TryLock() {
		return res, false, nil
	}
	defer m.Unlock()
	defer b.copies.setRunning(id, "")
	c := d.CopyOf
	err = func() error {
		xs := transfers.Of(b.s.a)
		v, ok := xs.OwnerViewer()
		if !ok {
			return errors.New(singleUserCopy)
		}
		if err := os.MkdirAll(*d.SourceRoot, 0o755); err != nil {
			return err
		}
		marked := false
		var e error
		res, e = xs.Pull(ctx, v, transfers.PullRequest{Location: c.Location, Path: c.Path, LibraryID: id, Label: d.Label, Dir: *d.SourceRoot,
			Progress: func(t transfers.Transfer) {
				b.copies.setRunning(id, t.ID)
				// A look that changes something shows as an update, and the
				// library cannot be removed while files land in its folder.
				if !marked && !wait {
					marked = true
					b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State = "updating" })
				}
				if progress != nil {
					cur := fmt.Sprintf("%s of %s files", enUS(int(t.Files.Done)), enUS(int(max(t.Files.Total, 0))))
					progress("Copying from "+copyWhere(c), int(t.Files.Done), &cur)
				}
			}})
		if marked && !b.stopping.Load() {
			b.s.Reg.UpdateBuild(id, func(lb *libraries.LocalBuild) { lb.State = "ready" })
		}
		if e != nil {
			var ae *apierr.Error
			if errors.As(e, &ae) && ae.Status == 404 && strings.HasPrefix(ae.Message, "no location") {
				return fmt.Errorf("its location, %s, is no longer in Transfers", copyWhere(c))
			}
		}
		return e
	}()
	now := isoNow()
	st := b.s.Reg.CopyState(id)
	st.CheckedAt = &now
	if err != nil {
		msg := truncRunes(libraries.PlainBuildError(err.Error()), 400)
		st.Error = &msg
	} else {
		st.Error = nil
		if res.Transfer != "" {
			t := res.Transfer
			st.ChangedAt, st.Transfer, st.Files, st.Deleted = &now, &t, res.Files, res.Deleted
		}
	}
	if werr := b.s.Reg.WriteCopyState(id, st); werr != nil {
		b.log(fmt.Sprintf("library %s: recording its look for changes: %s", id, werr))
	}
	return res, true, err
}

// update looks for changes to a copy library's location now, and indexes
// what the look brought. Nothing when a look or a build is running.
func (b *builds) update(ctx context.Context, id string) {
	b.copies.look(id, 1)
	defer b.copies.look(id, -1)
	if b.jobOf(id) != nil {
		return
	}
	res, ran, err := b.pull(ctx, id, false, nil)
	switch {
	case !ran:
	case err != nil:
		b.log(fmt.Sprintf("library %s: looking for changes at its location failed: %s", id, err))
	case res.Transfer != "":
		b.log(fmt.Sprintf("library %s: brought %d files from its location and removed %d", id, res.Files, res.Deleted))
		if _, err := b.sync(id); err != nil {
			b.log(fmt.Sprintf("library %s: sync failed: %s", id, err))
		}
	}
}

// copyDue is whether a copy library's next look is due at now.
func copyDue(c *libraries.CopyOf, st libraries.CopyState, now time.Time) bool {
	if c.Every <= 0 {
		return false
	}
	if st.CheckedAt == nil {
		return true
	}
	at, err := time.Parse(time.RFC3339Nano, *st.CheckedAt)
	return err != nil || !now.Before(at.Add(time.Duration(c.Every)*time.Minute))
}

// startCopies looks at each copy library's location on its interval, each
// library on its own, while the server runs.
func (b *builds) startCopies(ctx context.Context) {
	t := time.NewTicker(copyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		for _, id := range b.builtIDs() {
			d := b.s.Reg.AddedDef(id)
			if d == nil || d.CopyOf == nil || d.Build == nil || d.Build.State != "ready" || !copyDue(d.CopyOf, b.s.Reg.CopyState(id), now) {
				continue
			}
			go b.update(ctx, id)
		}
	}
}

// ---- routes ----

// copyAdd adds a copy library and starts its first build, which copies the
// folder and then indexes it.
func (s *Service) copyAdd(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserCopy)
	}
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	if !s.gufiTools() {
		return apierr.New(400, "GUFI is not installed here: there is no gufi_dir2index in %s.", s.a.Cfg.GufiBin)
	}
	locID := jsTrim(body.Str("location"))
	kind, _, _ := strings.Cut(locID, ":")
	if !slices.Contains(copyKinds, kind) {
		return apierr.New(400, "location: a saved location, a host, a cluster or an rclone remote from Transfers")
	}
	path, err := transfers.CleanRel(body.Str("path"))
	if err != nil {
		return err
	}
	every := libraries.DefaultCopyEvery
	if body.Has("every") {
		n, ok := body.Get("every").(float64)
		if !ok || !slices.Contains(libraries.CopyEvery, int(n)) || float64(int(n)) != n {
			return apierr.New(400, "every: %s minutes", joinInts(libraries.CopyEvery))
		}
		every = int(n)
	}
	if body.Has("meaning") && !index.IsMeaningLevel(body.Get("meaning")) {
		return apierr.New(400, "meaning: off, documents or full")
	}
	if body.Has("depth") && !index.IsDepth(body.Get("depth")) {
		return apierr.New(400, "depth: names, text, ocr, meaning-documents or meaning-full")
	}
	xs := transfers.Of(s.a)
	v, ok := xs.OwnerViewer()
	if !ok {
		return apierr.New(403, singleUserCopy)
	}
	var where *transfers.Location
	for _, l := range xs.Locations(r.Context(), v) {
		if l.ID == locID {
			where = &l
			break
		}
	}
	if where == nil {
		return apierr.New(400, "location: %s is not in Transfers", locID)
	}
	if !where.Available {
		return apierr.New(502, "%s cannot be reached: %s", where.Label, where.Why)
	}
	// The folder must be there and listable before anything is kept.
	if _, err := xs.Browse(r.Context(), v, locID, &path); err != nil {
		return err
	}
	label := jsTrim(body.Str("label"))
	if label == "" {
		label = where.Label
		if path != "" {
			label = filepath.Base(path)
		}
	}
	id := strings.ToLower(jsTrim(body.Str("id")))
	if id == "" {
		id = s.Reg.IDFor(label, where.Label+"/"+path)
	}
	if err := s.Reg.CheckNewID(id); err != nil {
		return err
	}
	own := s.Reg.BuiltLibraryDir(id)
	files := filepath.Join(own, "files")
	if err := os.MkdirAll(files, 0o755); err != nil {
		return err
	}
	indexDir := filepath.Join(own, "index")
	if _, err := s.Reg.AddBuilt(libraries.Def{
		ID: id, Label: label, IndexRoot: filepath.Join(indexDir, "files"), SourceRoot: &files,
		Build:  &libraries.LocalBuild{IndexDir: indexDir, CacheRoot: filepath.Join(own, "extract"), Managed: true, State: "building"},
		CopyOf: &libraries.CopyOf{Location: locID, Path: path, Label: where.Label, Every: every},
	}); err != nil {
		return err
	}
	set := map[string]any{"depth": string(index.NewLibraryDepth)}
	if body.Has("depth") {
		set = map[string]any{"depth": body.Get("depth")}
	} else if body.Has("meaning") {
		set = map[string]any{"meaning": body.Get("meaning")}
	}
	if _, err := s.ix().Settings.Save(id, set); err != nil {
		return err
	}
	job, err := s.builds.start(id)
	if err != nil {
		return err
	}
	lib, err := s.settingsLibrary(id)
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, libraryAndJob{lib, job})
	return nil
}

// copyUpdate looks for changes to a copy library's location now, in the
// background, and returns the library as Settings shows it.
func (s *Service) copyUpdate(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserCopy)
	}
	id := r.PathValue("id")
	d := s.Reg.AddedDef(id)
	if d == nil || d.CopyOf == nil {
		return apierr.New(404, "%s is not a copy of a location", id)
	}
	if d.Build == nil || d.Build.State != "ready" || s.builds.jobOf(id) != nil {
		return apierr.New(409, "%s is being built; it looks for changes when the build has finished", d.Label)
	}
	s.builds.copies.look(id, 1)
	go func() {
		defer s.builds.copies.look(id, -1)
		copyLook(s.builds, s.builds.ctx, id)
	}()
	lib, err := s.settingsLibrary(id)
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, struct {
		Library libraries.SettingsLibrary `json:"library"`
		Started bool                      `json:"started"`
	}{lib, true})
	return nil
}

// copySettings changes how often Studio looks for changes to a copy
// library's location.
func (s *Service) copySettings(w http.ResponseWriter, r *http.Request) error {
	if !s.localBuildOffered(r) {
		return apierr.New(403, singleUserCopy)
	}
	id := r.PathValue("id")
	d := s.Reg.AddedDef(id)
	if d == nil || d.CopyOf == nil {
		return apierr.New(404, "%s is not a copy of a location", id)
	}
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	n, ok := body.Get("every").(float64)
	if !ok || !slices.Contains(libraries.CopyEvery, int(n)) || float64(int(n)) != n {
		return apierr.New(400, "every: %s minutes", joinInts(libraries.CopyEvery))
	}
	s.Reg.SetCopyEvery(id, int(n))
	lib, err := s.settingsLibrary(id)
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, struct {
		Library libraries.SettingsLibrary `json:"library"`
	}{lib})
	return nil
}

func joinInts(a []int) string {
	s := make([]string, len(a))
	for i, n := range a {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, ", ")
}
