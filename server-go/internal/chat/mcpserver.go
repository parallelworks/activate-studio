package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// The knowledge base as an MCP server at POST /api/mcp (mcp.ts):
// streamable-HTTP MCP, stateless, one JSON-RPC message per POST. Only the
// corpus-reading tools are exposed, except to a pw code chat's token, which
// gets the Studio's own tools (pwcodetools.go), to a standing agent's
// session, which has one too, and to a person the platform's session proxy
// vouches for, a pw code session of theirs on another machine
// (mcpbridge.go).

// mcpExposed are the tools an MCP client may call.
var mcpExposed = []string{"search_kb", "read_kb_file", "read_original", "list_kb_dir", "query_corpus", "get_labels", "studio_docs"}

const mcpProtocolVersion = "2025-06-18"

// exposedTools is the corpus tools this deployment exposes, in TOOL_SPECS
// order, without those turned off. mcpExposed holds only tools that read.
func (m *Module) exposedTools() []json.RawMessage {
	disabled := map[string]bool{}
	for _, n := range m.eff().DisabledTools {
		disabled[n] = true
	}
	out := []json.RawMessage{}
	for _, s := range builtinSpecs {
		if contains(mcpExposed, s.Name) && !disabled[s.Name] {
			// Each only reads (MCP's readOnlyHint), so an agent in a read-only
			// mode may call it.
			b, _ := jsonobj.Marshal(reqbody.O("name", s.Name, "description", s.Description, "inputSchema", s.Parameters,
				"annotations", reqbody.O("readOnlyHint", true, "destructiveHint", false, "openWorldHint", false)))
			out = append(out, b)
		}
	}
	return out
}

func taskBearer(r *http.Request) string {
	a := strings.Join(r.Header.Values("Authorization"), ", ")
	if strings.HasPrefix(a, "Bearer ") {
		return jsTrim(a[7:])
	}
	return ""
}

func rpcResult(id json.RawMessage, result any) *jsonobj.Object {
	return reqbody.O("jsonrpc", "2.0", "id", id, "result", result)
}

func rpcError(id json.RawMessage, code int, message string) *jsonobj.Object {
	return reqbody.O("jsonrpc", "2.0", "id", id, "error", reqbody.O("code", code, "message", message))
}

func (m *Module) mcpGet(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is identified by Node")
		return nil
	}
	// The SSE half of the transport: this server sends no messages of its own.
	return reqbody.JSON(w, 405, reqbody.O("error", "POST JSON-RPC messages to this endpoint"))
}

func (m *Module) mcpPost(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is identified by Node")
		return nil
	}
	token := taskBearer(r)
	// A pw code chat session's token acts as the person it was made for
	// (pwcodechat.go); it is not a task token.
	chatGrant := false
	var chat *chatToken
	if ct, ok := m.openChatToken(token); ok {
		chat = ct
		cfg := m.A.Cfg
		if ct.User == nil && cfg.AuthEnabled() {
			return reqbody.JSON(w, 401, reqbody.O("error", "authentication required"))
		}
		if ct.User != nil {
			r = auth.WithUser(r, ct.User)
		}
		token, chatGrant = "", true
	} else if strings.HasPrefix(token, chatTokenPrefix) {
		return reqbody.JSON(w, 401, reqbody.O("error", "this chat's pw code token is not valid here; send a new message to renew it"))
	}
	// An unsigned call with some other bearer is refused where sign-in is
	// required. (The retired task engine's board tokens were the one
	// bearer admitted without sign-in.)
	cfg := m.A.Cfg
	if token != "" && user(r) == nil && cfg.AuthEnabled() && cfg.Auth.Required {
		return reqbody.JSON(w, 401, reqbody.O("error", "authentication required"))
	}
	person := !chatGrant && m.personGrant(r, token)
	rb, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	if !m.eff().McpEnabled && !chatGrant {
		return reqbody.JSON(w, 403, reqbody.O("error", "MCP access is disabled for this deployment (Settings, MCP access)"))
	}
	msg := jsonobj.New()
	if o, ok := rb.Value.(map[string]any); ok && o != nil {
		if p, err := jsonobj.Parse(rb.Raw); err == nil {
			msg = p
		}
	}
	rawMethod := msg.Get("method")
	method, isStr := jsonobj.String(rawMethod)
	if !jsonobj.IsNullish(rawMethod) && !isStr {
		// msg.method?.startsWith is not a function.
		return errors.New("msg.method?.startsWith is not a function")
	}
	// Notifications carry no id and expect no body.
	if strings.HasPrefix(method, "notifications/") {
		w.WriteHeader(202)
		return nil
	}
	id := json.RawMessage("null")
	if raw := msg.Get("id"); !jsonobj.IsNullish(raw) {
		id = jsRestringify(raw)
	}
	params, _ := jsonobj.Parse(msg.Get("params"))
	param := func(k string) json.RawMessage {
		if params == nil {
			return nil
		}
		return params.Get(k)
	}
	switch method {
	case "initialize":
		if person {
			m.bridgeSeen(r, method)
		}
		pv := mcpProtocolVersion
		if s, ok := jsonobj.String(param("protocolVersion")); ok {
			pv = s
		}
		return reqbody.OK(w, rpcResult(id, reqbody.O("protocolVersion", pv, "capabilities", reqbody.O("tools", jsonobj.New()),
			"serverInfo", reqbody.O("name", "activate-studio", "version", "0.1.0"))))
	case "ping":
		return reqbody.OK(w, rpcResult(id, jsonobj.New()))
	case "tools/list":
		if person {
			m.bridgeSeen(r, method)
			tools := []json.RawMessage{}
			for _, s := range m.personTools(r, m.personCtx(r)) {
				tools = append(tools, chatToolJSON(s))
			}
			return reqbody.OK(w, rpcResult(id, reqbody.O("tools", tools)))
		}
		if chatGrant {
			tools := []json.RawMessage{}
			for _, s := range m.chatTools(r, grantDelegates(chat.Conv)) {
				tools = append(tools, chatToolJSON(s))
			}
			return reqbody.OK(w, rpcResult(id, reqbody.O("tools", tools)))
		}
		return reqbody.OK(w, rpcResult(id, reqbody.O("tools", m.exposedTools())))
	case "tools/call":
		name := ""
		if raw := param("name"); !jsonobj.IsNullish(raw) {
			name = reqbody.JSString(decode(raw))
		}
		args := param("arguments")
		if jsonobj.IsNullish(args) {
			args = json.RawMessage("{}")
		}
		tc := ToolCtx{}
		if person {
			// A person's own pw code session on another machine, through
			// the session proxy: the tools a pw code chat of theirs gets,
			// as them, on their own credential.
			tc = m.personCtx(r)
			if !slices.Contains(specNames(m.personTools(r, tc)), name) {
				return reqbody.OK(w, rpcError(id, -32602, "unknown tool: "+name))
			}
			m.bridgeSeen(r, method)
		} else if chatGrant {
			// A pw code chat's call runs as the Studio assistant's would: the
			// conversation's scope, the person's key, the tools they left on.
			if !slices.Contains(specNames(m.chatTools(r, grantDelegates(chat.Conv))), name) {
				return reqbody.OK(w, rpcError(id, -32602, "unknown tool: "+name))
			}
			var link *conversations.PWCodeLink
			tc, link = m.chatToolCtx(r, chat.Conv)
			if why := m.chatToolOff(name, link); why != "" {
				return reqbody.OK(w, rpcResult(id, reqbody.O("content", []any{reqbody.O("type", "text", "text", why)}, "isError", true)))
			}
		} else if !contains(mcpExposed, name) {
			return reqbody.OK(w, rpcError(id, -32602, "unknown tool: "+name))
		}
		out, err := m.Tools.Execute(r, name, string(jsRestringify(args)), tc)
		if err != nil {
			// A tool's failure is a result for the calling model to read.
			return reqbody.OK(w, rpcResult(id, reqbody.O("content", []any{reqbody.O("type", "text", "text", err.Error())}, "isError", true)))
		}
		return reqbody.OK(w, rpcResult(id, reqbody.O("content", []any{reqbody.O("type", "text", "text", out.Result)}, "isError", false)))
	}
	shown := "(none)"
	if !jsonobj.IsNullish(rawMethod) {
		shown = method
	}
	return reqbody.OK(w, rpcError(id, -32601, "method not supported: "+shown))
}
