package chat

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/extract"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
)

// The tool layer's types: what the loop asks of it (Tools, implemented by
// goTools) and what it answers.

// Spec is a tool spec as the gateway is sent it.
type Spec = json.RawMessage

// Meta is what the loop knows about tools by name.
type Meta struct {
	MoreTools      string              `json:"moreTools"`
	PlatformGroups []string            `json:"platformGroups"`
	GroupOrder     []string            `json:"groupOrder"`
	Groups         map[string][]string `json:"groups"`
	GroupOf        map[string]string   `json:"groupOf"`
	AsksFirst      []string            `json:"asksFirst"`
	Labels         map[string]string   `json:"labels"`
}

// Changes is changesAvailable's answer.
type Changes struct {
	OK  bool   `json:"ok"`
	Why string `json:"why"`
}

// Viewer is viewerOf(req).
type Viewer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Begin is what the route works out before the first model turn.
type Begin struct {
	History []llm.Message `json:"history"`
	Listing *string       `json:"listing"`
	Viewer  *Viewer       `json:"viewer"`
	Changes Changes       `json:"changes"`
	// KBChanges is whether a library change can be asked for here.
	KBChanges Changes  `json:"kbChanges"`
	Platform  bool     `json:"platform"`
	Groups    []string `json:"groups"`
	Persona   *string  `json:"persona"`
	System    string   `json:"system"`
	Meta      Meta     `json:"meta"`
}

// Specs are a round's tools: those offered, and those sent.
type Specs struct {
	Available []Spec `json:"available"`
	Selected  []Spec `json:"selected"`
}

// Names is what a round's calls are, by name.
type Names struct {
	Allowed   map[string]bool    `json:"allowed"`
	Labels    map[string]string  `json:"labels"`
	GroupOf   map[string]*string `json:"groupOf"`
	AsksFirst map[string]bool    `json:"asksFirst"`
}

// Outcome is a tool's result.
type Outcome struct {
	Result  string               `json:"result"`
	Summary string               `json:"summary"`
	Failed  bool                 `json:"failed,omitempty"`
	Gufi    json.RawMessage      `json:"gufi,omitempty"`
	Files   []conversations.File `json:"files,omitempty"`
}

// ToolCtx is executeTool's ctx.
type ToolCtx struct {
	LabelScope     []string `json:"labelScope"`
	Library        *string  `json:"library"`
	UserKey        *string  `json:"userKey"`
	Model          *string  `json:"model"`
	ConversationID *string  `json:"conversationId"`
	UserID         *string  `json:"userId"`
}

// Prepared is prepareArgs's answer.
type Prepared struct {
	Args      json.RawMessage `json:"args"`
	Canonical string          `json:"canonical"`
	Problem   string          `json:"problem"`
}

// HeldRec is the part of a held call the resumed reply needs.
type HeldRec struct {
	ConversationID string        `json:"conversationId"`
	MessageID      string        `json:"messageId"`
	ToolCallID     string        `json:"toolCallId"`
	Tool           string        `json:"tool"`
	Turn           []llm.Message `json:"turn"`
	Groups         []string      `json:"groups"`
}

// Answered is answerHeld's answer.
type Answered struct {
	OK      bool     `json:"ok"`
	Why     string   `json:"why"`
	Rec     *HeldRec `json:"rec"`
	Tool    string   `json:"tool"`
	Summary string   `json:"summary"`
	Result  string   `json:"result"`
	Failed  bool     `json:"failed"`
}

// Access is the platform values the loop reads.
type Access struct {
	GatewayBase   string  `json:"gatewayBase"`
	DeploymentKey string  `json:"deploymentKey"`
	ContextName   *string `json:"contextName"`
	Platform      bool    `json:"platform"`
	// CLI is PW_CLI, the pw CLI Node chose.
	CLI string `json:"cli"`
}

// Tools is the tool layer the loop calls.
type Tools interface {
	Begin(r *http.Request, in map[string]any) (*Begin, error)
	System(r *http.Request, platform, connected bool) (string, error)
	Specs(r *http.Request, tools []string, hasTools bool, platform, changesOK bool, groups []string) (*Specs, error)
	Names(r *http.Request, tools []string, hasTools bool, names []string) (*Names, error)
	Execute(r *http.Request, name, args string, ctx ToolCtx) (*Outcome, error)
	Prepare(r *http.Request, name, args, changeKey string) (*Prepared, error)
	Hold(r *http.Request, in map[string]any) (json.RawMessage, error)
	Answer(r *http.Request, answer map[string]any, changeKey *string) (*Answered, error)
	Held(r *http.Request, conversationID string) ([]conversations.Holder, error)
	Access(r *http.Request, freshMS int) (*Access, error)
}

// envAccess is GATEWAY_BASE and the deployment credential from the
// environment and the pw CLI's credentials file, without Node.
func envAccess() *Access {
	base := os.Getenv("PW_GATEWAY_URL")
	if base == "" {
		base = os.Getenv("OPENAI_BASE_URL")
	}
	if base == "" {
		base = extract.DefaultGateway
	}
	key := os.Getenv("PW_API_KEY")
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key == "" {
		key = extract.GatewayKey()
	}
	return &Access{GatewayBase: strings.TrimRight(base, "/"), DeploymentKey: key}
}

// toolsArg is the reader's tool list as the request gave it: nil for none.
func toolsArg(tools []string, has bool) any {
	if !has {
		return nil
	}
	return tools
}
