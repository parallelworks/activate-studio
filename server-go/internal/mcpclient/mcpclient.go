// Package mcpclient is mcpClient.ts: this Studio as a client of the remote
// MCP servers a deployment attaches in Settings, so their tools join the
// assistant's own. Remote HTTP servers only. A remote server's tool names
// and descriptions are untrusted text the model reads, so they are
// namespaced under the server's name and length-capped.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/netx"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

const (
	ttl = 60 * time.Second
	// listTimeout is each step of a listing (initialize, then tools/list).
	listTimeout = 5 * time.Second
	callTimeout = 60 * time.Second
	// Sep separates the server name from the remote tool name.
	Sep = "__"
)

// Server is one attached server.
type Server struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Header string `json:"header,omitempty"`
}

// Spec is a remote tool as the model is sent it.
type Spec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type entry struct {
	at      time.Time
	tools   []Spec
	err     *string
	session string
}

// Status is one server's connection state.
type Status struct {
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	Tools     int     `json:"tools"`
	Error     *string `json:"error"`
	CheckedAt *string `json:"checkedAt,omitempty"`
}

// Client holds the listing cache.
type Client struct {
	// Servers reads the saved list (effectiveSettings().mcpServers).
	Servers func() []json.RawMessage
	HTTP    *http.Client

	mu        sync.Mutex
	cache     map[string]*entry
	cachedFor string
}

// New is a client over the saved server list.
func New(servers func() []json.RawMessage) *Client {
	return &Client{Servers: servers, HTTP: http.DefaultClient, cache: map[string]*entry{}}
}

// RemoteServers is the attached servers that name a name and a URL. A
// different list from the one the cache was filled for drops the cache.
func (c *Client) RemoteServers() []Server {
	var out []Server
	var kept []json.RawMessage
	for _, raw := range c.Servers() {
		var o map[string]json.RawMessage
		if json.Unmarshal(raw, &o) != nil {
			continue
		}
		if !jsval.Truthy(o["name"]) || !jsval.Truthy(o["url"]) {
			continue
		}
		out = append(out, Server{Name: jsval.String(o["name"]), URL: jsval.String(o["url"]), Header: jsval.String(o["header"])})
		kept = append(kept, raw)
	}
	key, _ := jsonobj.Marshal(orEmpty(kept))
	c.mu.Lock()
	if string(key) != c.cachedFor {
		c.cache = map[string]*entry{}
		c.cachedFor = string(key)
	}
	c.mu.Unlock()
	return out
}

func orEmpty(v []json.RawMessage) []json.RawMessage {
	if v == nil {
		return []json.RawMessage{}
	}
	return v
}

// Invalidate drops the cached listings.
func (c *Client) Invalidate() {
	c.mu.Lock()
	c.cache = map[string]*entry{}
	c.mu.Unlock()
}

func headersFor(s Server, session string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	if s.Header != "" {
		if i := strings.Index(s.Header, ":"); i > 0 {
			h.Set(jsval.Trim(s.Header[:i]), jsval.Trim(s.Header[i+1:]))
		}
	}
	if session != "" {
		h.Set("Mcp-Session-Id", session)
	}
	return h
}

type statusError struct{ msg string }

func (e *statusError) Error() string { return e.msg }

// rpc is one JSON-RPC round trip.
func (c *Client) rpc(ctx context.Context, s Server, method string, params any, session string, timeout time.Duration) (json.RawMessage, string, error) {
	id := time.Now().UnixMilli()
	body, _ := jsonobj.Marshal(reqbody.O("jsonrpc", "2.0", "id", id, "method", method, "params", params))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", s.URL, bytes.NewReader(body))
	if err != nil {
		return nil, session, err
	}
	req.Header = headersFor(s, session)
	res, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, session, &timeoutError{err}
		}
		return nil, session, err
	}
	defer res.Body.Close()
	next := session
	if v := res.Header.Get("Mcp-Session-Id"); v != "" {
		next = v
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, next, err
	}
	text := string(raw)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, next, &statusError{fmt.Sprintf("%d %s", res.StatusCode, jsval.Slice(text, 160))}
	}
	payload := text
	if t := strings.TrimLeft(text, " \t\r\n"); strings.HasPrefix(t, "event:") || strings.HasPrefix(t, "data:") {
		var parts []string
		for _, l := range strings.Split(text, "\n") {
			if strings.HasPrefix(l, "data:") {
				parts = append(parts, jsval.Trim(l[5:]))
			}
		}
		payload = strings.Join(parts, "")
	}
	var msg struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return nil, next, fmt.Errorf("%s", jsonParseMessage(payload))
	}
	if !jsonobj.IsNullish(msg.Error) && len(msg.Error) > 0 {
		var e map[string]json.RawMessage
		_ = json.Unmarshal(msg.Error, &e)
		m := ""
		if v, ok := e["message"]; ok && !jsonobj.IsNullish(v) {
			m = jsval.String(v)
		} else {
			m = string(msg.Error)
		}
		return nil, next, fmt.Errorf("%s", jsval.Slice(m, 200))
	}
	return msg.Result, next, nil
}

// jsonParseMessage stands in for JSON.parse's SyntaxError text.
func jsonParseMessage(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Unexpected end of JSON input"
	}
	return "Unexpected token in JSON"
}

type timeoutError struct{ err error }

func (e *timeoutError) Error() string { return "The operation was aborted due to timeout" }
func (e *timeoutError) Unwrap() error { return e.err }

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func (c *Client) listFor(ctx context.Context, s Server) *entry {
	c.mu.Lock()
	if hit := c.cache[s.Name]; hit != nil && time.Since(hit.at) < ttl {
		c.mu.Unlock()
		return hit
	}
	c.mu.Unlock()
	e := &entry{at: time.Now(), tools: []Spec{}}
	err := func() error {
		params := reqbody.O("protocolVersion", "2025-06-18", "capabilities", jsonobj.New(), "clientInfo", reqbody.O("name", "activate-studio", "version", "0.1.0"))
		_, session, err := c.rpc(ctx, s, "initialize", params, "", listTimeout)
		if err != nil {
			return err
		}
		e.session = session
		res, _, err := c.rpc(ctx, s, "tools/list", jsonobj.New(), e.session, listTimeout)
		if err != nil {
			return err
		}
		var listed struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(res, &listed)
		if len(listed.Tools) > 60 {
			listed.Tools = listed.Tools[:60]
		}
		for _, t := range listed.Tools {
			name := jsval.Slice(s.Name+Sep+unsafeName.ReplaceAllString(stringOr(t["name"]), "_"), 64)
			params := t["inputSchema"]
			if jsonobj.IsNullish(params) || len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			e.tools = append(e.tools, Spec{Name: name, Description: "[" + s.Name + "] " + jsval.Slice(stringOr(t["description"]), 400), Parameters: params})
		}
		return nil
	}()
	if err != nil {
		host := s.URL
		if u, perr := url.Parse(s.URL); perr == nil && u.Host != "" {
			host = u.Host
		}
		msg := netx.ConnectionProblem(err, host, os.Getenv)
		if msg == "" {
			if f := netx.Failure(err); f != nil {
				msg = strings.SplitN(netx.UnreachableMessage(host, f), ". ", 2)[0] + "."
			} else {
				msg = jsval.Slice(err.Error(), 200)
			}
		}
		e.err = &msg
	}
	c.mu.Lock()
	c.cache[s.Name] = e
	c.mu.Unlock()
	return e
}

// stringOr is String(v ?? '').
func stringOr(v json.RawMessage) string {
	if jsonobj.IsNullish(v) || len(v) == 0 {
		return ""
	}
	if s, ok := jsonobj.String(v); ok {
		return s
	}
	return jsval.String(v)
}

func (c *Client) listAll(ctx context.Context, servers []Server) []*entry {
	out := make([]*entry, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func() { defer wg.Done(); out[i] = c.listFor(ctx, s) }()
	}
	wg.Wait()
	return out
}

// ToolSpecs is every attached server's tools.
func (c *Client) ToolSpecs(ctx context.Context) []Spec {
	var out []Spec
	for _, e := range c.listAll(ctx, c.RemoteServers()) {
		out = append(out, e.tools...)
	}
	return out
}

// Status is each server's connection state, contacting each.
func (c *Client) Status(ctx context.Context) []Status {
	servers := c.RemoteServers()
	all := c.listAll(ctx, servers)
	out := make([]Status, len(servers))
	for i, s := range servers {
		out[i] = Status{Name: s.Name, URL: s.URL, Tools: len(all[i].tools), Error: all[i].err}
	}
	return out
}

// CachedStatus is the state as last seen, contacting no server.
func (c *Client) CachedStatus() []map[string]any {
	servers := c.RemoteServers()
	out := make([]map[string]any, len(servers))
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, s := range servers {
		hit := c.cache[s.Name]
		st := map[string]any{"name": s.Name, "url": s.URL, "tools": 0, "error": nil, "checkedAt": nil}
		if hit != nil {
			st["tools"] = len(hit.tools)
			if hit.err != nil {
				st["error"] = *hit.err
			}
			st["checkedAt"] = hit.at.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		out[i] = st
	}
	return out
}

// IsRemoteTool is whether name is an attached server's tool.
func (c *Client) IsRemoteTool(name string) bool {
	i := strings.Index(name, Sep)
	if i <= 0 {
		return false
	}
	for _, s := range c.RemoteServers() {
		if s.Name == name[:i] {
			return true
		}
	}
	return false
}

// Call runs one remote tool and flattens its result to text.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	i := strings.Index(name, Sep)
	if i < 0 {
		return "", fmt.Errorf("no such MCP server: %s", name)
	}
	serverName, toolName := name[:i], name[i+len(Sep):]
	var s *Server
	for _, x := range c.RemoteServers() {
		if x.Name == serverName {
			s = &x
			break
		}
	}
	if s == nil {
		return "", fmt.Errorf("no such MCP server: %s", serverName)
	}
	e := c.listFor(ctx, *s)
	if len(args) == 0 || jsonobj.IsNullish(args) {
		args = json.RawMessage("{}")
	}
	res, _, err := c.rpc(ctx, *s, "tools/call", reqbody.O("name", toolName, "arguments", args), e.session, callTimeout)
	if err != nil {
		return "", err
	}
	var body struct {
		Content json.RawMessage `json:"content"`
	}
	if jsval.IsObject(res) {
		_ = json.Unmarshal(res, &body)
	}
	if items, ok := jsval.Array(body.Content); ok {
		parts := make([]string, len(items))
		for i, it := range items {
			var o map[string]json.RawMessage
			if json.Unmarshal(it, &o) == nil {
				if t, ok := jsonobj.String(o["text"]); ok {
					parts[i] = t
					continue
				}
			}
			b, _ := jsonobj.Marshal(it)
			parts[i] = string(b)
		}
		return strings.Join(parts, "\n"), nil
	}
	if s, ok := jsonobj.String(res); ok {
		return s, nil
	}
	if jsonobj.IsNullish(res) || len(res) == 0 {
		return "{}", nil
	}
	b, _ := jsonobj.Marshal(res)
	return string(b), nil
}
