package chat

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/extensions"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// The built-in tool catalog of tools.ts and platformTools.ts: the tools'
// names, descriptions and parameters, what each one calls, the groups, and
// the platform operations. toolcatalog.json is the catalog as Node
// declares it (node scripts/golden/chat.mjs <repo> catalog); the golden
// test checks it, and everything built from it, against Node.
//
//go:embed toolcatalog.json
var catalogJSON []byte

// ToolSpec is a tool definition as the model is sent it.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// JSON is the spec in the gateway's shape, { type: 'function', function }.
func (t ToolSpec) JSON() json.RawMessage {
	b, _ := jsonobj.Marshal(reqbody.O("type", "function", "function", reqbody.O("name", t.Name, "description", t.Description, "parameters", t.Parameters)))
	return b
}

type toolGroup struct {
	ID      string
	Summary string
	Names   []string
	Cue     *regexp.Regexp
}

type catalogFile struct {
	Specs  []ToolSpec        `json:"specs"`
	Calls  map[string]string `json:"calls"`
	Groups json.RawMessage   `json:"groups"`
	// Platform is PLATFORM_TOOLS.
	Platform []PlatformTool    `json:"platform"`
	Hashes   map[string]string `json:"hashes"`
}

var (
	builtinSpecs []ToolSpec
	toolCalls    map[string]string
	toolGroups   []toolGroup
	nodeHashes   map[string]string
)

// MoreTools is the tool that adds a group.
const MoreTools = "more_tools"

// PlatformGroups are the groups that work only with a platform.
var PlatformGroups = []string{"workflows", "changes", "agents"}

func init() {
	var c catalogFile
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("chat: toolcatalog.json: " + err.Error())
	}
	for i := range c.Specs {
		c.Specs[i].Parameters = compact(c.Specs[i].Parameters)
	}
	builtinSpecs, toolCalls, nodeHashes = c.Specs, c.Calls, c.Hashes
	groups, err := jsonobj.Parse(c.Groups)
	if err != nil {
		panic("chat: toolcatalog.json groups: " + err.Error())
	}
	for _, g := range groups.Keys() {
		var d struct {
			Summary string   `json:"summary"`
			Names   []string `json:"names"`
			Cue     string   `json:"cue"`
		}
		_ = json.Unmarshal(groups.Get(g), &d)
		toolGroups = append(toolGroups, toolGroup{ID: g, Summary: d.Summary, Names: d.Names, Cue: regexp.MustCompile("(?i)" + d.Cue)})
	}
	setPlatformTools(c.Platform)
}

func compact(b json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return b
	}
	return buf.Bytes()
}

// BuiltinNames are TOOL_SPECS's names, in order.
func BuiltinNames() []string {
	out := make([]string, len(builtinSpecs))
	for i, t := range builtinSpecs {
		out[i] = t.Name
	}
	return out
}

func isBuiltin(name string) bool {
	return slices.ContainsFunc(builtinSpecs, func(t ToolSpec) bool { return t.Name == name })
}

/* ---- the deployment's own tools and skills ---- */

type customTool struct{ Name, Description, Command string }

// allCustomTools is tools.ts's: the extension tools, then the Settings
// ones, a Settings tool taking an extension tool's place by name.
func allCustomTools(eff settings.Effective, ext *extensions.Set) []customTool {
	var order []string
	byName := map[string]customTool{}
	set := func(t customTool) {
		if _, ok := byName[t.Name]; !ok {
			order = append(order, t.Name)
		}
		byName[t.Name] = t
	}
	for _, t := range ext.Tools() {
		set(customTool{t.Name, t.Description, t.Command})
	}
	for _, raw := range eff.CustomTools {
		var o map[string]json.RawMessage
		if json.Unmarshal(raw, &o) != nil {
			continue
		}
		t := customTool{optString(o["name"]), optString(o["description"]), optString(o["command"])}
		if jsval.Truthy(o["name"]) && jsval.Truthy(o["command"]) {
			set(t)
		}
	}
	out := make([]customTool, len(order))
	for i, n := range order {
		out[i] = byName[n]
	}
	return out
}

// commandFor is the command line behind a custom tool.
func commandFor(eff settings.Effective, ext *extensions.Set, name string) (string, bool) {
	for _, t := range allCustomTools(eff, ext) {
		if t.Name == name {
			return t.Command, true
		}
	}
	return "", false
}

const customArgs = `{"type":"object","properties":{"args":{"type":"string","description":"Extra command-line arguments for the configured command; omit for none. Passed as positional parameters, so shell syntax has no effect and quotes group words."}}}`

func customToolSpecs(eff settings.Effective, ext *extensions.Set) []ToolSpec {
	var out []ToolSpec
	for _, t := range allCustomTools(eff, ext) {
		if t.Name == "" || t.Command == "" || isBuiltin(t.Name) {
			continue
		}
		d := t.Description
		if d == "" {
			d = "Custom deployment tool."
		}
		out = append(out, ToolSpec{Name: t.Name, Description: d + " (Runs a configured command on the Studio server; output is returned here.)", Parameters: json.RawMessage(customArgs)})
	}
	return out
}

// skillToolSpec is use_skill, present when skill files are.
func skillToolSpec(ext *extensions.Set) *ToolSpec {
	skills := ext.Skills()
	if len(skills) == 0 {
		return nil
	}
	const fullDescLimit = 25
	clip := len(skills) > fullDescLimit
	listed := make([]string, len(skills))
	names := make([]string, len(skills))
	for i, s := range skills {
		d := s.Description
		if clip {
			d = jsTrim(jsval.Slice(s.Description, 60))
		}
		item := s.Name
		if d != "" {
			more := ""
			if clip && jsval.Len(s.Description) > 60 {
				more = "…"
			}
			item += " (" + d + more + ")"
		}
		listed[i], names[i] = item, s.Name
	}
	note := ""
	if clip {
		note = " Descriptions here are shortened; loading a skill returns its full instructions."
	}
	params, _ := jsonobj.Marshal(reqbody.O("type", "object", "properties", reqbody.O("name", reqbody.O("type", "string", "enum", names, "description", "Skill to load")), "required", []string{"name"}))
	return &ToolSpec{
		Name:        "use_skill",
		Description: "Load a skill: task-specific instructions to follow for the current request. Available skills: " + strings.Join(listed, "; ") + ". Load one whenever the user names it or the task clearly matches its description, then follow the returned instructions." + note,
		Parameters:  params,
	}
}

// activeToolSpecs is the tools a chat request gets: built-ins, custom
// tools and use_skill, less those turned off.
func activeToolSpecs(eff settings.Effective, ext *extensions.Set) []ToolSpec {
	disabled := map[string]bool{}
	for _, n := range eff.DisabledTools {
		disabled[n] = true
	}
	if !eff.DelegationEnabled {
		disabled["delegate"], disabled["task_status"] = true, true
	}
	all := append(append([]ToolSpec{}, builtinSpecs...), customToolSpecs(eff, ext)...)
	if s := skillToolSpec(ext); s != nil {
		all = append(all, *s)
	}
	out := []ToolSpec{}
	for _, t := range all {
		if !disabled[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

/* ---- groups ---- */

func toolGroupOf(name string) string {
	for _, g := range toolGroups {
		if slices.Contains(g.Names, name) {
			return g.ID
		}
	}
	return ""
}

func needsPlatform(name string) bool {
	g := toolGroupOf(name)
	return g != "" && slices.Contains(PlatformGroups, g)
}

// initialToolGroups is the groups a reply starts with.
func initialToolGroups(userText string, used []string) map[string]bool {
	groups := map[string]bool{}
	for _, g := range toolGroups {
		if g.Cue.MatchString(userText) {
			groups[g.ID] = true
		}
	}
	for _, n := range used {
		if g := toolGroupOf(n); g != "" {
			groups[g] = true
		}
	}
	return groups
}

// groupOrderList is a set of groups in TOOL_GROUPS order.
func groupOrderList(set map[string]bool) []string {
	out := []string{}
	for _, g := range toolGroups {
		if set[g.ID] {
			out = append(out, g.ID)
		}
	}
	return out
}

// selectToolSpecs is the tools sent this round.
func selectToolSpecs(all []ToolSpec, active map[string]bool) []ToolSpec {
	out := []ToolSpec{}
	for _, t := range all {
		if g := toolGroupOf(t.Name); g == "" || active[g] {
			out = append(out, t)
		}
	}
	var offered []toolGroup
	for _, g := range toolGroups {
		if active[g.ID] {
			continue
		}
		if slices.ContainsFunc(all, func(t ToolSpec) bool { return slices.Contains(g.Names, t.Name) }) {
			offered = append(offered, g)
		}
	}
	if len(offered) == 0 {
		return out
	}
	parts := make([]string, len(offered))
	ids := make([]string, len(offered))
	for i, g := range offered {
		parts[i], ids[i] = g.ID+" ("+g.Summary+")", g.ID
	}
	params, _ := jsonobj.Marshal(reqbody.O("type", "object", "properties", reqbody.O("group", reqbody.O("type", "string", "enum", ids)), "required", []string{"group"}))
	return append(out, ToolSpec{Name: MoreTools, Description: "More tools exist than are listed. Call this to add a group when the request needs it: " + strings.Join(parts, "; ") + ".", Parameters: params})
}

/* ---- the reader's switches ---- */

type optionText struct{ ID, Label, Description string }

var toolOptionText = []optionText{
	{"kb", "Knowledge base", "Search, read and query the library"},
	{"workflows", "Platform workflows", "Run and follow workflows and sessions"},
	{"changes", "Platform changes", "Start, stop, and destroy clusters and cancel runs"},
	{"agents", "Agents", "Start pw code agents and subtasks"},
	{"editing", "Editing the library", "Write files and apply labels"},
	{"extensions", "Extensions", "This deployment's own tools and skills"},
}

// toolOptionOf is which of the reader's switches a tool belongs to.
func (m *Module) toolOptionOf(name string) string {
	if g := toolGroupOf(name); g != "" {
		return g
	}
	if name == "use_skill" || slices.ContainsFunc(customToolSpecs(m.eff(), m.Ext), func(t ToolSpec) bool { return t.Name == name }) {
		return "extensions"
	}
	if m.MCP.IsRemoteTool(name) {
		return "mcp:" + name[:strings.Index(name, mcpSep)]
	}
	return "kb"
}

// readerToolOptions is the switches for what the deployment offers now.
func (m *Module) readerToolOptions() []*jsonobj.Object {
	ids := map[string]bool{}
	for _, t := range m.activeToolSpecsWithRemote() {
		ids[m.toolOptionOf(t.Name)] = true
	}
	out := []*jsonobj.Object{}
	for _, o := range toolOptionText {
		if !ids[o.ID] {
			continue
		}
		e := reqbody.O("id", o.ID, "label", o.Label, "description", o.Description)
		if o.ID == "changes" {
			e.SetValue("asksFirst", true)
		}
		out = append(out, e)
	}
	var servers []string
	for id := range ids {
		if strings.HasPrefix(id, "mcp:") {
			servers = append(servers, id)
		}
	}
	sort.Strings(servers)
	for _, id := range servers {
		out = append(out, reqbody.O("id", id, "label", id[4:], "description", "Tools from an attached MCP server"))
	}
	return out
}

// readerAllows is whether the reader left a tool's switch on; nil means no
// choice was sent.
func (m *Module) readerAllows(name string, allowed map[string]bool) bool {
	return allowed == nil || name == MoreTools || allowed[m.toolOptionOf(name)]
}

// toolOptionLabel is the switch's label.
func (m *Module) toolOptionLabel(name string) string {
	id := m.toolOptionOf(name)
	for _, o := range toolOptionText {
		if o.ID == id {
			return o.Label
		}
	}
	return strings.Replace(id, "mcp:", "MCP server ", 1)
}

// activeToolSpecsWithRemote is the tool list with the attached MCP servers'.
func (m *Module) activeToolSpecsWithRemote() []ToolSpec {
	eff := m.eff()
	out := activeToolSpecs(eff, m.Ext)
	for _, t := range m.MCP.ToolSpecs() {
		if !slices.Contains(eff.DisabledTools, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

/* ---- slash commands ---- */

func (m *Module) agentBody(name string) (string, bool) {
	want := extensions.NormalizeName(name)
	for _, a := range m.Ext.Agents() {
		if a.Name == want {
			b, err := os.ReadFile(filepath.Join(m.Ext.Ext, "agents", a.File))
			return string(b), err == nil
		}
	}
	return "", false
}

// slashListing is the /help listing.
func (m *Module) slashListing() string {
	lines := []string{"Slash commands resolve before the model sees the message.", ""}
	if skills := m.Ext.Skills(); len(skills) > 0 {
		lines = append(lines, "Skills (instructions applied to your request):")
		for _, s := range skills {
			lines = append(lines, "- /"+s.Name+describe(s.Description))
		}
		lines = append(lines, "")
	}
	if agents := m.Ext.Agents(); len(agents) > 0 {
		lines = append(lines, "Agents (standing instructions adopted for one message):")
		for _, a := range agents {
			lines = append(lines, "- /"+a.Name+describe(a.Description))
		}
		lines = append(lines, "")
	}
	if customs := allCustomTools(m.eff(), m.Ext); len(customs) > 0 {
		lines = append(lines, "Command tools (run directly; add arguments after the name):")
		for _, t := range customs {
			lines = append(lines, "- /"+t.Name+describe(t.Description))
		}
		lines = append(lines, "")
	}
	lines = append(lines, "Built-in tools can also be invoked by name, e.g. /search_kb my topic.", "Everything here also works in plain language; slash form just makes it explicit.")
	return strings.Join(lines, "\n")
}

func describe(d string) string {
	if d == "" {
		return ""
	}
	return " : " + d
}

var slashRe = regexp.MustCompile(`^/([A-Za-z0-9_-]+)((?s:.*))$`)

// expandSlashCommand resolves a leading /command: the replacement text,
// or a listing answered without a model call, or neither.
func (m *Module) expandSlashCommand(content string) (text string, listing *string) {
	mm := slashRe.FindStringSubmatch(jsTrim(content))
	if mm == nil {
		return "", nil
	}
	name := strings.ReplaceAll(strings.ToLower(mm[1]), "-", "_")
	rest := jsTrim(mm[2])
	switch name {
	case "help", "commands", "skills", "tools", "agents", "personas", "list":
		l := m.slashListing()
		return "", &l
	}
	if skill, ok := m.Ext.SkillBody(name); ok {
		if rest == "" {
			rest = "Apply the instructions to the current conversation context."
		}
		return "Follow these instructions for this request.\n\n" + skill + "\n\nRequest: " + rest, nil
	}
	if agent, ok := m.agentBody(name); ok {
		if rest == "" {
			rest = "Proceed."
		}
		return "Adopt these standing instructions for this request.\n\n" + agent + "\n\nRequest: " + rest, nil
	}
	if isBuiltin(name) || slices.ContainsFunc(customToolSpecs(m.eff(), m.Ext), func(t ToolSpec) bool { return t.Name == name }) {
		with := ""
		if rest != "" {
			with = " with arguments derived from: " + rest
		}
		return fmt.Sprintf("Call the %s tool now%s, then answer from its output.", name, with), nil
	}
	if rest == "" {
		l := "There is no /" + mm[1] + " command.\n\n" + m.slashListing()
		return "", &l
	}
	return "", nil
}

// optString is a saved field as text, "" when absent or null.
func optString(v json.RawMessage) string {
	if len(v) == 0 || jsonobj.IsNullish(v) {
		return ""
	}
	return jsval.String(v)
}
