package tasks

import (
	"math"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// States are A2A's task lifecycle, as tasks.ts uses them.
const (
	Submitted     = "submitted"
	Working       = "working"
	InputRequired = "input-required"
	Completed     = "completed"
	Failed        = "failed"
	Canceled      = "canceled"
)

// Usage is AgentUsage: tokens an agent's model calls consumed.
type Usage struct {
	Input, Output, Total float64
	Cost                 *float64
}

func (u *Usage) value() any {
	if u == nil {
		return nil
	}
	var cost any
	if u.Cost != nil {
		cost = *u.Cost
	}
	return jsonv.NewObject("input", u.Input, "output", u.Output, "total", u.Total, "cost", cost)
}

// MarshalJSON writes the usage as JSON.stringify does.
func (u *Usage) MarshalJSON() ([]byte, error) { return jsonv.Marshal(u.value()) }

// Approval is a pending approval request in one line.
type Approval struct {
	ID, Kind any
	Text     string
}

func (a Approval) value() any { return jsonv.NewObject("id", a.ID, "kind", a.Kind, "text", a.Text) }

// Agent is AgentTask: one agent of a task. Its JSON keys keep the order
// a JavaScript object gets them in: the fields spawnAgent writes, then
// usage, sessionId and approvals in the order they are first set (or, for
// an agent read back from a snapshot, the snapshot's order).
type Agent struct {
	Name, Persona, Objective string
	Parent                   any // string or nil
	Depth                    float64
	State                    string
	StartedAt, UpdatedAt     string
	Note                     string
	ResultPath               any // string or nil
	RunSlug                  any // string or nil
	Host                     any // string or nil
	Usage                    *Usage
	SessionID                any // string or nil
	Approvals                []Approval

	order []string
	extra map[string]any
}

var baseOrder = []string{"name", "persona", "objective", "parent", "depth", "state", "startedAt", "updatedAt", "note", "resultPath", "runSlug", "host"}

func (a *Agent) touch(k string) {
	for _, x := range a.order {
		if x == k {
			return
		}
	}
	a.order = append(a.order, k)
}

func (a *Agent) setUsage(u *Usage)          { a.Usage = u; a.touch("usage") }
func (a *Agent) setSession(id any)          { a.SessionID = id; a.touch("sessionId") }
func (a *Agent) setApprovals(ap []Approval) { a.Approvals = ap; a.touch("approvals") }

func (a *Agent) resultPath() string { s, _ := a.ResultPath.(string); return s }
func (a *Agent) sessionID() string  { return truthyStr(a.SessionID) }
func (a *Agent) runSlug() string    { return truthyStr(a.RunSlug) }

func truthyStr(v any) string {
	if !jsonv.Truthy(v) {
		return ""
	}
	return jsonv.String(v)
}

func (a *Agent) value() *jsonv.Object {
	o := jsonv.NewObject()
	for _, k := range a.order {
		switch k {
		case "name":
			o.Set(k, a.Name)
		case "persona":
			o.Set(k, a.Persona)
		case "objective":
			o.Set(k, a.Objective)
		case "parent":
			o.Set(k, a.Parent)
		case "depth":
			o.Set(k, a.Depth)
		case "state":
			o.Set(k, a.State)
		case "startedAt":
			o.Set(k, a.StartedAt)
		case "updatedAt":
			o.Set(k, a.UpdatedAt)
		case "note":
			o.Set(k, a.Note)
		case "resultPath":
			o.Set(k, a.ResultPath)
		case "runSlug":
			o.Set(k, a.RunSlug)
		case "host":
			o.Set(k, a.Host)
		case "usage":
			o.Set(k, a.Usage.value())
		case "sessionId":
			o.Set(k, a.SessionID)
		case "approvals":
			if a.Approvals == nil {
				o.Set(k, nil)
			} else {
				list := make([]any, len(a.Approvals))
				for i, x := range a.Approvals {
					list[i] = x.value()
				}
				o.Set(k, list)
			}
		default:
			o.Set(k, a.extra[k])
		}
	}
	return o
}

// MarshalJSON writes the agent as JSON.stringify does.
func (a *Agent) MarshalJSON() ([]byte, error) { return jsonv.Marshal(a.value()) }

// agentFrom reads an agent back from a snapshot, keeping its keys.
func agentFrom(o *jsonv.Object) *Agent {
	a := &Agent{order: o.Keys(), extra: map[string]any{}}
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		switch k {
		case "name":
			a.Name = jsonv.String(v)
		case "persona":
			a.Persona = jsonv.String(v)
		case "objective":
			a.Objective = jsonv.String(v)
		case "parent":
			a.Parent = v
		case "depth":
			a.Depth, _ = v.(float64)
		case "state":
			a.State = jsonv.String(v)
		case "startedAt":
			a.StartedAt = jsonv.String(v)
		case "updatedAt":
			a.UpdatedAt = jsonv.String(v)
		case "note":
			a.Note = jsonv.String(v)
		case "resultPath":
			a.ResultPath = v
		case "runSlug":
			a.RunSlug = v
		case "host":
			a.Host = v
		case "usage":
			a.Usage = usageFromJSON(v)
		case "sessionId":
			a.SessionID = v
		case "approvals":
			for _, x := range jsonv.Arr(v) {
				ao := jsonv.Obj(x)
				id, _ := ao.Get("id")
				kind, _ := ao.Get("kind")
				text, _ := ao.Get("text")
				a.Approvals = append(a.Approvals, Approval{ID: id, Kind: kind, Text: jsonv.String(text)})
			}
		default:
			a.extra[k] = v
		}
	}
	return a
}

func usageFromJSON(v any) *Usage {
	o := jsonv.Obj(v)
	if o == nil {
		return nil
	}
	n := func(k string) float64 { x, _ := o.Get(k); f, _ := x.(float64); return f }
	u := &Usage{Input: n("input"), Output: n("output"), Total: n("total")}
	if c, _ := o.Get("cost"); c != nil {
		if f, ok := c.(float64); ok {
			u.Cost = &f
		}
	}
	return u
}

// BoardMsg is one board message.
type BoardMsg struct {
	Seq   float64 `json:"seq"`
	At    string  `json:"at"`
	From  string  `json:"from"`
	Topic string  `json:"topic"`
	Body  string  `json:"body"`
}

func (b BoardMsg) value() any {
	return jsonv.NewObject("seq", b.Seq, "at", b.At, "from", b.From, "topic", b.Topic, "body", b.Body)
}

// SharedTask is one entry of a task's shared queue.
type SharedTask struct {
	ID, Objective, Persona string
	ClaimedBy              any // string or nil
	Done                   bool
	ResultPath             any // string or nil
}

func (t *SharedTask) value() any {
	return jsonv.NewObject("id", t.ID, "objective", t.Objective, "persona", t.Persona, "claimedBy", t.ClaimedBy, "done", t.Done, "resultPath", t.ResultPath)
}

// UsageOf is usageOf: token usage from whatever envelope pw code emitted,
// every known spelling tried; nil when none is present.
func UsageOf(env any) *Usage {
	o := jsonv.Obj(env)
	if o == nil {
		return nil
	}
	u := o
	if inner := jsonv.Obj(jsonv.Path(o, "usage")); inner != nil {
		u = inner
	}
	num := func(keys ...string) *float64 {
		for _, k := range keys {
			v, _ := u.Get(k)
			if v == nil {
				v, _ = o.Get(k)
			}
			if f, ok := v.(float64); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
				return &f
			}
		}
		return nil
	}
	in := num("prompt_tokens", "input_tokens", "inputTokens")
	out := num("completion_tokens", "output_tokens", "outputTokens")
	total := num("total_tokens", "totalTokens")
	if total == nil && (in != nil || out != nil) {
		t := deref(in) + deref(out)
		total = &t
	}
	cost := num("total_cost_usd", "cost_usd", "cost", "totalCostUsd")
	if total == nil && cost == nil {
		return nil
	}
	return &Usage{Input: deref(in), Output: deref(out), Total: deref(total), Cost: cost}
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// SumUsage is sumUsage: the agents' usage summed, or nil when none
// reported any.
func SumUsage(agents []*Agent) *Usage {
	var out *Usage
	for _, a := range agents {
		if a.Usage == nil {
			continue
		}
		if out == nil {
			out = &Usage{}
		}
		out.Input += a.Usage.Input
		out.Output += a.Usage.Output
		out.Total += a.Usage.Total
		if a.Usage.Cost != nil {
			c := deref(out.Cost) + *a.Usage.Cost
			out.Cost = &c
		}
	}
	return out
}
