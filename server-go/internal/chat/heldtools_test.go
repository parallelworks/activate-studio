package chat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// heldModule is a module whose pw logs each call and, as the machine's pw
// does where no daemon runs, refuses a bridge that may not start one.
func heldModule(t *testing.T, env map[string]string) (m *Module, calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	pw := filepath.Join(dir, "pw")
	os.WriteFile(pw, []byte(`#!/bin/sh
printf '%s\n' "$*" >> '`+log+`'
case "$*" in
  *"daemon proxy --ensure"*) echo 'Error: started' >&2; exit 1 ;;
  *"daemon proxy"*) echo 'Error: dial daemon socket: no such file (is the daemon running? try --ensure)' >&2; echo 'exit status 1' >&2; exit 1 ;;
  clusters*) echo '[{"name":"cluster-a","type":"slurm","status":"active"}]' ;;
  "workflows run"*) echo "run submitted as $PW_API_KEY" ;;
  *) echo ok ;;
esac
`), 0o755)
	e := map[string]string{"PW_CLI": pw, "PW_GATEWAY_URL": "https://platform.example/api/openai/v1", "HPC_STATUS_WORKFLOW": "monitor"}
	for k, v := range env {
		e[k] = v
	}
	m, _ = pwModule(t, &fakePW{}, e)
	was := agentsites.Current()
	d := pwcode.New(m.pf)
	agentsites.SetCurrent(&agentsites.Sites{P: m.pf, Daemon: d})
	t.Cleanup(func() { d.CloseBridges(); agentsites.SetCurrent(was) })
	return m, func() []string {
		b, _ := os.ReadFile(log)
		os.Remove(log)
		return strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' })
	}
}

// run calls a tool's handler as every caller does, whatever its schema
// says, and gives its text or error.
func runTool(t *testing.T, m *Module, name string, args map[string]any) string {
	t.Helper()
	key := "own-key"
	out, err := goExecutors[name](m, httptest.NewRequest(http.MethodPost, "/", nil), args, ToolCtx{UserKey: &key})
	if err != nil {
		return err.Error()
	}
	return out.Result
}

func TestPwHelpReadsHelpTopicsOnly(t *testing.T) {
	m, calls := heldModule(t, nil)
	for command, want := range map[string]string{
		"":                             "help",
		"workflows run":                "help workflows run",
		"Workflows":                    "help workflows",
		"ssh":                          "help ssh",
		"ssh cluster-a hostname":       "help ssh",
		"ssh -- cluster-a hostname":    "help ssh",
		"SSH cluster-a":                "help ssh",
		"--context x ssh cluster-a":    "help",
		"-o ssh":                       "help",
		"workflows run; hostname":      "help workflows",
		"a b c d e":                    "help a b c d",
		"workflows $(hostname)":        "help workflows",
		"code daemon start":            "help code daemon start",
		"workflows\nssh\ncluster-a ls": "help workflows ssh",
	} {
		runTool(t, m, "pw_help", map[string]any{"command": command})
		if got := calls(); !slices.Equal(got, []string{want}) {
			t.Errorf("%q ran %q, want %q", command, got, want)
		}
	}
}

func TestHpcStatusNeverLaunches(t *testing.T) {
	m, calls := heldModule(t, nil)
	for _, v := range []any{true, "true", 1.0} {
		out := runTool(t, m, "hpc_status", map[string]any{"view": "summary", "launch": v, "inputs": "{}"})
		if got := calls(); len(got) != 0 || !strings.Contains(out, launchMonitorTool) {
			t.Errorf("launch %v: ran %q, said %q", v, got, out)
		}
	}
}

func TestAgentReadsStartNothing(t *testing.T) {
	m, calls := heldModule(t, nil)
	out := runTool(t, m, "agent_sites", map[string]any{"probe": "cluster-a"})
	got := calls()
	if !strings.Contains(out, "cluster-a (slurm)") || !strings.Contains(out, startDaemonTool) || slices.ContainsFunc(got, func(l string) bool { return !strings.HasPrefix(l, "clusters ") }) {
		t.Errorf("agent_sites ran %q, said %q", got, out)
	}
	for name, args := range map[string]map[string]any{
		"agent_sessions": {"site": "cluster-a"},
		"agent_status":   {"site": "cluster-a", "session": "20261111-195119-7f57c8de"},
	} {
		out := runTool(t, m, name, args)
		got := calls()
		if len(got) == 0 || slices.ContainsFunc(got, func(l string) bool { return !strings.HasSuffix(l, "cluster-a pw code daemon proxy") }) {
			t.Errorf("%s ran %q", name, got)
		}
		if out != "pw code is not running on cluster-a, and reading does not start it." {
			t.Errorf("%s said %q", name, out)
		}
	}
	// By its words, the site's sessions are read the same way.
	runTool(t, m, "agent_status", map[string]any{"site": "cluster-a", "session": "the survey"})
	if got := calls(); len(got) == 0 || strings.Contains(strings.Join(got, "\n"), "--ensure") {
		t.Errorf("agent_status by words ran %q", got)
	}
}

// The tools that read are given without asking (a pw code chat, a session
// the Studio starts, a machine it is set up on), so none of them may be a
// tool that asks, and none keeps the arguments that acted.
func TestToolsGivenWithoutAskingOnlyRead(t *testing.T) {
	for n := range chatReadTools {
		if asksFirst(n) {
			t.Errorf("%s asks first", n)
		}
	}
	for _, s := range builtinSpecs {
		for tool, arg := range map[string]string{"hpc_status": `"launch"`, "agent_sites": `"probe"`} {
			if s.Name == tool && strings.Contains(string(s.Parameters), arg) {
				t.Errorf("%s still takes %s", tool, arg)
			}
		}
	}
	for n := range actionAsksFirst {
		if !asksFirst(n) || !chatLeftOut(n) || chatReadTools[n] || chatNoAsk[n] || chatDelegateTools[n] {
			t.Errorf("%s is given without asking", n)
		}
	}
	m, _ := heldModule(t, nil)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, person := range []bool{true, false} {
		for _, n := range specNames(m.chatTools(r, person)) {
			if isActionAsksFirst(n) {
				t.Errorf("a pw code chat (person %v) is given %s", person, n)
			}
		}
	}
	if a := m.toolMeta().AsksFirst; !slices.Contains(a, launchMonitorTool) || !slices.Contains(a, startDaemonTool) {
		t.Errorf("the Studio assistant asks for %q", a)
	}
}

func TestActionsRunOnceApproved(t *testing.T) {
	m, calls := heldModule(t, map[string]string{"PW_API_KEY": ""})
	tools := goTools{m: m}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	key := "own-key"
	for n := range actionAsksFirst {
		if out := runTool(t, m, n, map[string]any{"site": "cluster-a"}); !strings.Contains(out, "Nothing ran") || len(calls()) != 0 {
			t.Errorf("%s called directly: %q", n, out)
		}
	}
	hold := func(id, name, raw string) {
		t.Helper()
		prep, err := tools.Prepare(r, name, raw, key)
		if err != nil || prep.Problem != "" {
			t.Fatalf("prepare %s: %v %+v", name, err, prep)
		}
		if _, err := tools.Hold(r, map[string]any{"conversationId": "c1", "messageId": "m1", "toolCallId": id, "tool": name, "canonical": prep.Canonical, "args": prep.Args}); err != nil {
			t.Fatal(err)
		}
	}
	answer := func(id string, allowed bool, key *string) *Answered {
		t.Helper()
		a, err := tools.Answer(r, map[string]any{"conversationId": "c1", "messageId": "m1", "toolCallId": id, "allowed": allowed}, key)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	hold("1", launchMonitorTool, `{"inputs":{"account":"proj1"}}`)
	if a := answer("1", true, nil); a.OK || !strings.Contains(a.Why, "your own platform key") || len(calls()) != 0 {
		t.Errorf("launched without the person's key: %+v", a)
	}
	a := answer("1", true, &key)
	if got := calls(); !a.OK || a.Failed || !slices.Equal(got, []string{`workflows run -i {"account":"proj1"} monitor`}) || !strings.Contains(a.Result, "submitted as own-key") {
		t.Errorf("launch: %+v ran %q", a, got)
	}
	if a := answer("1", true, &key); a.OK || len(calls()) != 0 {
		t.Errorf("launched twice: %+v", a)
	}
	hold("2", launchMonitorTool, `{}`)
	if a := answer("2", false, &key); !a.OK || len(calls()) != 0 || !strings.Contains(a.Result, "declined") {
		t.Errorf("declined launch: %+v", a)
	}
	hold("3", startDaemonTool, `{"site":"cluster-a"}`)
	if a := answer("3", false, &key); !a.OK || len(calls()) != 0 {
		t.Errorf("declined start: %+v", a)
	}
	hold("4", startDaemonTool, `{"site":" cluster-a "}`)
	answer("4", true, &key)
	if got := calls(); len(got) != 1 || !strings.HasSuffix(got[0], "cluster-a pw code daemon proxy --ensure") {
		t.Errorf("start ran %q", got)
	}
	for name, raw := range map[string]string{launchMonitorTool: `{"inputs":"not json"}`, startDaemonTool: `{}`} {
		if p, _ := tools.Prepare(r, name, raw, key); p.Problem == "" {
			t.Errorf("%s took %s", name, raw)
		}
	}
}
