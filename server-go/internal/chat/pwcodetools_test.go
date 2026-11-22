package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
	"github.com/parallelworks/activate-studio/server-go/internal/pasttasks"
)

func TestPWCodeHasTheStudioToolsBeforeItAnswers(t *testing.T) {
	// A studio server that failed is reconnected, and the session kept.
	f := &fakePW{script: helloTurn, mcp: "fail", fix: true}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "What is the budget?"}}})
	if !strings.Contains(sse, "event: done") || f.reconnects != 1 || f.archived != 0 || len(f.messages) != 1 {
		t.Fatalf("reconnect: %d reconnects, %d archived, sent %q\n%s", f.reconnects, f.archived, f.messages, sse)
	}
	if got := m.Conv.Get(c.ID).PWCode; got == nil || got.MCPURL != "http://127.0.0.1:"+strconv.Itoa(m.A.Cfg.Port)+"/api/mcp" {
		t.Errorf("link: %+v", got)
	}

	// A kept session whose server lists other tools (the Studio was
	// updated) is reconnected before the next message.
	f.mu.Lock()
	f.mcp = "stale"
	f.mu.Unlock()
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u2", "labelScope": []any{"finance"},
		"messages": []any{map[string]any{"role": "user", "content": "And last year?"}}})
	if f.reconnects != 2 || len(f.created) != 1 || len(f.messages) != 2 {
		t.Fatalf("stale: %d reconnects, %d sessions, sent %q", f.reconnects, len(f.created), f.messages)
	}
	// The session hears of a scope it has not seen, and its tool calls run
	// in it.
	if !strings.HasPrefix(f.messages[1], "Scope from the Studio's chat bar: The person has scoped this conversation to material labeled: finance.") {
		t.Errorf("scope note: %q", f.messages[1])
	}
	if got := m.Conv.Get(c.ID).PWCode; got == nil || len(got.LabelScope) != 1 || got.LabelScope[0] != "finance" {
		t.Errorf("scope: %+v", got)
	}
}

func TestStudioAssistantAnswersWhenPWCodeCannotReachTheTools(t *testing.T) {
	f := &fakePW{script: helloTurn, mcp: "fail"}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1", "model": "m",
		"messages": []any{map[string]any{"role": "user", "content": "What is the budget?"}}})
	short := "pw code could not reach this Studio's tools at http://127.0.0.1:" + strconv.Itoa(m.A.Cfg.Port) + "/api/mcp (connection refused), so the Studio assistant answered this message"
	if !strings.Contains(sse, "event: pwcode_fallback") || !strings.Contains(sse, short) {
		t.Errorf("no fallback:\n%s", sse)
	}
	if len(f.messages) != 0 || f.reconnects != 1 || f.archived != 1 {
		t.Errorf("pw code: sent %q, %d reconnects, %d archived", f.messages, f.reconnects, f.archived)
	}
	if got := m.Conv.Get(c.ID); got.PWCode != nil || len(got.Messages) == 0 || got.Messages[0].ID != "u1" {
		t.Errorf("conversation: %+v", got)
	}
	// For a while auto answers with the Studio assistant, and says why.
	out := get(t, base+"/api/chat/engine")
	if down, _ := out["toolsDown"].(string); out["default"] != "studio" || out["pwcode"] != true || !strings.Contains(down, "connection refused") {
		t.Errorf("engine: %v", out)
	}
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u2", "model": "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if len(f.created) != 1 {
		t.Errorf("auto tried pw code again: %d sessions", len(f.created))
	}
}

func TestChatTokenGetsTheStudioTools(t *testing.T) {
	m, base := pwModule(t, &fakePW{}, nil)
	c := m.Conv.Create(nil, "")
	tok, err := m.issueChatToken(nil, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	rpc := func(body string) map[string]any {
		req, _ := http.NewRequest(http.MethodPost, base+"/api/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return out
	}
	list := rpc(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result, _ := list["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	read := map[string]bool{}
	for _, x := range tools {
		o := x.(map[string]any)
		read[o["name"].(string)] = o["annotations"].(map[string]any)["readOnlyHint"].(bool)
	}
	// A person's chat delegates as the Studio assistant does, and follows
	// what it delegated.
	for name, ro := range map[string]bool{"search_kb": true, "list_libraries": true, "query_corpus": true, "show_in_viewer": true, "write_kb_file": false, "edit_kb_file": false,
		"delegate": false, "agent_status": true, "agent_sessions": true} {
		if got, ok := read[name]; !ok || got != ro {
			t.Errorf("%s: listed %v, read only %v", name, ok, got)
		}
	}
	for _, name := range []string{"delete_kb_files", "transfer_files", "start_agent", "agent_steer", "agent_approve", "agent_stop", "more_tools", "start_cluster"} {
		if _, ok := read[name]; ok {
			t.Errorf("%s is listed", name)
		}
	}

	// A call runs in the conversation's scope, and a kind of tool the
	// person switched off does not run.
	on := []string{"workflows"}
	m.Conv.SetPWCode(c.ID, &conversations.PWCodeLink{Site: agentsites.Local, Session: "s1", Library: []string{"all"}, Tools: &on})
	tc, link := m.chatToolCtx(httptest.NewRequest(http.MethodPost, "/api/mcp", nil), c.ID)
	if link == nil || len(tc.Library) != 1 || tc.Library[0] != "all" || deref(tc.ConversationID) != c.ID {
		t.Errorf("tool context: %+v", tc)
	}
	res := rpc(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_kb","arguments":{"query":"budget"}}}`)
	r, _ := res["result"].(map[string]any)
	if r == nil || r["isError"] != true || !strings.Contains(fmt.Sprint(r["content"]), "switched off") {
		t.Errorf("switched off: %v", res)
	}
	if res := rpc(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delete_kb_files","arguments":{}}}`); res["error"] == nil {
		t.Errorf("delete ran: %v", res)
	}
}

func TestDelegationInAPersonsChatOnly(t *testing.T) {
	m, _ := pwModule(t, &fakePW{}, nil)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	has := func(specs []ToolSpec, n string) bool { return slices.Contains(specNames(specs), n) }
	if !grantDelegates("conv-1") || grantDelegates("fleet:pump") {
		t.Error("grantDelegates")
	}
	chat, agent := m.chatTools(r, true), m.chatTools(r, false)
	for _, n := range []string{"delegate", "agent_status", "agent_sessions", "task_status"} {
		if !has(chat, n) {
			t.Errorf("a person's chat lacks %s", n)
		}
		if has(agent, n) {
			t.Errorf("a standing agent's session has %s", n)
		}
	}
	// Delegation switched off: the chat does not get delegate.
	t.Setenv("DELEGATION_ENABLED", "0")
	if has(m.chatTools(r, true), "delegate") || has(m.chatTools(r, true), "task_status") {
		t.Error("delegate offered with delegation off")
	}
}

func TestMCPURLsReachThisStudio(t *testing.T) {
	m, _ := pwModule(t, &fakePW{}, nil)
	port := strconv.Itoa(m.A.Cfg.Port)
	host, _ := os.Hostname()
	for _, c := range []struct {
		host   string
		remote bool
		want   []string
	}{
		{"0.0.0.0", false, []string{"http://127.0.0.1:" + port + "/api/mcp"}},
		{"0.0.0.0", true, []string{"http://127.0.0.1:" + port + "/api/mcp", "http://" + host + ":" + port + "/api/mcp"}},
		{"10.1.2.3", true, []string{"http://10.1.2.3:" + port + "/api/mcp"}},
		{"::1", false, []string{"http://[::1]:" + port + "/api/mcp"}},
	} {
		m.A.Cfg.Host = c.host
		if got := m.mcpURLs(c.remote); strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s remote %v: %v", c.host, c.remote, got)
		}
	}
	t.Setenv("STUDIO_MCP_URL", "https://studio.example/api/mcp")
	if got := m.mcpURLs(true); len(got) != 1 || got[0] != "https://studio.example/api/mcp" {
		t.Errorf("override: %v", got)
	}
}

func TestAStudioWithoutACTIVATEAnswersWithTheStudioAssistant(t *testing.T) {
	// A Studio run on its own, with no platform connected: the chat is the
	// Studio assistant's, pw code is not offered, and its daemon is never
	// asked.
	f := &fakePW{script: helloTurn, mcp: "ok"}
	m, base := pwModule(t, f, nil)
	m.A.Providers.PlatformAvailable = func(*http.Request, string) bool { return false }
	out := get(t, base+"/api/chat/engine")
	if out["default"] != "studio" || out["pwcode"] != false || !strings.Contains(fmt.Sprint(out["reason"]), "pw code needs ACTIVATE") {
		t.Errorf("engine: %v", out)
	}
	c := m.Conv.Create(nil, "")
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1", "model": "m",
		"messages": []any{map[string]any{"role": "user", "content": "What is the budget?"}}})
	if strings.Contains(sse, "event: pwcode") || f.calls != 0 || m.Conv.Get(c.ID).PWCode != nil {
		t.Errorf("pw code was asked: %d daemon requests\n%s", f.calls, sse)
	}
}

// On a Studio with sign-in, task_status reads the earlier tasks for the
// administrators only, as /api/tasks does.
func TestTaskStatusIsTheAdministrators(t *testing.T) {
	m, _ := pwModule(t, &fakePW{}, nil)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "t-1"), 0o700)
	os.WriteFile(filepath.Join(dir, "t-1", "state.json"), []byte(`{"id":"t-1","objective":"Quote her notes","state":"completed","agents":[]}`), 0o600)
	defer func(d *pasttasks.Archive) { pasttasks.Default = d }(pasttasks.Default)
	pasttasks.Default = &pasttasks.Archive{Dir: dir}
	m.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	defer func(g func(string) string) { mainlib.Getenv = g }(mainlib.Getenv)
	mainlib.Getenv = func(k string) string {
		if k == "STUDIO_ADMINS" {
			return "cy"
		}
		return ""
	}
	run := func(u *auth.User, args map[string]any) string {
		r := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/mcp", nil), u)
		out, err := m.toolTaskStatus(r, args, ToolCtx{})
		if err != nil {
			t.Fatal(err)
		}
		return out.Result
	}
	bob, cy := &auth.User{ID: "bob"}, &auth.User{ID: "cy", Username: "cy"}
	for _, args := range []map[string]any{{}, {"id": "t-1"}} {
		if got := run(bob, args); strings.Contains(got, "Quote her notes") || got != pasttasks.AdminsOnly {
			t.Errorf("bob %v: %s", args, got)
		}
		if got := run(cy, args); !strings.Contains(got, "Quote her notes") {
			t.Errorf("cy %v: %s", args, got)
		}
	}
}
