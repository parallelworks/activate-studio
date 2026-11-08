package core

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/extensions"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
)

// iconSuffixes are the image types a persona icon may be.
var iconSuffixes = []string{".png", ".jpg", ".jpeg", ".webp", ".svg", ".gif"}

// labelPersonas is labelPersonas: every corpus persona labeled
// agent-persona; the count, or 0 when labels cannot be applied.
func (c *Core) labelPersonas(ctx context.Context, r *http.Request) int {
	var rels []string
	for _, f := range listMD(c.exts.Corpus) {
		rels = append(rels, filepath.Join(filepath.Base(c.exts.Corpus), f))
	}
	if len(rels) == 0 || c.a.Providers.ApplyLabels == nil {
		return 0
	}
	if c.a.Providers.ApplyLabels(ctx, r, rels, []string{extensions.PersonaLabel}, nil) != nil {
		return 0
	}
	return len(rels)
}

func listMD(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out
}

// strOr is String(b.k ?? d).
func strOr(b map[string]json.RawMessage, k, d string) string {
	if jsonobj.IsNullish(b[k]) {
		return d
	}
	return jsval.String(b[k])
}

func (c *Core) extPersonaWrite(w http.ResponseWriter, r *http.Request) error {
	b, err := body(r)
	if err != nil {
		return err
	}
	shared := string(b["shared"]) != "false"
	dir := c.exts.Corpus
	if !shared {
		dir = filepath.Join(c.exts.Ext, "agents")
	}
	name, _, err := extensions.WriteDoc(dir, strOr(b, "name", ""), strOr(b, "body", ""), strOr(b, "description", ""), strOr(b, "icon", ""))
	if err != nil {
		return writeJSON(w, 400, map[string]string{"error": err.Error()})
	}
	// A shared persona is corpus material: indexed at once, then labeled.
	if shared {
		c.a.Providers.IndexDir(r, filepath.Base(c.exts.Corpus))
		c.labelPersonas(r.Context(), r)
	}
	return ok(w, ordered("ok", true, "name", name, "shared", shared))
}

func (c *Core) extSkillWrite(w http.ResponseWriter, r *http.Request) error {
	b, err := body(r)
	if err != nil {
		return err
	}
	name, _, err := extensions.WriteDoc(filepath.Join(c.exts.Ext, "skills"), strOr(b, "name", ""), strOr(b, "body", ""), strOr(b, "description", ""), "")
	if err != nil {
		return writeJSON(w, 400, map[string]string{"error": err.Error()})
	}
	return ok(w, ordered("ok", true, "name", name))
}

func (c *Core) extPersonaRead(w http.ResponseWriter, r *http.Request) error {
	want := extensions.NormalizeName(r.URL.Query().Get("name"))
	var hit *extensions.AgentFile
	if want != "" {
		for _, a := range c.exts.AgentFiles() {
			if a.Name == want {
				a := a
				hit = &a
				break
			}
		}
	}
	if hit == nil {
		return writeJSON(w, 404, map[string]string{"error": "no such persona"})
	}
	raw, err := os.ReadFile(hit.File)
	if err != nil {
		return writeJSON(w, 404, map[string]string{"error": "unreadable"})
	}
	meta := extensions.DocMeta(hit.File)
	return ok(w, ordered("name", meta.Name, "description", meta.Description, "body", jsval.Trim(extensions.StripFrontmatter(string(raw))), "shared", hit.Shared, "icon", extensions.IconOf(hit.File)))
}

func (c *Core) extSkillRead(w http.ResponseWriter, r *http.Request) error {
	want := extensions.NormalizeName(r.URL.Query().Get("name"))
	var hit *extensions.Doc
	if want != "" {
		for _, k := range c.exts.Skills() {
			if k.Name == want {
				k := k
				hit = &k
				break
			}
		}
	}
	if hit == nil {
		return writeJSON(w, 404, map[string]string{"error": "no such skill"})
	}
	raw, err := os.ReadFile(filepath.Join(c.exts.Ext, "skills", hit.File))
	if err != nil {
		return writeJSON(w, 404, map[string]string{"error": "unreadable"})
	}
	return ok(w, ordered("name", hit.Name, "description", hit.Description, "body", jsval.Trim(extensions.StripFrontmatter(string(raw)))))
}

func (c *Core) extPersonaIcon(w http.ResponseWriter, r *http.Request) error {
	name := extensions.NormalizeName(r.URL.Query().Get("name"))
	if name == "" {
		return writeJSON(w, 400, map[string]string{"error": "a persona name is required"})
	}
	part, data, cut, err := firstFile(r, 2*1024*1024)
	if err != nil {
		return err
	}
	if part == nil {
		return writeJSON(w, 400, map[string]string{"error": "no image in request"})
	}
	if cut {
		// @fastify/multipart's RequestFileTooLargeError, which the app's
		// error handler answers as an internal error.
		return errFileTooLarge
	}
	ext := strings.ToLower(filepath.Ext(part.FileName()))
	okExt := false
	for _, e := range iconSuffixes {
		okExt = okExt || e == ext
	}
	if !okExt {
		return writeJSON(w, 400, map[string]string{"error": "image must be one of " + strings.Join(iconSuffixes, ", ")})
	}
	dir := filepath.Join(c.exts.Corpus, "icons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, e := range iconSuffixes {
		if e != ext {
			os.Remove(filepath.Join(dir, name+e))
		}
	}
	if err := os.WriteFile(filepath.Join(dir, name+ext), data, 0o644); err != nil {
		return err
	}
	icon := filepath.Base(c.exts.Corpus) + "/icons/" + name + ext
	attached := false
	for _, a := range c.exts.AgentFiles() {
		if a.Name != name {
			continue
		}
		if raw, err := os.ReadFile(a.File); err == nil {
			meta := extensions.DocMeta(a.File)
			body := jsval.Trim(extensions.StripFrontmatter(string(raw)))
			if _, _, err := extensions.WriteDoc(filepath.Dir(a.File), name, body, meta.Description, icon); err == nil {
				attached = true
			}
		}
		break
	}
	return ok(w, ordered("ok", true, "icon", icon, "attached", attached))
}

type fileTooLarge struct{}

func (fileTooLarge) Error() string { return "request file too large" }

var errFileTooLarge error = fileTooLarge{}

func (c *Core) extPersonaDelete(w http.ResponseWriter, r *http.Request) error {
	name := extensions.NormalizeName(r.URL.Query().Get("name"))
	if name == "" {
		return ok(w, map[string]bool{"ok": false})
	}
	for _, dir := range []string{c.exts.Corpus, filepath.Join(c.exts.Ext, "agents")} {
		os.Remove(filepath.Join(dir, name+".md"))
	}
	return ok(w, map[string]bool{"ok": true})
}

func (c *Core) extList(w http.ResponseWriter, r *http.Request) error {
	type tool struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		File        string `json:"file"`
		Command     string `json:"command"`
	}
	type agent struct {
		extensions.Doc
		Active bool `json:"active"`
	}
	tools := []tool{}
	for _, t := range c.exts.Tools() {
		tools = append(tools, tool{t.Name, t.Description, t.File, t.Command})
	}
	agents := []agent{}
	for _, a := range c.exts.Agents() {
		agents = append(agents, agent{a, a.File == "default.md"})
	}
	return ok(w, ordered("dir", c.exts.Ext, "tools", tools, "skills", c.exts.Skills(), "agents", agents, "personas", c.exts.Personas(), "corpusAgentDir", c.exts.Corpus))
}
