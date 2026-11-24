package chat

import (
	"net/http"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// directMachineOf is the Studio's own machine in /api/code/sessions.
func directMachineOf(t *testing.T, base string) map[string]any {
	t.Helper()
	for _, m := range get(t, base+"/api/code/sessions")["machines"].([]any) {
		if mm := m.(map[string]any); mm["id"] == "studio" {
			return mm
		}
	}
	t.Fatal("no studio machine")
	return nil
}

func claudeDaemon(accounts string) *fakePW {
	return &fakePW{
		status: map[string]any{"machineId": "d1", "hostname": "gpu-box.example", "claudeCodeAllowed": true, "claudeInstalled": true},
		routes: map[string]http.HandlerFunc{"GET /v1/claude-code/accounts": serveAs("application/json", accounts)},
	}
}

func TestClaudeCodeAccountsTurnedOffAreNeitherOfferedNorReached(t *testing.T) {
	f := claudeDaemon(`[{"name":"work"}]`)
	_, base, _ := continueModule(t, f, "d1", "CLAUDE_CODE_ACCOUNTS", "off")
	t.Cleanup(func() { pwcode.ClaudeCode = nil })

	here := get(t, base+"/api/chat/pwcode/here")
	if here["available"] != true || here["claudeCode"] != false || here["claudeCodeReason"] != pwcode.ClaudeCodeTurnedOff {
		t.Fatalf("here: %v", here)
	}
	// The machine says it does not allow Claude Code, so the package offers
	// no Claude models, account chip or panel for it.
	if d := directMachineOf(t, base); d["claudeCodeAllowed"] != false || d["claudeInstalled"] != true {
		t.Errorf("direct machine: %v", d)
	}
	// The account routes are refused before pw code is asked.
	f.mu.Lock()
	f.asked = nil
	f.mu.Unlock()
	for _, r := range []string{"GET /accounts", "GET /models", "POST /logins", "DELETE /accounts/work"} {
		method, suffix, _ := strings.Cut(r, " ")
		if code, out, _ := send(t, method, base+"/api/code/machines/studio/claude-code"+suffix, `{}`); code != 403 || !strings.Contains(out["detail"].(string)+out["title"].(string), "turned off") {
			t.Errorf("%s: %d %v", r, code, out)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.asked {
		if strings.Contains(a, "claude-code") {
			t.Errorf("pw code was asked: %s", a)
		}
	}
}

func TestOnADeployedStudioClaudeCodeIsOffUnlessThePersonAlreadyUsesIt(t *testing.T) {
	for _, c := range []struct {
		name, accounts string
		on             bool
	}{{"none", `[]`, false}, {"in use", `[{"name":"work"}]`, true}} {
		t.Run(c.name, func(t *testing.T) {
			f := claudeDaemon(c.accounts)
			_, base, _ := continueModule(t, f, "d1", "PW_PLATFORM_HOST", "platform.example.com")
			t.Cleanup(func() { pwcode.ClaudeCode = nil })
			here := get(t, base+"/api/chat/pwcode/here")
			if here["claudeCode"] != c.on {
				t.Fatalf("here: %v", here)
			}
			if d := directMachineOf(t, base); d["claudeCodeAllowed"] != c.on {
				t.Errorf("direct machine: %v", d)
			}
			code, _, _ := send(t, http.MethodGet, base+"/api/code/machines/studio/claude-code/accounts", "")
			if (code == 200) != c.on {
				t.Errorf("accounts: %d", code)
			}
		})
	}
}
