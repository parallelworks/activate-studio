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
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
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
	// tap sees every event, also once the page has gone (liveturn.go).
	tap func(name string, data []byte)
}

func (s *sseWriter) setTap(f func(name string, data []byte)) {
	s.mu.Lock()
	s.tap = f
	s.mu.Unlock()
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
	s.mu.Lock()
	tap := s.tap
	s.mu.Unlock()
	if tap != nil {
		tap(name, b)
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
	// Effort is the composer's reasoning effort for this turn; empty
	// leaves it to the model (and chatTemplateKwargs).
	Effort *string `json:"effort"`
	// Engine is the composer's choice of what answers: "pwcode",
	// "studio", or empty for the deployment's (pwcodechat.go).
	Engine *string `json:"engine"`
	// NewChatEngine is the engine last chosen in this browser, which a
	// chat nothing has answered yet starts with.
	NewChatEngine *string `json:"newChatEngine"`
	// PWCodeModel and PWCodeAllocation are the model a new pw code chat's
	// composer chose before its first message, and the allocation that
	// bills it: its session is made with them.
	PWCodeModel      *string `json:"pwcodeModel"`
	PWCodeAllocation *string `json:"pwcodeAllocation"`
	// PWCodeEffort is the reasoning effort that session starts at, one of
	// pw code's levels; pw code's own without it.
	PWCodeEffort *string `json:"pwcodeEffort"`
	// PWCodeMachine, PWCodeFolder and PWCodeMode are where the composer
	// chose to start it: another of the person's machines (its platform
	// id), the folder, and, on another machine, the permission mode
	// (pwcodestart.go).
	PWCodeMachine *string `json:"pwcodeMachine"`
	PWCodeFolder  *string `json:"pwcodeFolder"`
	// PWCodeLibrary and PWCodePath are the library's folder or file a chat
	// was started at from the Library, which PWCodeMachine and PWCodeFolder
	// must be where it is (pwcodeplace.go).
	PWCodeLibrary *string `json:"pwcodeLibrary"`
	PWCodePath    *string `json:"pwcodePath"`
	PWCodeMode    *string `json:"pwcodeMode"`
	// PWCodeTools is whether to add the Studio's tools to pw code on that
	// machine first, as the person chose with the message.
	PWCodeTools *bool `json:"pwcodeTools"`
	// OpensSession: the page runs a pw code command a chat starts with
	// itself, in the session's view, and takes back a first message the
	// session refused (pwcodefirst.go).
	OpensSession bool `json:"opensSession"`

	// engineNote is the note stored after the reply when the Studio
	// assistant answers in pw code's place (engineFor); never read from
	// the request.
	engineNote string
}

func sp(s string) *string { return &s }

// labelScopeOf is the labels a chat is scoped to, as the composer sends
// them: at most 16, without empty ones.
func labelScopeOf(v any) []string {
	labelScope := []string{}
	if ls, ok := v.([]any); ok {
		for _, x := range ls {
			// String(x), then filter(Boolean)
			s := reqbody.JSString(x)
			if x == nil {
				s = "null"
			}
			if s != "" && len(labelScope) < 16 {
				labelScope = append(labelScope, s)
			}
		}
	}
	return labelScope
}

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
	// A chat that drives a pw code session takes messages only from its
	// owner, as its Stop and approvals do: nothing another person sends is
	// stored in it or reaches the session.
	if id := deref(body.ConversationID); id != "" {
		c := m.Conv.Get(id)
		// Only its owner adds to a chat, changes what answers it or answers
		// a call it holds (an administrator, where it has no owner, whose it
		// is from then on); one the person may not see is not there. With
		// sign-in one that is not there gets the same answer, so nothing is
		// made under an id the page did not get from the Studio.
		if c != nil || m.Convs.AuthEnabled() {
			if _, err := m.ownChat(r, id); err != nil {
				return err
			}
		}
		if c != nil && c.PWCode != nil && c.PWCode.Session != "" {
			if _, err := m.ownPWChat(r, c.ID); err != nil {
				return err
			}
		}
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
	// A transfer's note waits until this reply is stored (transfertools.go).
	defer m.notes.begin(convID)()
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

	// pw code answers where it is chosen or, by default, offered; a
	// resumed Studio approval is the Studio assistant's.
	if _, isResume := body.Resume.(map[string]any); !isResume {
		engine, note, err := m.engineFor(r, deref(body.Engine), deref(body.NewChatEngine), convID)
		if err != nil {
			res.event("error", reqbody.O("message", pwSaid(apiMessage(err)), "kind", "pwcode"))
			return nil
		}
		if note != "" {
			// pw code is not for this person here: the Studio assistant
			// answers, and a note after its reply says why (once: the chat
			// is the Studio assistant's from now on).
			body.engineNote = note
			res.event("engine_fallback", reqbody.O("conversationId", convID, "note", note))
		}
		if engine == enginePWCode {
			err := m.streamPWCode(r, res, &body, convID)
			var down *studioToolsError
			if !errors.As(err, &down) {
				return err
			}
			// pw code could not reach the Studio's tools: the Studio
			// assistant answers this message with them, and says why.
			m.A.Log.Warn("pw code chat: the Studio answers in pw code's place", "conversation", convID, "detail", down.detail)
			// The chat goes on with the engine that answered it, until the
			// person switches it back.
			m.Conv.SetEngine(convID, engineStudio)
			res.event("pwcode_fallback", reqbody.O("conversationId", convID,
				"reason", down.Error()+", so the Studio answered this message with its own tools."))
		}
	}

	return m.answerOnPast(r, res, &body, convID, model)
}

// answer writes the Studio assistant's reply to a message, and stores it
// in the conversation.
func (m *Module) answer(r *http.Request, res *sseWriter, body streamBody, convID, model string) error {
	ctx := r.Context()
	userCred := m.cred(r)
	if userCred == nil {
		m.refreshAccess(r, 15000)
	}
	if m.personalRequired(userCred) {
		res.event("error", reqbody.O("message", m.signInFirstFor(r), "kind", "credential"))
		return nil
	}
	if m.deploymentModelsKey() == "" && userCred == nil && !m.LLM.SidecarConfigured() {
		res.event("error", reqbody.O("message", m.noCredential(r), "kind", "credential"))
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
	// The platform's tools run with the person's sign-in or ACTIVATE key,
	// also when chat answers from their endpoint.
	var pwToolKey *string
	if c := m.platformCred(r); c != nil {
		pwToolKey = sp(c.Key)
	}
	var changeKey *string
	if m.A.Cfg.AuthEnabled() {
		changeKey = pwToolKey
	} else if pwToolKey != nil && *pwToolKey != "" {
		changeKey = pwToolKey
	} else if k := m.GatewayKey(); k != "" {
		changeKey = &k
	}
	labelScope := labelScopeOf(body.LabelScope)
	scopeNote := ""
	if len(labelScope) > 0 {
		scopeNote = "\n\nThe user has scoped this conversation to knowledge base material labeled: " + strings.Join(labelScope, ", ") + ". search_kb is restricted server-side to those labels for this conversation. Ground answers in that material, and say so when the scoped material has nothing relevant rather than answering from outside the scope."
	}
	// The libraries the chat's Scope selects, as this user can read them.
	library := m.chatLibraries(body.Library, libraries.ViewerOf(userID(r)))
	scopeNote += libraryScopeNote(library)
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
	kbChanges := begin.KBChanges
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
	// Files the user linked with @ go in the system prompt, read as this
	// user (messages[0] points at sys).
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i].Role == "user" {
			sys += m.mentionNote(r, raw[i].Text(), ToolCtx{LabelScope: labelScope, Library: library, UserKey: pwToolKey, ConversationID: strOrNil(convID), UserID: strOrNil(userID(r))})
			break
		}
	}
	cited := &CitationContext{Known: map[string]string{}, Library: scopeOne(library)}
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
		if body.engineNote != "" {
			// The note follows the reply, and the chat is the Studio
			// assistant's from now on (engineFor).
			m.Conv.AppendNoteKind(convID, "engine", body.engineNote)
			m.Conv.SetEngine(convID, engineStudio)
		}
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
		viewer: viewer, changes: changes, kbChanges: kbChanges, changeKey: changeKey, platform: platform, meta: meta, readerTools: readerTools, hasReader: hasReader,
		activeGroups: activeGroups, groupsMu: &groupsMu, groupList: groupList, labelScope: labelScope, library: library, pwToolKey: pwToolKey,
		convID: convID, lastUser: lastUserMsg(messages), effort: effortOf(body.Effort),
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
		if errors.Is(ctx.Err(), context.Canceled) {
			// Stop, which the person asked for.
			m.A.Log.Info("chat reply stopped", "conversation", convID)
		} else {
			m.A.Log.Error("chat stream failed", "err", failErr.Error(), "errorId", errorID)
		}
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
