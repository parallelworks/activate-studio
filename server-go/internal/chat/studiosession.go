package chat

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// StudioSession starts a pw code session on c, this host's daemon, in
// workspace, carrying this Studio's MCP server. The server acts for owner
// (nil on a Studio without sign-in) under grant, with the tools a pw code
// chat of theirs gets: that is how a standing agent's session reaches the
// library, runs and systems. The token is a chat token and lasts as long
// (TokenTTL); a session that keeps working past that is replaced. Model
// and mode are pw code's; "" leaves the deployment's pw code settings.
// allow adds pw code rules the session uses without asking, after the
// Studio's own reading tools.
func (m *Module) StudioSession(ctx context.Context, c pwcode.Client, owner *auth.User, grant, workspace, model, mode string, allow []string) (string, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return "", err
	}
	if owner != nil {
		r = auth.WithUser(r, owner)
	}
	// Whose session it is, for what it may run on (pwcode.ClaudeCode).
	ctx = r.Context()
	tools := m.chatTools(r, false)
	want := specNames(tools)
	token, err := m.issueChatToken(owner, grant)
	if err != nil {
		return "", err
	}
	allowed := []any{}
	for _, s := range tools {
		if chatReadTools[s.Name] {
			allowed = append(allowed, studioToolPrefix+s.Name)
		}
	}
	for _, x := range allow {
		allowed = append(allowed, x)
	}
	e := m.eff()
	if model == "" {
		model = e.PWCodeModel
	}
	if mode == "" {
		mode = e.PWCodePermissionMode
	}
	prompt := m.pwcodePrompt(tools)
	var tried []string
	// c is this host's daemon: the Studio is reached as a daemon here reaches it.
	for _, url := range m.mcpURLs(false) {
		agents := jsonv.NewObject("studio", jsonv.NewObject(
			"description", "ACTIVATE Studio standing agent",
			"prompt", prompt,
			"mcpServers", []any{jsonv.NewObject(studioMCPName, jsonv.NewObject(
				"type", "http", "url", url,
				"headers", jsonv.NewObject("Authorization", "Bearer "+token)))},
		))
		agentsJSON, err := jsonv.Marshal(agents)
		if err != nil {
			return "", err
		}
		req := jsonv.NewObject("workspace", workspace, "remoteControl", true, "agent", "studio", "agentsJson", string(agentsJSON), "allowedTools", allowed)
		if mode != "" {
			req.Set("permissionMode", mode)
		}
		if model != "" {
			req.Set("model", model)
		}
		s, err := pwcode.CreateSession(ctx, c, req)
		if err != nil {
			return "", err
		}
		id := jsonv.String(jsonv.Path(s, "id"))
		if id == "" {
			return "", errors.New("pw code made no session")
		}
		if ck := ensureStudioMCP(ctx, c, id, want); !ck.OK {
			tried = append(tried, url+" ("+shortMCPError(ck.Detail)+")")
			_, _ = pwcode.ArchiveSession(ctx, c, id)
			continue
		}
		return id, nil
	}
	return "", errors.New("the session could not reach this Studio's tools: " + strings.Join(tried, "; "))
}

// TokenTTL is how long a Studio session's MCP token lasts.
const TokenTTL = chatTokenTTL
