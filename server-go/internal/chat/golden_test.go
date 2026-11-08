package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// The golden tests run the Node chat routes (scripts/golden/chat.mjs)
// and this package against one fake gateway replaying the same scripted
// answers, and compare what each sends the browser and the gateway.
// STUDIO_GOLDEN_NODE names the node binary; server/dist must be built.

// reply is one scripted gateway answer.
type reply struct {
	status int
	// sse frames (data objects), or a JSON body when status is set or
	// whole is true
	frames []any
	body   any
	whole  bool
}

type fakeGateway struct {
	mu       sync.Mutex
	queue    []reply
	requests []json.RawMessage
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/models"):
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"test-model","object":"model"}]}`)
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		b, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.requests = append(g.requests, b)
		var rp reply
		if len(g.queue) > 0 {
			rp, g.queue = g.queue[0], g.queue[1:]
		} else {
			rp = reply{frames: []any{delta(map[string]any{"content": "(script ran out)"}), finish("stop")}}
		}
		g.mu.Unlock()
		if rp.status != 0 || rp.whole {
			w.Header().Set("Content-Type", "application/json")
			if rp.status != 0 {
				w.WriteHeader(rp.status)
			}
			json.NewEncoder(w).Encode(rp.body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range rp.frames {
			b, _ := json.Marshal(f)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		io.WriteString(w, "data: [DONE]\n\n")
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeGateway) load(rs []reply) {
	g.mu.Lock()
	g.queue = append([]reply(nil), rs...)
	g.requests = nil
	g.mu.Unlock()
}

func delta(d map[string]any) map[string]any {
	return map[string]any{"model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": d}}}
}

func finish(reason string) map[string]any {
	return map[string]any{"model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}}}
}

func text(pieces ...string) reply {
	var fs []any
	for _, p := range pieces {
		fs = append(fs, delta(map[string]any{"content": p}))
	}
	return reply{frames: append(fs, finish("stop"))}
}

func call(name string, args string) reply {
	return reply{frames: []any{
		delta(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_" + name, "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}),
		finish("tool_calls"),
	}}
}

type goldenEnv struct {
	gw     *fakeGateway
	node   string
	goURL  string
	module *Module
}

func startGolden(t *testing.T) *goldenEnv {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	dir := t.TempDir()
	kb, ix := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	os.MkdirAll(filepath.Join(kb, "notes"), 0o755)
	os.MkdirAll(ix, 0o755)
	os.WriteFile(filepath.Join(kb, "notes", "kickoff agenda.md"), []byte("# Kickoff\n\nThe agenda.\n"), 0o644)
	gw := &fakeGateway{}
	gs := httptest.NewServer(gw)
	t.Cleanup(gs.Close)
	env := map[string]string{
		"INDEX_BASE": ix, "KB_ROOT": kb, "PW_GATEWAY_URL": gs.URL + "/v1", "PW_API_KEY": "test-key",
		"STUDIO_BRIDGE_SECRET": "golden-secret", "PW_CREDENTIALS_DIR": filepath.Join(dir, "nopw"), "PW_CLI": "/bin/false",
		"HOME": dir, "AI_ENABLED": "1",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	for _, k := range []string{"AUTH_JWKS_URL", "OPENAI_BASE_URL", "OPENAI_API_KEY", "SIDECAR_MODEL_URL", "PW_CONTEXT", "PW_ALLOCATION"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	cmd := exec.Command(node, "../../scripts/golden/chat.mjs", "../../..", "serve")
	cmd.Env = os.Environ()
	stdin, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("node did not start: %v", err)
	}
	nodeURL := "http://127.0.0.1:" + strings.TrimSpace(line)
	cfg, _ := config.Load(func(k string) string {
		// Its own INDEX_BASE: conversations.json has one writer at a time
		// (Node's run notes go through Go), and here both routes write.
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix-go"), "KB_ROOT": kb, "STUDIO_NODE_URL": nodeURL, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	u, _ := url.Parse(nodeURL)
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, app.NewNode(u, nil, ""))
	mux := http.NewServeMux()
	Register(mux, a)
	gsrv := httptest.NewServer(mux)
	t.Cleanup(gsrv.Close)
	return &goldenEnv{gw: gw, node: nodeURL, goURL: gsrv.URL, module: Default}
}

func post(t *testing.T, base string, body map[string]any) string {
	b, _ := json.Marshal(body)
	res, err := http.Post(base+"/api/chat/stream", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return string(out)
}

var (
	keepaliveRe = regexp.MustCompile(`: keepalive\n\n`)
	srvIDRe     = regexp.MustCompile(`srv-\d+`)
	emIDRe      = regexp.MustCompile(`em-[0-9a-z]{8}-\d+-`)
	ranRe       = regexp.MustCompile(`in \d+s\.`)
	convRe      = regexp.MustCompile(`conv-(node|go)`)
	dateRe      = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T[0-9:.]+Z`)
	nonceRe     = regexp.MustCompile(`nonce(\\?)":(\\?)"[0-9a-z]{8}`)
	// The corpus line of the system prompt follows the index, which the
	// conversation exports of the earlier runs build.
	corpusRe = regexp.MustCompile(`\\n(Corpus index not yet built; search_kb falls back to grep\.|Indexed corpus: [^\\]*)\\n`)
	durRe    = regexp.MustCompile(`"reasoningDuration":\d+`)
	idRe     = regexp.MustCompile(`"id":"[0-9a-f-]{36}"`)
	refRe    = regexp.MustCompile(`\(ref [0-9a-z]+\)`)
)

func normal(s string) string {
	s = keepaliveRe.ReplaceAllString(s, "")
	s = srvIDRe.ReplaceAllString(s, "srv-N")
	s = emIDRe.ReplaceAllString(s, "em-X-N-")
	s = ranRe.ReplaceAllString(s, "in Ns.")
	s = convRe.ReplaceAllString(s, "conv-X")
	s = dateRe.ReplaceAllString(s, "DATE")
	s = nonceRe.ReplaceAllString(s, `nonce$1":$2"N`)
	s = corpusRe.ReplaceAllString(s, `\nCORPUS\n`)
	return refRe.ReplaceAllString(s, "(ref R)")
}

type scenario struct {
	name    string
	model   string
	text    string
	replies []reply
	extra   map[string]any
}

func scenarios() []scenario {
	long := strings.Repeat("The kickoff agenda lists the topics for the first meeting. ", 5)
	return []scenario{
		{name: "plain", text: "hello there", replies: []reply{text("Hello", " world.")}},
		{name: "citation split across pieces", text: "where is the agenda?", replies: []reply{text("See [relative/path/notes/kick", "off agenda.md] for it, and [an aside].")}},
		{name: "a tool round, then the answer", text: "what is in my folder?", replies: []reply{call("list_kb_dir", `{"path":"notes"}`), text(long + "[notes/kickoff agenda.md]")}},
		{name: "a tool that is not offered", text: "do it", replies: []reply{call("no_such_tool", `{}`), text(long)}},
		{name: "short answer after tools forces a final turn", text: "list my notes", replies: []reply{call("list_kb_dir", `{"path":"notes"}`), text("Done."), text("The final answer.")}},
		{name: "reasoning and no answer", text: "think hard", replies: []reply{{frames: []any{delta(map[string]any{"reasoning_content": "hmm"}), finish("stop")}}}},
		{name: "empty reply", text: "say nothing", replies: []reply{{frames: []any{finish("stop")}}}},
		{name: "gateway refuses a parameter", text: "hi", replies: []reply{{status: 400, body: map[string]any{"error": map[string]any{"message": "unsupported parameter: foo"}}}}},
		{name: "rate limited once", text: "hi", replies: []reply{{status: 429, body: map[string]any{"error": map[string]any{"message": "rate limit exceeded"}}}, text("After the wait.")}},
		{name: "slash help", text: "/help", replies: nil},
		{name: "whole answer for a no-stream model", model: "gpt-oss-120b", text: "hi", replies: []reply{{whole: true, body: map[string]any{"model": "gpt-oss-120b",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "Whole answer, " + long}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}}}},
		{name: "emulated tools", model: "genai-pro", text: "what does the kickoff agenda say about topics?", replies: []reply{
			{whole: true, body: map[string]any{"model": "genai-pro", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": `{"protocol":"studio-tools-1","calls":[{"name":"list_kb_dir","arguments":{"path":"notes"}}]}`}, "finish_reason": "stop"}}}},
			{whole: true, body: map[string]any{"model": "genai-pro", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "It lists topics. " + long}, "finish_reason": "stop"}}}},
		}},
		{name: "two calls in a round, then the same call again", text: "compare my notes", replies: []reply{
			{frames: []any{delta(map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "c1", "type": "function", "function": map[string]any{"name": "list_kb_dir", "arguments": `{"path":"notes"}`}},
				map[string]any{"index": 1, "id": "c2", "type": "function", "function": map[string]any{"name": "read_kb_file", "arguments": `{"path":"notes/kickoff agenda.md"}`}}}}), finish("tool_calls")}},
			call("list_kb_dir", `{"path":"notes"}`), text(long + " [notes/kickoff agenda.md]")}},
		{name: "more_tools with a group that does not exist", text: "more", replies: []reply{call("more_tools", `{"group":"nope"}`), text(long)}},
		{name: "earlier tool traffic in the history", text: "and now?", extra: map[string]any{"messages": []any{
			map[string]any{"role": "user", "content": "first"},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "x", "type": "function", "function": map[string]any{"name": "list_kb_dir", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": "x", "content": "a result"},
			map[string]any{"role": "assistant", "content": "An earlier answer."},
			map[string]any{"role": "system", "content": "client system text"},
			map[string]any{"role": "user", "content": "and now?"}}}, replies: []reply{text("Now this.")}},
		{name: "usage is summed over turns", text: "count", replies: []reply{
			{frames: []any{delta(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "u1", "type": "function", "function": map[string]any{"name": "list_kb_dir", "arguments": "{}"}}}}),
				map[string]any{"model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3}}}},
			{frames: []any{delta(map[string]any{"content": long}), map[string]any{"model": "served-name", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 9, "total_tokens": 29}}}}}},
		{name: "a steer is not queued, labels scope the prompt", text: "scoped", extra: map[string]any{"labelScope": []any{"alpha", "beta"}}, replies: []reply{text("Scoped answer.")}},
	}
}

func TestGoldenStreamMatchesNode(t *testing.T) {
	g := startGolden(t)
	for i, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			model := sc.model
			if model == "" {
				model = "test-model"
			}
			run := func(base, who string) (string, []json.RawMessage) {
				g.gw.load(sc.replies)
				body := map[string]any{"model": model, "conversationId": fmt.Sprintf("conv-%s-%d", who, i), "userMessageId": "u1",
					"messages": []any{map[string]any{"role": "user", "content": sc.text}}}
				for k, v := range sc.extra {
					body[k] = v
				}
				sse := post(t, base, body)
				g.gw.mu.Lock()
				defer g.gw.mu.Unlock()
				return sse, append([]json.RawMessage(nil), g.gw.requests...)
			}
			nodeSSE, nodeReq := run(g.node, "node")
			goSSE, goReq := run(g.goURL, "go")
			if os.Getenv("GOLDEN_SHOW") != "" {
				t.Logf("node:\n%s\ngo:\n%s\nrequests: %d", nodeSSE, goSSE, len(goReq))
			}
			if normal(nodeSSE) != normal(goSSE) {
				t.Errorf("SSE differs\nnode:\n%s\ngo:\n%s", normal(nodeSSE), normal(goSSE))
			}
			// The stored conversations, as each server reads its own back.
			time.Sleep(150 * time.Millisecond)
			get := func(base, who string) string {
				res, err := http.Get(fmt.Sprintf("%s/api/chat/conversations/conv-%s-%d", base, who, i))
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				b, _ := io.ReadAll(res.Body)
				return durRe.ReplaceAllString(idRe.ReplaceAllString(normal(string(b)), `"id":"ID"`), `"reasoningDuration":D`)
			}
			if n, g2 := get(g.node, "node"), get(g.goURL, "go"); n != g2 {
				t.Errorf("stored conversation differs\nnode: %s\ngo:   %s", n, g2)
			}
			if len(nodeReq) != len(goReq) {
				t.Fatalf("gateway calls: node %d, go %d", len(nodeReq), len(goReq))
			}
			for k := range nodeReq {
				if normal(string(nodeReq[k])) != normal(string(goReq[k])) {
					t.Errorf("gateway request %d differs\nnode: %s\ngo:   %s", k, normal(string(nodeReq[k])), normal(string(goReq[k])))
				}
			}
		})
	}
}
