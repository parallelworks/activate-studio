package chat

import (
	"os"
	"path/filepath"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// Corpus is what the chat's corpus tools read through: the libraries
// track's registry and search engine.
//
// TODO(libraries track): the track will own one Registry and Engine for
// the server; at its merge, Module.Corpus is assigned that pair and
// newCorpus goes.
type Corpus struct {
	Reg    *libraries.Registry
	Search *search.Engine
}

// newCorpus builds a registry and engine over the server's configuration.
func newCorpus(a *app.App) *Corpus {
	c := a.Cfg
	pool := remote.New(remote.Options{IndexBase: c.IndexBase})
	reg := libraries.New(libraries.Options{
		KBRoot: c.KBRoot, IndexBase: c.IndexBase, GufiIndex: c.GufiIndex, GufiBin: c.GufiBin,
		AuthEnabled: c.AuthEnabled(), Runner: a.Gufi, SSHSite: remote.SSHSite, Log: a.Log,
	})
	eng := &search.Engine{
		Reg: reg, Keys: libraries.DeploymentKeys{Getenv: os.Getenv, AuthEnabled: c.AuthEnabled()}, Local: a.Gufi,
		GufiBin: c.GufiBin, GufiIndex: c.GufiIndex, KBRoot: c.KBRoot, IndexBase: c.IndexBase,
		Pool: pool, Remote: &remote.Runners{Pool: pool, Options: gufi.OptionsFrom(c.Gufi)},
		Embed:        &search.WorkerEmbedder{Python: os.Getenv("PYTHON_BIN"), ProjectRoot: c.ProjectRoot, IndexBase: c.IndexBase},
		Labels:       &search.FileLabels{IndexBase: c.IndexBase, KBRoot: c.KBRoot, GufiIndex: c.GufiIndex, Runner: a.Gufi},
		Getenv:       os.Getenv,
		SearchEngine: func() string { return a.Settings.Effective().SearchEngine },
		Log:          a.Log,
	}
	return &Corpus{Reg: reg, Search: eng}
}

// gufiInstalled is config.ts's gufiInstalled: gufi_query in GUFI_BIN.
func (m *Module) gufiInstalled() bool {
	_, err := os.Stat(filepath.Join(m.A.Cfg.GufiBin, "gufi_query"))
	return err == nil
}

// primaryLabel is listLibraries' default: the main folder's name.
func (m *Module) primaryLabel() string { return filepath.Base(m.A.Cfg.KBRoot) }

// viewerOfCtx is the tool call's viewer for the registry (toolContext's
// userId); nil for none.
func viewerOfCtx(ctx ToolCtx) libraries.Viewer {
	if ctx.UserID == nil || *ctx.UserID == "" {
		return nil
	}
	return libraries.ViewerOf(*ctx.UserID)
}
