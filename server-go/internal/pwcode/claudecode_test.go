package pwcode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// callsClient answers each route from answers and records what it was sent.
type callsClient struct {
	answers map[string]any
	calls   []string
}

func (c *callsClient) Site() string { return "local" }
func (c *callsClient) Call(_ context.Context, method, route string, _ any, _ time.Duration) (any, error) {
	c.calls = append(c.calls, method+" "+route)
	if v, ok := c.answers[method+" "+route]; ok {
		return v, nil
	}
	return jsonv.NewObject(), nil
}

// withPolicy sets ClaudeCode for one test.
func withPolicy(t *testing.T, mode string, personal bool) (*ClaudeCodePolicy, string) {
	t.Helper()
	dir := t.TempDir()
	p := NewClaudeCodePolicy(dir, func() string { return mode }, func() bool { return personal })
	was := ClaudeCode
	ClaudeCode = p
	t.Cleanup(func() { ClaudeCode = was })
	return p, dir
}

func as(id string) context.Context { return auth.ContextWithUser(context.Background(), &auth.User{ID: id}) }

func TestClaudeCodeIsOnForAStudioOfOneAndOffWhereSharedUnlessInUse(t *testing.T) {
	for _, c := range []struct {
		mode     string
		personal bool
		want     string
	}{
		{"auto", true, ""},
		{"auto", false, ClaudeCodeNotHere},
		{"on", false, ""},
		{"off", true, ClaudeCodeTurnedOff},
	} {
		p, _ := withPolicy(t, c.mode, c.personal)
		if got := p.Off("alex"); got != c.want {
			t.Errorf("%s, personal %v: %q, want %q", c.mode, c.personal, got, c.want)
		}
	}
	if (*ClaudeCodePolicy)(nil).Off("alex") != "" {
		t.Error("with no rule set, Claude Code accounts are allowed")
	}
}

func TestSomeoneAlreadyUsingClaudeCodeKeepsIt(t *testing.T) {
	p, dir := withPolicy(t, "auto", false)
	none := &callsClient{answers: map[string]any{"GET /v1/claude-code/accounts": []any{}}}
	if LookForAccounts(context.Background(), none, "sam") || p.Off("sam") == "" {
		t.Fatal("someone with no Claude Code accounts is not recorded")
	}
	if LookForAccounts(context.Background(), none, "sam") || len(none.calls) != 1 {
		t.Fatalf("their pw code is asked again within ten minutes: %v", none.calls)
	}
	some := &callsClient{answers: map[string]any{"GET /v1/claude-code/accounts": []any{map[string]any{"name": "work"}}}}
	if !LookForAccounts(context.Background(), some, "alex") || p.Off("alex") != "" {
		t.Fatal("someone whose pw code has an account keeps Claude Code")
	}
	if p.Off("sam") == "" {
		t.Error("one person's accounts turn nothing on for another")
	}
	// Kept beside the index, so a restart keeps it.
	again := NewClaudeCodePolicy(dir, func() string { return "auto" }, func() bool { return false })
	if again.Off("alex") != "" {
		t.Error("the record does not outlive the process")
	}
	if fi, err := os.Stat(filepath.Join(dir, "claude-code-in-use.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record file: %v %v", fi, err)
	}
	// An administrator's off holds for everyone, the recorded too.
	off := NewClaudeCodePolicy(dir, func() string { return "off" }, func() bool { return false })
	if off.Off("alex") != ClaudeCodeTurnedOff {
		t.Error("off turns them off for someone using them too")
	}
	if off.ShouldLook("nobody") {
		t.Error("with the setting on or off, pw code is not asked")
	}
}

func TestASessionOnClaudeCodeIsRefusedWhereItIsOff(t *testing.T) {
	withPolicy(t, "auto", false)
	c := &callsClient{}
	for _, req := range []*jsonv.Object{
		jsonv.NewObject("workspace", "/w", "model", "claude-code/sonnet"),
		jsonv.NewObject("workspace", "/w", "claudeAccount", "work"),
	} {
		_, err := CreateSession(as("sam"), c, req)
		var ae *apierr.Error
		if !errors.As(err, &ae) || ae.Status != 403 || ae.Message != ClaudeCodeNotHere {
			t.Fatalf("%v: %v", req, err)
		}
	}
	if len(c.calls) != 0 {
		t.Fatalf("pw code was asked: %v", c.calls)
	}
	// Another model starts as before.
	if _, err := CreateSession(as("sam"), c, jsonv.NewObject("workspace", "/w", "model", "platform/model-a")); err != nil || len(c.calls) != 1 {
		t.Fatalf("%v %v", err, c.calls)
	}
	// A session pw code starts on a Claude model by its own default is
	// archived, and the start refused.
	d := &callsClient{answers: map[string]any{"POST /v1/sessions": jsonv.NewObject("id", "s1", "model", "claude-code/haiku")}}
	_, err := CreateSession(as("sam"), d, jsonv.NewObject("workspace", "/w"))
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Status != 403 {
		t.Fatalf("default model: %v", err)
	}
	if len(d.calls) != 2 || d.calls[1] != "POST /v1/sessions/s1/archive" {
		t.Fatalf("calls: %v", d.calls)
	}
	// Where they are on, both start.
	withPolicy(t, "on", false)
	if _, err := CreateSession(as("sam"), c, jsonv.NewObject("workspace", "/w", "model", "claude-code/sonnet")); err != nil {
		t.Fatal(err)
	}
}
