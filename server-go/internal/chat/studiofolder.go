package chat

import (
	"context"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
)

// The Studio's section of AGENTS.md. pw code gives a session's model the
// AGENTS.md of its working folder as policy over its own advice, which sends
// it to GlobSearch and GrepSearch. So a folder of the Studio's own that a
// session works in (the main library, My library, agents/<persona> in
// either, ~/studio-chat) says there to use the index first. The section is
// merged in as agent instructions are (agentsmd.Apply): the person's text in
// the file stays. A folder the person chose, and one on another machine, is
// theirs and is never written; there the prompt says it alone.

// under is dir's path below root, "" for root itself.
func under(root, dir string) (string, bool) {
	root, dir = path.Clean(filepath.ToSlash(root)), path.Clean(filepath.ToSlash(dir))
	if !path.IsAbs(root) && filepath.VolumeName(root) == "" {
		return "", false
	}
	if dir == root {
		return "", true
	}
	return strings.CutPrefix(dir, strings.TrimSuffix(root, "/")+"/")
}

// spaceRel is t's folder as a path in the person's own library there.
func (m *Module) spaceRel(r *http.Request, t *pwTarget) (string, bool) {
	u := user(r)
	if u == nil {
		return "", false
	}
	root := m.spaceFolderOn(u.Username, t.site)
	if root == "" {
		return "", false
	}
	return under(root, t.workspace)
}

// folderKind is what t's working folder is to the person's libraries.
func (m *Module) folderKind(r *http.Request, t *pwTarget) folderKind {
	if t.relay {
		return folderElsewhere
	}
	if _, ok := m.spaceRel(r, t); ok {
		return folderInLibrary
	}
	// The main library's files are on the host the Studio runs on, which is
	// where a chat that is not relayed works.
	if _, ok := under(m.A.Cfg.KBRoot, t.workspace); ok {
		return folderInLibrary
	}
	if !t.folderPicked && path.Base(t.workspace) == path.Base(studioChatFolder) {
		return folderScratch
	}
	return folderElsewhere
}

// studioSection writes the Studio's section into the AGENTS.md of the folder
// t's new session works in, when the folder is the Studio's. A folder that
// cannot be written is left: the session's prompt says the same.
func (m *Module) studioSection(ctx context.Context, r *http.Request, t *pwTarget, tools []ToolSpec) {
	svc := agentsmd.Default
	if svc == nil || t.relay || t.folderPicked {
		return
	}
	var f agentsmd.Folder
	var err error
	kind := m.folderKind(r, t)
	if rel, ok := m.spaceRel(r, t); ok {
		id := ownSpace(r)
		if id == "" {
			return
		}
		f, _, err = svc.FolderToWrite(r, id, rel)
	} else if rel, ok := under(m.A.Cfg.KBRoot, t.workspace); ok && t.site == agentsites.Local && !m.A.Cfg.AuthEnabled() {
		if st, serr := os.Stat(t.workspace); serr != nil || !st.IsDir() {
			return
		}
		f, _, err = svc.FolderToWrite(r, "", rel)
	} else if kind == folderScratch && m.A.Cfg.AuthEnabled() {
		key := m.viewerPlatformKey(r)
		if s := pwSites(); s != nil && key != "" {
			f = s.OwnFolder(ctx, t.site, key, t.workspace)
		}
	}
	if f == nil && err == nil {
		return
	}
	body := m.studioSectionText(tools, kind)
	if body == "" {
		return
	}
	// Reading the file again is a command on another host for some: the
	// section once written there is left until it changes.
	done := userID(r) + "|" + t.site + "|" + t.workspace
	if t.site != agentsites.Local && m.sectionWritten(done, body) {
		return
	}
	if err == nil {
		err = agentsmd.ApplyStudio(f, body)
	}
	if err != nil {
		m.A.Log.Info("pw code chat: the Studio's section of AGENTS.md was not written; the session's prompt says the same", "folder", t.workspace, "why", apiMessage(err))
		return
	}
	m.pw.mu.Lock()
	if m.pw.sections == nil {
		m.pw.sections = map[string]string{}
	}
	m.pw.sections[done] = body
	m.pw.mu.Unlock()
}

func (m *Module) sectionWritten(done, body string) bool {
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	return m.pw.sections[done] == body
}

// studioSectionText is the section: the guide, for any session that works in
// the folder, which may be one the Studio did not start. "" with no tools.
func (m *Module) studioSectionText(tools []ToolSpec, kind folderKind) string {
	g := newToolGuide(specNames(tools), func(n string) string { return studioToolPrefix + n }, true)
	g.folder = kind
	if g.words() == "" {
		return ""
	}
	return "## Finding files: the Studio's index first\n\n" +
		"This is for a session that has the studio MCP server's tools (" + studioToolPrefix + "…), the index and tools of " + m.eff().AppName + ", an ACTIVATE Studio; one without them uses its own. " +
		guideLead + g.words() + g.find() +
		"\n\nThis comes before any advice to use GlobSearch, GrepSearch or the shell to look for files. They are for files the index does not have, such as code or files made in this session.\n\n" + citeRule
}
