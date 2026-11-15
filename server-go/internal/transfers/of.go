package transfers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/mcpclient"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

var (
	ofMu sync.Mutex
	ofs  = map[*app.App]*Service{}
)

// Of is a's one transfer service, made on first use, for the routes and
// for the chat tools (Locations, Browse, Plan, Hold, Approve, Deny, Start,
// Get, List, Cancel, OnEnd).
func Of(a *app.App) *Service {
	ofMu.Lock()
	defer ofMu.Unlock()
	if s := ofs[a]; s != nil {
		return s
	}
	pool, _ := libraries.SharedRemote(a)
	p := platform.Of(a)
	mcp := mcpclient.New(func() []json.RawMessage { return a.Settings.Effective().McpServers })
	runner := PoolRunner{pool}
	s := New(Deps{
		IndexBase: a.Cfg.IndexBase, Getenv: os.Getenv, Log: a.Log,
		Runner:    runner,
		Shell:     runner.RemoteShell,
		Libraries: func(ctx context.Context, v Viewer) []LibraryLoc { return libraryLocs(a, runner, v) },
		Clusters: func(ctx context.Context, v Viewer) ([]string, error) {
			sites, err := (&agentsites.Sites{P: p}).ListSites(ctx, v.Key, v.AuthOn)
			if err != nil {
				return nil, err
			}
			var out []string
			for _, o := range sites {
				if k, _ := o.Get("kind"); k == "cluster" {
					if n, ok := o.Get("name"); ok {
						out = append(out, jsonv.String(n))
					}
				}
			}
			return out, nil
		},
		Settings: FileSettings{a.Settings},
		Index:    kbIndex{a},
		Conduit:  MCPConduit{mcp},
		Home:     homeDir(),
	})
	s.viewerFor = func(owner string) Viewer {
		auth := a.Cfg.AuthEnabled()
		key := ""
		if !auth {
			key = (&agentsites.Sites{P: p}).AgentKey("", false)
		} else if a.Providers.PersonalCred != nil {
			if k, base, ok := a.Providers.PersonalCred(owner, ""); ok && base == "" {
				key = k
			}
		}
		return Viewer{ID: owner, AuthOn: auth, Key: key}
	}
	ofs[a] = s
	return s
}

// ViewerOf is the request's viewer: the signed-in user (401 when sign-in
// is on and nobody is), with the platform key their clusters are reached
// with.
func ViewerOf(a *app.App, r *http.Request) (Viewer, error) {
	p := platform.Of(a)
	v, err := p.ViewerOf(r)
	if err != nil {
		var ne *app.NodeError
		if errors.As(err, &ne) && ne.Status == http.StatusUnauthorized {
			return Viewer{}, apierr.New(401, "authentication required")
		}
		return Viewer{}, err
	}
	if v.AuthEnabled && v.UserID() == "" {
		return Viewer{}, apierr.New(401, "authentication required")
	}
	own := ""
	if v.UserID() != "" || !v.AuthEnabled {
		own = v.PlatformKey()
	}
	return Viewer{ID: v.UserID(), AuthOn: v.AuthEnabled, Key: (&agentsites.Sites{P: p}).AgentKey(own, v.AuthEnabled)}, nil
}

// libViewer is the viewer as the library registry takes it.
func libViewer(v Viewer) libraries.Viewer {
	if v.AuthOn && v.ID == "" {
		return libraries.Deployment
	}
	return libraries.ViewerOf(v.ID)
}

type viewerKeys struct{ key string }

func (k viewerKeys) KeyOf(string) string { return k.key }

// readThroughOff is why a library's originals host is not used with
// sign-in on (libapi's ReadThroughSingleUser, in this page's words).
const readThroughOff = "its files are read through another host with the ssh keys of the account the Studio runs as, which a Studio with sign-in on does not lend"

// libraryLocs is each library the viewer can read, with where its files
// are: the main folder and folders on this computer, a library on another
// system at its own host, else the host named for reading its originals.
func libraryLocs(a *app.App, run Runner, v Viewer) []LibraryLoc {
	reg := libraries.Of(a)
	var out []LibraryLoc
	for _, l := range reg.List(a.Settings.Effective().KbLabel, libViewer(v)) {
		x := LibraryLoc{ID: l.ID, Label: l.Label, Writable: l.Writable, Indexed: l.Primary}
		switch {
		case l.SourceRoot != "":
			x.FS = &LocalFS{Dir: l.SourceRoot}
		case l.Site != "":
			t, err := reg.Users().Target(l.IndexRoot, viewerKeys{v.Key}, remote.SSHSite)
			switch {
			case err != nil:
				x.Why = messageOf(err)
			case t == nil || t.SourceRoot == "":
				x.Why = "the library's folder on its host is not known"
			case t.Def.Shell != nil && !*t.Def.Shell:
				x.Why = remote.NoShell(t.Site)
			default:
				x.FS = &RemoteFS{R: run, SiteID: t.Site, KeyText: t.Key, Dir: t.SourceRoot, Name: remote.SiteName(t.Site)}
			}
		}
		if x.FS == nil && l.ReadThrough != nil {
			rt := l.ReadThrough
			root := ""
			if rt.Root != nil {
				root = *rt.Root
			}
			port := 0
			if rt.Port != nil {
				port = *rt.Port
			}
			site, err := remote.SSHSite(rt.Destination, port, rt.SSHOptions)
			switch {
			case v.AuthOn:
				x.Why = readThroughOff
			case err != nil:
				x.Why = messageOf(err)
			case root == "":
				x.Why = "the library's folder on its originals host is not known: name it in Settings, Indexing, Libraries"
			default:
				x.FS = &RemoteFS{R: run, SiteID: site, Dir: root, Name: remote.SiteName(site)}
				x.Writable = false
			}
		}
		if x.FS == nil && x.Why == "" {
			x.Why = "Studio has only its index, not its files"
		}
		out = append(out, x)
	}
	return out
}

// kbIndex is the index work for the main library, through the files
// track's indexer: the subtrees a landing touched are re-indexed, and the
// originals a move removed leave the index and the caches built from them.
type kbIndex struct{ a *app.App }

func (k kbIndex) module() *kb.Module {
	if kb.Default == nil || kb.Default.A != k.a {
		return nil
	}
	return kb.Default
}

// touched is the top-level subtrees of paths, and whether any is at the top.
func touched(paths []string) ([]string, bool) {
	var roots []string
	seen := map[string]bool{}
	top := false
	for _, p := range paths {
		if i := strings.IndexByte(p, '/'); i >= 0 {
			if r := p[:i]; !seen[r] {
				seen[r] = true
				roots = append(roots, r)
			}
		} else {
			top = true
		}
	}
	return roots, top
}

func (k kbIndex) Landed(ctx context.Context, libID string, files []string) {
	m := k.module()
	if m == nil || libID != libraries.PrimaryID {
		return
	}
	roots, top := touched(files)
	if _, err := kb.ReindexRoots(context.WithoutCancel(ctx), m.Files.Indexer, roots, top); err != nil {
		k.a.Log.Warn("transfers: re-indexing after a landing", "error", err)
	}
}

func (k kbIndex) Removed(ctx context.Context, libID string, files, dirs []string) {
	m := k.module()
	if m == nil || libID != libraries.PrimaryID {
		return
	}
	for _, f := range files {
		os.Remove(m.Roots.ExtractPath(f))
		m.Previews.RemovePreview(f)
		os.Remove(m.Roots.ModelCachePath(f))
	}
	var gone []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(m.Roots.KBRoot, filepath.FromSlash(d))); err == nil {
			continue
		}
		gone = append(gone, d)
		for _, c := range []string{"extract", "pdf-preview", "pdf-pages", "model-cache"} {
			os.RemoveAll(filepath.Join(m.Roots.IndexBase, c, d))
		}
		os.RemoveAll(filepath.Join(m.Roots.GufiIndex, filepath.FromSlash(d)))
	}
	ctx = context.WithoutCancel(ctx)
	if len(gone) > 0 {
		m.Files.Indexer.Changed(ctx, "dbList", "context")
	}
	roots, top := touched(append(append([]string{}, files...), dirs...))
	var live []string
	for _, r := range roots {
		if _, err := os.Lstat(filepath.Join(m.Roots.KBRoot, r)); err == nil {
			live = append(live, r)
		} else {
			top = true
		}
	}
	if _, err := kb.ReindexRoots(ctx, m.Files.Indexer, live, top); err != nil {
		k.a.Log.Warn("transfers: re-indexing after a move", "error", err)
	}
}
