package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
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

// POST /api/chat/stream: the agent loop, as routes.ts runs it.

// sseWriter writes the reply's events; writes after the client left are
// dropped.
type sseWriter struct {
	mu    sync.Mutex
	w     http.ResponseWriter
	f     http.Flusher
	ended bool
}

func (s *sseWriter) raw(b string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	if _, err := s.w.Write([]byte(b)); err != nil {
		s.ended = true
		return
	}
	if s.f != nil {
		s.f.Flush()
	}
}

func (s *sseWriter) event(name string, data any) {
	b, err := jsonobj.Marshal(data)
	if err != nil {
		b = []byte("null")
	}
	s.raw("event: " + name + "\ndata: " + string(b) + "\n\n")
}

func (s *sseWriter) end() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
}

type resumeBody struct {
	MessageID  any `json:"messageId"`
	ToolCallID any `json:"toolCallId"`
	Allowed    any `json:"allowed"`
	Feedback   any `json:"feedback"`
	Tool       any `json:"tool"`
	Args       any `json:"args"`
}

type streamBody struct {
	Model           any               `json:"model"`
	Messages        []json.RawMessage `json:"messages"`
	ConversationID  *string           `json:"conversationId"`
	UserMessageID   *string           `json:"userMessageId"`
	ParentMessageID *string           `json:"parentMessageId"`
	Allocation      *string           `json:"allocation"`
	LabelScope      any               `json:"labelScope"`
	Agent           *string           `json:"agent"`
	Tools           any               `json:"tools"`
	Resume          any               `json:"resume"`
	Library         any               `json:"library"`
}

func sp(s string) *string { return &s }

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func nonce8() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func shortID() string {
	const al = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = al[int(b[i])%36]
	}
	return string(b)
}

var spaceRunRe = regexp.MustCompile(`\s+`)

func msgsJSON(ms []llm.Message) []byte {
	b, _ := jsonobj.Marshal(ms)
	return b
}

func lastSeg(model string) string {
	p := strings.Split(model, "/")
	return p[len(p)-1]
}

func (m *Module) stream(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is identified by Node")
		return nil
	}
	rb, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	var body streamBody
	if rb.Present {
		_ = json.Unmarshal(rb.Raw, &body)
	}
	model := StripAvailabilityMark(jsString(body.Model))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	res := &sseWriter{w: w, f: f}
	if f != nil {
		f.Flush()
	}
	defer res.end()
	if !m.eff().AiEnabled {
		res.event("content", reqbody.O("text", AIOff))
		res.event("done", reqbody.O("finishReason", "stop", "model", nil))
		return nil
	}
	done := m.A.Stream()
	defer done()
	convID := deref(body.ConversationID)
	defer m.Conv.BeginAnswer(convID)()
	ctx := r.Context()
	kctx, stopKeep := context.WithCancel(ctx)
	defer stopKeep()
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-kctx.Done():
				return
			case <-t.C:
				res.raw(": keepalive\n\n")
			}
		}
	}()

	userCred := m.cred(r)
	if userCred == nil {
		m.refreshAccess(r, 15000)
	}
	if m.personalRequired(userCred) {
		res.event("error", reqbody.O("message", "This deployment requires your own model credential. Add your API key or platform token in Settings, Model access, then retry. Browsing, search, and adding material work without one.", "kind", "credential"))
		return nil
	}
	if m.GatewayKey() == "" && userCred == nil && !m.LLM.SidecarConfigured() {
		res.event("error", reqbody.O("message", "No gateway credential (set PW_API_KEY, authenticate the pw CLI, or add your key in Settings); chat is unavailable."))
		return nil
	}

	var resume *resumeBody
	if ro, ok := body.Resume.(map[string]any); ok && reqbody.Truthy(ro["toolCallId"]) {
		resume = &resumeBody{ro["messageId"], ro["toolCallId"], ro["allowed"], ro["feedback"], ro["tool"], ro["args"]}
	}
	raw := make([]llm.Message, len(body.Messages))
	for i, b := range body.Messages {
		_ = json.Unmarshal(b, &raw[i])
	}
	lastUserIdx := -1
	for i, mm := range raw {
		if mm.Role == "user" {
			lastUserIdx = i
		}
	}
	used := body.Messages
	if resume != nil && lastUserIdx >= 0 {
		used = body.Messages[:lastUserIdx+1]
	}
	var lastUser *llm.Message
	if lastUserIdx >= 0 {
		lastUser = &raw[lastUserIdx]
	}
	if resume == nil {
		content := ""
		if lastUser != nil && lastUser.Content != nil && isJSONString(body.Messages[lastUserIdx], "content") {
			content = *lastUser.Content
		}
		owner := ""
		if u := user(r); u != nil {
			owner = u.Username
		}
		m.Conv.RecordUserTurn(convID, deref(body.UserMessageID), content, body.ParentMessageID, owner)
	}

	onSidecar := m.LLM.IsSidecarModel(model)
	var userKey, userBase string
	var userEndpoint *modelkeys.Endpoint
	if onSidecar {
		userKey, userBase = m.LLM.SidecarKey, m.LLM.SidecarBase
	} else if userCred != nil {
		userKey, userBase, userEndpoint = userCred.Key, userCred.Base(), userCred.Endpoint
	}
	var pwToolKey *string
	if userCred != nil && userCred.Base() == "" {
		pwToolKey = sp(userCred.Key)
	}
	var changeKey *string
	if m.A.Cfg.AuthEnabled() {
		changeKey = pwToolKey
	} else if pwToolKey != nil && *pwToolKey != "" {
		changeKey = pwToolKey
	} else if k := m.GatewayKey(); k != "" {
		changeKey = &k
	}
	labelScope := []string{}
	if ls, ok := body.LabelScope.([]any); ok {
		for _, x := range ls {
			// String(x), then filter(Boolean)
			v := reqbody.JSString(x)
			if x == nil {
				v = "null"
			}
			if v != "" && len(labelScope) < 16 {
				labelScope = append(labelScope, v)
			}
		}
	}
	scopeNote := ""
	if len(labelScope) > 0 {
		scopeNote = "\n\nThe user has scoped this conversation to knowledge base material labeled: " + strings.Join(labelScope, ", ") + ". search_kb is restricted server-side to those labels for this conversation. Ground answers in that material, and say so when the scoped material has nothing relevant rather than answering from outside the scope."
	}
	var userTexts []string
	for _, mm := range raw {
		if mm.Role == "user" {
			userTexts = append(userTexts, mm.Text())
		}
	}
	if len(userTexts) > 2 {
		userTexts = userTexts[len(userTexts)-2:]
	}
	var readerTools []string
	hasReader := false
	if lst, ok := body.Tools.([]any); ok {
		hasReader = true
		readerTools = []string{}
		for _, x := range lst {
			readerTools = append(readerTools, reqbody.JSString(x))
		}
	}
	var owner any
	if convID != "" {
		if c := m.Conv.Get(convID); c != nil && c.Owner != nil {
			owner = *c.Owner
		}
	}
	usedArr := make([]json.RawMessage, len(used))
	copy(usedArr, used)
	begin, err := m.Tools.Begin(r, map[string]any{
		"used": usedArr, "lastUserIdx": lastUserIdx, "changeKey": changeKey, "owner": owner,
		"tools": toolsArg(readerTools, hasReader), "recentUserText": strings.Join(userTexts, "\n"),
		"usedTools": orEmptyStr(m.Conv.ToolsUsedIn(convID)), "agent": body.Agent,
	})
	if err != nil {
		m.A.Log.Error("chat stream failed", "err", err.Error())
		res.event("error", reqbody.O("message", fmt.Sprintf("The reply failed with an internal error (ref %s).", shortID()), "kind", "error"))
		return nil
	}
	meta := begin.Meta
	viewer := begin.Viewer
	changes := begin.Changes
	platform := begin.Platform
	var groupsMu sync.Mutex
	activeGroups := map[string]bool{}
	for _, g := range begin.Groups {
		activeGroups[g] = true
	}
	groupList := func() []string {
		groupsMu.Lock()
		defer groupsMu.Unlock()
		var out []string
		for _, g := range meta.GroupOrder {
			if activeGroups[g] {
				out = append(out, g)
			}
		}
		for g := range activeGroups {
			if !contains(meta.GroupOrder, g) {
				out = append(out, g)
			}
		}
		return orEmptyStr(out)
	}
	today := time.Now().UTC().Format("2006-01-02")
	sys := begin.System + "\n\nToday's date is " + today + ". Treat earlier dates as past; call out deadlines that have already passed." + scopeNote
	if begin.Persona != nil && *begin.Persona != "" {
		sys += "\n\n--- DEPLOYMENT AGENT INSTRUCTIONS (extensions/agents/default.md) ---\n" + *begin.Persona
	}
	messages := append([]llm.Message{{Role: "system", Content: &sys}}, begin.History...)
	baseLen := len(messages)
	var resumedFrom *string

	finalContent := ""
	var usageTotal llm.Usage
	var finalModel *string
	finalReasoning := ""
	modelReasoned := false
	turnBreak := false
	var finalParts []conversations.Part
	var reasoningStart, reasoningDuration int64
	requestStart := time.Now()
	nowMS := func() int64 { return time.Now().UnixMilli() }
	timing := struct {
		rounds, toolCalls                     int
		modelMs, toolMs, seedMs, firstModelMs int64
		perRound                              []string
	}{}
	markThinking := func() {
		if reasoningStart == 0 {
			reasoningStart = nowMS()
		}
	}
	allocation := deref(body.Allocation)
	if body.Allocation == nil {
		allocation = os.Getenv("PW_ALLOCATION")
	}
	library := strOrNil(jsString(body.Library))
	cited := &CitationContext{Known: map[string]string{}, Library: deref(library)}
	var citedMu sync.Mutex
	if c := m.Conv.Get(convID); c != nil {
		for _, mm := range c.Messages {
			for _, p := range mm.Parts {
				if p.Kind == "tool_call" {
					for _, f := range p.Files {
						cited.Known[f.Path] = deref(f.Library)
					}
				}
			}
		}
	}
	cites := NewCitationStream(cited)
	var emitMu sync.Mutex
	emit := func(t string) {
		if turnBreak && t != "" {
			turnBreak = false
			if sep := turnSeparator(finalContent); sep != "" {
				finalContent += sep
				res.event("content", reqbody.O("text", sep))
			}
		}
		finalContent += t
		res.event("content", reqbody.O("text", t))
	}
	flushCites := func() {
		citedMu.Lock()
		rest := cites.Flush()
		citedMu.Unlock()
		if rest != "" {
			emit(rest)
		}
	}
	onContent := func(t string) {
		emitMu.Lock()
		defer emitMu.Unlock()
		if reasoningStart != 0 && reasoningDuration == 0 {
			reasoningDuration = nowMS() - reasoningStart
		}
		citedMu.Lock()
		out := cites.Push(t)
		citedMu.Unlock()
		if out != "" {
			emit(out)
		}
	}
	callbacks := llm.Callbacks{
		OnContent: onContent,
		OnReasoning: func(t string) {
			markThinking()
			modelReasoned = true
			finalReasoning += t
			res.event("reasoning", reqbody.O("text", t))
		},
	}
	recorded := false
	reasoningDur := func() *float64 {
		if reasoningDuration != 0 {
			f := float64(reasoningDuration)
			return &f
		}
		if reasoningStart != 0 {
			f := float64(nowMS() - reasoningStart)
			return &f
		}
		return nil
	}
	tokensUsed := func() json.RawMessage {
		if fin(usageTotal.TotalTokens) == 0 {
			return nil
		}
		b, _ := jsonobj.Marshal(usageTotal)
		return b
	}
	finish := func(finishReason *string, id string) {
		flushCites()
		m.A.Log.Info("reply timing", "model", model, "rounds", timing.rounds, "toolCalls", timing.toolCalls, "modelMs", timing.modelMs, "toolMs", timing.toolMs,
			"seedMs", timing.seedMs, "firstModelMs", timing.firstModelMs, "perRound", timing.perRound, "totalMs", time.Since(requestStart).Milliseconds(), "finishReason", deref(finishReason))
		messageID := id
		if messageID == "" {
			messageID = "srv-" + strconv.FormatInt(nowMS(), 10)
		}
		paused := finishReason != nil && *finishReason == "tool_approval"
		callCount := 0
		for _, p := range finalParts {
			if p.Kind == "tool_call" {
				callCount++
			}
		}
		recorded = true
		if jsTrim(finalReasoning) == "" && callCount > 0 {
			elapsed := time.Since(requestStart).Milliseconds()
			if reasoningDuration == 0 {
				reasoningDuration = elapsed
			}
			secs := int64(math.Max(1, math.Round(float64(elapsed)/1000)))
			s := "s"
			if callCount == 1 {
				s = ""
			}
			finalReasoning = fmt.Sprintf("Ran %d tool call%s in %ds.", callCount, s, secs)
			res.event("reasoning", reqbody.O("text", finalReasoning))
		}
		parent := resumedFrom
		if parent == nil {
			parent = body.UserMessageID
		}
		m.Conv.RecordAssistantTurn(convID, conversations.Message{
			ID: messageID, Role: "assistant", Content: sp(finalContent), ParentID: parent, Timestamp: nowISO(),
			Model: finalModel, HasModel: true, Reasoning: finalReasoning, ReasoningDuration: reasoningDur(),
			TokensUsed: tokensUsed(), Parts: finalParts, FinishReason: deref(finishReason),
		})
		if jsTrim(finalContent) == "" && !paused {
			m.A.Log.Warn("empty reply", "model", model, "finishReason", deref(finishReason), "toolCalls", callCount, "modelReasoned", modelReasoned)
			var ct *float64
			if fin(usageTotal.TotalTokens) != 0 {
				ct = usageTotal.CompletionTokens
			}
			var same *string
			if modelReasoned {
				same = sp(model)
			}
			note := emptyReplyNote(modelReasoned, callCount, finishReason, ct) + retryLinks(m.retryChoices(r, model), same, body.UserMessageID)
			finalContent = note
			res.event("content", reqbody.O("text", note))
		}
		fr := "stop"
		if finishReason != nil {
			fr = *finishReason
		}
		res.event("done", reqbody.O("content", finalContent, "finishReason", fr, "model", finalModel, "messageId", messageID))
		res.end()
	}
	if begin.Listing != nil {
		onContent(*begin.Listing)
		finish(sp("stop"), "")
		return nil
	}

	failErr := m.loop(ctx, r, &loopState{
		model: model, body: &body, messages: &messages, baseLen: baseLen, callbacks: callbacks, res: res, cites: cites, citedMu: &citedMu, cited: cited,
		allocation: allocation, userKey: userKey, userBase: userBase, userEndpoint: userEndpoint, finalContent: &finalContent,
		usage: &usageTotal, finalModel: &finalModel, finalReasoning: &finalReasoning, turnBreak: &turnBreak, finalParts: &finalParts,
		markThinking: markThinking, flushCites: flushCites, finish: finish, resume: resume, resumedFrom: &resumedFrom,
		viewer: viewer, changes: changes, changeKey: changeKey, platform: platform, meta: meta, readerTools: readerTools, hasReader: hasReader,
		activeGroups: activeGroups, groupsMu: &groupsMu, groupList: groupList, labelScope: labelScope, library: library, pwToolKey: pwToolKey,
		convID: convID, lastUser: lastUserMsg(messages),
		timing: func(kind string, ms int64, extra string) {
			switch kind {
			case "round":
				timing.rounds++
				timing.modelMs += ms
				if timing.firstModelMs == 0 {
					timing.firstModelMs = time.Since(requestStart).Milliseconds()
				}
				if extra != "" {
					timing.perRound = append(timing.perRound, extra)
				}
			case "final":
				timing.rounds++
			case "tools":
				timing.toolMs += ms
			case "calls":
				timing.toolCalls += int(ms)
			case "seed":
				timing.seedMs = ms
			}
		},
	})
	if failErr != nil && !recorded {
		errorID := shortID()
		m.A.Log.Error("chat stream failed", "err", failErr.Error(), "errorId", errorID)
		if ctx.Err() == nil {
			msg := failErr.Error()
			var auth *llm.AuthError
			isAuth := errors.As(failErr, &auth)
			if isRateLimit(msg) && !isAuth {
				onContent(lastSeg(model) + " is rate limited by its provider right now; the Studio waited and tried twice more." +
					retryLinks(m.retryChoices(r, model), sp(model), body.UserMessageID))
				finish(sp("rate_limited"), "")
				return nil
			}
			var un *llm.Unreachable
			var userMsg *string
			if isAuth || errors.As(failErr, &un) {
				userMsg = &msg
			} else if strings.HasPrefix(msg, "gateway chat ") {
				userMsg = sp(GatewayChatMessage(msg, orStr(model, "the model")))
			} else if strings.HasPrefix(msg, "gateway ") {
				userMsg = &msg
			}
			kind := "error"
			if isAuth {
				kind = "credential"
			}
			text := fmt.Sprintf("The reply failed with an internal error (ref %s).", errorID)
			if userMsg != nil {
				text = *userMsg
			}
			res.event("error", reqbody.O("message", text, "kind", kind))
		}
	}
	if !recorded {
		citedMu.Lock()
		finalContent += cites.Flush()
		citedMu.Unlock()
	}
	if !recorded && (finalContent != "" || len(finalParts) > 0) {
		parent := resumedFrom
		if parent == nil {
			parent = body.UserMessageID
		}
		fr := ""
		if ctx.Err() != nil {
			fr = "stopped"
		}
		m.Conv.RecordAssistantTurn(convID, conversations.Message{
			ID: "srv-" + strconv.FormatInt(nowMS(), 10), Role: "assistant", Content: sp(finalContent), ParentID: parent, Timestamp: nowISO(),
			Model: finalModel, HasModel: true, Reasoning: finalReasoning, ReasoningDuration: reasoningDur(),
			TokensUsed: tokensUsed(), Parts: finalParts, FinishReason: fr,
		})
	}
	return nil
}

func lastUserMsg(ms []llm.Message) *llm.Message {
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Role == "user" {
			c := ms[i]
			return &c
		}
	}
	return nil
}

func orEmptyStr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// isJSONString is typeof m[k] === 'string' for a raw message.
func isJSONString(raw json.RawMessage, k string) bool {
	o, err := jsonobj.Parse(raw)
	if err != nil {
		return false
	}
	_, ok := jsonobj.String(o.Get(k))
	return ok
}

var _ = jsval.Len
