// Package core is the core track's routes: routes.ts's /api/config,
// /api/version, /api/help, /api/brand-icon, /api/favicon and
// /manifest.webmanifest; settings.ts's GET and PUT /api/settings;
// setup.ts's eight routes; extensions.ts's seven; model.ts's /api/kb/model.
// Values owned by other tracks come through app.Providers.
//
// GET /api/settings/mcp-servers stays with the chat track: it contacts
// each MCP server (mcpClient.ts). The saved queries (GET and POST
// /api/query/saved, DELETE /api/query/saved/:id) are the libraries
// track's, with query.ts.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/extensions"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// Core holds the routes' state (the remote icon cache).
type Core struct {
	a     *app.App
	log   *slog.Logger
	icons *iconCache
	exts  *extensions.Set
	step  stepMesher
}

// Register registers the core routes on mux and the core's start-up work
// on a (seeding the starter extensions in Node-free mode, which Node does
// when it runs).
func Register(mux *http.ServeMux, a *app.App) {
	c := &Core{a: a, log: a.Log, icons: newIconCache(), exts: extensions.New(a.Cfg.IndexBase, a.Cfg.ProjectRoot, a.Cfg.KBRoot)}
	h := func(pattern string, fn func(http.ResponseWriter, *http.Request) error) {
		mux.Handle(pattern, apierr.Handler(a.Log, fn))
	}
	h("GET /api/config", c.config)
	h("GET /api/version", c.version)
	h("GET /api/help", c.help)
	h("GET /api/brand-icon", c.brandIcon)
	h("GET /api/favicon", c.favicon)
	h("GET /manifest.webmanifest", c.manifest)

	h("GET /api/settings", c.getSettings)
	h("PUT /api/settings", c.putSettings)

	h("GET /api/setup/status", c.setupStatus)
	h("GET /api/setup/browse", c.setupBrowse)
	h("POST /api/setup/primary-folder", c.setupPrimaryFolder)
	h("POST /api/setup/port", c.setupPort)
	h("POST /api/setup/index", c.setupIndex)
	h("POST /api/setup/icon", c.setupIcon)
	h("DELETE /api/setup/icon", c.setupIconDelete)
	h("POST /api/setup/complete", c.setupComplete)

	h("POST /api/extensions/persona", c.extPersonaWrite)
	h("POST /api/extensions/skill", c.extSkillWrite)
	h("GET /api/extensions/persona", c.extPersonaRead)
	h("GET /api/extensions/skill", c.extSkillRead)
	h("POST /api/extensions/persona-icon", c.extPersonaIcon)
	h("DELETE /api/extensions/persona", c.extPersonaDelete)
	h("GET /api/extensions", c.extList)

	h("GET /api/kb/model", c.model)

	if a.NodeFree() {
		c.exts.Seed(func(m string) { a.Log.Info(m) })
		a.OnStart("personas: label", func(ctx context.Context) {
			if n := c.labelPersonas(ctx, nil); n > 0 {
				a.Log.Info("personas: labeled definitions", "n", n)
			}
		})
	}
}

// writeJSON writes v as Fastify does: application/json; charset=utf-8,
// and <, > and & as they are.
func writeJSON(w http.ResponseWriter, status int, v any) error {
	b, err := jsonobj.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(b)
	return err
}

func ok(w http.ResponseWriter, v any) error { return writeJSON(w, http.StatusOK, v) }

// body reads a JSON object body; anything else (none, an array, a
// non-JSON type) is an empty object, as the routes read req.body ?? {}.
func body(r *http.Request) (map[string]json.RawMessage, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		// Fastify's bodyLimit (1 MiB).
		return nil, apierr.New(413, "Request body is too large")
	}
	m := map[string]json.RawMessage{}
	t := bytes.TrimSpace(b)
	if len(t) == 0 {
		return m, nil
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "json") {
		return m, nil
	}
	if t[0] != '{' {
		if !json.Valid(t) {
			return nil, &apierr.Validation{Message: "Body is not valid JSON"}
		}
		return m, nil
	}
	if err := json.Unmarshal(t, &m); err != nil {
		return nil, apierr.New(400, "Body is not valid JSON but content-type is set to 'application/json'")
	}
	return m, nil
}

// viewer is req.user's id, or "" without a verified user.
func viewer(r *http.Request) string {
	if u := auth.UserOf(r); u != nil {
		return u.ID
	}
	return ""
}

// lookupEnv is process.env.X ?? fallback: a set variable, even empty, wins.
func lookupEnv(k, d string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return d
}
