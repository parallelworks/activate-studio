package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
	"github.com/parallelworks/activate-studio/server-go/internal/mcpclient"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// goTools is the tool layer in Go: the catalog, the system prompt, the
// held calls and every tool.
type goTools struct {
	m *Module
}

const mcpSep = mcpclient.Sep

// remoteTools is the MCP client as the catalog reads it.
type remoteTools struct{ c *mcpclient.Client }

func (t remoteTools) ToolSpecs() []ToolSpec {
	var out []ToolSpec
	for _, s := range t.c.ToolSpecs(context.Background()) {
		out = append(out, ToolSpec{Name: s.Name, Description: s.Description, Parameters: s.Parameters})
	}
	return out
}

func (t remoteTools) IsRemoteTool(name string) bool { return t.c.IsRemoteTool(name) }

/* ---- the platform values ---- */

// Platform is the part of internal/platform's *Platform the chat reads, by
// the names it has there; m.Plat is the shared one (platform.Of), and
// tests put a stand-in in its place.
type Platform interface {
	GatewayBase() string
	Origin() string
	PlatformGateway() bool
	CredentialContextName() string
	CLI() string
}

// pwCLI runs the pw CLI and returns its standard output.
func (m *Module) pwCLI(ctx context.Context, args []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, m.Plat.CLI(), args...).Output()
	return string(out), err
}

/* ---- Tools ---- */

func remarshal(in any, out any) {
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, out)
}

// readerSet is the reader's switches, nil when none were sent.
func readerSet(raw json.RawMessage) map[string]bool {
	var list []any
	if json.Unmarshal(raw, &list) != nil || list == nil {
		return nil
	}
	set := map[string]bool{}
	for _, x := range list {
		set[reqbody.JSString(x)] = true
	}
	return set
}

func readerFrom(tools []string, has bool) map[string]bool {
	if !has {
		return nil
	}
	set := map[string]bool{}
	for _, t := range tools {
		set[t] = true
	}
	return set
}

func (t goTools) Begin(r *http.Request, in map[string]any) (*Begin, error) {
	m := t.m
	var b struct {
		Used           []json.RawMessage `json:"used"`
		LastUserIdx    *float64          `json:"lastUserIdx"`
		ChangeKey      *string           `json:"changeKey"`
		Owner          *string           `json:"owner"`
		Tools          json.RawMessage   `json:"tools"`
		RecentUserText string            `json:"recentUserText"`
		UsedTools      []string          `json:"usedTools"`
		Agent          any               `json:"agent"`
	}
	remarshal(in, &b)
	last := -1
	if b.LastUserIdx != nil {
		last = int(*b.LastUserIdx)
	}
	out := &Begin{History: []llm.Message{}}
	for i, raw := range b.Used {
		var msg llm.Message
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		var extra struct {
			AttachmentIDs []string `json:"attachment_ids"`
		}
		_ = json.Unmarshal(raw, &extra)
		if msg.Role == "system" || isEarlierToolTraffic(msg) {
			continue
		}
		content := msg.Text()
		if i == last && strings.HasPrefix(jsTrim(content), "/") {
			text, listing := m.expandSlashCommand(content)
			if listing != nil {
				out.Listing = listing
			} else if text != "" {
				content = text
			}
		}
		if len(extra.AttachmentIDs) > 0 {
			if ctx := m.attachmentContext(extra.AttachmentIDs); ctx != "" {
				if content != "" {
					content = content + "\n\n" + ctx
				} else {
					content = ctx
				}
			}
		}
		c := content
		out.History = append(out.History, llm.Message{Role: msg.Role, Content: &c})
	}
	viewer := m.viewerOf(r)
	var username *string
	if u := user(r); u != nil && u.Username != "" {
		username = &u.Username
	}
	out.Viewer = viewer
	out.Changes = m.changesAvailable(viewer, b.ChangeKey, b.Owner, username)
	out.Platform = m.A.Providers.PlatformAvailable(r, userID(r))
	reader := readerSet(b.Tools)
	groups := initialToolGroups(b.RecentUserText, b.UsedTools)
	for g := range groups {
		if reader != nil && !reader[g] {
			delete(groups, g)
		}
	}
	if !out.Platform {
		for _, g := range PlatformGroups {
			delete(groups, g)
		}
	}
	out.Groups = groupOrderList(groups)
	agent := ""
	if b.Agent != nil {
		agent = reqbody.JSString(b.Agent)
	}
	if p := m.Ext.AgentPrompt(agent); p != "" {
		out.Persona = &p
	}
	out.System = m.systemPrompt(r.Context(), groups["workflows"] || groups["agents"], out.Platform)
	out.Meta = m.toolMeta()
	return out, nil
}

// toolMeta is what the loop knows about tools by name.
func (m *Module) toolMeta() Meta {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, s := range activeToolSpecs(m.eff(), m.Ext) {
		add(s.Name)
	}
	meta := Meta{MoreTools: MoreTools, PlatformGroups: PlatformGroups, Groups: map[string][]string{}, GroupOf: map[string]string{}, Labels: map[string]string{}, AsksFirst: []string{}}
	for _, g := range toolGroups {
		meta.GroupOrder = append(meta.GroupOrder, g.ID)
		meta.Groups[g.ID] = g.Names
		for _, n := range g.Names {
			add(n)
		}
	}
	for _, n := range names {
		if g := toolGroupOf(n); g != "" {
			meta.GroupOf[n] = g
		}
		if asksFirst(n) {
			meta.AsksFirst = append(meta.AsksFirst, n)
		}
		meta.Labels[n] = m.toolOptionLabel(n)
	}
	return meta
}

func (t goTools) System(r *http.Request, platform, connected bool) (string, error) {
	return t.m.systemPrompt(r.Context(), platform, connected), nil
}

// offered is whether a tool goes to a reply with this platform and these
// changes.
func offered(n string, platform, changesOK bool) bool {
	if !platform && needsPlatform(n) {
		return false
	}
	if !isPlatformTool(n) {
		return true
	}
	if asksFirst(n) {
		return changesOK
	}
	return platform
}

func (t goTools) Specs(r *http.Request, tools []string, has bool, platform, changesOK bool, groups []string) (*Specs, error) {
	reader := readerFrom(tools, has)
	var available []ToolSpec
	for _, s := range t.m.activeToolSpecsWithRemote() {
		if t.m.readerAllows(s.Name, reader) && offered(s.Name, platform, changesOK) {
			available = append(available, s)
		}
	}
	active := map[string]bool{}
	for _, g := range groups {
		active[g] = true
	}
	out := &Specs{Available: []Spec{}, Selected: []Spec{}}
	for _, s := range available {
		out.Available = append(out.Available, s.JSON())
	}
	for _, s := range selectToolSpecs(available, active) {
		out.Selected = append(out.Selected, s.JSON())
	}
	return out, nil
}

func (t goTools) Names(r *http.Request, tools []string, has bool, names []string) (*Names, error) {
	reader := readerFrom(tools, has)
	out := &Names{Allowed: map[string]bool{}, Labels: map[string]string{}, GroupOf: map[string]*string{}, AsksFirst: map[string]bool{}}
	for _, n := range names {
		out.Allowed[n] = t.m.readerAllows(n, reader)
		out.Labels[n] = t.m.toolOptionLabel(n)
		if g := toolGroupOf(n); g != "" {
			out.GroupOf[n] = &g
		} else {
			out.GroupOf[n] = nil
		}
		out.AsksFirst[n] = asksFirst(n)
	}
	return out, nil
}

func (t goTools) Execute(r *http.Request, name, args string, ctx ToolCtx) (*Outcome, error) {
	if o, ok := t.m.executeInGo(r, name, args, ctx); ok {
		return o, nil
	}
	return &Outcome{Result: "The tool " + name + " does not exist in this environment; it may be a backend built-in that is unavailable here. Use only the tools declared for this conversation, or answer in plain text.",
		Summary: "no such tool " + name}, nil
}

func (t goTools) Prepare(r *http.Request, name, args, changeKey string) (*Prepared, error) {
	m := t.m
	pt := platformByName[name]
	needsAccount := pt != nil && len(pt.Defaults) > 0
	var account *Account
	if needsAccount {
		account = m.viewerAccount(r.Context(), changeKey)
	}
	username := ""
	if account != nil {
		username = account.Username
	}
	obj, canonical, problem := prepareArgs(name, args, username)
	if problem != "" {
		if needsAccount && account == nil {
			return &Prepared{Problem: fmt.Sprintf("Your platform account could not be read with your key, so %s could not be prepared. Nothing ran.", name)}, nil
		}
		return &Prepared{Problem: problem + " Nothing ran."}, nil
	}
	b, _ := jsonobj.Marshal(obj)
	return &Prepared{Args: b, Canonical: canonical}, nil
}

func (t goTools) Hold(r *http.Request, in map[string]any) (json.RawMessage, error) {
	viewer := t.m.viewerOf(r)
	if viewer == nil {
		return nil, fmt.Errorf("no viewer")
	}
	var b struct {
		ConversationID string            `json:"conversationId"`
		MessageID      string            `json:"messageId"`
		ToolCallID     string            `json:"toolCallId"`
		Tool           string            `json:"tool"`
		Canonical      string            `json:"canonical"`
		Args           json.RawMessage   `json:"args"`
		Turn           []json.RawMessage `json:"turn"`
		Groups         []string          `json:"groups"`
	}
	remarshal(in, &b)
	rec := newHold(b.ConversationID, b.MessageID, b.ToolCallID, b.Tool, b.Canonical, viewer, b.Turn, b.Groups, t.m.held.now())
	args, err := jsonobj.Parse(b.Args)
	if err != nil || args == nil {
		args = jsonobj.New()
	}
	approval := approvalPart(b.Tool, args, b.ToolCallID, rec.ExpiresAt)
	t.m.held.hold(rec)
	return jsonobj.Marshal(approval)
}

func (t goTools) Answer(r *http.Request, ans map[string]any, changeKey *string) (*Answered, error) {
	var a answer
	remarshal(ans, &a)
	m := t.m
	enabled := func(tool string) bool {
		return slices.ContainsFunc(activeToolSpecs(m.eff(), m.Ext), func(s ToolSpec) bool { return s.Name == tool })
	}
	return m.answerHeld(r.Context(), a, m.viewerOf(r), changeKey, enabled), nil
}

func (t goTools) Held(r *http.Request, conversationID string) ([]conversations.Holder, error) {
	return holders(t.m.held.in(conversationID)), nil
}

// Access is the platform values the chat reads, from the shared platform:
// ensureFreshAccess(freshMS), GATEWAY_BASE, the deployment credential, the
// credential's context, platformAvailable for the viewer and the pw CLI.
func (t goTools) Access(r *http.Request, freshMS int) (*Access, error) {
	p := t.m.pf
	if freshMS > 0 {
		p.EnsureFreshAccess(time.Duration(freshMS) * time.Millisecond)
	}
	key := p.APIKeyEnv()
	if key == "" {
		key = p.CLICredential()
	}
	var ctxName *string
	if n := p.CredentialContextName(); n != "" {
		ctxName = &n
	}
	avail := false
	if r != nil && t.m.A.Providers.PlatformAvailable != nil {
		avail = t.m.A.Providers.PlatformAvailable(r, userID(r))
	}
	return &Access{GatewayBase: p.GatewayBase(), DeploymentKey: key, ContextName: ctxName, Platform: avail, CLI: p.CLI()}, nil
}
