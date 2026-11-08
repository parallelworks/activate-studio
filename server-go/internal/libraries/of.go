package libraries

import (
	"os"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

var (
	ofMu sync.Mutex
	ofs  = map[*app.App]*Registry{}
)

// SharedRemote is a's one ssh and pw ssh pool and runner set
// (remote.Shared), with the platform's pw CLI and gateway.
func SharedRemote(a *app.App) (*remote.Pool, *remote.Runners) {
	return remote.Shared(a, func() remote.Options {
		p := platform.Of(a)
		return remote.Options{IndexBase: a.Cfg.IndexBase, PwCLI: p.CLI, GatewayBase: p.GatewayBase}
	})
}

// Of is a's one registry, made on first use: every package of the server
// (search, chat's tools, user libraries, the routes) reads libraries
// through it, so the probes, caps and stores are one state. Its index
// settings are the index track's store (index.Of(a).Settings).
func Of(a *app.App) *Registry {
	ofMu.Lock()
	defer ofMu.Unlock()
	if r := ofs[a]; r != nil {
		return r
	}
	cfg := a.Cfg
	settings := &FileIndexSettings{File: NewFileIndexSettings(cfg.IndexBase).File, Store: index.Of(a).Settings}
	r := New(Options{
		KBRoot: cfg.KBRoot, IndexBase: cfg.IndexBase, GufiIndex: cfg.GufiIndex, GufiBin: cfg.GufiBin,
		AuthEnabled: cfg.AuthEnabled(), Getenv: os.Getenv, Runner: a.Gufi, Settings: settings,
		Users: &Users{IndexBase: cfg.IndexBase, AuthEnabled: cfg.AuthEnabled()},
		SSHSite: remote.SSHSite, Log: a.Log,
		ReachedLabel: func(site string) string {
			pool, _ := SharedRemote(a)
			if h := pool.ReachedHost(site); h != nil {
				return h.Label
			}
			return ""
		},
		Learn: func(site string) {
			pool, _ := SharedRemote(a)
			pool.Warm(site)
		},
	})
	ofs[a] = r
	return r
}

// FileSettings is the registry's index settings as the concrete type the
// user library module keeps.
func (r *Registry) FileSettings() *FileIndexSettings {
	s, _ := r.o.Settings.(*FileIndexSettings)
	return s
}
