package chat

import (
	"os"
	"path/filepath"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// Corpus is what the chat's corpus tools read through: the server's one
// library registry and search engine.
type Corpus struct {
	Reg    *libraries.Registry
	Search *search.Engine
}

// newCorpus is a's shared pair (libraries.Of, search.Of).
func newCorpus(a *app.App) *Corpus {
	return &Corpus{Reg: libraries.Of(a), Search: search.Of(a)}
}

// gufiInstalled is config.ts's gufiInstalled: gufi_query in GUFI_BIN.
func (m *Module) gufiInstalled() bool {
	_, err := os.Stat(filepath.Join(m.A.Cfg.GufiBin, "gufi_query"))
	return err == nil
}

// primaryLabel is listLibraries' default: the main folder's name.
func (m *Module) primaryLabel() string { return filepath.Base(m.A.Cfg.KBRoot) }

// viewerOfCtx is the tool call's viewer for the registry (toolContext's
// userId).
func viewerOfCtx(ctx ToolCtx) libraries.Viewer {
	// executeTool's userId is req.user?.id ?? null: never undefined, so with
	// sign-in off the one person's own libraries are in scope (ownerOf).
	return libraries.ViewerOf(deref(ctx.UserID))
}
