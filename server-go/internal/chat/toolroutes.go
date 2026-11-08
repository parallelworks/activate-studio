package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

/* ---- routes ---- */

// toolsRoute is GET /api/chat/tools: the Settings tool catalog.
func (m *Module) toolsRoute(w http.ResponseWriter, r *http.Request) error {
	eff := m.eff()
	disabled := func(n string) bool { return slices.Contains(eff.DisabledTools, n) }
	tools := []*jsonobj.Object{}
	for _, t := range builtinSpecs {
		o := reqbody.O("name", t.Name, "description", t.Description, "builtin", true, "enabled", !disabled(t.Name), "parameters", t.Parameters,
			"implementation", "built-in (server/src/chat/tools.ts)")
		if c, ok := toolCalls[t.Name]; ok {
			o.SetValue("calls", c)
		}
		if asksFirst(t.Name) {
			o.SetValue("asksFirst", true)
			o.SetValue("instead", insteadOf(t.Name))
		}
		tools = append(tools, o)
	}
	for _, t := range customToolSpecs(eff, m.Ext) {
		o := reqbody.O("name", t.Name, "description", t.Description, "builtin", false, "enabled", !disabled(t.Name), "parameters", t.Parameters)
		if c, ok := commandFor(eff, m.Ext, t.Name); ok {
			o.SetValue("command", c)
		}
		tools = append(tools, o)
	}
	if s := skillToolSpec(m.Ext); s != nil {
		tools = append(tools, reqbody.O("name", "use_skill", "description", s.Description, "builtin", true, "enabled", !disabled("use_skill"), "parameters", s.Parameters, "calls", toolCalls["use_skill"]))
	}
	return reqbody.OK(w, reqbody.O("tools", tools))
}

// toolOptions is GET /api/chat/tool-options: the composer's Tools menu.
func (m *Module) toolOptions(w http.ResponseWriter, r *http.Request) error {
	if !m.known() {
		m.toNode(w, r, "the viewer is Node's")
		return nil
	}
	options := m.readerToolOptions()
	var own *string
	if c := m.cred(r); c != nil && c.BaseURL == nil {
		k := c.Key
		own = &k
	}
	key := own
	if !m.A.Cfg.AuthEnabled() && (key == nil || *key == "") {
		if k := m.GatewayKey(); k != "" {
			key = &k
		} else {
			key = nil
		}
	}
	ok := m.changesAvailable(m.viewerOf(r), key, nil, nil).OK
	platform := m.A.Providers.PlatformAvailable(r, userID(r))
	out := []*jsonobj.Object{}
	for _, o := range options {
		id, _ := jsonobj.String(o.Get("id"))
		if id == "changes" && !ok {
			continue
		}
		if !platform && slices.Contains(PlatformGroups, id) {
			continue
		}
		out = append(out, o)
	}
	return reqbody.OK(w, reqbody.O("tools", out))
}

// mcpServers is GET /api/settings/mcp-servers: each attached server's
// connection state; ?cached=1 contacts none.
func (m *Module) mcpServers(w http.ResponseWriter, r *http.Request) error {
	if r.URL.Query().Get("cached") == "1" {
		return reqbody.OK(w, reqbody.O("servers", m.mcp.CachedStatus()))
	}
	return reqbody.OK(w, reqbody.O("servers", m.mcp.Status(r.Context())))
}

/* ---- the tools that run in Go ---- */

const toolOutputCap = 24_000

// goExecutors are the tools ported to Go, by name.
var goExecutors = map[string]func(m *Module, r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error){
	"use_skill":   (*Module).toolUseSkill,
	"studio_docs": (*Module).toolStudioDocs,
}

// studioDocs are the documents studio_docs reads, by the name the model passes.
var studioDocs = map[string]string{
	"help": "HELP.md", "reference": "REFERENCE.md", "deploy": "DEPLOY.md", "architecture": "ARCHITECTURE.md", "customization": "CUSTOMIZATION.md", "libraries": "LIBRARIES.md",
	"workflows": "WORKFLOWS.md", "agents": "AGENTS.md", "multi-user": "MULTI-USER.md", "macos": "MACOS.md", "updates": "UPDATES.md",
}

// toolError is a failure the tool reports as its result.
type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

// executeInGo runs a tool ported to Go; false when the tool is Node's.
func (m *Module) executeInGo(r *http.Request, name, argsJSON string, ctx ToolCtx) (*Outcome, bool) {
	fn, builtin := goExecutors[name]
	remote := !builtin && !isBuiltin(name) && m.mcp.IsRemoteTool(name)
	var custom *customTool
	if !builtin && !remote && !isBuiltin(name) {
		for _, t := range allCustomTools(m.eff(), m.Ext) {
			if t.Name == name {
				t := t
				custom = &t
				break
			}
		}
	}
	if !builtin && !remote && custom == nil {
		return nil, false
	}
	args := map[string]any{}
	if argsJSON != "" {
		var v any
		if json.Unmarshal([]byte(argsJSON), &v) == nil {
			if o, ok := v.(map[string]any); ok {
				args = o
			}
		}
	}
	var out *Outcome
	var err error
	switch {
	case builtin:
		out, err = fn(m, r, args, ctx)
	case remote:
		var text string
		text, err = m.mcp.Call(r.Context(), name, rawArgs(argsJSON))
		if err == nil {
			out = &Outcome{Result: capOutput(text), Summary: name}
		}
	default:
		out, err = m.runCustomTool(r.Context(), *custom, args)
	}
	if errors.Is(err, errNodeTool) {
		return nil, false
	}
	if err != nil {
		return &Outcome{Result: "Tool error: " + err.Error(), Summary: "error"}, true
	}
	return out, true
}

// rawArgs is the arguments as callRemoteTool gets them: the parsed value,
// or {} for text that is not JSON.
func rawArgs(s string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

// capOutput is (out.trim() || '(no output)').slice(0, TOOL_OUTPUT_CAP).
func capOutput(s string) string {
	t := jsTrim(s)
	if t == "" {
		t = "(no output)"
	}
	return jsval.Slice(t, toolOutputCap)
}

func argString(args map[string]any, k string) string {
	v, ok := args[k]
	if !ok || v == nil {
		return ""
	}
	return reqbody.JSString(v)
}

func (m *Module) toolUseSkill(r *http.Request, args map[string]any, _ ToolCtx) (*Outcome, error) {
	name := argString(args, "name")
	body, ok := m.Ext.SkillBody(name)
	if !ok {
		return &Outcome{Result: "No such skill: " + name, Summary: "error"}, nil
	}
	return &Outcome{Result: "Skill " + name + " loaded. Follow these instructions for the current request:\n\n" + jsval.Slice(body, toolOutputCap), Summary: "skill " + name}, nil
}

func (m *Module) toolStudioDocs(r *http.Request, args map[string]any, _ ToolCtx) (*Outcome, error) {
	which := "help"
	if v, ok := args["doc"]; ok && v != nil {
		which = strings.ToLower(reqbody.JSString(v))
	}
	file, ok := studioDocs[which]
	if !ok {
		file = "HELP.md"
	}
	b, err := os.ReadFile(filepath.Join(m.A.Cfg.ProjectRoot, "docs", file))
	if err != nil {
		return nil, nodeFSError(err, "open", filepath.Join(m.A.Cfg.ProjectRoot, "docs", file))
	}
	eff := m.eff()
	md := strings.ReplaceAll(strings.ReplaceAll(string(b), "{appName}", eff.AppName), "{kbLabel}", eff.KbLabel)
	return &Outcome{Result: jsval.Slice(md, toolOutputCap), Summary: file + " (" + itoaInt(jsval.Len(md)) + " chars)"}, nil
}

// nodeFSError is the message a failed fs call has in Node.
func nodeFSError(err error, syscall, path string) error {
	if errors.Is(err, os.ErrNotExist) {
		return &toolError{"ENOENT: no such file or directory, " + syscall + " '" + path + "'"}
	}
	if errors.Is(err, os.ErrPermission) {
		return &toolError{"EACCES: permission denied, " + syscall + " '" + path + "'"}
	}
	return err
}

/* ---- custom command tools ---- */

// splitToolArgs is tools.ts's: quote-aware argv words, nothing more.
func splitToolArgs(input string) []string {
	out := []string{}
	var cur strings.Builder
	started := false
	var quote rune
	for _, ch := range input {
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				cur.WriteRune(ch)
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			started = true
			continue
		}
		if isJSSpace(ch) {
			if started || cur.Len() > 0 {
				out = append(out, cur.String())
			}
			cur.Reset()
			started = false
			continue
		}
		cur.WriteRune(ch)
		started = true
	}
	if started || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

const customTimeout = 90 * time.Second
const customMaxBuffer = 8 * 1024 * 1024

// runCustomTool runs a configured command with the model's words as its
// positional parameters, as execFile('bash', ['-lc', ...]) does.
func (m *Module) runCustomTool(ctx context.Context, t customTool, args map[string]any) (*Outcome, error) {
	extra := jsval.Slice(argString(args, "args"), 400)
	argv := append([]string{"-lc", t.Command + ` "$@"`, t.Name}, splitToolArgs(extra)...)
	ctx, cancel := context.WithTimeout(ctx, customTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitBuf{&stdout, customMaxBuffer}, &limitBuf{&stderr, customMaxBuffer}
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, &toolError{stderr.String()}
		}
		return nil, &toolError{"Command failed: bash " + strings.Join(argv, " ") + "\n"}
	}
	return &Outcome{Result: capOutput(stdout.String()), Summary: t.Name}, nil
}

type limitBuf struct {
	b *bytes.Buffer
	n int
}

func (l *limitBuf) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func itoaInt(n int) string { return jsval.NumberString(float64(n)) }
