package chat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/people"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

func v1(t *testing.T, srv *httptest.Server, method, path, bearer, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// TestAgentModeIsRefusedWhereSignInIs: /v1 is outside sign-in and knows
// no person of this Studio, so on a Studio with sign-in agent mode
// (studio-agent and studio-voice, bare or with a model, streamed or not)
// is refused before any model call or tool, with whatever key; it is not
// listed; studio-rag answers as before. Without sign-in agent mode runs
// and is listed as before.
func TestAgentModeIsRefusedWhereSignInIs(t *testing.T) {
	for _, signIn := range []bool{true, false} {
		env := map[string]string{"RAG_ADVERTISE_AGENT_MODEL": "1"}
		if signIn {
			env["AUTH_JWKS_URL"] = "http://127.0.0.1:9/jwks"
			env["STUDIO_FRONT_AUTH"] = "1"
		}
		m, srv, gw, _ := newTestModule(t, env)
		m.Tools = runningTools{m.Tools.(fakeTools), m}
		mark := filepath.Join(t.TempDir(), "ran")
		m.A.Settings.Update(map[string]any{"customTools": []any{map[string]any{"name": "mark", "description": "", "command": "touch " + mark}}})
		m.A.Settings.Update(map[string]any{settings.CustomToolsApprovedKey: settings.CustomToolsDigest(m.A.Settings.Effective().CustomTools)})
		// The key is a person's who has signed in here: agent mode is
		// refused even so, and studio-rag answers them.
		aPlatform(t, map[string]string{"any-gateway-key": "user:bob"}, nil)
		people.Of(m.A).Seen(&auth.User{ID: "user:bob", Username: "bob"})
		for _, model := range []string{"studio-agent", "studio-agent/test-model", "studio-voice", "studio-voice/test-model"} {
			for _, stream := range []bool{false, true} {
				for _, bearer := range []string{"any-gateway-key", ""} {
					if !signIn && bearer == "" {
						continue
					}
					gw.load([]reply{call("mark", `{}`), text("Done.")})
					b, _ := json.Marshal(map[string]any{"model": model, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "mark it"}}})
					code, out := v1(t, srv, "POST", "/v1/chat/completions", bearer, string(b))
					_, statErr := os.Stat(mark)
					ran := statErr == nil
					if signIn {
						if code != http.StatusForbidden || !strings.Contains(out, AgentNeedsPerson) || len(gw.requests) != 0 || ran {
							t.Errorf("%s stream=%v key=%q: %d %s, the model asked %d times, the tool ran=%v", model, stream, bearer, code, out, len(gw.requests), ran)
						}
					} else if code != http.StatusOK || len(gw.requests) == 0 || ran {
						// A Studio of one answers, and runs no custom tool
						// (TestAgentModeNeverRunsCustomTools).
						t.Errorf("no sign-in, %s stream=%v: %d %s, the model asked %d times, the tool ran=%v", model, stream, code, out, len(gw.requests), ran)
					}
					os.Remove(mark)
				}
			}
		}
		if signIn {
			m.rag.mu.Lock()
			logged := len(m.rag.calls) > 0 && m.rag.calls[0].Mode == "agent" && m.rag.calls[0].Status == "error"
			m.rag.mu.Unlock()
			if !logged {
				t.Error("the refusal is not in the call log")
			}
		}
		// studio-rag answers as before.
		gw.load([]reply{text("Grounded.")})
		if code, out := v1(t, srv, "POST", "/v1/chat/completions", "any-gateway-key", `{"model":"studio-rag/test-model","messages":[{"role":"user","content":"hi"}]}`); code != http.StatusOK || len(gw.requests) != 1 {
			t.Errorf("sign-in=%v, studio-rag: %d %s", signIn, code, out)
		}
		for _, q := range []string{"", "?all=1"} {
			_, out := v1(t, srv, "GET", "/v1/models"+q, "any-gateway-key", "")
			if listed := strings.Contains(out, `"studio-agent"`) || strings.Contains(out, `"studio-voice"`); listed == signIn {
				t.Errorf("sign-in=%v: /v1/models%s lists %s", signIn, q, out)
			}
		}
	}
}

// TestAgentModeRunsOnlyWhatItOffers: no one is present to approve a
// change, so a call to a tool agent mode does not offer (one that asks
// first, like deleting files, or one that is not there) runs nothing.
func TestAgentModeRunsOnlyWhatItOffers(t *testing.T) {
	m, srv, gw, _ := newTestModule(t, nil)
	m.Tools = runningTools{m.Tools.(fakeTools), m}
	kb := m.A.Cfg.KBRoot
	os.MkdirAll(kb, 0o755)
	keep := filepath.Join(kb, "plan.md")
	os.WriteFile(keep, []byte("the plan"), 0o644)
	gw.load([]reply{call("delete_kb_files", `{"paths":["plan.md"]}`), text("Done.")})
	code, out := v1(t, srv, "POST", "/v1/chat/completions", "any-gateway-key", `{"model":"studio-agent/test-model","messages":[{"role":"user","content":"delete the plan"}]}`)
	if _, err := os.Stat(keep); err != nil || code != http.StatusOK {
		t.Fatalf("the file is gone (%v): %d %s", err, code, out)
	}
	if len(gw.requests) != 2 || !strings.Contains(string(gw.requests[1]), "delete_kb_files is not available here, so nothing ran") {
		t.Errorf("the model was told %s", gw.requests)
	}
}

// TestAgentModeNeverRunsCustomTools: a custom tool runs a command as the
// Studio's account, and /v1 knows a caller only by a key the model gateway
// takes. So agent mode never offers or runs one, with sign-in or without,
// for anyone, an administrator included. It keeps its other tools.
func TestAgentModeNeverRunsCustomTools(t *testing.T) {
	for _, signIn := range []bool{false, true} {
		env := map[string]string{"STUDIO_ADMINS": "cy"}
		if signIn {
			env["AUTH_JWKS_URL"] = "http://127.0.0.1:9/jwks"
			env["STUDIO_FRONT_AUTH"] = "1"
		}
		m, _, gw, _ := newTestModule(t, env)
		m.Tools = runningTools{m.Tools.(fakeTools), m}
		mark := filepath.Join(t.TempDir(), "ran")
		m.A.Settings.Update(map[string]any{"customTools": []any{map[string]any{"name": "mark", "description": "", "command": "touch " + mark}}})
		m.A.Settings.Update(map[string]any{settings.CustomToolsApprovedKey: settings.CustomToolsDigest(m.A.Settings.Effective().CustomTools)})
		os.MkdirAll(m.A.Cfg.KBRoot, 0o755)
		os.WriteFile(filepath.Join(m.A.Cfg.KBRoot, "plan.md"), []byte("PLAN-4242 the plan"), 0o644)
		for _, who := range []string{"", "bob", "cy"} {
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			if who != "" {
				r = auth.WithUser(r, &auth.User{ID: who, Username: who})
			}
			gw.load([]reply{call("mark", `{}`), call("read_kb_file", `{"path":"plan.md"}`), text("Done.")})
			msg, _ := json.Marshal(map[string]any{"role": "user", "content": "mark it, then read the plan"})
			if _, _, err := m.agentic(r.Context(), r, "test-model", []json.RawMessage{msg}, &callerAuth{Key: "any-gateway-key"}, func(string) {}, false); err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(mark)
			if len(gw.requests) != 3 {
				t.Fatalf("sign-in=%v %q: the model was asked %d times", signIn, who, len(gw.requests))
			}
			first, last := string(gw.requests[0]), string(gw.requests[2])
			if statErr == nil || strings.Contains(first, `"name":"mark"`) || !strings.Contains(string(gw.requests[1]), "mark is not available here, so nothing ran") {
				t.Errorf("sign-in=%v %q: the custom tool ran=%v, offered=%v", signIn, who, statErr == nil, strings.Contains(first, `"name":"mark"`))
			}
			if !strings.Contains(first, `"name":"read_kb_file"`) || !strings.Contains(last, "PLAN-4242") {
				t.Errorf("sign-in=%v %q: read_kb_file was not offered or did not run: %s", signIn, who, last)
			}
			os.Remove(mark)
		}
	}
}
