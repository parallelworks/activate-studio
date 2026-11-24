package pwcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// ClaudeCode is the deployment's rule for Claude Code accounts, which pw
// code uses to run on Anthropic's models (models named claude-code/...).
// nil, as in tests that do not set it, allows them.
var ClaudeCode *ClaudeCodePolicy

// ClaudeCodePolicy decides, for each person, whether this Studio offers
// Claude Code accounts. Settings' claudeCodeAccounts (or
// CLAUDE_CODE_ACCOUNTS) is on, off, or auto. Auto is on for a Studio of
// one (no sign-in, not deployed by the platform) and off elsewhere, since a
// shared or hosted Studio serves people who may not use Anthropic; a person
// whose pw code already had Claude Code accounts when the Studio first
// looked keeps them, so nobody loses access without being told. Those
// people are kept in claude-code-in-use.json beside the index.
type ClaudeCodePolicy struct {
	// Mode is the effective claudeCodeAccounts.
	Mode func() string
	// Personal says the Studio is one person's own (settings.PersonalInstall).
	Personal func() bool
	file     string
	mu       sync.Mutex
	read     bool
	users    map[string]string
	// checked is when each person's pw code was last asked, so it is not
	// asked on every request.
	checked map[string]time.Time
}

// NewClaudeCodePolicy is the rule for the Studio whose index is under
// indexBase.
func NewClaudeCodePolicy(indexBase string, mode func() string, personal func() bool) *ClaudeCodePolicy {
	return &ClaudeCodePolicy{Mode: mode, Personal: personal, file: filepath.Join(indexBase, "claude-code-in-use.json"),
		users: map[string]string{}, checked: map[string]time.Time{}}
}

// Refusals, as the person reads them.
const (
	ClaudeCodeTurnedOff = "Claude Code accounts are turned off on this Studio by its administrator."
	ClaudeCodeNotHere   = "Claude Code accounts are off on this Studio, since several people share it or ACTIVATE deployed it. Its administrator can turn them on under Settings, External access."
)

func (p *ClaudeCodePolicy) load() {
	if p.read {
		return
	}
	p.read = true
	if b, err := os.ReadFile(p.file); err == nil {
		_ = json.Unmarshal(b, &p.users)
	}
	if p.users == nil {
		p.users = map[string]string{}
	}
}

// Off is why Claude Code accounts are off for the person (their user id,
// "" on a Studio without sign-in), or "" when they are on.
func (p *ClaudeCodePolicy) Off(viewer string) string {
	if p == nil {
		return ""
	}
	switch p.Mode() {
	case "on":
		return ""
	case "off":
		return ClaudeCodeTurnedOff
	}
	if p.Personal() {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	if _, ok := p.users[viewer]; ok {
		return ""
	}
	return ClaudeCodeNotHere
}

// ShouldLook says whether to ask the person's pw code if it has Claude
// Code accounts: only where auto would turn them off for someone not yet
// known to use them, and at most every ten minutes.
func (p *ClaudeCodePolicy) ShouldLook(viewer string) bool {
	if p == nil || p.Mode() != "auto" || p.Personal() {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	if _, ok := p.users[viewer]; ok {
		return false
	}
	if at, ok := p.checked[viewer]; ok && time.Since(at) < 10*time.Minute {
		return false
	}
	p.checked[viewer] = time.Now()
	return true
}

// Record keeps that the person already uses Claude Code accounts here.
func (p *ClaudeCodePolicy) Record(viewer string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	if _, ok := p.users[viewer]; ok {
		return nil
	}
	p.users[viewer] = time.Now().UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(p.users, "", " ")
	if err != nil {
		return err
	}
	tmp := p.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.file)
}

// LookForAccounts asks the person's pw code (c) for its Claude Code
// accounts when the policy wants to know, and records them as in use when
// it has any. It reports whether it recorded the person.
func LookForAccounts(ctx context.Context, c Client, viewer string) bool {
	p := ClaudeCode
	if !p.ShouldLook(viewer) {
		return false
	}
	v, err := c.Call(ctx, "GET", "/v1/claude-code/accounts", nil, 0)
	if err != nil {
		return false
	}
	if list, ok := v.([]any); !ok || len(list) == 0 {
		return false
	}
	return p.Record(viewer) == nil
}

// Viewer is the person ClaudeCode's rule is asked about for ctx: the
// signed-in user, or "" on a Studio without sign-in.
func Viewer(ctx context.Context) string {
	if u := auth.UserFromContext(ctx); u != nil {
		return u.ID
	}
	return ""
}

// ClaudeCodeOff is why Claude Code accounts are off for the person ctx is
// for, or "".
func ClaudeCodeOff(ctx context.Context) string { return ClaudeCode.Off(Viewer(ctx)) }

// IsClaudeModel says the model runs on a Claude Code account, as
// @parallelworks/code's isClaudeModel does.
func IsClaudeModel(model string) bool { return strings.HasPrefix(model, "claude-code/") }

// ClaudeCodeRefused is the refusal (403) of a session start or a settings
// change that would run on a Claude Code account where they are off, or nil.
func ClaudeCodeRefused(ctx context.Context, body any) error {
	why := ClaudeCodeOff(ctx)
	if why == "" {
		return nil
	}
	if IsClaudeModel(jsonv.String(jsonv.Path(body, "model"))) || jsonv.String(jsonv.Path(body, "claudeAccount")) != "" {
		return apierr.New(403, "%s", why)
	}
	return nil
}
