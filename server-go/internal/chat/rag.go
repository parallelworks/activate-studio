package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// The OpenAI-compatible surface at /v1 (ragProxy.ts): GET /v1/models,
// POST /v1/chat/completions and the call log at GET /api/rag/calls.
//
// studio-agent[/<model>] (and studio-voice, the agent with answers shaped
// to be spoken) runs the assistant's own loop with the tools no person has
// to approve; studio-rag[/<model>] and any other model id put retrieved
// context blocks in front of the request and pass it through. Calls run on
// the caller's bearer key, else the stored personal key of the request's
// viewer (none on /v1, which no sign-in reaches), else, where Settings
// allow it, the deployment's credential.

const ragDirective = `Context blocks retrieved from the knowledge base follow, numbered [1], [2], … with their source file paths. ` +
	`Ground your answer in them and cite blocks inline as [n]. End with a "References:" section listing one "[n] file_path" per line for every block you used. ` +
	`Do not invent citations. If the context does not cover the question, say so briefly.`

// VoiceStyle is ragProxy.ts's VOICE_STYLE.
const VoiceStyle = "You are speaking aloud in a live voice conversation. Answer in one or two short sentences of plain spoken language. No markdown, no lists, no headings, no links: say a file name instead of writing a path. Before a tool call that will take time, say in a few words what you are checking. If the user is silent or vague, ask one short question."

const noBearer = "bearer API key required"

// callerAuth is whose credential a call runs on and where it goes: the
// gateway (BaseURL ""), or an endpoint of the caller's own.
type callerAuth struct {
	Key, Source, BaseURL string
	Endpoint             *modelkeys.Endpoint
}

// ragContext is buildContext's answer.
type ragContext struct {
	Text   string   `json:"text"`
	Terms  []string `json:"terms"`
	Blocks int      `json:"blocks"`
	Chars  int      `json:"chars"`
}

// ragRetrieval is what a call's retrieval found, as the call log shows it.
type ragRetrieval struct {
	Terms         []string
	Blocks, Chars int
}

// ragCall is RagCallRecord.
type ragCall struct {
	At, Model, Mode, Underlying, AuthSource string
	Stream                                  bool
	DurationMs                              *int64
	Status                                  string
	Detail                                  *string
	Retrieval                               *ragRetrieval
}

func (c *ragCall) object() *jsonobj.Object {
	var dur, detail, retrieval any
	if c.DurationMs != nil {
		dur = *c.DurationMs
	}
	if c.Detail != nil {
		detail = *c.Detail
	}
	if c.Retrieval != nil {
		retrieval = reqbody.O("terms", orEmptyStr(c.Retrieval.Terms), "blocks", c.Retrieval.Blocks, "chars", c.Retrieval.Chars)
	}
	return reqbody.O("at", c.At, "model", c.Model, "mode", c.Mode, "underlying", c.Underlying, "authSource", c.AuthSource,
		"stream", c.Stream, "durationMs", dur, "status", c.Status, "detail", detail, "retrieval", retrieval)
}

// ragState is the module's /v1 state: the call log and the model listings
// each credential got, kept five minutes.
type ragState struct {
	mu     sync.Mutex
	calls  []*ragCall
	models map[string]modelIDs
	// failOpen is RAG_FAIL_OPEN: a failed retrieval passes the request
	// through without context.
	failOpen bool
	// retrieve is the retrieval: available is whether there is an index.
	retrieve func(r *http.Request, query string, topK float64, tags []string) (available bool, c *ragContext, err error)
}

type modelIDs struct {
	ids []string
	at  time.Time
}

func newRagState(m *Module) *ragState {
	return &ragState{
		models:   map[string]modelIDs{},
		failOpen: strings.ToLower(os.Getenv("RAG_FAIL_OPEN")) != "false",
		retrieve: m.retrieve,
	}
}

// retrieve is ragProxy.ts's retrieval: buildContext when there is an index.
func (m *Module) retrieve(r *http.Request, query string, topK float64, tags []string) (bool, *ragContext, error) {
	if m.Corpus == nil || !m.A.Cfg.GufiAvailable() {
		return false, nil, nil
	}
	c, err := m.buildContext(r.Context(), query, int(topK), tags)
	return true, c, err
}

// logCall is logCall: newest first, fifty kept.
func (s *ragState) logCall(c *ragCall) *ragCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append([]*ragCall{c}, s.calls...)
	if len(s.calls) > 50 {
		s.calls = s.calls[:50]
	}
	return c
}

// update changes a logged record under the log's lock.
func (s *ragState) update(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

func (m *Module) ragCalls(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is identified by Node")
		return nil
	}
	m.rag.mu.Lock()
	out := make([]*jsonobj.Object, len(m.rag.calls))
	for i, c := range m.rag.calls {
		out[i] = c.object()
	}
	m.rag.mu.Unlock()
	return reqbody.OK(w, reqbody.O("calls", out))
}

/* ---- whose call this is ---- */

func bearerOf(r *http.Request) string {
	h := strings.Join(r.Header.Values("Authorization"), ", ")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return jsval.Trim(h[7:])
	}
	return ""
}

// callerKey is callerKey(req).
func (m *Module) callerKey(r *http.Request) *callerAuth {
	if b := bearerOf(r); b != "" {
		return &callerAuth{Key: b, Source: "caller bearer"}
	}
	// A verified viewer's stored key; /v1 is outside sign-in, so this is
	// the single owner's with sign-in off and nobody's with it on.
	if c := m.Keys.Resolve(userID(r)); c != nil {
		if c.Base() != "" {
			return &callerAuth{Key: c.Key, Source: "stored endpoint (session identity)", BaseURL: c.Base(), Endpoint: c.Endpoint}
		}
		return &callerAuth{Key: c.Key, Source: "stored personal key (session identity)"}
	}
	if m.eff().RagAllowDeploymentKey {
		if k := m.GatewayKey(); k != "" {
			return &callerAuth{Key: k, Source: "deployment (keyless fallback)"}
		}
	}
	return nil
}

// modelIDsFor is the ids a credential lists, none when the listing fails.
func (m *Module) modelIDsFor(ctx context.Context, a *callerAuth) []string {
	l, err := m.LLM.ListModels(ctx, a.Key, a.BaseURL, a.Endpoint)
	if err != nil {
		return []string{}
	}
	ids := []string{}
	for _, o := range l.Items {
		ids = append(ids, llm.ObjString(o, "id"))
	}
	return ids
}

// defaultModelFor is defaultModelFor(auth): the deployment's default model
// when the caller's credential lists it, else the caller's first own one.
func (m *Module) defaultModelFor(ctx context.Context, a *callerAuth) string {
	preferred := m.eff().RagDefaultModel
	cacheID := a.BaseURL + "|" + lastN(a.Key, 16)
	m.rag.mu.Lock()
	hit, ok := m.rag.models[cacheID]
	m.rag.mu.Unlock()
	ids := hit.ids
	if !ok || time.Since(hit.at) >= 300*time.Second {
		ids = m.modelIDsFor(ctx, a)
		m.rag.mu.Lock()
		m.rag.models[cacheID] = modelIDs{ids, time.Now()}
		m.rag.mu.Unlock()
	}
	if preferred != "" && contains(ids, preferred) {
		return preferred
	}
	for _, id := range ids {
		if !strings.HasPrefix(id, "org:") && !studioModelRe.MatchString(id) {
			return id
		}
	}
	for _, id := range ids {
		if !studioModelRe.MatchString(id) {
			return id
		}
	}
	return preferred
}

// resolvedModel is resolveModel's answer.
type resolvedModel struct {
	Mode, Underlying string
	Voice            bool
}

// resolveModel maps a virtual model id to its mode and underlying model.
func (m *Module) resolveModel(requested string) resolvedModel {
	fallback := m.eff().RagDefaultModel
	under := func(prefix string) string {
		if s := strings.TrimPrefix(requested, prefix); s != "" && s != requested {
			return s
		}
		return fallback
	}
	switch {
	case requested == "studio-voice" || strings.HasPrefix(requested, "studio-voice/"):
		return resolvedModel{"agent", under("studio-voice/"), true}
	case requested == "studio-agent" || strings.HasPrefix(requested, "studio-agent/"):
		return resolvedModel{"agent", under("studio-agent/"), false}
	case requested == "studio-rag" || strings.HasPrefix(requested, "studio-rag/"):
		return resolvedModel{"rag", under("studio-rag/"), false}
	}
	return resolvedModel{"rag", requested, false}
}

/* ---- the routes ---- */

// ragGate is the /v1 routes' onRequest hook: the Settings switch, then the
// AI switch. true when it answered.
func (m *Module) ragGate(w http.ResponseWriter) bool {
	eff := m.eff()
	if !eff.RagProxyEnabled {
		_ = reqbody.JSON(w, 403, reqbody.O("error", "The grounded-model endpoint is disabled for this deployment (Settings, External access)"))
		return true
	}
	if !eff.AiEnabled {
		_ = reqbody.JSON(w, 403, reqbody.O("error", "AI features are off in this Studio."))
		return true
	}
	return false
}

func unauthorized(w http.ResponseWriter) error {
	return reqbody.JSON(w, 401, reqbody.O("error", reqbody.O("message", noBearer)))
}

func (m *Module) v1Models(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the RAG proxy's call log is Node's where Node identifies the viewer")
		return nil
	}
	if m.ragGate(w) {
		return nil
	}
	m.refreshAccess(r, 0)
	a := m.callerKey(r)
	if a == nil {
		return unauthorized(w)
	}
	created := time.Now().Unix()
	entry := func(id, owner string) *jsonobj.Object {
		return reqbody.O("id", id, "object", "model", "created", created, "owned_by", owner)
	}
	eff := m.eff()
	data := []*jsonobj.Object{}
	has := map[string]bool{}
	add := func(id string) {
		data = append(data, entry(id, "studio"))
		has[id] = true
	}
	if eff.RagAdvertiseAgentModel {
		add("studio-agent")
		add("studio-voice")
	}
	if eff.RagAdvertiseRagModel {
		add("studio-rag")
	}
	if strings.Join(r.URL.Query()["all"], ",") == "1" {
		for _, id := range []string{"studio-agent", "studio-rag"} {
			if !has[id] {
				add(id)
			}
		}
		owner := "gateway"
		if a.BaseURL != "" {
			owner = "endpoint"
		}
		for _, id := range m.modelIDsFor(r.Context(), a) {
			data = append(data, entry(id, owner))
		}
	}
	return reqbody.OK(w, reqbody.O("object", "list", "data", data))
}

func chunkOf(id, model string, delta *jsonobj.Object, finish *string) string {
	var f any
	if finish != nil {
		f = *finish
	}
	b, _ := jsonobj.Marshal(reqbody.O("id", id, "object", "chat.completion.chunk", "created", time.Now().Unix(), "model", model,
		"choices", []any{reqbody.O("index", 0, "delta", delta, "finish_reason", f)}))
	return "data: " + string(b) + "\n\n"
}

// headerString is a request header as Node reads it: several values joined.
func headerString(r *http.Request, k string) string { return strings.Join(r.Header.Values(k), ", ") }

func (m *Module) v1Completions(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the RAG proxy's call log is Node's where Node identifies the viewer")
		return nil
	}
	if m.ragGate(w) {
		return nil
	}
	rb, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	body := jsonobj.New()
	if o, ok := rb.Value.(map[string]any); ok && o != nil {
		if p, err := jsonobj.Parse(rb.Raw); err == nil {
			body = p
		}
	}
	m.refreshAccess(r, 0)
	a := m.callerKey(r)
	field := func(k string) any {
		var v any
		if raw := body.Get(k); len(raw) > 0 {
			_ = json.Unmarshal(raw, &v)
		}
		return v
	}
	if a == nil {
		model := "?"
		if v := field("model"); v != nil {
			model = reqbody.JSString(v)
		}
		m.rag.logCall(&ragCall{At: nowISO(), Model: model, Mode: "?", AuthSource: "none", DurationMs: new(int64),
			Status: "error", Detail: sp("rejected: no credential (bearer API key required)")})
		return unauthorized(w)
	}
	eff := m.eff()
	requested := "studio-rag"
	if v := field("model"); v != nil {
		requested = reqbody.JSString(v)
	}
	res := m.resolveModel(requested)
	mode, underlying := res.Mode, res.Underlying
	if requested == "studio-agent" || requested == "studio-rag" || requested == "studio-voice" {
		underlying = m.defaultModelFor(r.Context(), a)
	}
	if underlying == "" {
		return reqbody.JSON(w, 400, reqbody.O("error", reqbody.O("message", "No underlying model available to this key: pass "+requested+"/<gateway-model-id> or set a default model on the Settings RAG endpoint section.")))
	}
	messages := []json.RawMessage{}
	if arr, ok := jsval.Array(body.Get("messages")); ok {
		messages = append(messages, arr...)
	}
	wantStream := string(body.Get("stream")) == "true"
	authSource := a.Source
	if headerString(r, "X-Studio-Injected-Key") == "1" {
		authSource = "deployment (injected by registration forwarder)"
	}
	m.A.Log.Info("rag proxy call", "model", requested, "underlying", underlying, "mode", mode, "authSource", authSource)
	started := time.Now()
	rec := m.rag.logCall(&ragCall{At: nowISO(), Model: requested, Mode: mode, Underlying: underlying, AuthSource: authSource,
		Stream: wantStream, Status: "running"})
	elapsed := func() *int64 { d := time.Since(started).Milliseconds(); return &d }
	id := "studio-" + strconv.FormatInt(time.Now().UnixMilli(), 36)

	if mode == "agent" {
		if wantStream {
			return m.agentStream(w, r, id, requested, underlying, messages, a, res.Voice, rec, elapsed)
		}
		content, fin, err := m.agentic(r.Context(), r, underlying, messages, a, func(string) {}, false)
		if err != nil {
			m.rag.update(func() {
				rec.Status, rec.Detail, rec.DurationMs = "error", sp(jsval.Slice(err.Error(), 200)), elapsed()
			})
			return err
		}
		m.rag.update(func() { rec.Status, rec.DurationMs = "ok", elapsed() })
		return reqbody.OK(w, reqbody.O("id", id, "object", "chat.completion", "created", time.Now().Unix(), "model", requested,
			"choices", []any{reqbody.O("index", 0, "message", reqbody.O("role", "assistant", "content", content), "finish_reason", fin)},
			"usage", nil))
	}

	// studio-rag: context put in front of the request, then the answer
	// passed through byte for byte.
	ragOff := headerString(r, "X-Rag-Off") == "1"
	topK := reqbody.JSNumber(headerString(r, "X-Rag-Top-K"))
	if topK == 0 || math.IsNaN(topK) {
		topK = float64(eff.RagTopK)
	}
	topK = math.Min(math.Max(topK, 1), 20)
	var tags []string
	for _, t := range strings.Split(headerString(r, "X-Rag-Tags"), ",") {
		if t = jsval.Trim(t); t != "" {
			tags = append(tags, t)
		}
	}
	if ragOff {
		m.rag.update(func() { rec.Detail = sp("retrieval disabled by X-RAG-Off header") })
	}
	if !ragOff {
		if q, ok := lastUserText(messages); ok && jsval.Trim(q) != "" {
			available, c, err := m.rag.retrieve(r, q, topK, tags)
			switch {
			case err != nil:
				if !m.rag.failOpen {
					return reqbody.JSON(w, 502, reqbody.O("error", reqbody.O("message", "retrieval failed: "+err.Error())))
				}
				m.A.Log.Warn("rag retrieval failed; passing through without context", "err", err.Error())
				m.rag.update(func() { rec.Detail = sp("retrieval failed; passed through without context") })
			case !available:
			case c != nil:
				m.rag.update(func() { rec.Retrieval = &ragRetrieval{c.Terms, c.Blocks, c.Chars} })
				at := 0
				for at < len(messages) && roleOf(messages[at]) == "system" {
					at++
				}
				sys, _ := jsonobj.Marshal(reqbody.O("role", "system", "content", ragDirective+"\n\n"+c.Text))
				messages = append(messages[:at], append([]json.RawMessage{sys}, messages[at:]...)...)
			default:
				m.rag.update(func() { rec.Detail = sp("no retrieval hits") })
			}
		}
	}
	return m.passThrough(w, r, body, underlying, messages, a, rec, elapsed)
}

// passThrough sends the request on with the model and messages replaced
// and streams the answer back as it comes.
func (m *Module) passThrough(w http.ResponseWriter, r *http.Request, body *jsonobj.Object, underlying string, messages []json.RawMessage, a *callerAuth, rec *ragCall, elapsed func() *int64) error {
	// JSON.stringify({ ...body, model, messages }): the keys in the
	// client's order, every value written as JavaScript writes it.
	send := body.Clone()
	_ = send.SetValue("model", underlying)
	_ = send.SetValue("messages", messages)
	raw, err := jsonobj.Marshal(send)
	if err != nil {
		return err
	}
	payload := jsRestringify(raw)
	base := a.BaseURL
	if base == "" {
		base = m.gatewayBase()
	}
	req, err := http.NewRequestWithContext(r.Context(), "POST", llm.WithQuery(base+"/chat/completions", a.Endpoint), strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header = llm.Headers(a.Key, a.Endpoint)
	req.Header.Set("Content-Type", "application/json")
	if alloc := headerString(r, "X-Allocation"); alloc != "" {
		req.Header.Set("X-Allocation", alloc)
	}
	done := m.A.Stream()
	defer done()
	up, err := m.LLM.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("fetch failed: %w", err)
	}
	defer up.Body.Close()
	m.rag.update(func() {
		rec.Status = "ok"
		if up.StatusCode < 200 || up.StatusCode > 299 {
			rec.Status, rec.Detail = "error", sp("upstream "+strconv.Itoa(up.StatusCode))
		}
		rec.DurationMs = elapsed()
	})
	ct := up.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(up.StatusCode)
	f, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := up.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return nil
			}
			if f != nil {
				f.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	tail(r.Context())
	return nil
}

// tail is the 300 ms the answer's last frames get to flush through a
// tunnel before the response ends.
func tail(ctx context.Context) {
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
	}
}

func (m *Module) agentStream(w http.ResponseWriter, r *http.Request, id, requested, underlying string, messages []json.RawMessage, a *callerAuth, voice bool, rec *ragCall, elapsed func() *int64) error {
	done := m.A.Stream()
	defer done()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	out := &sseWriter{w: w, f: f}
	out.raw(chunkOf(id, requested, reqbody.O("role", "assistant"), nil))
	ctx := r.Context()
	// The tool loop can be silent for tens of seconds, and intermediaries
	// close idle streams: an empty delta every 3 s.
	kctx, stopKeep := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-kctx.Done():
				return
			case <-t.C:
				out.raw(chunkOf(id, requested, jsonobj.New(), nil))
			}
		}
	}()
	_, fin, err := m.agentic(ctx, r, underlying, messages, a, func(t string) {
		out.raw(chunkOf(id, requested, reqbody.O("content", t), nil))
	}, voice)
	if err == nil {
		out.raw(chunkOf(id, requested, jsonobj.New(), &fin))
		m.rag.update(func() { rec.Status = "ok" })
	} else {
		msg := err.Error()
		m.rag.update(func() { rec.Status, rec.Detail = "error", sp(jsval.Slice(msg, 200)) })
		if ctx.Err() == nil {
			out.raw(chunkOf(id, requested, reqbody.O("content", "\n[studio-agent error: "+jsval.Slice(msg, 300)+"]"), sp("stop")))
		}
	}
	stopKeep()
	m.rag.update(func() {
		rec.DurationMs = elapsed()
		if ctx.Err() != nil && rec.Status == "error" {
			rec.Detail = sp("caller disconnected")
		}
	})
	out.raw("data: [DONE]\n\n")
	tail(ctx)
	out.end()
	return nil
}

// agentic is agenticCompletion: the assistant's loop behind an OpenAI
// answer, with the tools no person has to approve.
func (m *Module) agentic(ctx context.Context, r *http.Request, underlying string, client []json.RawMessage, a *callerAuth, onDelta func(string), voice bool) (string, string, error) {
	sys := m.systemPrompt(ctx, true, true)
	voiceText := ""
	if voice {
		voiceText = "\n\n" + VoiceStyle
	}
	messages := []any{llm.Message{Role: "system", Content: llm.Str(sys + "\n\nToday's date is " + time.Now().UTC().Format("2006-01-02") + "." + voiceText)}}
	for _, raw := range client {
		if roleOf(raw) == "system" {
			continue
		}
		messages = append(messages, clientMessage(raw))
	}
	specs := []json.RawMessage{}
	for _, s := range activeToolSpecs(m.eff(), m.Ext) {
		// No one is present to approve a change, so the tools that change
		// something are not offered (and do not run if called).
		if !asksFirst(s.Name) {
			specs = append(specs, s.JSON())
		}
	}
	final := ""
	cb := llm.Callbacks{OnContent: func(t string) { final += t; onDelta(t) }}
	var key *string
	if a.BaseURL == "" {
		// An endpoint's key is never a platform credential.
		key = &a.Key
	}
	cache := map[string]string{}
	for iter := 0; iter < MaxToolIterations; iter++ {
		turn, err := m.LLM.StreamTurn(ctx, reqbody.O("model", underlying, "messages", messages, "tools", specs, "tool_choice", "auto"), "", cb, a.Key, a.BaseURL, a.Endpoint)
		if err != nil {
			return final, "", err
		}
		if deref(turn.FinishReason) == "tool_calls" && len(turn.ToolCalls) > 0 {
			var content *string
			if turn.Content != "" {
				content = llm.Str(turn.Content)
			}
			messages = append(messages, llm.Message{Role: "assistant", Content: content, ToolCalls: turn.ToolCalls})
			for _, tc := range turn.ToolCalls {
				ck := tc.Function.Name + ":" + tc.Function.Arguments
				result, ok := cache[ck]
				if !ok {
					out, err := m.Tools.Execute(r, tc.Function.Name, tc.Function.Arguments, ToolCtx{UserKey: key})
					if err != nil {
						result = "tool failed: " + err.Error()
					} else {
						result = out.Result
					}
					cache[ck] = result
				}
				callID := tc.ID
				if callID == "" {
					callID = tc.Function.Name + "-" + strconv.Itoa(iter)
				}
				messages = append(messages, llm.Message{Role: "tool", ToolCallID: callID, Content: llm.Str(result)})
			}
			continue
		}
		return final, orStr(deref(turn.FinishReason), "stop"), nil
	}
	messages = append(messages, llm.Message{Role: "user", Content: llm.Str("Tool budget exhausted; answer now from what you gathered.")})
	turn, err := m.LLM.StreamTurn(ctx, reqbody.O("model", underlying, "messages", messages), "", cb, a.Key, a.BaseURL, a.Endpoint)
	if err != nil {
		return final, "", err
	}
	return final, orStr(deref(turn.FinishReason), "stop"), nil
}

// roleOf is message.role when it is a string.
func roleOf(raw json.RawMessage) string {
	o, err := jsonobj.Parse(raw)
	if err != nil {
		return ""
	}
	s, _ := jsonobj.String(o.Get("role"))
	return s
}

// clientMessage is { role: m.role, content } of a client's message, the
// content a string: itself, ” for none, or the JSON of anything else.
func clientMessage(raw json.RawMessage) json.RawMessage {
	o, err := jsonobj.Parse(raw)
	if err != nil {
		o = jsonobj.New()
	}
	out := jsonobj.New()
	if role := o.Get("role"); len(role) > 0 {
		out.Set("role", jsRestringify(role))
	}
	c := o.Get("content")
	switch s, isStr := jsonobj.String(c); {
	case isStr:
		_ = out.SetValue("content", s)
	case jsonobj.IsNullish(c):
		_ = out.SetValue("content", "")
	default:
		_ = out.SetValue("content", string(jsRestringify(c)))
	}
	b, _ := jsonobj.Marshal(out)
	return b
}

// lastUserText is the text of the last user message: its string, or the
// text parts of its content joined; false when it has none.
func lastUserText(messages []json.RawMessage) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		o, err := jsonobj.Parse(messages[i])
		if err != nil {
			continue
		}
		if r, _ := jsonobj.String(o.Get("role")); r != "user" {
			continue
		}
		c := o.Get("content")
		if s, ok := jsonobj.String(c); ok {
			return s, true
		}
		if parts, ok := jsval.Array(c); ok {
			texts := make([]string, len(parts))
			for k, p := range parts {
				po, err := jsonobj.Parse(p)
				if err != nil {
					continue
				}
				if t, _ := jsonobj.String(po.Get("type")); t == "text" {
					texts[k] = reqbody.JSString(decode(po.Get("text")))
				}
			}
			t := jsval.Trim(strings.Join(texts, " "))
			return t, t != ""
		}
		return "", false
	}
	return "", false
}
