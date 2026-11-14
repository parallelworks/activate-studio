package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// loopState is the stream handler's state the loop reads and writes.
type loopState struct {
	model          string
	body           *streamBody
	messages       *[]llm.Message
	baseLen        int
	callbacks      llm.Callbacks
	res            *sseWriter
	cites          *CitationStream
	citedMu        *sync.Mutex
	cited          *CitationContext
	allocation     string
	userKey        string
	userBase       string
	userEndpoint   *modelkeys.Endpoint
	finalContent   *string
	usage          *llm.Usage
	finalModel     **string
	finalReason    *string
	finalReasoning *string
	turnBreak      *bool
	finalParts     *[]conversations.Part
	markThinking   func()
	flushCites     func()
	finish         func(finishReason *string, id string)
	resume         *resumeBody
	resumedFrom    **string
	viewer         *Viewer
	changes        Changes
	kbChanges      Changes
	changeKey      *string
	platform       bool
	meta           Meta
	readerTools    []string
	hasReader      bool
	activeGroups   map[string]bool
	groupsMu       *sync.Mutex
	groupList      func() []string
	labelScope     []string
	library        *string
	pwToolKey      *string
	convID         string
	lastUser       *llm.Message
	effort         string
	timing         func(kind string, ms int64, extra string)
}

func (st *loopState) readerAllows(name string, names *Names) bool {
	if !st.hasReader || name == st.meta.MoreTools {
		return true
	}
	if names != nil {
		if v, ok := names.Allowed[name]; ok {
			return v
		}
	}
	return false
}

func (st *loopState) addGroup(g string) {
	st.groupsMu.Lock()
	st.activeGroups[g] = true
	st.groupsMu.Unlock()
}

func (st *loopState) readerHas(g string) bool { return !st.hasReader || contains(st.readerTools, g) }

func specName(raw Spec) string {
	var s specFn
	_ = json.Unmarshal(raw, &s)
	return s.Function.Name
}

func hasSpec(specs []Spec, name string) bool {
	for _, s := range specs {
		if specName(s) == name {
			return true
		}
	}
	return false
}

// payload is a turn's request body, in the route's key order.
type payload struct {
	model     string
	messages  []llm.Message
	tools     []Spec
	hasTools  bool
	maxTokens int
	kwargs    json.RawMessage
	// effort is the turn's reasoning effort, "" for the model's own.
	effort string
}

func (p payload) object(stream, foldSystem bool, nonce string, dropCap, dropEffort bool) (*jsonobj.Object, bool) {
	o := jsonobj.New()
	_ = o.SetValue("model", p.model)
	msgs := p.messages
	emulating := foldSystem && p.hasTools && len(p.tools) > 0
	if foldSystem {
		var specs []Spec
		if emulating {
			specs = p.tools
		}
		msgs = EmulatedPrompt(msgs, specs, nonce)
	}
	o.Set("messages", msgsJSON(msgs))
	if p.hasTools && !foldSystem {
		tb, _ := jsonobj.Marshal(orEmptySpecs(p.tools))
		o.Set("tools", tb)
		_ = o.SetValue("tool_choice", "auto")
	}
	if !dropCap {
		_ = o.SetValue("max_tokens", p.maxTokens)
	}
	kwargs, topEffort := withEffort(p.kwargs, p.effort)
	if dropEffort {
		kwargs, topEffort = p.kwargs, ""
	}
	if kwargs != nil {
		o.Set("chat_template_kwargs", kwargs)
	}
	if topEffort != "" {
		_ = o.SetValue("reasoning_effort", topEffort)
	}
	if !stream {
		_ = o.SetValue("stream", false)
	}
	return o, emulating
}

func orEmptySpecs(s []Spec) []Spec {
	if s == nil {
		return []Spec{}
	}
	return s
}

func (m *Module) loop(ctx context.Context, r *http.Request, st *loopState) error {
	model := st.model
	ns := noStream(model)
	foldSystem := emulatesTools(model)
	nonce := nonce8()
	dropTokenCap := false
	dropEffort := false
	retryUnstreamed := false
	res := st.res

	turnWithRetry := func(p payload) (*llm.Turn, error) {
		for attempt := 0; ; attempt++ {
			streamThis := !ns && (attempt == 0 || !retryUnstreamed)
			drop := dropTokenCap || m.noCap.has(model)
			obj, emulating := p.object(streamThis, foldSystem, nonce, drop, dropEffort || m.noEffort(model))
			var t *llm.Turn
			var err error
			if !emulating {
				t, err = m.LLM.StreamTurn(ctx, obj, st.allocation, st.callbacks, st.userKey, st.userBase, st.userEndpoint)
			} else {
				buffered := ""
				muted := st.callbacks
				muted.OnContent = func(s string) { buffered += s }
				t, err = m.LLM.StreamTurn(ctx, obj, st.allocation, muted, st.userKey, st.userBase, st.userEndpoint)
				if err == nil {
					if len(t.ToolCalls) > 0 {
						if attempt < 2 && ctx.Err() == nil {
							m.A.Log.Warn("native tool leakage on emulated path; retrying with corrective note", "attempt", attempt)
							// Node writes a corrective note into the emulated prompt here, which
							// the next attempt rebuilds from the payload; the retry goes out as
							// before, and so it does here.
							continue
						}
						t.ToolCalls = nil
					}
					var offered []string
					for _, s := range p.tools {
						if n := specName(s); n != "" {
							offered = append(offered, n)
						}
					}
					text := t.Content
					if text == "" {
						text = buffered
					}
					prose, calls := splitEnvelope(text, nonce, offered, p.hasTools)
					if calls == nil && attempt < 2 && ctx.Err() == nil && looksLikeRefusal(text) {
						m.A.Log.Warn("model refused the emulated tool format; retrying with a plainer request", "attempt", attempt)
						// Node writes a corrective note into the emulated prompt here, which
						// the next attempt rebuilds from the payload; the retry goes out as
						// before, and so it does here.
						continue
					}
					if calls != nil {
						now := strconv.FormatInt(time.Now().UnixMilli(), 10)
						t.ToolCalls = nil
						for ci, c := range calls {
							i := ci
							t.ToolCalls = append(t.ToolCalls, llm.ToolCall{Index: &i, ID: fmt.Sprintf("em-%s-%s-%d", nonce, now, ci), Type: "function", Function: llm.Function{Name: c.Name, Arguments: c.Arguments}})
						}
						t.FinishReason = sp("tool_calls")
					}
					// Text the model wrote around an envelope is shown, as
					// a natively calling model's would be; the envelope
					// itself never is, even one naming no offered lookup.
					t.Content = prose
					if prose != "" {
						st.callbacks.OnContent(prose)
					}
					return t, nil
				}
			}
			if err == nil {
				return t, nil
			}
			errText := err.Error()
			// A provider that takes no reasoning effort, or not this one,
			// gets the turn again without it, and this model keeps going
			// without it until the Studio restarts.
			if p.effort != "" && !dropEffort && rejectedEffort(errText) && attempt < 2 && ctx.Err() == nil && len(*st.finalContent) == 0 {
				dropEffort = true
				m.rememberNoEffort(model)
				m.A.Log.Warn("provider refused the reasoning effort; retrying without it", "model", model, "effort", p.effort, "err", errText)
				continue
			}
			rejectedTokenCap := unsupportedRe.MatchString(errText) && tokenCapRe.MatchString(errText)
			var se *llm.StreamedError
			streamed := errors.As(err, &se)
			generationError := streamed || maskedRe.MatchString(errText) || rejectedTokenCap
			spoken := len(*st.finalContent) > 0
			if isRateLimit(errText) && attempt < 2 && ctx.Err() == nil && !spoken {
				wait := 3 * time.Second
				if attempt > 0 {
					wait = 8 * time.Second
				}
				m.A.Log.Warn("provider rate limited; waiting before retrying", "attempt", attempt, "wait", wait.Milliseconds(), "model", model)
				res.event("reasoning", reqbody.O("text", fmt.Sprintf("\n↳ %s is rate limited; retrying in %d s\n", lastSeg(orStr(model, "the model")), int(wait.Seconds()))))
				st.citedMu.Lock()
				st.cites.Drop()
				st.citedMu.Unlock()
				select {
				case <-ctx.Done():
				case <-time.After(wait):
				}
				continue
			}
			if generationError && attempt < 2 && ctx.Err() == nil && !spoken {
				if !streamed {
					dropTokenCap = true
				}
				if !rejectedTokenCap {
					retryUnstreamed = true
				}
				if rejectedTokenCap {
					m.noCap.remember(model)
				}
				m.A.Log.Warn("serve failed generating; retrying turn", "attempt", attempt, "dropTokenCap", dropTokenCap, "err", errText)
				st.citedMu.Lock()
				st.cites.Drop()
				st.citedMu.Unlock()
				continue
			}
			return nil, err
		}
	}

	toolCache := map[string]string{}
	var cacheMu sync.Mutex
	usedTools := false
	messages := st.messages
	push := func(ms ...llm.Message) { *messages = append(*messages, ms...) }
	toolCtx := func() ToolCtx {
		return ToolCtx{LabelScope: st.labelScope, Library: st.library, UserKey: st.pwToolKey, Model: strOrNil(model), ConversationID: strOrNil(st.convID), UserID: strOrNil(userID(r))}
	}

	if st.resume != nil {
		rs := st.resume
		ans := map[string]any{
			"conversationId": st.convID, "messageId": jsString(rs.MessageID), "toolCallId": jsString(rs.ToolCallID), "allowed": rs.Allowed == true,
		}
		if jsTruthy(rs.Tool) {
			ans["tool"] = jsString(rs.Tool)
		}
		if rs.Args != nil {
			ans["args"] = jsString(rs.Args)
		}
		feedback := ""
		if jsTruthy(rs.Feedback) {
			feedback = jsString(rs.Feedback)
			ans["feedback"] = jsval.Slice(feedback, 2000)
		}
		answered, err := m.Tools.Answer(r, ans, st.changeKey)
		if err != nil {
			return err
		}
		if !answered.OK || answered.Rec == nil {
			m.A.Log.Warn("held call answer refused", "toolCallId", jsString(rs.ToolCallID), "why", answered.Why)
			res.event("tool", reqbody.O("phase", "result", "id", jsString(rs.ToolCallID), "name", "", "summary", "not run: "+answered.Why, "error", true))
			res.event("error", reqbody.O("message", answered.Why))
			res.end()
			return nil
		}
		rec := answered.Rec
		m.A.Log.Info("held call answered", "tool", answered.Tool, "held", rec.Tool, "allowed", rs.Allowed == true, "failed", answered.Failed)
		*st.resumedFrom = sp(rec.MessageID)
		res.event("tool", reqbody.O("phase", "result", "id", rec.ToolCallID, "name", answered.Tool, "summary", answered.Summary, "error", answered.Failed))
		status, answer, decision := "ok", "Declined", "denied"
		if answered.Failed {
			status = "error"
		}
		if rs.Allowed == true {
			answer, decision = "Allowed", "approved"
		}
		d := &conversations.Decision{Status: decision, By: "user", At: nowISO()}
		if st.viewer != nil {
			d.Name = st.viewer.Name
		}
		if feedback != "" {
			d.Note = jsval.Slice(feedback, 200)
		}
		m.Conv.SettleHeld(rec.ConversationID, rec.MessageID, rec.ToolCallID, answered.Tool, status, answered.Summary, d, answer)
		push(rec.Turn...)
		push(llm.Message{Role: "tool", ToolCallID: rec.ToolCallID, Content: sp(answered.Result)})
		for _, g := range rec.Groups {
			if st.readerHas(g) && (st.platform || !contains(st.meta.PlatformGroups, g)) {
				st.addGroup(g)
			}
		}
		usedTools = true
	}

	hasTool := func() bool {
		for _, mm := range *messages {
			if mm.Role == "tool" {
				return true
			}
		}
		return false
	}
	if foldSystem && !hasTool() {
		names, _ := m.Tools.Names(r, st.readerTools, st.hasReader, []string{"search_kb"})
		if st.readerAllows("search_kb", names) {
			q := ""
			if st.lastUser != nil {
				q = jsTrim(st.lastUser.Text())
			}
			if q != "" && jsval.Len(q) > 12 {
				seedStart := time.Now()
				args, _ := jsonobj.Marshal(reqbody.O("query", jsval.Slice(q, 300)))
				seeded, err := m.Tools.Execute(r, "search_kb", string(args), toolCtx())
				if err == nil {
					st.timing("seed", time.Since(seedStart).Milliseconds(), "")
					callID := "seed-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
					zero := 0
					push(llm.Message{Role: "assistant", Content: nil, ToolCalls: []llm.ToolCall{{Index: &zero, ID: callID, Type: "function", Function: llm.Function{Name: "search_kb", Arguments: string(args)}}}})
					push(llm.Message{Role: "tool", ToolCallID: callID, Content: sp(seeded.Result)})
					st.citedMu.Lock()
					for _, f := range seeded.Files {
						st.cited.Known[f.Path] = deref(f.Library)
					}
					st.citedMu.Unlock()
					line := "\n↳ pre-searched the knowledge base\n"
					*st.finalReasoning += line
					res.event("tool", reqbody.O("phase", "result", "name", "search_kb", "summary", "pre-searched the knowledge base", "pretty", line))
				}
			}
		}
	}

	maxRounds := int(m.eff().MaxToolRounds)
	hitLimit := true
	for iter := 0; iter < maxRounds; iter++ {
		*st.turnBreak = true
		for _, s := range m.Conv.TakeSteers(st.convID) {
			push(llm.Message{Role: "user", Content: sp("I am adding this while you work; take it into account from here on: " + s.Text)})
			note := "\n\n*You added:* " + spaceRunRe.ReplaceAllString(s.Text, " ") + "\n\n"
			*st.finalContent += note
			res.event("content", reqbody.O("text", note))
			res.event("steer", reqbody.O("id", s.ID))
		}
		specs, err := m.Tools.Specs(r, st.readerTools, st.hasReader, st.platform, st.changes.OK, st.groupList())
		if err != nil {
			return err
		}
		available := specs.Available
		selected := orEmptySpecs(specs.Selected)
		sb, _ := jsonobj.Marshal(selected)
		maxTokens := m.fitWindow(*messages, model, jsval.Len(string(sb)))
		modelStart := time.Now()
		turn, err := turnWithRetry(payload{model: model, messages: *messages, tools: selected, hasTools: true, maxTokens: maxTokens, kwargs: m.templateKwargsFor(model), effort: st.effort})
		if err != nil {
			return err
		}
		st.flushCites()
		tcj, _ := jsonobj.Marshal(orEmptyCalls(turn.ToolCalls))
		st.timing("round", time.Since(modelStart).Milliseconds(), fmt.Sprintf("%dms in%d out%d calls%d", time.Since(modelStart).Milliseconds(),
			jsval.Len(string(msgsJSON(*messages)))+jsval.Len(string(sb)), jsval.Len(turn.Content)+jsval.Len(string(tcj)), len(turn.ToolCalls)))
		if turn.Usage != nil {
			*st.usage = addUsage(*st.usage, *turn.Usage)
		}
		if turn.Model != nil {
			*st.finalModel = turn.Model
		}
		if turn.FinishReason != nil && *turn.FinishReason == "tool_calls" && len(turn.ToolCalls) > 0 {
			usedTools = true
			calls := turn.ToolCalls
			iterParts := make([]conversations.Part, len(calls))
			for i, tc := range calls {
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("%s-%d-%d", tc.Function.Name, iter, i)
				}
				iterParts[i] = conversations.Part{Kind: "tool_call", ID: id, Name: tc.Function.Name, Args: tc.Function.Arguments, Status: "ok"}
			}
			for i := range calls {
				if calls[i].ID == "" {
					calls[i].ID = iterParts[i].ID
				}
			}
			var content *string
			if turn.Content != "" {
				content = sp(turn.Content)
			}
			push(llm.Message{Role: "assistant", Content: content, ToolCalls: calls})
			base := len(*st.finalParts)
			*st.finalParts = append(*st.finalParts, iterParts...)
			parts := (*st.finalParts)[base:]
			for i, tc := range calls {
				m.A.Log.Info("tool call", "tool", tc.Function.Name, "args", jsval.Slice(tc.Function.Arguments, 300), "iter", iter)
				st.markThinking()
				res.event("tool", reqbody.O("phase", "call", "id", parts[i].ID, "name", tc.Function.Name, "args", tc.Function.Arguments))
			}
			var callNames []string
			for _, tc := range calls {
				callNames = append(callNames, tc.Function.Name)
			}
			names, err := m.Tools.Names(r, st.readerTools, st.hasReader, callNames)
			if err != nil {
				return err
			}
			type heldCall struct {
				i         int
				tool      string
				args      json.RawMessage
				canonical string
			}
			var held *heldCall
			notRun := map[int]*Outcome{}
			for i, tc := range calls {
				name := tc.Function.Name
				if !names.AsksFirst[name] || !st.readerAllows(name, names) {
					continue
				}
				why := ""
				gate := st.changes
				if isKBAsksFirst(name) {
					gate = st.kbChanges
				}
				switch {
				case !gate.OK:
					why = name + " is not available in this chat: " + gate.Why + ". Nothing ran."
				case !hasSpec(available, name):
					why = name + " is turned off for this deployment. Nothing ran."
				case st.convID == "":
					why = name + " needs a saved conversation to ask the user in. Nothing ran."
				case held != nil:
					why = "Only one change waits for the user's approval at a time, so " + name + " did not run. Call it again after the user answers " + held.tool + "."
				}
				if why != "" {
					notRun[i] = &Outcome{Result: why, Summary: "not run", Failed: true}
					continue
				}
				prep, err := m.Tools.Prepare(r, name, tc.Function.Arguments, deref(st.changeKey))
				if err != nil {
					return err
				}
				if prep.Problem != "" {
					notRun[i] = &Outcome{Result: prep.Problem, Summary: "not run", Failed: true}
					continue
				}
				held = &heldCall{i, name, prep.Args, prep.Canonical}
			}
			outcomes := make([]*Outcome, len(calls))
			toolStart := time.Now()
			st.timing("calls", int64(len(calls)), "")
			var cursorMu sync.Mutex
			cursor := 0
			var wg sync.WaitGroup
			workers := min(6, len(calls))
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						cursorMu.Lock()
						i := cursor
						cursor++
						cursorMu.Unlock()
						if i >= len(calls) {
							return
						}
						outcomes[i] = m.runCall(r, st, calls[i], i, names, available, notRun, held != nil && held.i == i, toolCache, &cacheMu, toolCtx)
					}
				}()
			}
			wg.Wait()
			st.timing("tools", time.Since(toolStart).Milliseconds(), "")
			for i, tc := range calls {
				if held != nil && held.i == i {
					parts[i].Status = "waiting"
					continue
				}
				o := outcomes[i]
				parts[i].Status = "ok"
				if o.Failed {
					parts[i].Status = "error"
				}
				parts[i].Result = sp(o.Summary)
				hasGufi := len(o.Gufi) > 0 && string(o.Gufi) != "[]" && string(o.Gufi) != "null"
				if hasGufi {
					parts[i].Gufi = o.Gufi
					if st.viewer != nil {
						parts[i].GufiViewer = st.viewer.ID
					}
				}
				if len(o.Files) > 0 {
					parts[i].Files = o.Files
					st.citedMu.Lock()
					for _, f := range o.Files {
						st.cited.Known[f.Path] = deref(f.Library)
					}
					st.citedMu.Unlock()
				}
				ev := reqbody.O("phase", "result", "id", parts[i].ID, "name", tc.Function.Name, "summary", o.Summary, "error", o.Failed)
				if hasGufi {
					ev.Set("gufi", o.Gufi)
				}
				res.event("tool", ev)
				push(llm.Message{Role: "tool", ToolCallID: tc.ID, Content: sp(o.Result)})
			}
			if held != nil && st.viewer != nil && st.convID != "" {
				id := parts[held.i].ID
				messageID := "srv-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
				turnMsgs := append([]llm.Message{}, (*messages)[st.baseLen:]...)
				approval, err := m.Tools.Hold(r, map[string]any{
					"conversationId": st.convID, "messageId": messageID, "toolCallId": id, "tool": held.tool, "canonical": held.canonical,
					"args": held.args, "turn": turnMsgs, "groups": st.groupList(),
				})
				if err != nil {
					return err
				}
				var part conversations.Part
				_ = json.Unmarshal(approval, &part)
				*st.finalParts = append(*st.finalParts, part)
				m.A.Log.Info("tool call held for approval", "tool", held.tool, "conversation", st.convID)
				res.event("tool", reqbody.O("phase", "wait", "id", id, "name", held.tool))
				res.event("approval", reqbody.O("approval", approval))
				st.finish(sp("tool_approval"), messageID)
				return nil
			}
			continue
		}
		if jsTrim(turn.Content) == "" || (usedTools && jsval.Len(jsTrim(turn.Content)) < 200) {
			m.A.Log.Warn("empty turn with no tool calls; forcing final answer", "iter", iter)
			hitLimit = false
			break
		}
		st.finish(turn.FinishReason, "")
		return nil
	}
	if hitLimit {
		m.A.Log.Warn("tool round limit reached", "maxRounds", maxRounds)
	}
	m.A.Log.Warn("forcing final answer (budget exhausted or empty turn)")
	push(llm.Message{Role: "user", Content: sp("No further tool calls are available for this reply. Give your final answer now, using only the information gathered above. State plainly anything you could not verify.")})
	var gathered []string
	n := 0
	for _, mm := range *messages {
		if mm.Role == "tool" && mm.Content != nil && *mm.Content != TrimStub {
			n++
			gathered = append(gathered, fmt.Sprintf("[%d] %s", n, *mm.Content))
		}
	}
	g := strings.Join(gathered, "\n\n")
	if g == "" {
		g = "(no tool results were collected)"
	}
	finalMessages := []llm.Message{{Role: "system", Content: sp("Answer the user using the gathered material below. State plainly anything the material does not cover.")}}
	if st.lastUser != nil {
		finalMessages = append(finalMessages, *st.lastUser)
	}
	finalMessages = append(finalMessages,
		llm.Message{Role: "assistant", Content: sp("I looked through the knowledge base and gathered this material:\n\n" + g)},
		llm.Message{Role: "user", Content: sp("Based on that material, give your final answer to my question now. State plainly anything you could not verify.")})
	var finalTurn *llm.Turn
	for attempt := 0; attempt < 2; attempt++ {
		*st.turnBreak = true
		mt := m.fitWindow(finalMessages, model, 0)
		t, err := turnWithRetry(payload{model: model, messages: finalMessages, maxTokens: mt, kwargs: m.templateKwargsFor(model), effort: st.effort})
		if err != nil {
			return err
		}
		finalTurn = t
		st.flushCites()
		if t.Model != nil {
			*st.finalModel = t.Model
		}
		if t.Usage != nil {
			*st.usage = addUsage(*st.usage, *t.Usage)
		}
		st.timing("final", 0, "")
		if jsTrim(t.Content) != "" {
			break
		}
		m.A.Log.Warn("forced final answer came back empty", "attempt", attempt)
		finalMessages = append(finalMessages, llm.Message{Role: "user", Content: sp("Reply with the final answer as plain text.")})
	}
	if hitLimit {
		note := fmt.Sprintf("\n\n*Stopped after %d tool rounds, the limit set under Settings, Assistant tools. Ask to continue and the assistant picks up from here.*", maxRounds)
		*st.finalContent += note
		res.event("content", reqbody.O("text", note))
	}
	fr := sp("stop")
	if finalTurn != nil && finalTurn.FinishReason != nil {
		fr = finalTurn.FinishReason
	}
	st.finish(fr, "")
	return nil
}

func orEmptyCalls(c []llm.ToolCall) []llm.ToolCall {
	if c == nil {
		return []llm.ToolCall{}
	}
	return c
}

// liveTools read something that changes while a reply is written: an
// agent, a run, a queue. The same call made again is a second look, so it
// runs again and is never answered from the reply's earlier calls.
var liveTools = map[string]bool{
	"agent_status": true, "agent_sessions": true, "watch_run": true, "workflow_runs": true, "workflow_run_detail": true,
	"list_sessions": true, "list_clusters": true, "hpc_status": true, "task_status": true, "cluster_command": true,
}

// changingTools change what other tools read: a file, a label, an agent,
// a run, a cluster. After one runs, a read made before it is made again
// when the model repeats it, and not repeated from before the change.
var changingTools = map[string]bool{
	"write_kb_file": true, "edit_kb_file": true, "move_kb_files": true, "rename_kb_file": true, "copy_kb_files": true, "delete_kb_files": true, "apply_labels": true,
	"start_agent": true, "agent_steer": true, "agent_goal": true, "agent_approve": true, "agent_stop": true, "delegate": true,
	"run_workflow": true, "serve_model": true, "cancel_workflow_run": true,
	"start_cluster": true, "resume_cluster": true, "stop_cluster": true, "destroy_cluster": true,
}

// repeatedCall is the result of this exact call made earlier in the
// reply, when it still stands.
func repeatedCall(cache map[string]string, mu *sync.Mutex, name, args string) (string, bool) {
	if liveTools[name] {
		return "", false
	}
	mu.Lock()
	defer mu.Unlock()
	hit, ok := cache[name+":"+args]
	return hit, ok
}

// rememberCall keeps a call's result for the rest of the reply. A call
// that changes something first drops the results kept before it.
func rememberCall(cache map[string]string, mu *sync.Mutex, name, args, result string) {
	mu.Lock()
	defer mu.Unlock()
	if changingTools[name] {
		clear(cache)
	}
	cache[name+":"+args] = result
}

// runCall is one call's outcome, as the route's worker computes it.
func (m *Module) runCall(r *http.Request, st *loopState, tc llm.ToolCall, i int, names *Names, available []Spec, notRun map[int]*Outcome, isHeld bool,
	cache map[string]string, cacheMu *sync.Mutex, toolCtx func() ToolCtx) *Outcome {
	name := tc.Function.Name
	if name == st.meta.MoreTools {
		group := ""
		a := tc.Function.Arguments
		if a == "" {
			a = "{}"
		}
		var v map[string]any
		if json.Unmarshal([]byte(a), &v) == nil && v != nil {
			if g, ok := v["group"]; ok && g != nil {
				group = jsString(g)
			}
		}
		def, isGroup := st.meta.Groups[group]
		if isGroup && st.hasReader && !contains(st.readerTools, group) {
			return &Outcome{Result: "The reader switched off the " + group + " tools for this chat. Answer without them, or say what switching them on would let you do.", Summary: "switched off by the reader", Failed: true}
		}
		if isGroup {
			any := false
			for _, s := range available {
				if contains(def, specName(s)) {
					any = true
					break
				}
			}
			if !any {
				return &Outcome{Result: "The " + group + " tools are not available in this Studio. Answer with the tools you have.", Summary: "not available", Failed: true}
			}
			st.addGroup(group)
			return &Outcome{Result: "Added the " + group + " tools: " + strings.Join(def, ", ") + ". Use them in your next step.", Summary: "added " + group + " tools"}
		}
		return &Outcome{Result: "No tool group \"" + group + "\". Groups: " + strings.Join(st.meta.GroupOrder, ", ") + ".", Summary: "unknown group", Failed: true}
	}
	if o := notRun[i]; o != nil {
		return o
	}
	if isHeld {
		return &Outcome{Result: "", Summary: "waiting for approval"}
	}
	if !st.readerAllows(name, names) {
		return &Outcome{Result: "The reader switched off " + names.Labels[name] + " for this chat, so " + name + " did not run. Answer without it, or say what switching it on would let you do.", Summary: "switched off by the reader", Failed: true}
	}
	if !hasSpec(available, name) {
		return &Outcome{Result: name + " is not available in this Studio, so it did not run. Answer with the tools you have.", Summary: "not available", Failed: true}
	}
	if g := names.GroupOf[name]; g != nil && *g != "" {
		st.addGroup(*g)
	}
	if hit, ok := repeatedCall(cache, cacheMu, name, tc.Function.Arguments); ok {
		return &Outcome{Result: "[You already made this exact call in this reply; identical result repeated below. Do not repeat it again.]\n" + hit, Summary: "repeated call (cached)"}
	}
	out, err := m.Tools.Execute(r, name, tc.Function.Arguments, toolCtx())
	if err != nil {
		return &Outcome{Result: "tool failed: " + err.Error(), Summary: "failed", Failed: true}
	}
	rememberCall(cache, cacheMu, name, tc.Function.Arguments, out.Result)
	st.markThinking()
	return out
}
