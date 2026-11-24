package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
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
	library        []string
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
	// A tool request of the model's that could not be read is asked for
	// again once per turn; after that it is withheld, and its notice is
	// the answer (withheld keeps the notice from being taken for a stub).
	askedAgain := false
	// partRows is how many rows to ask for in each part, from the last
	// request cut off at the output limit (0 before one is).
	partRows := 0
	withheld := false
	// heldText: on the emulated path a round's text waits until the loop
	// knows what the round was, and is shown only when it stands (beside
	// the round's calls, or as the answer), not when the model is asked
	// again. pendingNote is a withheld request's notice, shown only if the
	// turn ends without getting further.
	heldText := ""
	pendingNote := ""

	turnWithRetry := func(p payload) (*llm.Turn, error) {
		nonces := append(turnNonces(p.messages, st.baseLen), nonce)
		withheld = false
		heldText = ""
		pendingNote = ""
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
					prose, calls := splitEnvelope(text, nonces, offered, p.hasTools)
					if calls == nil && p.hasTools {
						// A reply that ended on its own (not at the output
						// limit) but lacks its last closing brackets is
						// read with them added.
						if be, ok := brokenEnvelope(text, nonces); ok && deref(t.FinishReason) != "length" {
							if closed, ok := closeEnvelope(text[be.at:]); ok {
								if pr, cs := splitEnvelope(strings.TrimSpace(text[:be.at]+"\n"+closed), nonces, offered, p.hasTools); cs != nil {
									m.A.Log.Info("model's tool request read once its closing brackets were added", "model", model, "tool", be.tool)
									prose, calls = pr, cs
								}
							}
						}
					}
					if calls == nil && p.hasTools {
						if be, ok := brokenEnvelope(text, nonces); ok {
							why := deref(t.FinishReason)
							cut := be.cutOff || why == "length"
							if cut {
								partRows = rowsPerPart(text[be.at:])
							}
							m.A.Log.Warn("model's tool request could not be read", "model", model, "tool", be.tool, "finish", why, "cutOff", cut, "chars", jsval.Len(text), "attempt", attempt)
							// What it was, for extending the mender to the
							// cases models produce; it can hold file content,
							// so only at debug.
							errAt, near := envelopeDecodeErr(text[be.at:])
							m.A.Log.Debug("unreadable tool request", "model", model, "error", errAt, "near", near,
								"head", jsval.Slice(text[be.at:], 200), "tail", tailChars(text, 200))
							if !askedAgain && ctx.Err() == nil {
								askedAgain = true
								line := "\n↳ " + upperFirst(be.asked()) + " could not be read; asking it again\n"
								if cut {
									line = "\n↳ " + upperFirst(be.asked()) + " was cut off; asking it again in smaller parts\n"
									p.maxTokens = m.raisedCap(p, model)
								}
								*st.finalReasoning += line
								res.event("reasoning", reqbody.O("text", line))
								p.messages = append(append([]llm.Message{}, p.messages...), llm.Message{Role: "system", Content: sp(askAgainNote(be, cut, partRows))})
								continue
							}
							withheld = true
							prose = strings.TrimSpace(text[:be.at])
							note := upperFirst(be.asked()) + " could not be read, so nothing ran."
							line := "\n↳ " + upperFirst(be.asked()) + " could not be read again; nothing ran\n"
							if cut {
								note = upperFirst(be.asked()) + " was cut off at the model's output limit, so nothing ran. Ask for it in smaller parts, for example a few sheets or sections at a time."
								line = "\n↳ " + upperFirst(be.asked()) + " was cut off again; nothing ran\n"
							}
							*st.finalReasoning += line
							res.event("reasoning", reqbody.O("text", line))
							pendingNote = note
						}
					}
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
					heldText = prose
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
	// afterChange: the latest tool results came from a call that changed
	// something, or from an answer to a held call. A short reply then is a
	// confirmation, and stands as the answer.
	afterChange := false
	messages := st.messages
	push := func(ms ...llm.Message) { *messages = append(*messages, ms...) }
	rowsAsked, rowsFile := 0, ""
	if st.lastUser != nil {
		rowsAsked, rowsFile = rowCap(st.lastUser.Text())
	}
	toolCtx := func() ToolCtx {
		return ToolCtx{LabelScope: st.labelScope, Library: st.library, UserKey: st.pwToolKey, Model: strOrNil(model), ConversationID: strOrNil(st.convID), UserID: strOrNil(userID(r)), RowsAsked: rowsAsked, RowsFile: rowsFile}
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
		afterChange = true
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
			if q != "" && jsval.Len(q) > 12 && !noSearchRe.MatchString(q) {
				seedStart := time.Now()
				args, _ := jsonobj.Marshal(reqbody.O("query", jsval.Slice(q, 300)))
				seeded, err := m.Tools.Execute(r, "search_kb", string(args), toolCtx())
				if err == nil {
					st.timing("seed", time.Since(seedStart).Milliseconds(), "")
					callID := "seed-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
					zero := 0
					push(llm.Message{Role: "assistant", Content: nil, ToolCalls: []llm.ToolCall{{Index: &zero, ID: callID, Type: "function", Function: llm.Function{Name: "search_kb", Arguments: string(args)}}}})
					push(llm.Message{Role: "tool", ToolCallID: callID, Content: sp(presearchNote + seeded.Result)})
					st.citedMu.Lock()
					for _, f := range seeded.Files {
						st.cited.Known[f.Path] = deref(f.Library)
					}
					st.citedMu.Unlock()
					// The Studio's own search, not one the model chose: said
					// as a line of the reply's progress, not as a tool call
					// of the model's.
					line := "\n↳ Studio searched your libraries first, for the model to answer from\n"
					*st.finalReasoning += line
					res.event("reasoning", reqbody.O("text", line))
				}
			}
		}
	}

	maxRounds := int(m.eff().MaxToolRounds)
	hitLimit := true
	// nudged: a short or empty turn with no request was answered with a
	// note to make the request (emulated path); it is allowed again once a
	// round has made calls since, up to maxNudges in a turn.
	nudged := false
	nudges := 0
	// changed: a call this turn changed something (a file was written),
	// so the final answer reports what was done rather than what could
	// not be verified. lastWrite is the latest write_kb_file result.
	changed := false
	lastWrite := ""
	// asked is how many rows the user's message asks for ("a sheet of 250
	// rows"), 0 when it names none; held is how many the workbook last
	// written holds after its header (heldRows), -1 before one is written.
	asked := 0
	if st.lastUser != nil {
		asked = askedRows(st.lastUser.Text())
	}
	heldRows, heldPath := -1, ""
	// fileAsked is a file the user's message asks to have made ("write a
	// spreadsheet reports/sales.xlsx"), "" when it names none.
	fileAsked := ""
	if st.lastUser != nil {
		fileAsked = askedFile(st.lastUser.Text())
	}
	shortNudges := 0
	// overNudged: a workbook holding more rows than asked for was answered
	// once with a note to write it again with the count asked.
	overNudged := false
	// closeRows ends a turn that wrote a workbook against a count of rows
	// with what it holds, from the last write's result, unless the answer
	// already gives that count: the model's last words can predate the
	// results of its last parts.
	closeRows := func() {
		if asked <= 0 || heldRows < 0 || strings.Contains(*st.finalContent, strconv.Itoa(heldRows)) {
			return
		}
		line := fmt.Sprintf("%s holds %d of the %d rows asked for.", orStr(heldPath, "The workbook"), heldRows, asked)
		if heldRows > asked {
			line = fmt.Sprintf("%s holds %d rows; %d were asked for.", orStr(heldPath, "The workbook"), heldRows, asked)
		}
		note := turnSeparator(*st.finalContent) + line
		*st.finalContent += note
		res.event("content", reqbody.O("text", note))
	}
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
		// The text written beside a round's calls is shown, as a natively
		// calling model's would be.
		showHeld := func() {
			if heldText != "" {
				st.callbacks.OnContent(heldText)
				st.flushCites()
				heldText = ""
			}
		}
		if turn.FinishReason != nil && *turn.FinishReason == "tool_calls" && len(turn.ToolCalls) > 0 {
			showHeld()
			pendingNote = ""
			usedTools = true
			nudged = false
			calls := turn.ToolCalls
			afterChange = false
			for _, tc := range calls {
				if changingTools[tc.Function.Name] {
					afterChange = true
				}
			}
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
			var heldWhere *whereDecision
			notRun := map[int]*Outcome{}
			for i, tc := range calls {
				name := tc.Function.Name
				// A new file whose library is not clear waits for the
				// person to say where (where.go).
				if name == "write_kb_file" && held == nil && st.convID != "" && st.kbChanges.OK && st.readerAllows(name, names) {
					if d := m.writeAsks(r, toolCtx(), tc.Function.Arguments); d != nil {
						held, heldWhere = &heldCall{i, name, json.RawMessage(tc.Function.Arguments), tc.Function.Arguments}, d
						continue
					}
				}
				if !names.AsksFirst[name] || !st.readerAllows(name, names) {
					continue
				}
				why := ""
				gate := st.changes
				if localAsksFirst(name) {
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
			// Writes to library files run one at a time in the order asked,
			// so parts appended in one round land in order and each result
			// says what the file holds after it; other calls run beside them.
			var par, seq []int
			for i, tc := range calls {
				if tc.Function.Name == "write_kb_file" {
					seq = append(seq, i)
				} else {
					par = append(par, i)
				}
			}
			run := func(i int) {
				outcomes[i] = m.runCall(r, st, calls[i], i, names, available, notRun, held != nil && held.i == i, toolCache, &cacheMu, toolCtx)
			}
			var cursorMu sync.Mutex
			cursor := 0
			var wg sync.WaitGroup
			if len(seq) > 0 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					var done []sentPart
					for _, i := range seq {
						part, ok := workbookPart(calls[i].Function.Arguments)
						if ok {
							if j := part.repeats(done); j >= 0 && !outcomes[j].Failed {
								m.A.Log.Info("tool call not run: the rows of a part written earlier in this reply", "tool", calls[i].Function.Name)
								outcomes[i] = &Outcome{Result: "[Not run: these rows are the same as the rows of an earlier write to " + part.path + " in this reply, so adding them would repeat them in the workbook. Its result is below; add the next rows instead.]\n" + outcomes[j].Result, Summary: "not run: the same rows as an earlier part"}
								continue
							}
						}
						run(i)
						if ok && !outcomes[i].Failed {
							part.i = i
							done = append(done, part)
						}
					}
				}()
			}
			workers := min(6, len(par))
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						cursorMu.Lock()
						k := cursor
						cursor++
						cursorMu.Unlock()
						if k >= len(par) {
							return
						}
						run(par[k])
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
				} else if changingTools[tc.Function.Name] {
					changed = true
					if tc.Function.Name == "write_kb_file" {
						lastWrite = jsval.Slice(o.Result, 400)
						if n, ok := rowsHeld(o.Result); ok {
							heldRows, heldPath = n, writePath(tc.Function.Arguments)
						}
					}
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
				result := o.Result
				if tc.Function.Name == "write_kb_file" && !o.Failed && asked > 0 {
					if n, ok := rowsHeld(o.Result); ok {
						result += rowsAgainstAsked(n, asked)
					}
				}
				push(llm.Message{Role: "tool", ToolCallID: tc.ID, Content: sp(result)})
			}
			if held != nil && st.viewer != nil && st.convID != "" {
				id := parts[held.i].ID
				messageID := "srv-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
				turnMsgs := append([]llm.Message{}, (*messages)[st.baseLen:]...)
				in := map[string]any{
					"conversationId": st.convID, "messageId": messageID, "toolCallId": id, "tool": held.tool, "canonical": held.canonical,
					"args": held.args, "turn": turnMsgs, "groups": st.groupList(),
				}
				if heldWhere != nil {
					in["where"], in["whereWhy"] = heldWhere.options, heldWhere.why
				}
				approval, err := m.Tools.Hold(r, in)
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
		// A workbook short of the rows the user asked for, with no request
		// to add them, is not the end: the model is asked to add the rest
		// (emulated path), whatever its reply says.
		// It is sent again after a reply that stops short (a request cut
		// off, or text giving up), up to maxShortNudges.
		if foldSystem && asked > 0 && heldRows >= 0 && heldRows < asked && shortNudges < maxShortNudges && iter < maxRounds-1 && ctx.Err() == nil {
			nudged = true
			shortNudges++
			m.A.Log.Warn("workbook short of the rows asked; asking for the rest", "iter", iter, "held", heldRows, "asked", asked)
			if c := jsTrim(turn.Content); c != "" {
				push(llm.Message{Role: "assistant", Content: sp(c)})
			}
			push(llm.Message{Role: "system", Content: sp(fmt.Sprintf("%s holds %d of the %d rows the user asked for, so it is not finished. Add the next %d rows now: write_kb_file with the same path, append=true and the same sheet name, the rows as values%s, one request after another, until it holds all %d. Then answer.", orStr(heldPath, "The workbook"), heldRows, asked, asked-heldRows, perPart(orInt(partRows, 50)), asked))})
			continue
		}
		// A workbook holding more rows than the user asked for is written
		// again with the count asked, once (emulated path); the answer
		// says the count either way (closeRows).
		if foldSystem && asked > 0 && heldRows > asked && !overNudged && iter < maxRounds-1 && ctx.Err() == nil {
			overNudged = true
			m.A.Log.Warn("workbook over the rows asked; asking for it again", "iter", iter, "held", heldRows, "asked", asked)
			if c := jsTrim(turn.Content); c != "" {
				push(llm.Message{Role: "assistant", Content: sp(c)})
			}
			push(llm.Message{Role: "system", Content: sp(fmt.Sprintf("%s holds %d rows after the header; the user asked for %d. Write it again with exactly %d: write_kb_file with the same path and overwrite=true, the header and the first rows%s, then append=true with the next rows until it holds %d. Then answer.", orStr(heldPath, "The workbook"), heldRows, asked, asked, perPart(orInt(partRows, 50)), asked))})
			continue
		}
		// A file the user asked to have made, of which nothing has been
		// written, is not given up on in a reply without a request: the
		// model is told it does not exist until written (emulated path).
		if foldSystem && fileAsked != "" && !changed && !nudged && nudges < maxNudges && iter < maxRounds-1 && ctx.Err() == nil {
			nudged = true
			nudges++
			m.A.Log.Warn("file asked for and not written; asking for the write", "iter", iter, "nudge", nudges)
			if c := jsTrim(turn.Content); c != "" {
				push(llm.Message{Role: "assistant", Content: sp(c)})
			}
			push(llm.Message{Role: "system", Content: sp("The user asked you to make " + fileAsked + ", and nothing has been written yet. Searching will not find it: it does not exist until you write it. You have write_kb_file: write it now (a large file in parts, the first part, then append=true for each next part), then answer.")})
			continue
		}
		// A withheld request's notice is the answer when the turn ends here.
		if pendingNote != "" {
			if asked > 0 && heldRows >= 0 && heldRows < asked {
				pendingNote = fmt.Sprintf("%s holds %d of the %d rows asked for. The request to add the rest was cut off at the model's output limit, so nothing more was written; ask again to add the rest.", orStr(heldPath, "The workbook"), heldRows, asked)
			}
			if jsTrim(turn.Content) != "" {
				turn.Content += "\n\n"
				heldText += "\n\n"
			}
			turn.Content += pendingNote
			heldText += pendingNote
			pendingNote = ""
		}
		if jsTrim(turn.Content) == "" || (usedTools && !afterChange && !withheld && !showsSomething.MatchString(turn.Content) && jsval.Len(jsTrim(turn.Content)) < 200) {
			// A model on the text protocol that says "I'll write it now"
			// and stops is asked once to make the request, with its tools,
			// rather than forced to a final answer without them.
			if foldSystem && !nudged && nudges < maxNudges && iter < maxRounds-1 && ctx.Err() == nil && (jsTrim(turn.Content) == "" || announcesRe.MatchString(turn.Content)) {
				nudged = true
				nudges++
				m.A.Log.Warn("short turn with no request; asking for the request", "iter", iter, "nudge", nudges)
				if c := jsTrim(turn.Content); c != "" {
					push(llm.Message{Role: "assistant", Content: sp(c)})
				}
				note := nudgeNote
				if lastWrite != "" {
					note += " The last write's result was: " + lastWrite + " If that file does not yet hold all the user asked for, send its next part now (write_kb_file, append=true, the same path and sheet name)."
				}
				push(llm.Message{Role: "system", Content: sp(note)})
				continue
			}
			m.A.Log.Warn("empty turn with no tool calls; forcing final answer", "iter", iter)
			hitLimit = false
			break
		}
		showHeld()
		closeRows()
		st.finish(turn.FinishReason, "")
		return nil
	}
	if hitLimit {
		m.A.Log.Warn("tool round limit reached", "maxRounds", maxRounds)
	}
	m.A.Log.Warn("forcing final answer (budget exhausted or empty turn)")
	closing := "State plainly anything you could not verify."
	if changed {
		// After a write the Studio made, the answer says what was done,
		// with the tool results' figures, not doubts about its own file.
		closing = "Say what was done, with the figures the tool results gave (such as how many rows a file holds), and plainly what was asked for that was not done. Tools are left out of this last step only: do not tell the user they are unavailable."
	}
	push(llm.Message{Role: "user", Content: sp("No further tool calls are available for this reply. Give your final answer now, using only the information gathered above. " + closing)})
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
		llm.Message{Role: "assistant", Content: sp(gatheredLead(changed) + g)},
		llm.Message{Role: "user", Content: sp("Based on that material, give your final answer to my question now. " + closing)})
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
	closeRows()
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

// showsSomething: a reply that embeds something (a session card, a
// document page, a DAG) shows the user what was asked for, and stands
// however short its words are. Forced into a second answer, the model put
// the card in again and said it could not check that it shows.
var showsSomething = regexp.MustCompile(`!\[[^\]]*\]\(/\?embed=`)

// liveTools read something that changes while a reply is written: an
// agent, a run, a queue. The same call made again is a second look, so it
// runs again and is never answered from the reply's earlier calls.
var liveTools = map[string]bool{
	"agent_status": true, "agent_sessions": true, "watch_run": true, "workflow_runs": true, "workflow_run_detail": true,
	"list_sessions": true, "list_clusters": true, "hpc_status": true, "task_status": true, "cluster_command": true,
	"transfer_status": true, "browse_location": true,
}

// changingTools change what other tools read: a file, a label, an agent,
// a run, a cluster. After one runs, a read made before it is made again
// when the model repeats it, and not repeated from before the change.
var changingTools = map[string]bool{
	"write_kb_file": true, "edit_kb_file": true, "move_kb_files": true, "rename_kb_file": true, "copy_kb_files": true, "delete_kb_files": true, "apply_labels": true,
	"start_agent": true, "agent_steer": true, "agent_goal": true, "agent_approve": true, "agent_stop": true, "delegate": true,
	"run_workflow": true, "serve_model": true, "cancel_workflow_run": true,
	"start_cluster": true, "resume_cluster": true, "stop_cluster": true, "destroy_cluster": true,
	"transfer_files": true, "cancel_transfer": true, "save_transfer_location": true,
	"save_chat_to_library": true,
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
		m.A.Log.Info("tool call not run: the same call made earlier in this reply", "tool", name)
		return &Outcome{Result: "[Not run: you already made this exact call in this reply, and its result is below. Asking again gives the same. Go on with the task instead: a file the user asked you to make does not exist until you write it with write_kb_file.]\n" + hit, Summary: "repeated call (cached)"}
	}
	if hit, ok := askedTwice(cache, cacheMu, name, tc.Function.Arguments); ok {
		m.A.Log.Info("tool call not run: asks what two earlier calls asked", "tool", name)
		return &Outcome{Result: "[Not run: this asks the same as two calls before it in this reply, with only how many results or the argument's name changed, and they gave the result below. Asking again gives the same. Go on with the task instead: a file the user asked you to make does not exist until you write it with write_kb_file.]\n" + hit, Summary: "same as before, not run"}
	}
	out, err := m.Tools.Execute(r, name, tc.Function.Arguments, toolCtx())
	if err != nil {
		return &Outcome{Result: "tool failed: " + err.Error(), Summary: "failed", Failed: true}
	}
	rememberCall(cache, cacheMu, name, tc.Function.Arguments, out.Result)
	rememberAsked(cache, cacheMu, name, tc.Function.Arguments, out.Result)
	st.markThinking()
	return out
}

// countArgs are the arguments that only say how many results to give;
// calls that differ in them alone ask the same thing.
var countArgs = map[string]bool{"limit": true, "max_results": true, "top_k": true, "k": true, "count": true, "n": true, "max": true}

// askedKey is a call by what it asks: its tool and its argument values,
// without those that only say how many, whatever the arguments are named
// ({"name":"a.xlsx"} and {"query":"a.xlsx"} ask the same).
func askedKey(name, args string) string {
	var v map[string]any
	if json.Unmarshal([]byte(args), &v) != nil {
		return ""
	}
	var vals []string
	for k, x := range v {
		if countArgs[strings.ToLower(k)] {
			continue
		}
		if s, ok := x.(string); ok {
			vals = append(vals, strings.ToLower(strings.TrimSpace(s)))
			continue
		}
		b, _ := json.Marshal(x)
		vals = append(vals, string(b))
	}
	sort.Strings(vals)
	return "\x00asked:" + name + ":" + strings.Join(vals, "\x00")
}

// askedTwice is the result of a call that asks what two calls before it
// in this reply asked: a reading call, not run a third time.
func askedTwice(cache map[string]string, mu *sync.Mutex, name, args string) (string, bool) {
	if liveTools[name] || changingTools[name] {
		return "", false
	}
	k := askedKey(name, args)
	if k == "" {
		return "", false
	}
	mu.Lock()
	defer mu.Unlock()
	if cache[k+"\x00n"] == "2" {
		return cache[k], true
	}
	return "", false
}

// rememberAsked counts a reading call by what it asks.
func rememberAsked(cache map[string]string, mu *sync.Mutex, name, args, result string) {
	if liveTools[name] || changingTools[name] {
		return
	}
	k := askedKey(name, args)
	if k == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if cache[k+"\x00n"] == "" {
		cache[k+"\x00n"] = "1"
	} else {
		cache[k+"\x00n"] = "2"
	}
	cache[k] = result
}

// emNonceRe reads the nonce from an emulated call's id, em-<nonce>-<ms>-<i>.
var emNonceRe = regexp.MustCompile(`^em-([0-9a-f]+)-\d+-\d+$`)

// turnNonces are the nonces issued earlier in this turn: those of the
// emulated calls among its messages (from base on; a turn resumed after
// an approval carries its calls from before). Nonces of earlier turns,
// before base, and any quoted in a file are not among them.
func turnNonces(messages []llm.Message, base int) []string {
	var out []string
	if base < 0 || base > len(messages) {
		return nil
	}
	for _, mm := range messages[base:] {
		for _, tc := range mm.ToolCalls {
			if x := emNonceRe.FindStringSubmatch(tc.ID); x != nil && !contains(out, x[1]) {
				out = append(out, x[1])
			}
		}
	}
	return out
}

// raisedCap is the output limit for asking again after a request was cut
// off: twice the turn's, up to 32768 tokens, and no more than the model's
// window leaves when that is known. A provider that refuses it is asked
// without a limit (the token cap retry above).
func (m *Module) raisedCap(p payload, model string) int {
	up := min(max(p.maxTokens, MaxCompletionTokens)*2, 32768)
	if w := m.windowFor(model); w > 0 {
		up = min(up, w-estimateTokens(p.messages)-512)
	}
	return max(up, p.maxTokens)
}

// askAgainNote tells the model why its request did not run and how to
// send it so that it does.
func askAgainNote(e brokenEnv, cut bool, rows int) string {
	what := "Your last reply was a tool request"
	if e.tool != "" {
		what += " for " + e.tool
	}
	if e.path != "" {
		what += " (" + e.path + ")"
	}
	parts := "Write a large file in parts: call write_kb_file with the first part, then again with append=true for each next part, each part under about 12,000 characters. A spreadsheet's part is rows as JSON {\"sheets\":[{\"name\":\"...\",\"rows\":[...]}]}; a part's rows are added to the sheet of the same name, as values (not a formula that makes the rows). The file is not done until every part is in: each write's result says how many rows each sheet holds, so after each part, if the sheet holds fewer rows than were asked for, send the next part at once (write_kb_file with append=true, the same path and sheet name, the next rows), before any reply to the user; tell the user it is done only when it holds them all, with the count the result gave."
	if cut {
		return what + " that was cut off at the output limit before it ended, so nothing ran. Send it again with less in each request" + perPart(rows) + ". " + parts
	}
	return what + " that could not be read as JSON, so nothing ran. Send it again as one JSON object: inside a string value, such as a file's content, write every quote as \\\" and every line break as \\n. " + parts
}

// noSearchRe: the user asked for no search first ("do not search", "don't
// search the library", "without searching"), so the Studio does not run its
// own before the model's first turn.
var noSearchRe = regexp.MustCompile(`(?i)\b(do not|don't|dont|no need to|without|never|no)\s+(search|searching|look\s+up|lookups?)\b`)

// presearchNote heads the Studio's own search's results in the prompt, so
// the model knows it did not ask for that search.
const presearchNote = "The Studio searched the libraries for the user's message before your first turn (you did not ask for this search; say so if the user asks):\n"

// announcesRe: a short reply that says what it is about to do rather than
// doing it ("I'll write the file now", "Let me create it").
var announcesRe = regexp.MustCompile(`(?i)\b(i'll|i’ll|i will|let me|i am going to|i'm going to|i’m going to|going to|next,? i|now (writing|creating|saving|making)|writing (it|the|that)|creating (it|the|that))\b`)

// maxNudges is how many times a turn's short or empty replies are answered
// with nudgeNote.
const maxNudges = 3

// nudgeNote answers such a reply on the emulated path.
const nudgeNote = "Your last reply was empty or said what you would do, but made no tool request, so nothing ran. If the user's request needs a tool (write_kb_file to save a file, say), reply now with the JSON request and nothing else; otherwise give your final answer."

// gatheredLead opens the forced final answer's material: what the tools
// gave, which after a change includes what was written.
func gatheredLead(changed bool) string {
	if changed {
		return "I worked on the request with the tools; these are their results, including what was written:\n\n"
	}
	return "I looked through the knowledge base and gathered this material:\n\n"
}

// closeEnvelope is an envelope (from its opening brace, without a fence
// after it) with the closing brackets it lacks at its end added, as
// written or once mended (repairJSON); ok is false when none are lacking.
func closeEnvelope(s string) (string, bool) {
	o := strings.Index(s, "{")
	if o < 0 {
		return "", false
	}
	s = strings.TrimSpace(fenceCloseRe.ReplaceAllString(strings.TrimSpace(s[o:]), ""))
	if c, ok := docwrite.CloseJSON(s); ok {
		return c, true
	}
	if r, ok := repairJSON(s); ok {
		return docwrite.CloseJSON(r)
	}
	return "", false
}

// envelopeDecodeErr is why an envelope does not decode, and the text
// around where it stopped.
func envelopeDecodeErr(s string) (string, string) {
	o := strings.Index(s, "{")
	if o < 0 {
		return "no object", ""
	}
	s = s[o:]
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	err := dec.Decode(&v)
	if err == nil {
		return "", ""
	}
	at := int(dec.InputOffset())
	var se *json.SyntaxError
	if errors.As(err, &se) {
		at = int(se.Offset)
	}
	from, to := max(0, at-60), min(len(s), at+60)
	return err.Error(), s[from:to]
}

// tailChars is the last n characters of s.
func tailChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// maxShortNudges is how many times a turn asks for the rest of a workbook
// short of the rows asked for.
const maxShortNudges = 6

// askedRowsRe reads a count of rows from a request: "250 rows", "600 data
// rows", "1,000 records".
var askedRowsRe = regexp.MustCompile(`(?i)\b(\d{1,3}(?:,\d{3})+|\d+)\s+(?:data\s+|more\s+)?(?:rows|records|lines|entries)\b`)

// askedRows is the largest count of rows text asks for, 0 for none.
func askedRows(text string) int {
	best := 0
	for _, m := range askedRowsRe.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", "")); err == nil && n > best && n <= 1_000_000 {
			best = n
		}
	}
	if best < 2 {
		return 0
	}
	return best
}

// sentPart is a workbook part a round wrote: its path, and its rows.
type sentPart struct {
	i    int
	path string
	rows [][]any
}

// workbookPart reads a write_kb_file call that writes rows to one sheet of
// a workbook; ok is false for any other call.
func workbookPart(args string) (sentPart, bool) {
	var v map[string]any
	if json.Unmarshal([]byte(args), &v) != nil {
		return sentPart{}, false
	}
	p, _ := v["path"].(string)
	content, _ := v["content"].(string)
	p = strings.ToLower(strings.Trim(p, "/"))
	if !strings.HasSuffix(p, ".xlsx") || content == "" {
		return sentPart{}, false
	}
	sheets, err := docwrite.Sheets(content)
	if err != nil || len(sheets) != 1 || len(sheets[0].Rows) == 0 {
		return sentPart{}, false
	}
	return sentPart{path: p, rows: sheets[0].Rows}, true
}

// repeats is the call of a part written before it in the round to the
// same workbook with the same rows past the header (the header is the
// first row of the round's first part to it), -1 for none. A model that
// sends a first part as a replace and again as an append would otherwise
// write its rows twice.
func (p sentPart) repeats(done []sentPart) int {
	var header []any
	for _, d := range done {
		if d.path == p.path {
			header = d.rows[0]
			break
		}
	}
	if header == nil {
		return -1
	}
	body := func(rows [][]any) string {
		if len(rows) > 0 && rowText(rows[0]) == rowText(header) {
			rows = rows[1:]
		}
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(rowText(r))
			b.WriteByte('\n')
		}
		return b.String()
	}
	mine := body(p.rows)
	if mine == "" {
		return -1
	}
	for _, d := range done {
		if d.path == p.path && body(d.rows) == mine {
			return d.i
		}
	}
	return -1
}

func rowText(r []any) string {
	cells := make([]string, len(r))
	for k, c := range r {
		cells[k] = strings.TrimSpace(fmt.Sprint(c))
	}
	return strings.Join(cells, "\x1f")
}

// rowCap is the count of rows a message asks a new workbook to hold and
// the file it names ("" for none): a message that asks to write, create
// or make something with a count of rows, not "more rows" or rows added
// to a file already there. 0 when it asks for none. A model that sends
// the parts of a workbook all at once cannot see the count until they
// have run, so the write itself stops at it.
func rowCap(text string) (int, string) {
	if !makeVerbRe.MatchString(text) || moreRowsRe.MatchString(text) {
		return 0, ""
	}
	n := askedRows(text)
	if n == 0 {
		return 0, ""
	}
	return n, askedFile(text)
}

var moreRowsRe = regexp.MustCompile(`(?i)\b(?:more|another|additional|extra)\s+(?:\d[\d,]*\s+)?(?:data\s+)?(?:rows|records|lines|entries)\b|\b(?:add|append)\b`)

var (
	heldPartRe = regexp.MustCompile(`(?:workbook of \d+ sheets? \(|now holds: )([^\n]*?)(?:\)[;,.] |\)$|; \d+ bytes in all)`)
	heldRowsRe = regexp.MustCompile(`(\d+) rows? after the header`)
)

// rowsHeld is how many rows a workbook write's result says its sheets
// hold after their headers, summed; ok is false for another file. What a
// replaced workbook held before is not counted.
func rowsHeld(result string) (int, bool) {
	m := heldPartRe.FindStringSubmatch(result)
	if m == nil {
		return 0, false
	}
	n, found := 0, false
	for _, x := range heldRowsRe.FindAllStringSubmatch(m[1], -1) {
		if v, err := strconv.Atoi(x[1]); err == nil {
			n += v
			found = true
		}
	}
	return n, found
}

// rowsAgainstAsked is said after a workbook write's result when the user
// asked for a count of rows.
func rowsAgainstAsked(held, asked int) string {
	switch {
	case held < asked:
		return fmt.Sprintf("\n[The user asked for %d rows; the workbook holds %d after the header, so %d are still to add with append=true before you answer.]", asked, held, asked-held)
	case held > asked:
		return fmt.Sprintf("\n[The user asked for %d rows; the workbook holds %d after the header, more than asked. Say %d in your answer, or write it again with %d.]", asked, held, held, asked)
	}
	return fmt.Sprintf("\n[The user asked for %d rows; the workbook holds %d after the header, all of them.]", asked, held)
}

// writePath is the path a write_kb_file call names.
func writePath(args string) string {
	var v map[string]any
	if json.Unmarshal([]byte(args), &v) != nil {
		return ""
	}
	s, _ := v["path"].(string)
	return s
}

var (
	makeVerbRe  = regexp.MustCompile(`(?i)\b(write|create|make|save|generate|build|produce|export)\b`)
	fileNamedRe = regexp.MustCompile(`(?i)(?:^|[\s"'(\x60])([\w][\w./-]*\.(?:xlsx|csv|md|markdown|pdf|docx|txt|json|html|ya?ml|py|stl))\b`)
)

// askedFile is the file a request asks to have made: a file name in a
// message that asks to write, create or make something; "" otherwise.
func askedFile(text string) string {
	if !makeVerbRe.MatchString(text) {
		return ""
	}
	if m := fileNamedRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

var rowSepRe = regexp.MustCompile(`\]\s*,\s*\[`)

// rowsPerPart is how many rows to ask for in each part after a request
// cut off at the output limit: half the rows it got to, from 10 to 50;
// 50 when it shows none.
func rowsPerPart(cut string) int {
	seen := len(rowSepRe.FindAllStringIndex(cut, -1))
	n := seen / 2
	switch {
	case seen == 0:
		return 50
	case n < 10:
		return 10
	case n > 50:
		return 50
	}
	return n
}

// perPart says how many rows to send in each request, "" for none.
func perPart(rows int) string {
	if rows <= 0 {
		return ""
	}
	return fmt.Sprintf(", at most %d rows in each request", rows)
}

func orInt(n, or int) int {
	if n > 0 {
		return n
	}
	return or
}
