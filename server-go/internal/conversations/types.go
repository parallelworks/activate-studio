package conversations

import (
	"bytes"
	"encoding/json"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// File is one file a tool call returned, and its library (nil for the
// main folder).
type File struct {
	Path    string  `json:"path"`
	Library *string `json:"library"`
}

// Decision is a held call's answer.
type Decision struct {
	Status string `json:"status"`
	By     string `json:"by"`
	Name   string `json:"name,omitempty"`
	At     string `json:"at,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Alternative is a tool the user may run in place of a held call.
type Alternative struct {
	Tool        string `json:"tool"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
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
}

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
		return jsonobj.Marshal(o)
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
	return jsonobj.Marshal(o)
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
	return nil
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
}

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
	return jsonobj.Marshal(o)
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
	return nil
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
	d.Messages = make([]Message, len(c.Messages))
	for i, m := range c.Messages {
		d.Messages[i] = m
		if m.Parts != nil {
			d.Messages[i].Parts = append([]Part(nil), m.Parts...)
		}
	}
	return &d
}
