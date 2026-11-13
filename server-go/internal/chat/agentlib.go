package chat

import (
	"io/fs"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/tasks"
)

// localAgentTools reach a pw code daemon, which runs without the
// platform: they are offered without it where this Studio offers its own
// host as a site (sign-in off, or AGENT_LOCAL_SITE=1).
var localAgentTools = map[string]bool{"agent_sites": true, "agent_sessions": true, "start_agent": true, "agent_status": true,
	"agent_steer": true, "agent_goal": true, "agent_approve": true, "agent_stop": true}

func (m *Module) localAgents() bool {
	s := tasks.CurrentSites()
	return s != nil && s.LocalSiteAllowed(m.A.Cfg.AuthEnabled())
}

// A pw code session id: the date and time it was made and a short hash.
var sessionIDRe = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{6,}$`)

type agentTool = func(*Module, *http.Request, map[string]any, ToolCtx) (*Outcome, error)

// bySession lets a session be named by its id, its name, or words from
// its topic: anything not an id is looked up among the site's sessions
// and replaced by the one it matches best. Two that match as well are
// listed back, so the model asks which.
func bySession(fn agentTool) agentTool {
	return func(m *Module, r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
		given := strings.TrimSpace(tmplOr(arg(args, "session"), ""))
		if given == "" || sessionIDRe.MatchString(given) {
			return fn(m, r, args, ctx)
		}
		s, err := m.sites()
		if err != nil {
			return nil, err
		}
		rows, err := s.Sessions(r.Context(), tmplOr(arg(args, "site"), ""), m.agentKey(s, ctx), m.A.Cfg.AuthEnabled())
		if err != nil {
			return fn(m, r, args, ctx)
		}
		for _, x := range rows {
			if tmpl(prop(x, "id")) == given {
				return fn(m, r, args, ctx)
			}
		}
		hits := findSessions(rows, given)
		if len(hits) == 0 {
			return &Outcome{Result: "No session on that site matches " + quoteText(given) + ". List them with agent_sessions.", Summary: "not found"}, nil
		}
		if len(hits) > 1 && sessionScore(hits[1], given) == sessionScore(hits[0], given) && !exactName(hits[0], given) {
			lines := []string{"More than one session matches " + quoteText(given) + "; ask the user which, then pass its id:"}
			for _, x := range hits[:min(len(hits), 8)] {
				lines = append(lines, sessionLine(x))
			}
			return &Outcome{Result: strings.Join(lines, "\n"), Summary: "several match"}, nil
		}
		next := make(map[string]any, len(args))
		for k, v := range args {
			next[k] = v
		}
		next["session"] = tmpl(prop(hits[0], "id"))
		return fn(m, r, next, ctx)
	}
}

func quoteText(s string) string { return "\"" + s + "\"" }

func exactName(x any, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	return q == strings.ToLower(tmplOr(prop(x, "name"), "")) || q == strings.ToLower(tmplOr(prop(x, "autoName"), ""))
}

var sessionWord = regexp.MustCompile(`[\p{L}\p{N}]{2,}`)

var sessionStop = map[string]bool{"the": true, "and": true, "for": true, "my": true, "on": true, "of": true, "to": true, "in": true, "session": true, "sessions": true, "pw": true, "code": true, "agent": true, "about": true, "with": true, "that": true, "one": true, "a": true}

// sessionScore is how many of the query's words a session's name, topic,
// opening request and workspace hold, with an exact name ahead of all.
func sessionScore(x any, q string) int {
	if exactName(x, q) {
		return 1000
	}
	hay := strings.ToLower(strings.Join([]string{tmplOr(prop(x, "name"), ""), tmplOr(prop(x, "autoName"), ""), tmplOr(prop(x, "preview"), ""), tmplOr(prop(x, "workspace"), "")}, " "))
	n := 0
	for _, w := range sessionWord.FindAllString(strings.ToLower(q), -1) {
		if !sessionStop[w] && strings.Contains(hay, w) {
			n++
		}
	}
	return n
}

// findSessions is the sessions matching the query, best and then most
// recent first.
func findSessions(rows []any, q string) []any {
	var hits []any
	for _, x := range rows {
		if sessionScore(x, q) > 0 {
			hits = append(hits, x)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := sessionScore(hits[i], q), sessionScore(hits[j], q)
		if a != b {
			return a > b
		}
		return tmplOr(prop(hits[i], "updatedAt"), "") > tmplOr(prop(hits[j], "updatedAt"), "")
	})
	return hits
}

func recentSessions(rows []any, n int) []any {
	out := append([]any(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		return tmplOr(prop(out[i], "updatedAt"), "") > tmplOr(prop(out[j], "updatedAt"), "")
	})
	return out[:min(n, len(out))]
}

// sessionLine is a session in a listing: id, state, its name (or the
// topic pw code gave it), workspace, model, and how its first request
// began.
func sessionLine(x any) string {
	name := tmplOr(prop(x, "name"), "")
	if name == "" {
		name = tmplOr(prop(x, "autoName"), "")
	}
	line := jsTrim(tmpl(prop(x, "id")) + "  " + tmplOr(prop(x, "status"), "") + "  " + name + "  " + tmplOr(prop(x, "workspace"), "") + "  " + tmplOr(prop(x, "model"), ""))
	if p := strings.Join(strings.Fields(tmplOr(prop(x, "preview"), "")), " "); p != "" {
		line += "\n    asked: " + string([]rune(p)[:min(len([]rune(p)), 140)])
	}
	return line
}

// awaitAgent holds agent_status for up to wait seconds (at most 600)
// while the agent is working: through a turn, and between the turns of
// a goal it is still pursuing. It returns early when the agent stops,
// asks for approval, or the request ends.
func (m *Module) awaitAgent(r *http.Request, s *agentsites.Sites, args map[string]any, ctx ToolCtx) {
	wait := math.Max(0, math.Min(numOr(arg(args, "wait"), 0), 600))
	if wait == 0 {
		return
	}
	site, id := tmplOr(arg(args, "site"), ""), tmplOr(arg(args, "session"), "")
	until := time.Now().Add(time.Duration(wait * float64(time.Second)))
	for time.Now().Before(until) {
		st, err := s.Status(r.Context(), site, m.agentKey(s, ctx), id, 1, m.A.Cfg.AuthEnabled())
		if err != nil || agentSettled(st) {
			return
		}
		if sleepCtx(r.Context(), 5*time.Second) != nil {
			return
		}
	}
}

func agentSettled(st any) bool {
	switch tmpl(prop(st, "status")) {
	case "running":
		return false
	case "awaiting_approval", "live_elsewhere":
		return true
	}
	if len(jsonv.Arr(prop(st, "approvals"))) > 0 {
		return true
	}
	g := prop(st, "goal")
	return !(truthy(g) && truthy(prop(g, "active")) && !truthy(prop(g, "paused")))
}

// agentLibraryFiles lists what an agent working in a library folder has
// there, newest first, with links, once it is not running, and indexes
// the folder so the files are found in search. "" when the workspace is
// not in the library or holds nothing yet.
func (m *Module) agentLibraryFiles(r *http.Request, st any) string {
	if tmpl(prop(st, "status")) == "running" {
		return ""
	}
	root := m.A.Cfg.KBRoot
	rel, ok := agentsites.InLibrary(root, tmplOr(prop(st, "workspace"), ""))
	if !ok {
		return ""
	}
	type file struct {
		rel string
		mod time.Time
	}
	var files []file
	dir := filepath.Join(root, filepath.FromSlash(rel))
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		sub, _ := filepath.Rel(root, p)
		files = append(files, file{filepath.ToSlash(sub), info.ModTime()})
		return nil
	})
	if len(files) == 0 {
		return ""
	}
	_, _ = kb.ReindexForDir(r.Context(), kb.ProviderIndexer{A: m.A}, rel)
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	lines := []string{"in the library at " + rel + " (indexed; give the user these links):"}
	for _, f := range files[:min(len(files), 25)] {
		lines = append(lines, "  ["+f.rel+"](#open=file:"+strings.ReplaceAll(f.rel, " ", "%20")+")")
	}
	if len(files) > 25 {
		lines = append(lines, "  and "+itoaInt(len(files)-25)+" more")
	}
	return strings.Join(lines, "\n")
}
