package chat

import (
	"net/http"
	"path"
	"path/filepath"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// A pw code chat reads its agent instructions the way every pw code
// session does: from AGENTS.md in the folder it works in. A conversation
// with a persona chosen gets its session in agents/<persona>/ of the
// library the session sees (the main library on a Studio one person uses,
// the person's own library on a shared one), with the persona's text in
// that folder's AGENTS.md; the Studio assistant keeps taking a persona as
// its stance in the system prompt.

// personaFolder points t at the persona's folder, written first; where no
// library the session sees can take it, the session works where it would
// have without one.
func (m *Module) personaFolder(r *http.Request, t *pwTarget, persona string) {
	t.persona = persona
	svc := agentsmd.Default
	if persona == "" || svc == nil {
		return
	}
	rel := "agents/" + persona
	var lib, ws string
	switch {
	case t.site == agentsites.Local && !m.A.Cfg.AuthEnabled():
		ws = filepath.Join(m.A.Cfg.KBRoot, "agents", persona)
	case t.inSpace:
		id := ownSpace(r)
		if id == "" {
			return
		}
		lib, ws = id, path.Join(t.workspace, rel)
	default:
		return
	}
	f, _, err := svc.FolderToWrite(r, lib, rel)
	if err == nil {
		err = svc.ApplyNamed(f, persona, nil)
	}
	if err != nil {
		m.A.Log.Warn("pw code chat: the persona's folder could not be written, so the session works without it", "persona", persona, "err", err.Error())
		return
	}
	t.workspace = ws
}

// ownSpace is the id of the person's own library, "" when they have none.
func ownSpace(r *http.Request) string {
	if kb.LibraryResolver == nil || kb.SpaceFiles == nil || user(r) == nil {
		return ""
	}
	libs, err := kb.LibraryResolver.All(r)
	if err != nil {
		return ""
	}
	for _, l := range libs {
		if l.Space && l.SharedFrom == "" {
			return l.ID
		}
	}
	return ""
}
