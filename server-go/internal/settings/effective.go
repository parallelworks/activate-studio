package settings

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
)

// UpdatePolicy is settings.ts's UpdatePolicy.
type UpdatePolicy struct {
	Check   bool   `json:"check"`
	Install string `json:"install"`
	Channel string `json:"channel"`
}

// MCPServer is one saved MCP server.
type MCPServer struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Header string `json:"header,omitempty"`
}

// CustomTool is one saved custom tool.
type CustomTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
}

// Effective is effectiveSettings()'s object, its keys in Node's order.
type Effective struct {
	AppName                string            `json:"appName"`
	KbLabel                string            `json:"kbLabel"`
	Sections               []string          `json:"sections"`
	WorkflowCollection     []string          `json:"workflowCollection"`
	Theme                  string            `json:"theme"`
	Accent                 string            `json:"accent"`
	Surface                string            `json:"surface"`
	IconURL                string            `json:"iconUrl"`
	IconURLDark            string            `json:"iconUrlDark"`
	SweepIntervalSec       Number            `json:"sweepIntervalSec"`
	SearchEngine           string            `json:"searchEngine"`
	GufiLog                bool              `json:"gufiLog"`
	SuggestedPrompts       []string          `json:"suggestedPrompts"`
	VisionModel            string            `json:"visionModel"`
	DisabledTools          []string          `json:"disabledTools"`
	McpEnabled             bool              `json:"mcpEnabled"`
	RagProxyEnabled        bool              `json:"ragProxyEnabled"`
	HistoryIntervalSec     Number            `json:"historyIntervalSec"`
	McpServers             []json.RawMessage `json:"mcpServers"`
	DelegationEnabled      bool              `json:"delegationEnabled"`
	DelegationMaxAgents    Number            `json:"delegationMaxAgents"`
	MaxToolRounds          Number            `json:"maxToolRounds"`
	DelegationMaxDepth     Number            `json:"delegationMaxDepth"`
	AgentExecution         string            `json:"agentExecution"`
	VoiceEnabled           bool              `json:"voiceEnabled"`
	VoiceURL               string            `json:"voiceUrl"`
	SetupDone              bool              `json:"setupDone"`
	AiEnabled              bool              `json:"aiEnabled"`
	Updates                UpdatePolicy      `json:"updates"`
	CustomTools            []json.RawMessage `json:"customTools"`
	RagDefaultModel        string            `json:"ragDefaultModel"`
	RagTopK                Number            `json:"ragTopK"`
	RagAllowDeploymentKey  bool              `json:"ragAllowDeploymentKey"`
	RagAdvertiseRagModel   bool              `json:"ragAdvertiseRagModel"`
	RagAdvertiseAgentModel bool              `json:"ragAdvertiseAgentModel"`
	ChatSharedHistory      bool              `json:"chatSharedHistory"`
	RagEndpointAutoStart   bool              `json:"ragEndpointAutoStart"`
	RagEndpointName        string            `json:"ragEndpointName"`
	RequirePersonalKey     bool              `json:"requirePersonalKey"`
	BannerText             string            `json:"bannerText"`
	KbMission              string            `json:"kbMission"`
	BannerColor            string            `json:"bannerColor"`
	BannerWhenEmbedded     bool              `json:"bannerWhenEmbedded"`
	AllowKeySharing        bool              `json:"allowKeySharing"`
	HpcStatusURL           string            `json:"hpcStatusUrl"`
	HpcStatusWorkflow      string            `json:"hpcStatusWorkflow"`
	ChatTemplateKwargs     string            `json:"chatTemplateKwargs"`
	ChatModelWindows       string            `json:"chatModelWindows"`
	ServeWorkflows         string            `json:"serveWorkflows"`
}

// PersonalPrompts, PersonalSections and PersonalMission are settings.ts's.
var PersonalPrompts = []string{
	"What is in my folder, and how is it organized?",
	"Which files changed most recently, and what changed in them?",
	"Summarize the documents in a folder I name.",
	"Find my notes and documents about a topic I name.",
	"Which PDFs do I have, and what is each one about?",
	"Which are my largest files and folders?",
	"Compare two documents I name and list how they differ.",
	"Which spreadsheets do I have, and what does each one track?",
	"List my presentations and the topic of each.",
	"What have I written about a project I name, and when?",
}

var deploymentPrompts = []string{
	"What tools can you use and what can I ask you to do?",
	"What is in this knowledge base?",
	"Which workflows are available to run?",
}

var PersonalSections = []string{"chat", "library", "search", "query", "overview", "history", "agents", "settings", "help"}

var PersonalMission = strings.Join([]string{
	"This Studio indexes one person's own files on their computer: notes, documents, papers, spreadsheets, code and other files in the indexed folder.",
	"The person asking owns these files.",
	"Answer from the files, cite the ones used, and say when the files do not cover a question.",
	"Most questions are about finding a file, summarizing or comparing documents, or what changed recently.",
}, " ")

// PersonalInstall is settings.ts's personalInstall: nobody signs in and
// the platform did not deploy it.
func (s *Store) PersonalInstall() bool {
	return !s.authEnabled && s.env("PW_PLATFORM_HOST") == ""
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Store) envPrompts() []string {
	fallback := deploymentPrompts
	if s.PersonalInstall() {
		fallback = PersonalPrompts
	}
	raw := s.envOr("SUGGESTED_PROMPTS", "[]")
	var list []any
	if json.Unmarshal([]byte(raw), &list) != nil || len(list) == 0 {
		return fallback
	}
	out := make([]string, len(list))
	for i, v := range list {
		if str, ok := v.(string); ok {
			out[i] = str
		} else {
			b, _ := json.Marshal(v)
			out[i] = string(b)
		}
	}
	return out
}

// jsTrim is String.prototype.trim: Unicode white space and line ends.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff })
}

func splitList(v string) []string {
	out := []string{}
	for _, x := range strings.Split(v, ",") {
		if x = jsTrim(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func or[T any](p *T, d T) T {
	if p != nil {
		return *p
	}
	return d
}

// envOr is `process.env[k] ?? d`: a variable set to the empty string is
// a value.
func (s *Store) envOr(k, d string) string {
	if v, ok := s.lookup(k); ok {
		return v
	}
	return d
}

// Effective is effectiveSettings(): saved settings over environment
// defaults.
func (s *Store) Effective() Effective {
	m := s.Saved()
	env := s.env
	setupDone := field[bool](m, "setupDone")
	freshPersonal := s.PersonalInstall() && !or(setupDone, false)
	str := func(k string) *string { return field[string](m, k) }
	b := func(k string) *bool { return field[bool](m, k) }
	num := func(k string) *Number { return field[Number](m, k) }
	list := func(k string) *[]string { return field[[]string](m, k) }
	raws := func(k string) *[]json.RawMessage { return field[[]json.RawMessage](m, k) }
	envNum := func(k, d string) Number { return Number(JSNumber(s.envOr(k, d))) }

	e := Effective{}
	e.AppName = or(str("appName"), s.envOr("APP_NAME", "Studio"))
	e.KbLabel = or(str("kbLabel"), s.envOr("KB_LABEL", filepath.Base(s.kbRoot)))
	switch {
	case list("sections") != nil:
		e.Sections = *list("sections")
	case env("STUDIO_SECTIONS") != "":
		e.Sections = splitList(env("STUDIO_SECTIONS"))
	case freshPersonal:
		e.Sections = PersonalSections
	default:
		e.Sections = []string{}
	}
	if l := list("workflowCollection"); l != nil {
		e.WorkflowCollection = *l
	} else if v := env("STUDIO_WORKFLOWS"); v != "" {
		e.WorkflowCollection = splitList(v)
	} else {
		e.WorkflowCollection = []string{}
	}
	theme := "light"
	if env("THEME") == "dark" {
		theme = "dark"
	}
	e.Theme = or(str("theme"), theme)
	e.Accent = or(str("accent"), s.envOr("APP_ACCENT", "navy"))
	e.Surface = or(str("surface"), s.envOr("APP_SURFACE", "neutral"))
	e.IconURL = or(str("iconUrl"), env("APP_ICON"))
	e.IconURLDark = or(str("iconUrlDark"), env("APP_ICON_DARK"))
	e.SweepIntervalSec = or(num("sweepIntervalSec"), envNum("SWEEP_INTERVAL_SEC", "300"))
	engine := "parallel"
	if env("STUDIO_SEARCH_ENGINE") == "gufi_vt" {
		engine = "gufi_vt"
	}
	e.SearchEngine = or(str("searchEngine"), engine)
	e.GufiLog = or(b("gufiLog"), env("STUDIO_GUFI_LOG") == "1")
	saved := list("suggestedPrompts")
	if saved == nil || len(*saved) == 0 || (s.PersonalInstall() && sameList(*saved, deploymentPrompts)) {
		e.SuggestedPrompts = s.envPrompts()
	} else {
		e.SuggestedPrompts = *saved
	}
	e.VisionModel = or(str("visionModel"), env("ADE_VISION_MODEL"))
	e.DisabledTools = or(list("disabledTools"), []string{})
	e.McpEnabled = or(b("mcpEnabled"), env("MCP_ENABLED") != "0")
	e.RagProxyEnabled = or(b("ragProxyEnabled"), env("RAG_PROXY_ENABLED") != "0")
	e.HistoryIntervalSec = or(num("historyIntervalSec"), envNum("HISTORY_INTERVAL_SEC", "86400"))
	e.McpServers = or(raws("mcpServers"), []json.RawMessage{})
	e.DelegationEnabled = or(b("delegationEnabled"), env("DELEGATION_ENABLED") != "0")
	e.DelegationMaxAgents = or(num("delegationMaxAgents"), envNum("DELEGATION_MAX_AGENTS", "6"))
	rounds := JSNumber(env("STUDIO_MAX_TOOL_ROUNDS"))
	if !truthy(rounds) {
		rounds = 50
	}
	e.MaxToolRounds = or(num("maxToolRounds"), Number(rounds))
	e.DelegationMaxDepth = or(num("delegationMaxDepth"), envNum("DELEGATION_MAX_DEPTH", "1"))
	exec := "runner"
	if v := env("AGENT_EXECUTION"); v == "runner" || v == "sessions" || v == "both" {
		exec = v
	}
	if v := str("agentExecution"); v != nil {
		exec = *v
	}
	e.AgentExecution = exec
	e.VoiceEnabled = or(b("voiceEnabled"), env("VOICE_ENABLED") == "1")
	e.VoiceURL = or(str("voiceUrl"), env("VOICE_URL"))
	e.SetupDone = or(setupDone, false)
	ai := env("AI_ENABLED") != "0"
	if freshPersonal {
		ai = env("AI_ENABLED") == "1"
	}
	e.AiEnabled = or(b("aiEnabled"), ai)
	e.Updates = asPolicy(m["updates"])
	e.CustomTools = or(raws("customTools"), []json.RawMessage{})
	e.RagDefaultModel = or(str("ragDefaultModel"), env("RAG_DEFAULT_MODEL"))
	topK := JSNumber(env("RAG_TOP_K"))
	if !truthy(topK) {
		topK = 6
	}
	e.RagTopK = or(num("ragTopK"), Number(min(max(topK, 1), 20)))
	e.RagAllowDeploymentKey = or(b("ragAllowDeploymentKey"), env("RAG_PROXY_ALLOW_DEPLOYMENT_KEY") == "1")
	e.RagAdvertiseRagModel = or(b("ragAdvertiseRagModel"), env("RAG_ADVERTISE_RAG_MODEL") == "1")
	e.RagAdvertiseAgentModel = or(b("ragAdvertiseAgentModel"), env("RAG_ADVERTISE_AGENT_MODEL") != "0")
	e.ChatSharedHistory = or(b("chatSharedHistory"), env("CHAT_SHARED_HISTORY") != "0")
	e.RagEndpointAutoStart = or(b("ragEndpointAutoStart"), env("RAG_ENDPOINT_AUTOSTART") == "1")
	e.RagEndpointName = or(str("ragEndpointName"), env("RAG_ENDPOINT_NAME"))
	e.RequirePersonalKey = or(b("requirePersonalKey"), env("REQUIRE_PERSONAL_KEY") == "1")
	e.BannerText = or(str("bannerText"), env("BANNER_TEXT"))
	if v := str("kbMission"); v != nil && jsTrim(*v) != "" {
		e.KbMission = *v
	} else if v, ok := s.lookup("KB_MISSION"); ok {
		e.KbMission = v
	} else if s.PersonalInstall() {
		e.KbMission = PersonalMission
	}
	e.BannerColor = or(str("bannerColor"), s.envOr("BANNER_COLOR", "#24612e"))
	e.BannerWhenEmbedded = or(b("bannerWhenEmbedded"), env("BANNER_WHEN_EMBEDDED") == "1")
	e.AllowKeySharing = or(b("allowKeySharing"), env("ALLOW_KEY_SHARING") != "0")
	e.HpcStatusURL = or(str("hpcStatusUrl"), env("HPC_STATUS_URL"))
	e.HpcStatusWorkflow = or(str("hpcStatusWorkflow"), s.envOr("HPC_STATUS_WORKFLOW", "hpc_status"))
	e.ChatTemplateKwargs = or(str("chatTemplateKwargs"), s.envOr("CHAT_TEMPLATE_KWARGS", `{"qwen":{"enable_thinking":false},"gpt-oss":{"reasoning_effort":"low"}}`))
	e.ChatModelWindows = or(str("chatModelWindows"), s.envOr("CHAT_MODEL_WINDOWS", `{"qwen":57344,"gpt-oss":65536}`))
	e.ServeWorkflows = or(str("serveWorkflows"), s.envOr("SERVE_WORKFLOWS", `{"vllm":{"workflow":"vllm-endpoint","inputs":{}},"ollama":{"workflow":"ollama-endpoint","inputs":{}}}`))
	return e
}

func asPolicy(raw json.RawMessage) UpdatePolicy {
	p := UpdatePolicy{Check: true, Install: "manual", Channel: "stable"}
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return p
	}
	if c, ok := v["check"].(bool); ok {
		p.Check = c
	}
	if v["install"] == "auto" {
		p.Install = "auto"
	}
	if v["channel"] == "prerelease" {
		p.Channel = "prerelease"
	}
	return p
}
