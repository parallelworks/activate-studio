package tasks

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// Rehydrate loads the tasks of an earlier process back (the last 100
// directories, by name). An agent that was working as a pw code session
// or a campaign run is followed again, since its worker never depended on
// that process; any other working agent was killed with it and is marked
// interrupted.
func (e *Engine) Rehydrate() {
	ents, err := os.ReadDir(e.dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(ents))
	for _, d := range ents {
		names = append(names, d.Name())
	}
	if len(names) > 100 {
		names = names[len(names)-100:]
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range names {
		if _, ok := e.tasks[id]; ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.dir, id, "state.json"))
		if err != nil {
			continue
		}
		v, err := jsonv.Decode(b)
		snap := jsonv.Obj(v)
		if err != nil || snap == nil {
			continue
		}
		g := func(k string) any { x, _ := snap.Get(k); return x }
		m := &Task{
			ID: jsonv.String(g("id")), Objective: jsonv.String(g("objective")), Model: jsonv.String(g("model")),
			CreatedAt: jsonv.String(g("createdAt")), State: jsonv.String(g("state")),
			Execution: "local", Runtime: "runner", Shared: []*SharedTask{},
			byName: map[string]*Agent{}, procs: map[string]*runnerProc{}, pollers: map[string]context.CancelFunc{},
		}
		m.MaxAgents, _ = g("maxAgents").(float64)
		m.MaxDepth, _ = g("maxDepth").(float64)
		if x := g("execution"); x != nil {
			m.Execution = jsonv.String(x)
		}
		m.Resource = g("resource")
		if x := g("runtime"); x != nil {
			m.Runtime = jsonv.String(x)
		}
		reattached := 0
		for _, x := range jsonv.Arr(g("agents")) {
			ao := jsonv.Obj(x)
			if ao == nil {
				continue
			}
			a := agentFrom(ao)
			if alive(a) {
				switch {
				case a.sessionID() != "":
					a.Note = "reattached after a server restart"
					reattached++
				case m.Execution == "campaign" && a.runSlug() != "":
					a.Note = "reattached after a server restart"
					reattached++
				default:
					a.State = Canceled
					a.Note = "interrupted by a server restart"
				}
			}
			m.addNode(a)
		}
		if (m.State == Working || m.State == Submitted) && reattached == 0 {
			m.State = Canceled
		}
		if reattached > 0 {
			for _, a := range m.nodes {
				if alive(a) && a.sessionID() != "" {
					e.watchSession(m, a)
				} else if a.State == Working && a.runSlug() != "" {
					e.watchCampaign(m, a)
				}
			}
		}
		if raw, err := os.ReadFile(filepath.Join(e.dir, id, "board.jsonl")); err == nil {
			lines := strings.Split(trimJS(string(raw)), "\n")
			if len(lines) > 500 {
				lines = lines[len(lines)-500:]
			}
			var board []BoardMsg
			ok := true
			for _, l := range lines {
				bv, err := jsonv.DecodeString(l)
				bo := jsonv.Obj(bv)
				if err != nil {
					ok = false
					break
				}
				seq, _ := jsonv.Path(bo, "seq").(float64)
				board = append(board, BoardMsg{Seq: seq, At: jsonv.String(jsonv.Path(bo, "at")), From: jsonv.String(jsonv.Path(bo, "from")),
					Topic: jsonv.String(jsonv.Path(bo, "topic")), Body: jsonv.String(jsonv.Path(bo, "body"))})
			}
			if ok {
				m.board = board
				if len(board) > 0 {
					m.seq = board[len(board)-1].Seq
				}
			}
		}
		// Keyed by the directory name, as Node's map is.
		if _, ok := e.tasks[id]; !ok {
			e.order = append(e.order, id)
		}
		e.tasks[id] = m
	}
}

/* ---- personas ---- */

var (
	fmBlock = regexp.MustCompile(`^---\n((?s:.*?))\n---`)
	nameKey = regexp.MustCompile(`(?m)^name:\s*(.+)$`)
	notName = regexp.MustCompile(`[^a-z0-9_]`)
	edgeUnd = regexp.MustCompile(`^_+|_+$`)
)

func normalizeName(raw string) string {
	return slice(edgeUnd.ReplaceAllString(notName.ReplaceAllString(strings.ToLower(raw), "_"), ""), 40)
}

// ReadPersonas is extensions.ts's personas() as far as the engine needs
// it: names and file names from the corpus's .agents and the deployment's
// extensions/agents (which wins on a name collision).
func ReadPersonas(kbRoot, indexBase string) []Persona {
	found := map[string]Persona{}
	var order []string
	for _, dir := range []string{filepath.Join(kbRoot, ".agents"), filepath.Join(indexBase, "extensions", "agents")} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, d := range ents {
			f := d.Name()
			if !strings.HasSuffix(f, ".md") || strings.HasPrefix(f, ".") {
				continue
			}
			fallback := normalizeName(strings.TrimSuffix(f, ".md"))
			name := fallback
			if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
				if fm := fmBlock.FindStringSubmatch(string(b)); fm != nil {
					if n := nameKey.FindStringSubmatch(fm[1]); n != nil {
						if v := normalizeName(trimJS(n[1])); v != "" {
							name = v
						}
					}
				}
			}
			if _, ok := found[name]; !ok {
				order = append(order, name)
			}
			found[name] = Persona{Name: name, File: f}
		}
	}
	out := make([]Persona, 0, len(order))
	for _, n := range order {
		out = append(out, found[n])
	}
	return out
}
