package transfers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// loc is a location with what reaches it.
type loc struct {
	Location
	fs    FS
	libID string
	// readOnly is why a reachable library is read only; refresh is when
	// its index shows a transfer's change (LibraryLoc).
	readOnly, refresh string
	conduit           *ConduitServer
	// home is the folder browsing starts in, relative to the root (a
	// cluster's home folder); "" for the root.
	home    string
	cluster string
}

const (
	clusterTTL = 30 * time.Second
	homeTTL    = 10 * time.Minute
	probeWait  = 10 * time.Second
)

// Locations is every location the viewer may use, in kind order.
func (s *Service) Locations(ctx context.Context, v Viewer) []Location {
	ls := s.all(ctx, v, "")
	out := make([]Location, len(ls))
	for i, l := range ls {
		out[i] = l.Location
	}
	return out
}

// resolve is one location by id, for the viewer; 404 for an id the viewer
// has no location by.
func (s *Service) resolve(ctx context.Context, v Viewer, id string) (*loc, error) {
	kind, _, ok := strings.Cut(id, ":")
	if !ok {
		return nil, apierr.New(400, "%q is not a location id", short(id))
	}
	for _, l := range s.all(ctx, v, kind) {
		if l.ID == id {
			return l, nil
		}
	}
	return nil, apierr.New(404, "no location %q", short(id))
}

// all builds the viewer's locations; only of one kind when kind is set.
func (s *Service) all(ctx context.Context, v Viewer, kind string) []*loc {
	var out []*loc
	want := func(k string) bool { return kind == "" || kind == k }
	if want(KindLibrary) && s.d.Libraries != nil {
		for _, l := range s.d.Libraries(ctx, v) {
			x := &loc{Location: Location{ID: KindLibrary + ":" + l.ID, Kind: KindLibrary, Label: l.Label,
				Writable: l.Writable && l.FS != nil, Indexed: l.Indexed, Browsable: l.FS != nil, Available: l.FS != nil, Why: l.Why},
				fs: l.FS, libID: l.ID, refresh: l.Refresh}
			if l.FS != nil {
				x.Detail = l.FS.Where() + ", " + l.FS.Root()
				if !x.Writable {
					x.readOnly = l.ReadOnly
					if x.readOnly != "" {
						x.Detail += "; " + x.readOnly
					}
				}
			} else {
				x.Detail = "files not reachable from Studio"
			}
			out = append(out, x)
		}
	}
	if want(KindComputer) {
		out = append(out, s.computer(v)...)
	}
	if want(KindCluster) {
		out = append(out, s.clusterLocs(ctx, v)...)
	}
	set := s.settingsFor(v)
	if want(KindSaved) && !v.AuthOn {
		var saved []*loc
		for _, sl := range set.SavedLocations {
			x := &loc{Location: Location{ID: KindSaved + ":" + sl.ID, Kind: KindSaved, Label: sl.Label, Browsable: true, Writable: true}}
			if site, err := sl.site(); err != nil {
				x.Why = messageOf(err)
				x.Browsable, x.Writable = false, false
			} else {
				x.Detail = remote.SiteName(site) + ", " + sl.Root
				x.fs = &RemoteFS{R: s.d.Runner, SiteID: site, Dir: sl.Root, Name: remote.SiteName(site), RsyncPath: sl.RsyncPath}
				x.Available = true
			}
			saved = append(saved, x)
		}
		s.probe(ctx, saved)
		out = append(out, saved...)
	}
	if want(KindHost) && !v.AuthOn && set.HostLocations.Enabled {
		out = append(out, s.hostLocs(ctx, v, set)...)
	}
	if want(KindRclone) && !v.AuthOn {
		out = append(out, s.rcloneLocs(ctx, v, set)...)
	}
	if cs, on := set.conduit(); want(KindConduit) && !v.AuthOn && s.d.Conduit != nil && on {
		x := &loc{Location: Location{ID: KindConduit + ":" + cs.Server, Kind: KindConduit, Label: cs.Label,
			Detail: "paths in the service's namespace, typed by hand", Writable: true}, conduit: &cs}
		if ok, why := s.d.Conduit.Ready(ctx, cs.Server); ok {
			x.Available = true
		} else {
			x.Why = why
		}
		out = append(out, x)
	}
	return out
}

// libraryHosts is each host a library's files are reached on (its own
// host, or the host its originals are read through), once each, with the
// key that reaches it.
func (s *Service) libraryHosts(ctx context.Context, v Viewer) []*RemoteFS {
	if s.d.Libraries == nil {
		return nil
	}
	var out []*RemoteFS
	seen := map[string]bool{}
	for _, l := range s.d.Libraries(ctx, v) {
		r, ok := l.FS.(*RemoteFS)
		if !ok || seen[r.SiteID] {
			continue
		}
		seen[r.SiteID] = true
		out = append(out, r)
	}
	return out
}

// hostLocs is the home folder of each library host, not hidden in the
// settings, browsable and writable as the account that reaches it.
func (s *Service) hostLocs(ctx context.Context, v Viewer, set Settings) []*loc {
	hidden := map[string]bool{}
	for _, h := range set.HostLocations.Hidden {
		hidden[h] = true
	}
	var out []*loc
	used := map[string]bool{}
	for _, r := range s.libraryHosts(ctx, v) {
		name := remote.SiteName(r.SiteID)
		if hidden[name] {
			continue
		}
		id := slugID(name)
		for n := 2; used[id]; n++ {
			id = slugID(name) + "-" + itoa(n)
		}
		used[id] = true
		x := &loc{Location: Location{ID: KindHost + ":" + id, Kind: KindHost, Label: name + " home"}}
		x.fs = &RemoteFS{R: s.d.Runner, SiteID: r.SiteID, KeyText: r.KeyText, Name: name}
		out = append(out, x)
	}
	var wg sync.WaitGroup
	for _, l := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, probeWait)
			defer cancel()
			r := l.fs.(*RemoteFS)
			h, err := s.homeAbs(c, r.SiteID, r.KeyText)
			if err != nil {
				l.Why, l.fs = messageOf(err), nil
				l.Detail = r.Name + ", home folder"
				return
			}
			r.Dir = h
			l.Detail = r.Name + ", " + h
			l.Available, l.Browsable, l.Writable = true, true, true
		}()
	}
	wg.Wait()
	return out
}

// probe marks a location unavailable when its folder cannot be listed in
// a few seconds, all at once.
func (s *Service) probe(ctx context.Context, ls []*loc) {
	var wg sync.WaitGroup
	for _, l := range ls {
		if l.fs == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, probeWait)
			defer cancel()
			if _, _, err := l.fs.List(c, "", 1); err != nil {
				l.Available, l.Why = false, messageOf(err)
			}
		}()
	}
	wg.Wait()
}

// computer is the folders on this computer the viewer may use: with
// sign-in off Downloads, Desktop and Documents (those there and switched
// on), STUDIO_TRANSFER_FOLDERS and the folders added in the settings
// (switched on); with it on, STUDIO_TRANSFER_FOLDERS only.
func (s *Service) computer(v Viewer) []*loc {
	set := s.settingsFor(v)
	type folder struct {
		path, label string
		builtIn     bool
	}
	var dirs []folder
	if !v.AuthOn {
		if h := s.d.Home; h != "" {
			for _, n := range BuiltInFolders {
				if set.builtInOn(n) {
					dirs = append(dirs, folder{filepath.Join(h, n), n, true})
				}
			}
		}
		for _, d := range envFolders(s.d.Getenv("STUDIO_TRANSFER_FOLDERS")) {
			dirs = append(dirs, folder{d, filepath.Base(d), false})
		}
	}
	for _, f := range set.ComputerFolders {
		if !f.BuiltIn && f.Enabled {
			dirs = append(dirs, folder{f.Path, f.Label, false})
		}
	}
	var out []*loc
	used := map[string]bool{}
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d.path] {
			continue
		}
		seen[d.path] = true
		name := filepath.Base(d.path)
		id := name
		for n := 2; used[id]; n++ {
			id = name + "-" + itoa(n)
		}
		used[id] = true
		label := d.label
		if label == "" {
			label = name
		}
		x := &loc{Location: Location{ID: KindComputer + ":" + id, Kind: KindComputer, Label: label, Detail: "on this computer, " + d.path}}
		if why := s.refusedFolder(d.path); why != "" {
			x.Why = why
		} else if fi, err := os.Stat(d.path); err != nil || !fi.IsDir() {
			x.Why = "the folder is not there"
		} else {
			x.Available, x.Browsable, x.Writable = true, true, true
			x.fs = &LocalFS{Dir: d.path}
		}
		// Built-in folders that do not exist are left out.
		if d.builtIn && !x.Available && x.Why == "the folder is not there" {
			continue
		}
		out = append(out, x)
	}
	return out
}

// refusedFolder is why a folder may not be a computer location: the whole
// file system, the home folder itself, or a folder holding the Studio's
// own data.
func (s *Service) refusedFolder(d string) string {
	c := filepath.Clean(d)
	if c == string(filepath.Separator) || filepath.Dir(c) == c {
		return "the whole file system is not offered; name a folder"
	}
	if h := homeDir(); h != "" && filepath.Clean(h) == c {
		return "the home folder itself is not offered; name a folder in it"
	}
	if s.d.IndexBase != "" {
		ib := filepath.Clean(s.d.IndexBase)
		if ib == c || strings.HasPrefix(ib, c+string(filepath.Separator)) {
			return "it holds the Studio's own data"
		}
	}
	return ""
}

// clusterLocs is the viewer's active clusters, rooted at / with browsing
// starting in the home folder.
func (s *Service) clusterLocs(ctx context.Context, v Viewer) []*loc {
	if s.d.Clusters == nil {
		return nil
	}
	if v.Key == "" {
		return nil
	}
	names, err := s.clusterNames(ctx, v)
	if err != nil {
		s.d.Log.Warn("transfers: listing clusters", "error", messageOf(err))
		return nil
	}
	var out []*loc
	for _, n := range names {
		x := &loc{Location: Location{ID: KindCluster + ":" + n, Kind: KindCluster, Label: n, Detail: n + " over pw ssh, as your account",
			Writable: true, Browsable: true, Available: true}, cluster: n}
		x.fs = &RemoteFS{R: s.d.Runner, SiteID: n, KeyText: v.Key, Dir: "/", Name: n}
		out = append(out, x)
	}
	return out
}

func (s *Service) clusterNames(ctx context.Context, v Viewer) ([]string, error) {
	k := keyTag(v.Key)
	s.mu.Lock()
	e, ok := s.clusters[k]
	s.mu.Unlock()
	if ok && s.now().Sub(e.at) < clusterTTL {
		return e.names, e.err
	}
	names, err := s.d.Clusters(ctx, v)
	s.mu.Lock()
	s.clusters[k] = clusterEntry{s.now(), names, err}
	s.mu.Unlock()
	return names, err
}

// keyTag tells keys apart in a cache without keeping the key itself.
func keyTag(key string) string {
	if len(key) > 8 {
		return key[len(key)-8:] + itoa(len(key))
	}
	return key
}

// homeOf is a cluster location's home folder relative to /.
func (s *Service) homeOf(ctx context.Context, l *loc) (string, error) {
	r, ok := l.fs.(*RemoteFS)
	if !ok {
		return "", nil
	}
	h, err := s.homeAbs(ctx, r.SiteID, r.KeyText)
	return strings.Trim(h, "/"), err
}

// homeAbs is a host's home folder, kept 10 minutes once found.
func (s *Service) homeAbs(ctx context.Context, site, key string) (string, error) {
	k := site + "|" + keyTag(key)
	s.mu.Lock()
	e, hit := s.homes[k]
	s.mu.Unlock()
	if hit && e.err == nil && s.now().Sub(e.at) < homeTTL {
		return e.home, nil
	}
	h, err := Home(ctx, s.d.Runner, site, key)
	s.mu.Lock()
	s.homes[k] = homeEntry{s.now(), h, err}
	s.mu.Unlock()
	return h, err
}

// Browse lists a folder of a location. path nil starts where the location
// starts: the root, or a cluster's home folder.
func (s *Service) Browse(ctx context.Context, v Viewer, id string, path *string) (*Listing, error) {
	l, err := s.resolve(ctx, v, id)
	if err != nil {
		return nil, err
	}
	if !l.Browsable || l.fs == nil {
		why := l.Why
		if why == "" {
			why = "this location cannot be browsed; type the paths"
		}
		return nil, apierr.New(400, "%s", why)
	}
	var rel string
	if path == nil && l.Kind == KindCluster {
		if rel, err = s.homeOf(ctx, l); err != nil {
			return nil, apierr.New(502, "%s", messageOf(err))
		}
	} else if path != nil {
		if rel, err = CleanRel(*path); err != nil {
			return nil, err
		}
	}
	es, more, err := l.fs.List(ctx, rel, MaxBrowse)
	if err != nil {
		return nil, err
	}
	out := &Listing{Location: id, Path: rel, Entries: es, Truncated: more}
	if rel != "" {
		p := parentOf(rel)
		out.Parent = &p
	}
	return out, nil
}
