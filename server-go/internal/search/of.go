package search

import (
	"os"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/embed"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

var (
	ofMu sync.Mutex
	ofs  = map[*app.App]*Engine{}
)

// Of is a's one search engine, made on first use, over libraries.Of(a),
// the shared remote pool, the embed-query child and the files track's
// labels (kb.Default, once the files track has registered).
func Of(a *app.App) *Engine {
	ofMu.Lock()
	defer ofMu.Unlock()
	if e := ofs[a]; e != nil {
		if e.Tags == nil && kb.Default != nil {
			e.Tags = kb.Default.Tags
		}
		return e
	}
	cfg := a.Cfg
	pool, runners := libraries.SharedRemote(a)
	dir := embed.ModelDir(cfg.IndexBase)
	q := &embed.Queries{Dir: dir}
	a.OnStop("search: query embedder", func() int { q.Stop(); return 0 })
	e := &Engine{
		Reg: libraries.Of(a), Keys: libraries.DeploymentKeys{}, Local: a.Gufi,
		GufiBin: cfg.GufiBin, GufiIndex: cfg.GufiIndex, KBRoot: cfg.KBRoot, IndexBase: cfg.IndexBase,
		Pool: pool, Remote: runners, Embed: &QueryEmbedder{Dir: dir, Q: q}, Getenv: os.Getenv,
		SearchEngine: func() string { return a.Settings.Effective().SearchEngine }, Log: a.Log,
	}
	if kb.Default != nil {
		e.Tags = kb.Default.Tags
	}
	ofs[a] = e
	return e
}
