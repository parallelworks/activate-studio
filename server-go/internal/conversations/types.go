package conversations

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// File is one file a tool call returned, and its library (nil for the
// main folder).
type File struct {
	Path    string  `json:"path"`
	Library *string `json:"library"`
	Extra   Extra   `json:"-"`
}

type fileFields File

var fileKeys = keys("path", "library")

func (f File) MarshalJSON() ([]byte, error) { return marshalWith(f.Extra, fileFields(f)) }

func (f *File) UnmarshalJSON(b []byte) error {
	var v fileFields
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	x, err := readExtra(b, fileKeys)
	*f, f.Extra = File(v), x
	return err
}

// Decision is a held call's answer.
type Decision struct {
	Status string `json:"status"`
	By     string `json:"by"`
	Name   string `json:"name,omitempty"`
	At     string `json:"at,omitempty"`
	Note   string `json:"note,omitempty"`
	Extra  Extra  `json:"-"`
}

type decisionFields Decision

var decisionKeys = keys("status", "by", "name", "at", "note")

func (d Decision) MarshalJSON() ([]byte, error) { return marshalWith(d.Extra, decisionFields(d)) }

func (d *Decision) UnmarshalJSON(b []byte) error {
	var v decisionFields
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	x, err := readExtra(b, decisionKeys)
	*d, d.Extra = Decision(v), x
	return err
}

// Alternative is a tool the user may run in place of a held call.
type Alternative struct {
	Tool        string `json:"tool"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Extra       Extra  `json:"-"`
}

type alternativeFields Alternative

var alternativeKeys = keys("tool", "label", "description")

func (a Alternative) MarshalJSON() ([]byte, error) { return marshalWith(a.Extra, alternativeFields(a)) }

func (a *Alternative) UnmarshalJSON(b []byte) error {
	var v alternativeFields
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	x, err := readExtra(b, alternativeKeys)
	*a, a.Extra = Alternative(v), x
	return err
}

// Part is StoredToolCallPart or StoredApprovalPart, by Kind.
type Part struct {
	Kind string // "tool_call" or "approval"
	ID   string

	// tool_call
	Name       string
	Args       string
	Status     string
	Result     *string
	Gufi       json.RawMessage
	GufiViewer string
	Files      []File
	Decision   *Decision

	// approval
	ApprovalKind      string
	ToolName          string
	Subject           string
	Header            string
	Command           string
	Reason            string
	CanAllowDirectory *bool
	ExpiresAt         string
	Instead           []Alternative
	Resolved          bool
	Answer            string
	WaitingOn         string

	// Extra is the stored part's other fields and key order.
	Extra Extra
}

var (
	toolCallKeys = keys("kind", "id", "name", "args", "status", "result", "gufi", "gufiViewer", "files", "decision")
	approvalKeys = keys("kind", "id", "approvalKind", "toolName", "subject", "header", "command", "reason", "canAllowDirectory", "expiresAt", "instead", "resolved", "answer", "waitingOn")
)

// MarshalJSON writes the part's keys in the order Node builds them.
func (p Part) MarshalJSON() ([]byte, error) {
	o := jsonobj.New()
	_ = o.SetValue("kind", p.Kind)
	_ = o.SetValue("id", p.ID)
	if p.Kind == "approval" {
		_ = o.SetValue("approvalKind", p.ApprovalKind)
		if p.ToolName != "" {
			_ = o.SetValue("toolName", p.ToolName)
		}
		if p.Subject != "" {
			_ = o.SetValue("subject", p.Subject)
		}
		if p.Header != "" {
			_ = o.SetValue("header", p.Header)
		}
		if p.Command != "" {
			_ = o.SetValue("command", p.Command)
		}
		if p.Reason != "" {
			_ = o.SetValue("reason", p.Reason)
		}
		if p.CanAllowDirectory != nil {
			_ = o.SetValue("canAllowDirectory", *p.CanAllowDirectory)
		}
		if p.ExpiresAt != "" {
			_ = o.SetValue("expiresAt", p.ExpiresAt)
		}
		if len(p.Instead) > 0 {
			_ = o.SetValue("instead", p.Instead)
		}
		if p.Resolved {
			_ = o.SetValue("resolved", true)
		}
		if p.Answer != "" {
			_ = o.SetValue("answer", p.Answer)
		}
		if p.WaitingOn != "" {
			_ = o.SetValue("waitingOn", p.WaitingOn)
		}
		return jsonobj.Marshal(p.Extra.apply(o))
	}
	_ = o.SetValue("name", p.Name)
	_ = o.SetValue("args", p.Args)
	_ = o.SetValue("status", p.Status)
	if p.Result != nil {
		_ = o.SetValue("result", *p.Result)
	}
	if len(p.Gufi) > 0 {
		o.Set("gufi", p.Gufi)
	}
	if p.GufiViewer != "" {
		_ = o.SetValue("gufiViewer", p.GufiViewer)
	}
	if len(p.Files) > 0 {
		_ = o.SetValue("files", p.Files)
	}
	if p.Decision != nil {
		_ = o.SetValue("decision", p.Decision)
	}
	return jsonobj.Marshal(p.Extra.apply(o))
}

// UnmarshalJSON reads either kind.
func (p *Part) UnmarshalJSON(b []byte) error {
	var raw struct {
		Kind              string          `json:"kind"`
		ID                string          `json:"id"`
		Name              string          `json:"name"`
		Args              string          `json:"args"`
		Status            string          `json:"status"`
		Result            *string         `json:"result"`
		Gufi              json.RawMessage `json:"gufi"`
		GufiViewer        string          `json:"gufiViewer"`
		Files             []File          `json:"files"`
		Decision          *Decision       `json:"decision"`
		ApprovalKind      string          `json:"approvalKind"`
		ToolName          string          `json:"toolName"`
		Subject           string          `json:"subject"`
		Header            string          `json:"header"`
		Command           string          `json:"command"`
		Reason            string          `json:"reason"`
		CanAllowDirectory *bool           `json:"canAllowDirectory"`
		ExpiresAt         string          `json:"expiresAt"`
		Instead           []Alternative   `json:"instead"`
		Resolved          bool            `json:"resolved"`
		Answer            string          `json:"answer"`
		WaitingOn         string          `json:"waitingOn"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*p = Part{Kind: raw.Kind, ID: raw.ID, Name: raw.Name, Args: raw.Args, Status: raw.Status, Result: raw.Result, GufiViewer: raw.GufiViewer, Files: raw.Files, Decision: raw.Decision,
		ApprovalKind: raw.ApprovalKind, ToolName: raw.ToolName, Subject: raw.Subject, Header: raw.Header, Command: raw.Command, Reason: raw.Reason, CanAllowDirectory: raw.CanAllowDirectory,
		ExpiresAt: raw.ExpiresAt, Instead: raw.Instead, Resolved: raw.Resolved, Answer: raw.Answer, WaitingOn: raw.WaitingOn}
	if g := bytes.TrimSpace(raw.Gufi); len(g) > 0 && string(g) != "null" {
		p.Gufi = append(json.RawMessage(nil), g...)
	}
	known := toolCallKeys
	if raw.Kind == "approval" {
		known = approvalKeys
	}
	x, err := readExtra(b, known)
	p.Extra = x
	return err
}

// Usage is the token counts summed over a turn's model calls.
type Usage struct {
	PromptTokens     float64 `json:"prompt_tokens"`
	CompletionTokens float64 `json:"completion_tokens"`
	TotalTokens      float64 `json:"total_tokens"`
}

// Message is StoredMessage.
type Message struct {
	ID        string
	Role      string
	Content   *string
	ParentID  *string
	Timestamp string
	// Model is written (null included) when HasModel.
	Model             *string
	HasModel          bool
	Reasoning         string
	ReasoningDuration *float64
	TokensUsed        json.RawMessage
	Parts             []Part
	FinishReason      string
	// Error is set on read only (withUnanswered); never stored.
	Error string

	// Extra is the stored message's other fields and key order.
	Extra Extra
}

var messageKeys = keys("id", "role", "content", "parentId", "timestamp", "model", "reasoning", "reasoningDuration", "tokensUsed", "parts", "finishReason", "error")

// Text is the content, ” for null.
func (m *Message) Text() string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// MarshalJSON writes the message in Node's key order.
func (m Message) MarshalJSON() ([]byte, error) {
	o := jsonobj.New()
	_ = o.SetValue("id", m.ID)
	_ = o.SetValue("role", m.Role)
	if m.Content == nil {
		o.Set("content", json.RawMessage("null"))
	} else {
		_ = o.SetValue("content", *m.Content)
	}
	if m.ParentID == nil {
		o.Set("parentId", json.RawMessage("null"))
	} else {
		_ = o.SetValue("parentId", *m.ParentID)
	}
	_ = o.SetValue("timestamp", m.Timestamp)
	if m.HasModel {
		if m.Model == nil {
			o.Set("model", json.RawMessage("null"))
		} else {
			_ = o.SetValue("model", *m.Model)
		}
	}
	if m.Reasoning != "" {
		_ = o.SetValue("reasoning", m.Reasoning)
	}
	if m.ReasoningDuration != nil {
		_ = o.SetValue("reasoningDuration", *m.ReasoningDuration)
	}
	if len(m.TokensUsed) > 0 {
		o.Set("tokensUsed", m.TokensUsed)
	}
	if len(m.Parts) > 0 {
		_ = o.SetValue("parts", m.Parts)
	}
	if m.FinishReason != "" {
		_ = o.SetValue("finishReason", m.FinishReason)
	}
	if m.Error != "" {
		_ = o.SetValue("error", m.Error)
	}
	return jsonobj.Marshal(m.Extra.apply(o))
}

// UnmarshalJSON reads a stored message.
func (m *Message) UnmarshalJSON(b []byte) error {
	o, err := jsonobj.Parse(b)
	if err != nil {
		return err
	}
	var raw struct {
		ID                string          `json:"id"`
		Role              string          `json:"role"`
		Content           *string         `json:"content"`
		ParentID          *string         `json:"parentId"`
		Timestamp         string          `json:"timestamp"`
		Model             *string         `json:"model"`
		Reasoning         string          `json:"reasoning"`
		ReasoningDuration *float64        `json:"reasoningDuration"`
		TokensUsed        json.RawMessage `json:"tokensUsed"`
		Parts             []Part          `json:"parts"`
		FinishReason      string          `json:"finishReason"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = Message{ID: raw.ID, Role: raw.Role, Content: raw.Content, ParentID: raw.ParentID, Timestamp: raw.Timestamp, Model: raw.Model, HasModel: o.Has("model"),
		Reasoning: raw.Reasoning, ReasoningDuration: raw.ReasoningDuration, Parts: raw.Parts, FinishReason: raw.FinishReason}
	if t := bytes.TrimSpace(raw.TokensUsed); len(t) > 0 && string(t) != "null" {
		m.TokensUsed = append(json.RawMessage(nil), t...)
	}
	x, err := readExtra(b, messageKeys)
	m.Extra = x
	return err
}

// Conversation is StoredConversation.
type Conversation struct {
	ID             string    `json:"id"`
	Title          *string   `json:"title"`
	Owner          *string   `json:"owner"`
	CreatedAt      string    `json:"createdAt"`
	UpdatedAt      string    `json:"updatedAt"`
	ActiveBranchID *string   `json:"activeBranchId"`
	Messages       []Message `json:"messages"`
	// PWCode is the pw code session this conversation runs in, when its
	// replies come from pw code.
	PWCode *PWCodeLink `json:"pwcode,omitempty"`
	// Engine is what answers the conversation ("pwcode" or "studio"): the
	// engine that answered it last, or the one the person switched it to.
	// "" for a conversation stored before it was kept.
	Engine string `json:"engine,omitempty"`
	// Extra is the stored conversation's other fields and key order.
	Extra Extra `json:"-"`
}

// PWCodeLink is a conversation's pw code session: the site whose daemon
// holds it ("local" for this host's), its id, the folder it works in,
// its PW Code on the web address, and when the Studio made it.
type PWCodeLink struct {
	Site      string `json:"site"`
	Session   string `json:"session"`
	Workspace string `json:"workspace,omitempty"`
	URL       string `json:"url,omitempty"`
	Model     string `json:"model,omitempty"`
	CreatedAt string `json:"createdAt"`
	// LastMessage is the id of the last reply the session gave here, so
	// a later turn tells it what came after (a reply from the Studio
	// assistant, say).
	LastMessage string `json:"lastMessage,omitempty"`
	// MCPURL is where the session reaches this Studio's MCP server.
	MCPURL string `json:"mcpUrl,omitempty"`
	// Library and LabelScope are the conversation's scope as of its latest
	// message, which the session's Studio tool calls run in; Tools is the
	// kinds of tools the person left on (nil for all); ScopeNote is what
	// the session was last told of the scope.
	Library    []string  `json:"library,omitempty"`
	LabelScope []string  `json:"labelScope,omitempty"`
	Tools      *[]string `json:"tools,omitempty"`
	ScopeNote  string    `json:"scopeNote,omitempty"`
	// Continued: the session began outside this Studio (in a terminal, or
	// in PW Code on the web) and the person continued it here from the
	// Code page. The Studio never archives or replaces such a session.
	// Machine and MachineName are its machine as the platform lists it.
	Continued bool `json:"continued,omitempty"`
	// ModelNote says why the session runs on Model when the Studio moved
	// it there from pw code's own model, which the AI connection does not
	// offer.
	ModelNote string `json:"modelNote,omitempty"`
	// ModelFrom is the model the session was moved off, with ModelNote.
	ModelFrom string `json:"modelFrom,omitempty"`
	// Relay: the session runs on another of the person's machines and
	// the chat drives it through the platform's relay as them, so it
	// stays where it is (Site is then pwcode.RelaySite(Machine)).
	Relay       bool   `json:"relay,omitempty"`
	Machine     string `json:"machine,omitempty"`
	MachineName string `json:"machineName,omitempty"`
	// ToolsError is why the session lacked the Studio's tools at the last
	// message, after a retry; empty when it had them.
	ToolsError string `json:"toolsError,omitempty"`
	// Persona is the agent instructions the conversation chose when its
	// session was made, "" for none: a session with instructions works in
	// agents/<persona>/, whose AGENTS.md carries them.
	Persona string `json:"persona,omitempty"`
	// Picked: Workspace is a folder the person chose as the chat started,
	// so a later session of the chat works there too, and instructions go
	// into its session rather than into the folder.
	Picked bool `json:"picked,omitempty"`
	// HasTools: a session on another machine that started with this
	// Studio's tools in pw code there. Untold: it has not been given the
	// Studio's instructions yet, which its first message brings.
	HasTools bool `json:"hasTools,omitempty"`
	Untold   bool `json:"untold,omitempty"`
}

type conversationFields Conversation

var conversationKeys = keys("id", "title", "owner", "createdAt", "updatedAt", "activeBranchId", "messages", "pwcode", "engine")

// MarshalJSON writes the fields in their declared order over the stored object.
func (c Conversation) MarshalJSON() ([]byte, error) {
	return marshalWith(c.Extra, conversationFields(c))
}

// UnmarshalJSON reads a stored conversation.
func (c *Conversation) UnmarshalJSON(b []byte) error {
	var v conversationFields
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	x, err := readExtra(b, conversationKeys)
	*c, c.Extra = Conversation(v), x
	return err
}

// AnsweredBy is what answers the conversation when nothing is picked for
// a message: the engine it keeps (the last to answer it, or the one the
// person switched it to); for one stored before that was kept, the engine
// of its last reply (pw code's are pwc- and pwh- messages, notes are not
// replies), or "pwcode" for a chat that continues a session; "" for a chat
// nothing has answered yet, which a new chat's default answers.
func (c *Conversation) AnsweredBy() string {
	if c.Engine == "pwcode" || c.Engine == "studio" {
		return c.Engine
	}
	for i := len(c.Messages) - 1; i >= 0; i-- {
		m := c.Messages[i]
		if m.Role != "assistant" || strings.HasPrefix(m.ID, "note-") {
			continue
		}
		if strings.HasPrefix(m.ID, "pwc-") || strings.HasPrefix(m.ID, "pwh-") || (m.Model != nil && strings.HasPrefix(*m.Model, "pw code")) {
			return "pwcode"
		}
		return "studio"
	}
	if c.PWCode != nil && c.PWCode.Continued {
		return "pwcode"
	}
	return ""
}

// OwnerName is the owner, ” for none.
func (c *Conversation) OwnerName() string {
	if c.Owner == nil {
		return ""
	}
	return *c.Owner
}

// Clone is a deep enough copy for the read-time decorations.
func (c *Conversation) Clone() *Conversation {
	d := *c
	if c.PWCode != nil {
		l := *c.PWCode
		d.PWCode = &l
	}
	d.Messages = make([]Message, len(c.Messages))
	for i, m := range c.Messages {
		d.Messages[i] = m
		if m.Parts != nil {
			d.Messages[i].Parts = append([]Part(nil), m.Parts...)
		}
	}
	return &d
}
