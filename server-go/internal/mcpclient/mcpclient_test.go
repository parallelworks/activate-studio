package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// fakeMCP is a streamable-HTTP MCP server: "plain" answers JSON, "sse"
// answers each request as one event, "broken" refuses tools/list.
func fakeMCP(t *testing.T) (*httptest.Server, *[]string) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		kind := strings.Trim(r.URL.Path, "/")
		seen = append(seen, kind+" "+req.Method+" session="+r.Header.Get("Mcp-Session-Id")+" auth="+r.Header.Get("X-Api-Key"))
		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-"+kind)
			result = map[string]any{"protocolVersion": "2025-06-18"}
		case "tools/list":
			if kind == "broken" {
				w.WriteHeader(500)
				io.WriteString(w, "list failed")
				return
			}
			result = map[string]any{"tools": []any{
				map[string]any{"name": "find.deals", "description": "Find deals", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}},
				map[string]any{"name": "ping"},
			}}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			switch p.Name {
			case "fail":
				out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -1, "message": "no such deal"}})
				w.Write(out)
				return
			case "ping":
				result = "pong"
			default:
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "args " + string(p.Arguments)}, map[string]any{"type": "image", "data": "x"}}}
			}
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if kind == "sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", out)
			return
		}
		w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func servers(url string) []json.RawMessage {
	return []json.RawMessage{
		json.RawMessage(`{"name":"crm","url":"` + url + `/plain","header":"X-Api-Key: secret-1"}`),
		json.RawMessage(`{"name":"desk","url":"` + url + `/sse"}`),
		json.RawMessage(`{"name":"bad","url":"` + url + `/broken"}`),
		json.RawMessage(`{"name":"","url":"` + url + `/plain"}`),
	}
}

var callCases = []map[string]any{
	{"name": "crm__find_deals", "args": map[string]any{"q": "pumps"}},
	{"name": "desk__ping", "args": nil},
	{"name": "crm__fail", "args": map[string]any{}},
	{"name": "gone__x", "args": map[string]any{}},
}

func goView(t *testing.T, c *Client) map[string]any {
	ctx := context.Background()
	var specs []any
	for _, s := range c.ToolSpecs(ctx) {
		specs = append(specs, map[string]any{"type": "function", "function": map[string]any{"name": s.Name, "description": s.Description, "parameters": s.Parameters}})
	}
	out := map[string]any{"specs": specs, "status": c.Status(ctx)}
	var remote []bool
	var calls []any
	for _, x := range callCases {
		remote = append(remote, c.IsRemoteTool(x["name"].(string)))
		args, _ := json.Marshal(x["args"])
		if x["args"] == nil {
			args = nil
		}
		s, err := c.Call(ctx, x["name"].(string), args)
		if err != nil {
			calls = append(calls, map[string]any{"error": err.Error()})
		} else {
			calls = append(calls, map[string]any{"ok": s})
		}
	}
	out["remote"], out["calls"] = remote, calls
	return out
}

func TestClientListsCallsAndCaches(t *testing.T) {
	srv, seen := fakeMCP(t)
	list := servers(srv.URL)
	c := New(func() []json.RawMessage { return list })
	v := goView(t, c)
	b, _ := json.Marshal(v["specs"])
	if !strings.Contains(string(b), `"name":"crm__find_deals"`) || !strings.Contains(string(b), `"description":"[desk] "`) {
		t.Errorf("specs %s", b)
	}
	st := c.Status(context.Background())
	if len(st) != 3 || st[2].Error == nil || !strings.HasPrefix(*st[2].Error, "500 list failed") || st[0].Tools != 2 {
		t.Errorf("status %+v", st)
	}
	calls, _ := json.Marshal(v["calls"])
	if !strings.Contains(string(calls), `args {\"q\":\"pumps\"}\n{\"data\":\"x\",\"type\":\"image\"}`) || !strings.Contains(string(calls), `"ok":"pong"`) || !strings.Contains(string(calls), `"error":"no such deal"`) {
		t.Errorf("calls %s", calls)
	}
	// One listing per server inside the minute; the header and session ride along.
	n := len(*seen)
	c.ToolSpecs(context.Background())
	if len(*seen) != n {
		t.Errorf("listed again inside the TTL")
	}
	joined := strings.Join(*seen, "\n")
	if !strings.Contains(joined, "plain tools/list session=sess-plain auth=secret-1") || !strings.Contains(joined, "sse tools/call session=sess-sse") {
		t.Errorf("requests:\n%s", joined)
	}
	if cs := c.CachedStatus(); cs[0]["checkedAt"] == nil || cs[0]["tools"] != 2 {
		t.Errorf("cached %v", cs)
	}
	// A changed list drops the cache.
	list = list[:1]
	if cs := c.CachedStatus(); len(cs) != 1 || cs[0]["checkedAt"] != nil {
		t.Errorf("after a change %v", cs)
	}
}

// TestClientMatchesNode runs mcpClient.ts against the same server.
func TestClientMatchesNode(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	srv, _ := fakeMCP(t)
	list := servers(srv.URL)
	dir := t.TempDir()
	ix := filepath.Join(dir, "ix")
	os.MkdirAll(ix, 0o755)
	settings, _ := json.Marshal(map[string]any{"mcpServers": list})
	os.WriteFile(filepath.Join(ix, "settings.json"), settings, 0o644)
	in, _ := json.Marshal(callCases)
	cmd := exec.Command(node, "../../scripts/golden/chat.mjs", "../../..", "mcp")
	cmd.Env = append(os.Environ(), "INDEX_BASE="+ix, "KB_ROOT="+dir, "HOME="+dir)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	c := New(func() []json.RawMessage { return list })
	got, _ := jsonobj.Marshal(goView(t, c))
	var want, have any
	_ = json.Unmarshal(raw, &want)
	_ = json.Unmarshal(got, &have)
	w, _ := json.Marshal(want)
	h, _ := json.Marshal(have)
	if string(w) != string(h) {
		t.Errorf("differs\n go:   %s\n node: %s", h, w)
	}
}
