package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/extract"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
)

// TODO(chat tools): the adapter below asks Node (server/src/chat/goBridge.ts)
// for what tools.ts, context.ts, approvals.ts and access.ts work out, until
// those are ported. Every method is one in-process call of the Node route.

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
	History  []llm.Message `json:"history"`
	Listing  *string       `json:"listing"`
	Viewer   *Viewer       `json:"viewer"`
	Changes  Changes       `json:"changes"`
	Platform bool          `json:"platform"`
	Groups   []string      `json:"groups"`
	Persona  *string       `json:"persona"`
	System   string        `json:"system"`
	Meta     Meta          `json:"meta"`
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

// nodeTools is Tools over the Node bridge.
type nodeTools struct{ a *app.App }

func (n nodeTools) call(r *http.Request, op string, in any, out any) error {
	if n.a.NodeFree() || n.a.NodeAPI == nil {
		return fmt.Errorf("the chat tools are not available without the Node server")
	}
	// The bridge secret rides as a header; Node.Do signs nothing itself.
	rr := r.Clone(r.Context())
	rr.Header = r.Header.Clone()
	return n.a.NodeAPI.DoSigned(rr, "POST", "/internal/go/chat/"+op, in, out)
}

func (n nodeTools) Begin(r *http.Request, in map[string]any) (*Begin, error) {
	var b Begin
	return &b, n.call(r, "begin", in, &b)
}

func (n nodeTools) System(r *http.Request, platform, connected bool) (string, error) {
	var out struct {
		Prompt string `json:"prompt"`
	}
	err := n.call(r, "system", map[string]any{"platform": platform, "connected": connected}, &out)
	return out.Prompt, err
}

func toolsArg(tools []string, has bool) any {
	if !has {
		return nil
	}
	return tools
}

func (n nodeTools) Specs(r *http.Request, tools []string, has bool, platform, changesOK bool, groups []string) (*Specs, error) {
	var s Specs
	return &s, n.call(r, "specs", map[string]any{"tools": toolsArg(tools, has), "platform": platform, "changesOk": changesOK, "groups": groups}, &s)
}

func (n nodeTools) Names(r *http.Request, tools []string, has bool, names []string) (*Names, error) {
	var s Names
	return &s, n.call(r, "names", map[string]any{"tools": toolsArg(tools, has), "names": names}, &s)
}

func (n nodeTools) Execute(r *http.Request, name, args string, ctx ToolCtx) (*Outcome, error) {
	var o Outcome
	if err := n.call(r, "execute", map[string]any{"name": name, "args": args, "ctx": ctx}, &o); err != nil {
		var ne *app.NodeError
		if asNodeError(err, &ne) {
			var e struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(ne.Body, &e) == nil && e.Error != "" {
				return nil, fmt.Errorf("%s", e.Error)
			}
		}
		return nil, err
	}
	return &o, nil
}

func asNodeError(err error, out **app.NodeError) bool {
	ne, ok := err.(*app.NodeError)
	if ok {
		*out = ne
	}
	return ok
}

func (n nodeTools) Prepare(r *http.Request, name, args, changeKey string) (*Prepared, error) {
	var p Prepared
	return &p, n.call(r, "prepare", map[string]any{"name": name, "args": args, "changeKey": changeKey}, &p)
}

func (n nodeTools) Hold(r *http.Request, in map[string]any) (json.RawMessage, error) {
	var out struct {
		Approval json.RawMessage `json:"approval"`
		Error    string          `json:"error"`
	}
	if err := n.call(r, "hold", in, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s", out.Error)
	}
	return out.Approval, nil
}

func (n nodeTools) Answer(r *http.Request, answer map[string]any, changeKey *string) (*Answered, error) {
	var a Answered
	return &a, n.call(r, "answer", map[string]any{"answer": answer, "changeKey": changeKey}, &a)
}

func (n nodeTools) Held(r *http.Request, conversationID string) ([]conversations.Holder, error) {
	var out struct {
		Held []struct {
			ToolCallID string `json:"toolCallId"`
			Viewer     string `json:"viewer"`
			ViewerName string `json:"viewerName"`
		} `json:"held"`
	}
	if err := n.call(r, "held", map[string]any{"conversationId": conversationID}, &out); err != nil {
		return nil, err
	}
	hs := make([]conversations.Holder, len(out.Held))
	for i, h := range out.Held {
		hs[i] = conversations.Holder{ToolCallID: h.ToolCallID, Viewer: h.Viewer, ViewerName: h.ViewerName}
	}
	return hs, nil
}

func (n nodeTools) Access(r *http.Request, freshMS int) (*Access, error) {
	if n.a.NodeFree() || n.a.NodeAPI == nil {
		return envAccess(), nil
	}
	var a Access
	if err := n.call(r, "access", map[string]any{"fresh": freshMS}, &a); err != nil {
		return envAccess(), err
	}
	return &a, nil
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

var _ = bridge.Header
