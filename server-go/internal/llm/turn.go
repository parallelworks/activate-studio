package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
)

// Function is a tool call's function.
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall is WireToolCall, in the key order Node writes it.
type ToolCall struct {
	Index    *int     `json:"index,omitempty"`
	ID       string   `json:"id,omitempty"`
	Type     string   `json:"type,omitempty"`
	Function Function `json:"function"`
}

// Message is WireMessage. Content nil is JSON null (an assistant turn with
// only tool calls); NoContent leaves the key out.
type Message struct {
	Role       string
	Content    *string
	NoContent  bool
	ToolCallID string
	ToolCalls  []ToolCall
	Name       string
}

// Text is the content as a string (” for null).
func (m Message) Text() string {
	if m.Content == nil {
		return ""
	}
	return *m.Content
}

// Str is a string's pointer, for Message.Content.
func Str(s string) *string { return &s }

// MarshalJSON writes the keys in the order the Node code builds them.
func (m Message) MarshalJSON() ([]byte, error) {
	o := jsonobj.New()
	_ = o.SetValue("role", m.Role)
	if m.Role == "tool" {
		_ = o.SetValue("tool_call_id", m.ToolCallID)
	}
	if !m.NoContent {
		if m.Content == nil {
			o.Set("content", json.RawMessage("null"))
		} else {
			_ = o.SetValue("content", *m.Content)
		}
	}
	if m.Role != "tool" && m.ToolCallID != "" {
		_ = o.SetValue("tool_call_id", m.ToolCallID)
	}
	if m.ToolCalls != nil {
		_ = o.SetValue("tool_calls", m.ToolCalls)
	}
	if m.Name != "" {
		_ = o.SetValue("name", m.Name)
	}
	return jsonobj.Marshal(o)
}

// UnmarshalJSON reads a client's message.
func (m *Message) UnmarshalJSON(b []byte) error {
	var raw struct {
		Role       any             `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []ToolCall      `json:"tool_calls"`
		Name       string          `json:"name"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = Message{Role: fmt.Sprint(raw.Role), ToolCallID: raw.ToolCallID, ToolCalls: raw.ToolCalls, Name: raw.Name}
	if raw.Role == nil {
		m.Role = ""
	}
	if len(raw.Content) == 0 {
		m.NoContent = true
	} else if string(bytes.TrimSpace(raw.Content)) != "null" {
		var s string
		if json.Unmarshal(raw.Content, &s) == nil {
			m.Content = &s
		} else {
			c := string(raw.Content)
			m.Content = &c
		}
	}
	return nil
}

// Usage is TokenUsage.
type Usage struct {
	PromptTokens     *float64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *float64 `json:"completion_tokens,omitempty"`
	TotalTokens      *float64 `json:"total_tokens,omitempty"`
}

// Turn is TurnResult.
type Turn struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason *string
	Model        *string
	Usage        *Usage
}

// Callbacks receive content and reasoning as they arrive.
type Callbacks struct {
	OnContent   func(string)
	OnReasoning func(string)
}

var harmonyRe = regexp.MustCompile(`<\|(call|channel|message|constrain|end)\|`)
var toRe = regexp.MustCompile(`^\s*to=`)

// IdleError is the reply stopped for a silent stream.
type IdleError struct{ Msg string }

func (e *IdleError) Error() string { return e.Msg }

// StreamTurn is streamTurn(body, allocation, cb, signal, key, baseUrl,
// endpoint). body is the request in its key order; stream is set from its
// stream field (absent means true).
func (c *Client) StreamTurn(ctx context.Context, body *jsonobj.Object, allocation string, cb Callbacks, key, baseURL string, endpoint *modelkeys.Endpoint) (*Turn, error) {
	var target *modelkeys.Endpoint
	if baseURL != "" {
		target = endpoint
	}
	k := key
	if k == "" {
		k = c.Key()
	}
	headers := Headers(k, target)
	headers.Set("Content-Type", "application/json")
	if allocation != "" {
		headers.Set("X-Allocation", allocation)
	}
	streaming := string(body.Get("stream")) != "false"
	send := body.Clone()
	_ = send.SetValue("stream", streaming)
	payload, err := jsonobj.Marshal(send)
	if err != nil {
		return nil, err
	}
	base := baseURL
	if base == "" {
		base = c.Base()
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", WithQuery(base+"/chat/completions", target), bytes.NewReader(payload))
	req.Header = headers
	model, _ := jsonobj.String(body.Get("model"))
	res, err := c.reach(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		b, _ := io.ReadAll(res.Body)
		text := string(b)
		gerr := c.gatewayError(res.StatusCode, text, "chat")
		var ae *AuthError
		c.RecordOutcome(model, &ModelFailure{Message: Slice(text, 200), Auth: errors.As(gerr, &ae), Status: res.StatusCode})
		return nil, gerr
	}
	c.RecordOutcome(model, nil)

	if !streaming {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, err
		}
		var full struct {
			Error   json.RawMessage `json:"error"`
			Model   *string         `json:"model"`
			Usage   json.RawMessage `json:"usage"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
				Message      struct {
					Content          any        `json:"content"`
					ReasoningContent any        `json:"reasoning_content"`
					ToolCalls        []ToolCall `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(b, &full); err != nil {
			return nil, err
		}
		if !jsonobj.IsNullish(full.Error) {
			return nil, &StreamedError{errorMessage(full.Error)}
		}
		t := &Turn{Model: full.Model}
		if len(full.Choices) > 0 {
			ch := full.Choices[0]
			content, isStr := ch.Message.Content.(string)
			if isStr && (harmonyRe.MatchString(content) || toRe.MatchString(content)) {
				content = ""
			}
			if isStr && content != "" && cb.OnContent != nil {
				cb.OnContent(content)
			}
			if r, ok := ch.Message.ReasoningContent.(string); ok && r != "" {
				if cb.OnReasoning != nil {
					cb.OnReasoning(r)
				}
				t.Reasoning = r
			}
			t.Content = content
			for i, tc := range ch.Message.ToolCalls {
				i := i
				typ := tc.Type
				if typ == "" {
					typ = "function"
				}
				t.ToolCalls = append(t.ToolCalls, ToolCall{Index: &i, ID: tc.ID, Type: typ, Function: tc.Function})
			}
			t.FinishReason = ch.FinishReason
		}
		if len(full.Usage) > 0 && bytes.TrimSpace(full.Usage)[0] == '{' {
			var u Usage
			_ = json.Unmarshal(full.Usage, &u)
			t.Usage = &u
		}
		if t.ToolCalls == nil {
			t.ToolCalls = []ToolCall{}
		}
		return t, nil
	}

	t := &Turn{ToolCalls: []ToolCall{}}
	calls := map[int]*ToolCall{}
	var order []int
	lines := make(chan string, 64)
	readErr := make(chan error, 1)
	go func() {
		sc := bufio.NewReader(res.Body)
		for {
			line, err := sc.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				readErr <- err
				close(lines)
				return
			}
		}
	}()
	idle := time.NewTimer(c.IdleTimeout)
	defer idle.Stop()
	for {
		var line string
		var ok bool
		select {
		case line, ok = <-lines:
		case <-idle.C:
			res.Body.Close()
			return nil, &IdleError{fmt.Sprintf("The model sent nothing for %d seconds, so the reply was stopped. Try again, or pick another model.", int((c.IdleTimeout+500*time.Millisecond)/time.Second))}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if !ok {
			if err := <-readErr; err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, err
			}
			break
		}
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(c.IdleTimeout)
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			continue
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Model   *string         `json:"model"`
			Usage   json.RawMessage `json:"usage"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
				Delta        *struct {
					Content          any `json:"content"`
					ReasoningContent any `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function *struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if !jsonobj.IsNullish(chunk.Error) {
			return nil, &StreamedError{errorMessage(chunk.Error)}
		}
		if chunk.Model != nil {
			t.Model = chunk.Model
		}
		if u := bytes.TrimSpace(chunk.Usage); len(u) > 0 && u[0] == '{' {
			var us Usage
			_ = json.Unmarshal(u, &us)
			t.Usage = &us
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta != nil {
			if s, ok := ch.Delta.Content.(string); ok && s != "" {
				t.Content += s
				if cb.OnContent != nil {
					cb.OnContent(s)
				}
			}
			if s, ok := ch.Delta.ReasoningContent.(string); ok && s != "" {
				t.Reasoning += s
				if cb.OnReasoning != nil {
					cb.OnReasoning(s)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				cur := calls[idx]
				if cur == nil {
					i := idx
					typ := tc.Type
					if typ == "" {
						typ = "function"
					}
					cur = &ToolCall{Index: &i, ID: tc.ID, Type: typ}
					calls[idx] = cur
					order = append(order, idx)
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Function != nil {
					cur.Function.Name += tc.Function.Name
					cur.Function.Arguments += tc.Function.Arguments
				}
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			t.FinishReason = ch.FinishReason
		}
	}
	for _, i := range order {
		t.ToolCalls = append(t.ToolCalls, *calls[i])
	}
	return t, nil
}

func errorMessage(raw json.RawMessage) string {
	var o struct {
		Message any `json:"message"`
	}
	t := bytes.TrimSpace(raw)
	if len(t) > 0 && t[0] == '{' {
		if json.Unmarshal(t, &o) == nil && o.Message != nil {
			return jsStringOf(mustRaw(o.Message))
		}
		var c bytes.Buffer
		_ = json.Compact(&c, t)
		return c.String()
	}
	return jsStringOf(t)
}

func mustRaw(v any) json.RawMessage {
	b, _ := jsonobj.Marshal(v)
	return b
}
