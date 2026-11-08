package chat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestGoldenRAGMatchesNode runs ragProxy.ts and mcp.ts (scripts/golden/
// chat.mjs) and this package's /v1 and /api/mcp against one fake gateway
// and compares the answers, every gateway request and the call log.

type httpCase struct {
	name, method, path string
	headers            map[string]string
	body               string
	replies            []reply
	// goStatus is set where Go answers differently on purpose: its status.
	goStatus int
}

type httpAnswer struct {
	status      int
	contentType string
	body        string
}

func doHTTP(t *testing.T, base string, c httpCase) httpAnswer {
	var rd io.Reader
	if c.body != "" {
		rd = strings.NewReader(c.body)
	}
	req, _ := http.NewRequest(c.method, base+c.path, rd)
	if c.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return httpAnswer{res.StatusCode, res.Header.Get("Content-Type"), string(b)}
}

var (
	createdRe  = regexp.MustCompile(`"created":\d+`)
	studioIDRe = regexp.MustCompile(`"id":"studio-[0-9a-z]+"`)
	durMsRe    = regexp.MustCompile(`"durationMs":\d+`)
)

func normalRAG(s string) string {
	s = normal(s)
	s = createdRe.ReplaceAllString(s, `"created":N`)
	s = studioIDRe.ReplaceAllString(s, `"id":"studio-N"`)
	return durMsRe.ReplaceAllString(s, `"durationMs":N`)
}

func ragCases() []httpCase {
	bearer := map[string]string{"Authorization": "Bearer caller-key"}
	with := func(h map[string]string) map[string]string {
		out := map[string]string{"Authorization": "Bearer caller-key"}
		for k, v := range h {
			out[k] = v
		}
		return out
	}
	long := strings.Repeat("The kickoff agenda lists the topics. ", 3)
	whole := func(content string) reply {
		return reply{whole: true, body: map[string]any{"id": "up-1", "model": "test-model",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}}}
	}
	msgs := `[{"role":"system","content":"client rules"},{"role":"user","content":"what is on the agenda?"}]`
	return []httpCase{
		{name: "models without a credential", method: "GET", path: "/v1/models"},
		{name: "models", method: "GET", path: "/v1/models", headers: bearer},
		{name: "models, all of them", method: "GET", path: "/v1/models?all=1", headers: bearer},
		{name: "a call without a credential", method: "POST", path: "/v1/chat/completions", body: `{"model":"studio-agent","messages":[]}`},
		{name: "no underlying model", method: "POST", path: "/v1/chat/completions", headers: bearer, body: `{"model":"studio-rag/","messages":[]}`},
		{name: "passthrough, whole", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body:    `{"temperature":1.0,"model":"test-model","2":"x","messages":` + msgs + `,"max_tokens":50,"1":"y","s":"a<b"}`,
			replies: []reply{whole("Grounded.")}},
		{name: "passthrough, streamed, retrieval off", method: "POST", path: "/v1/chat/completions", headers: with(map[string]string{"X-RAG-Off": "1", "X-Allocation": "acct"}),
			body: `{"model":"studio-rag/test-model","stream":true,"messages":` + msgs + `}`, replies: []reply{text("Streamed ", "through.")}},
		{name: "passthrough, the gateway refuses", method: "POST", path: "/v1/chat/completions", headers: with(map[string]string{"X-RAG-Top-K": "3", "X-RAG-Tags": "a, b,,"}),
			body:    `{"messages":[{"role":"user","content":[{"type":"text","text":"parts"},{"type":"image_url"}]}],"model":"other-model"}`,
			replies: []reply{{status: 400, body: map[string]any{"error": map[string]any{"message": "no such model"}}}}},
		{name: "agent, whole, a tool round", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body: `{"model":"studio-agent/test-model","messages":[{"role":"system","content":"dropped"},{"role":"user","content":"list my notes"},{"role":"assistant","content":null},{"role":"user","content":{"k":[1.0,2]}}]}`,
			replies: []reply{
				{frames: []any{delta(map[string]any{"tool_calls": []any{
					map[string]any{"index": 0, "id": "c1", "type": "function", "function": map[string]any{"name": "list_kb_dir", "arguments": `{"path":"notes"}`}},
					map[string]any{"index": 1, "id": "c2", "type": "function", "function": map[string]any{"name": "studio_docs", "arguments": `{"doc":"nope"}`}},
					map[string]any{"index": 2, "id": "c3", "type": "function", "function": map[string]any{"name": "stop_cluster", "arguments": `{"clusterName":"a"}`}}}}), finish("tool_calls")}},
				call("list_kb_dir", `{"path":"notes"}`), text(long)}},
		{name: "agent, streamed", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body: `{"model":"studio-agent/test-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`, replies: []reply{text("Hello", " there.")}},
		{name: "voice, bare, its default model", method: "POST", path: "/v1/chat/completions", headers: with(map[string]string{"X-Studio-Injected-Key": "1"}),
			body: `{"model":"studio-voice","messages":[{"role":"user","content":"hi"}]}`, replies: []reply{text("Spoken.")}},
		{name: "agent, whole, the gateway fails", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body: `{"model":"studio-agent/test-model","messages":[{"role":"user","content":"hi"}]}`, replies: []reply{{status: 400, body: map[string]any{"error": map[string]any{"message": "unsupported parameter: tools"}}}}},
		{name: "agent, streamed, the gateway fails", method: "POST", path: "/v1/chat/completions", headers: bearer,
			body: `{"model":"studio-agent/test-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`, replies: []reply{{status: 401, body: map[string]any{"error": map[string]any{"message": "invalid key"}}}}},
		// Node's error handler turns Fastify's 400 for a body that is not
		// JSON into a 500; Go answers 400, as every Go route does.
		{name: "a body that is not JSON", method: "POST", path: "/v1/chat/completions", headers: bearer, body: `{"model":`, goStatus: 400},
		{name: "the call log", method: "GET", path: "/api/rag/calls"},

		{name: "mcp initialize", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`},
		{name: "mcp initialize, no version", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":"a","method":"initialize"}`},
		{name: "mcp ping", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":2,"method":"ping"}`},
		{name: "mcp notification", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{name: "mcp tools", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`},
		{name: "mcp a Go tool", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_labels"}}`},
		{name: "mcp the docs", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"studio_docs","arguments":{"doc":"nope"}}}`},
		{name: "mcp a Node tool", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"list_kb_dir","arguments":{"path":"notes"}}}`},
		{name: "mcp a tool not exposed", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"write_kb_file","arguments":{}}}`},
		{name: "mcp a board tool without a token", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"board_post","arguments":{"body":"x"}}}`},
		{name: "mcp no method", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":9}`},
		{name: "mcp an unknown method", method: "POST", path: "/api/mcp", body: `{"jsonrpc":"2.0","id":null,"method":"resources/list"}`},
		{name: "mcp a bearer that is no task's", method: "POST", path: "/api/mcp", headers: bearer, body: `{"jsonrpc":"2.0","id":10,"method":"tools/list"}`},
		{name: "mcp GET", method: "GET", path: "/api/mcp"},
	}
}

func TestGoldenRAGMatchesNode(t *testing.T) {
	g := startGolden(t)
	for _, c := range ragCases() {
		t.Run(c.name, func(t *testing.T) { runHTTPCase(t, g, c) })
	}
}

// runHTTPCase sends one request to both servers and compares the answers
// and the gateway requests.
func runHTTPCase(t *testing.T, g *goldenEnv, c httpCase) {
	run := func(base string) (httpAnswer, []json.RawMessage) {
		g.gw.load(c.replies)
		a := doHTTP(t, base, c)
		g.gw.mu.Lock()
		defer g.gw.mu.Unlock()
		return a, append([]json.RawMessage(nil), g.gw.requests...)
	}
	n, nreq := run(g.node)
	o, oreq := run(g.goURL)
	if os.Getenv("GOLDEN_SHOW") != "" {
		t.Logf("node: %d %s\n%s\ngo: %d %s\n%s", n.status, n.contentType, n.body, o.status, o.contentType, o.body)
		for _, q := range oreq {
			t.Logf("go request: %s", q)
		}
	}
	if c.goStatus != 0 {
		if n.status != 500 || o.status != c.goStatus {
			t.Errorf("status node %d (want 500), go %d (want %d)", n.status, o.status, c.goStatus)
		}
		return
	}
	if n.status != o.status || n.contentType != o.contentType {
		t.Errorf("status node %d %q, go %d %q", n.status, n.contentType, o.status, o.contentType)
	}
	if normalRAG(n.body) != normalRAG(o.body) {
		t.Errorf("body differs\nnode: %s\ngo:   %s", normalRAG(n.body), normalRAG(o.body))
	}
	if len(nreq) != len(oreq) {
		t.Fatalf("gateway calls: node %d, go %d", len(nreq), len(oreq))
	}
	for k := range nreq {
		if normalRAG(string(nreq[k])) != normalRAG(string(oreq[k])) {
			t.Errorf("gateway request %d differs\nnode: %s\ngo:   %s", k, normalRAG(string(nreq[k])), normalRAG(string(oreq[k])))
		}
	}
}

// TestRAGContextSplice checks the studio-rag request with retrieval found:
// the context block after the client's leading system messages, the call
// log's retrieval, and a failed retrieval passed through or refused.
func TestRAGContextSplice(t *testing.T) {
	m, ts, gw, _ := newTestModule(t, nil)
	srv := ts.URL
	m.rag.retrieve = func(r *http.Request, q string, topK float64, tags []string) (bool, *ragContext, error) {
		if q != "the agenda?" || topK != 4 || strings.Join(tags, "|") != "a|b" {
			t.Errorf("retrieve(%q, %v, %q)", q, topK, tags)
		}
		return true, &ragContext{Text: "[1] notes/a.md\nAgenda.", Terms: []string{"agenda"}, Blocks: 1, Chars: 7}, nil
	}
	gw.load([]reply{{whole: true, body: map[string]any{"ok": true}}})
	body := `{"model":"m1","messages":[{"role":"system","content":"s1"},{"role":"system","content":"s2"},{"role":"user","content":"the agenda?"}]}`
	req, _ := http.NewRequest("POST", srv+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("X-RAG-Top-K", "4")
	req.Header.Set("X-RAG-Tags", "a,b")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var sent struct {
		Messages []struct{ Role, Content string }
	}
	json.Unmarshal(gw.requests[0], &sent)
	if len(sent.Messages) != 4 || sent.Messages[2].Role != "system" || !strings.HasPrefix(sent.Messages[2].Content, "Context blocks retrieved") ||
		!strings.HasSuffix(sent.Messages[2].Content, "\n\n[1] notes/a.md\nAgenda.") || sent.Messages[3].Content != "the agenda?" {
		t.Fatalf("sent %s", gw.requests[0])
	}
	if c := m.rag.calls[0]; c.Retrieval == nil || c.Retrieval.Blocks != 1 || c.Detail != nil || c.Status != "ok" {
		t.Fatalf("logged %+v", c)
	}

	m.rag.retrieve = func(*http.Request, string, float64, []string) (bool, *ragContext, error) {
		return false, nil, io.ErrUnexpectedEOF
	}
	post := func() *http.Response {
		req, _ := http.NewRequest("POST", srv+"/v1/chat/completions", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer k")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	gw.load([]reply{{whole: true, body: map[string]any{"ok": true}}})
	post().Body.Close()
	if d := m.rag.calls[0].Detail; d == nil || *d != "retrieval failed; passed through without context" {
		t.Fatalf("fail-open detail %v", d)
	}
	m.rag.failOpen = false
	res = post()
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 502 || string(b) != `{"error":{"message":"retrieval failed: unexpected EOF"}}` {
		t.Fatalf("fail-closed %d %s", res.StatusCode, b)
	}
}

// TestMCPTaskBearer checks /api/mcp with the task board set: a task's
// bearer lists and runs the board tools here, another bearer gets the
// corpus tools only, and without the board a bearer is left to Node.
func TestMCPTaskBearer(t *testing.T) {
	m, ts, _, _ := newTestModule(t, nil)
	var asked []string
	m.A.Providers.TaskBoard = func(token, agent, tool string, args json.RawMessage) (bool, json.RawMessage) {
		asked = append(asked, token+" "+agent+" "+tool+" "+string(args))
		if token != "task-1" {
			return false, nil
		}
		if tool == "" {
			return true, nil
		}
		return true, json.RawMessage(`{"ok":true}`)
	}
	rpc := func(bearer, body string) (int, string) {
		req, _ := http.NewRequest("POST", ts.URL+"/api/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set("X-Task-Agent", "w1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if _, b := rpc("task-1", list); !strings.Contains(b, `"name":"board_offer_task"`) || !strings.Contains(b, `"name":"search_kb"`) {
		t.Errorf("task list %s", b)
	}
	if _, b := rpc("other", list); strings.Contains(b, "board_") {
		t.Errorf("other bearer %s", b)
	}
	if _, b := rpc("task-1", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"board_post","arguments":{"body":"hi","n":1.0}}}`); b != `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}],"isError":false}}` {
		t.Errorf("board_post %s", b)
	}
	if last := asked[len(asked)-1]; last != `task-1 w1 board_post {"body":"hi","n":1}` {
		t.Errorf("board asked %q", last)
	}
	if names := m.TaskToolNames(); len(names) != 13 || names[0] != "search_kb" || names[12] != "board_offer_task" {
		t.Errorf("task tools %q", names)
	}
	m.A.Providers.TaskBoard = nil
	if st, _ := rpc("task-1", list); st != 501 {
		t.Errorf("without the board: %d", st)
	}
}
