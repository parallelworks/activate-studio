package tasks

import (
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// The board: what agents with a task's token call over MCP. Node's /api/mcp
// serves the MCP protocol and, with the Go server owning tasks, hands each
// board call here (POST /internal/platform/board). The check and the write
// of a claim happen under one lock, so a task is claimed once.

// TaskByToken is taskByToken: the id of the task a bearer authorizes, or
// "" (a rehydrated task has no token).
func (e *Engine) TaskByToken(token string) string {
	if token == "" {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range e.order {
		if m := e.tasks[id]; m != nil && m.Token == token {
			return id
		}
	}
	return ""
}

// BoardTool is runBoardTool for the task the token authorizes: found is
// whether it authorizes one, out the tool's answer in Node's shape.
func (e *Engine) BoardTool(token, agent, name string, args *jsonv.Object) (found bool, out any) {
	if token == "" {
		return false, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var m *Task
	for _, id := range e.order {
		if t := e.tasks[id]; t != nil && t.Token == token {
			m = t
			break
		}
	}
	if m == nil {
		return false, nil
	}
	if name == "" {
		return true, nil
	}
	if args == nil {
		args = jsonv.NewObject()
	}
	arg := func(k string) any { v, _ := args.Get(k); return v }
	// String(a.k ?? d)
	str := func(k, d string) string {
		v := arg(k)
		if v == nil {
			return d
		}
		return jsonv.String(v)
	}
	switch name {
	case "board_post":
		return true, e.boardPost(m, agent, str("topic", "note"), str("body", ""))
	case "board_read":
		since := jsNumber(arg("since"))
		if since != since { // NaN
			since = 0
		}
		return true, e.boardRead(m, since, 50)
	case "board_status":
		return true, e.boardStatus(m, agent, str("note", ""))
	case "board_claim":
		id := ""
		if v := arg("task_id"); jsonv.Truthy(v) {
			id = jsonv.String(v)
		}
		return true, e.boardClaim(m, agent, id)
	case "board_complete":
		return true, e.boardComplete(m, agent, str("task_id", ""), str("result_path", ""))
	case "board_offer_task":
		return true, e.boardAddTask(m, str("objective", ""), str("persona", ""))
	}
	return true, jsonv.NewObject("error", "unknown board tool")
}

func (e *Engine) boardPost(m *Task, from, topic, body string) any {
	if topic == "" {
		topic = "note"
	}
	e.post(m, from, topic, body)
	return jsonv.NewObject("ok", true)
}

func (e *Engine) boardRead(m *Task, since float64, limit int) any {
	var hits []BoardMsg
	for _, b := range m.board {
		if b.Seq > since {
			hits = append(hits, b)
		}
	}
	if len(hits) > limit {
		hits = hits[len(hits)-limit:]
	}
	out := make([]any, len(hits))
	for i, b := range hits {
		out[i] = b.value()
	}
	return out
}

func (e *Engine) boardStatus(m *Task, from, note string) any {
	if p := m.node(from); p != nil {
		p.Note = slice(note, 200)
		p.UpdatedAt = now()
	}
	e.post(m, from, "status", note)
	return jsonv.NewObject("ok", true)
}

func (e *Engine) boardAddTask(m *Task, objective, persona string) any {
	t := &SharedTask{ID: "s" + num(float64(len(m.Shared)+1)), Objective: slice(objective, 2000), Persona: persona}
	m.Shared = append(m.Shared, t)
	e.post(m, "orchestrator", "task", t.ID+" offered: "+t.Objective)
	return t.value()
}

func (e *Engine) boardClaim(m *Task, from, taskID string) any {
	var t *SharedTask
	for _, x := range m.Shared {
		if (taskID != "" && x.ID == taskID) || (taskID == "" && !jsonv.Truthy(x.ClaimedBy) && !x.Done) {
			t = x
			break
		}
	}
	if t == nil {
		return jsonv.NewObject("claimed", nil, "reason", "no unclaimed task")
	}
	if jsonv.Truthy(t.ClaimedBy) {
		return jsonv.NewObject("claimed", nil, "reason", "already claimed by "+jsonv.String(t.ClaimedBy))
	}
	t.ClaimedBy = from
	e.post(m, from, "claim", t.ID+": "+t.Objective)
	return jsonv.NewObject("claimed", t.value(), "reason", "")
}

func (e *Engine) boardComplete(m *Task, from, taskID, resultPath string) any {
	var t *SharedTask
	for _, x := range m.Shared {
		if x.ID == taskID {
			t = x
			break
		}
	}
	if t == nil {
		return jsonv.NewObject("ok", false, "reason", "no such task")
	}
	t.Done = true
	if resultPath != "" {
		t.ResultPath = resultPath
	} else {
		t.ResultPath = nil
	}
	body := t.ID + " done"
	if resultPath != "" {
		body += " -> " + resultPath
	}
	e.post(m, from, "complete", body)
	return jsonv.NewObject("ok", true)
}
