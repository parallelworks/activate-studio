package chat

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/sessions"
	"github.com/parallelworks/activate-studio/server-go/internal/tasks"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows"
)

// The workflow, cluster, session, agent and task tools against a fake pw
// CLI and a fake platform API, run by both servers. The CLI answers from
// fixtures keyed by its arguments (the longest leading run of them that
// has one) and logs each call, and each scenario also compares the calls
// the two servers made.

const fakePwCLI = `#!/bin/sh
d="$(dirname "$0")"
printf '%s\n' "$*" >> "$d/calls.log"
n=$#
while [ $n -gt 0 ]; do
  k=$(i=0; for a in "$@"; do i=$((i+1)); [ $i -le $n ] && printf '%s\n' "$a"; done | md5sum | cut -c1-32)
  if [ -f "$d/$k.out" ]; then
    cat "$d/$k.out"
    [ -f "$d/$k.err" ] && cat "$d/$k.err" >&2
    exit $(cat "$d/$k.code" 2>/dev/null || echo 0)
  fi
  n=$((n-1))
done
echo "pw: no fixture for: $*" >&2
exit 1
`

type cliFixture struct {
	args   []string
	out    string
	stderr string
	code   string
}

func cliKey(args []string) string {
	sum := md5.Sum([]byte(strings.Join(args, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}

const helloRecord = `{"name":"hello","displayName":"Hello","description":"Says hello","yaml":{"jobs":{"main":{"steps":[{"name":"greet","run":"echo hi"}]}},` +
	`"on":{"execute":{"inputs":{"resource":{"type":"compute-clusters","label":"System"},"slurm":{"type":"group","items":{"account":{"type":"slurm-accounts"},"qos":{"type":"slurm-qos"},"partition":{"type":"slurm-partitions"}}},` +
	`"opt":{"type":"group","items":{"batch_size":{"type":"number","default":2}}},"greeting":{"type":"string","default":"world"}}}}},` +
	`"configurations":[{"name":"default-vega","inputs":{"resource":"pw://alice/vega","slurm":{"account":"proj1"}}},{"id":"small"}]}`

const trainRecord = `{"name":"train","displayName":"Train","yaml":{"jobs":{"main":{"steps":[{"run":"python train.py"}]}},` +
	`"on":{"execute":{"inputs":{"cluster":{"type":"group","items":{"resource":{"type":"compute-clusters"},"slurm":{"type":"group","items":{"account":{"type":"slurm-accounts"}}}}}}}}},` +
	`"configurations":[{"name":"a","inputs":{"cluster":{"slurm":{"account":"proj1"}}}},{"name":"b","inputs":{"cluster":{"slurm":{"account":"proj2"}}}}]}`

func platformFixtures() []cliFixture {
	return []cliFixture{
		{args: []string{"workflows", "ls", "-o", "json"}, out: `[{"name":"hello","displayName":"Hello","description":"Says hello"},{"name":"train","displayName":"Train"}]`},
		{args: []string{"workflows", "runs", "list", "-o", "json"}, out: "A new version of the PW CLI is out\n" + `[{"slug":"hello-00002","id":"r2","workflowName":"hello","status":"running","createdAt":"2026-11-02T10:00:00Z"},` +
			`{"slug":"hello-00001","id":"r1","workflowName":"hello","status":"completed","createdAt":"2026-11-01T10:00:00Z"},{"slug":"train-00001","workflowName":"train","status":"completed","createdAt":"2026-10-01T10:00:00Z"}]`},
		{args: []string{"cluster", "ls", "-o", "json"}, out: `[{"name":"vega","status":"active","type":"slurm","owner":"alice"},{"name":"lyra","status":"off","type":"pbs"}]`},
		{args: []string{"environments", "ls", "-o", "json"}, out: `[{"clusterName":"vega","clusterUser":"alice","partitionName":"cpu","partitionState":"up","availNodes":3,"totalNodes":8,"maxTime":"1-00:00:00","accounts":["proj1"]},` +
			`{"clusterName":"vega","clusterUser":"alice","partitionName":"gpu","partitionState":"up","availNodes":5,"totalNodes":6,"maxTime":"2-00:00:00"},{"clusterName":"lyra","partitionName":"debug","partitionState":"down","availNodes":0,"totalNodes":2}]`},
		{args: []string{"ssh", "vega", "squeue -u alice"}, out: "JOBID NAME\n 101 train\n"},
		{args: []string{"workflows", "runs", "view", "hello-00001", "-o", "json"}, out: `{"slug":"hello-00001","workflowName":"hello","status":"completed","inputs":{"resource":"pw://alice/vega"},"executedJobs":{"main":{"status":"completed","ssh":{"remoteHost":"vega"},"steps":[{"name":"greet","status":"completed"}]},"later":{}}}`},
		{args: []string{"workflows", "runs", "view", "hello-00001"}, out: "Run hello-00001 completed\n"},
		{args: []string{"workflows", "runs", "errors", "hello-00001"}, out: "no errors\n"},
		{args: []string{"workflows", "runs", "logs", "hello-00001"}, out: "line 1\nline 2\nhi\n"},
		{args: []string{"workflows", "runs", "view", "r1", "-o", "json"}, out: "", stderr: "Error: run not found\n", code: "1"},
		{args: []string{"workflows", "run", "--dry-run"}, out: `{"run":{"slug":"hello-dry"},"valid":true}`},
		{args: []string{"workflows", "run", "-i"}, out: `{"run":{"slug":"hello-00003","status":"queued"}}`},
		{args: []string{"workflows", "run", "--saved-inputs"}, out: `{"run":{"slug":"hello-00004"}}`},
		{args: []string{"workflows", "run", "train"}, out: "", stderr: "Error: validation failed: missing required field: cluster.slurm.account\n", code: "1"},
		{args: []string{"workflows", "run", "--dry-run", "-i", `{"cluster":{"resource":"orion"}}`, "train", "-o", "json"}, out: "", stderr: "Error: validation failed: missing required field: cluster.slurm.account\n", code: "1"},
		{args: []string{"clusters", "ls", "--status", "active", "-o", "json"}, out: `[{"name":"vega","type":"slurm","status":"active","namespace":"alice"},{"name":"lyra","type":"pbs","status":"active","namespace":"alice"}]`},
		{args: []string{"workflows", "--help"}, out: "Manage workflows.\n\nUsage: pw workflows [command]\n"},
		{args: []string{"workflows", "run", "--help"}, out: "Run a workflow.\n"},
	}
}

func platformAPI() map[string]string {
	return map[string]string{
		"/api/workflows/hello":           helloRecord,
		"/api/workflows/train":           trainRecord,
		"/api/workflow-runs/hello-00001": `{"slug":"hello-00001","inputs":{"resource":"pw://alice/vega","slurm":{"account":"proj1","qos":"normal","partition":"gpu"}}}`,
		"/api/workflow-runs/train-00001": `{"slug":"train-00001","inputs":{"cluster":{"resource":"pw://alice/lyra","slurm":{"account":"other"}}}}`,
		"/api/storage":                   `[{"name":"bucket1","category":"buckets","type":"aws-bucket","csp":"aws","region":"us-east-1","status":"ready","secret":"x"},{"name":"home","category":"nfs","type":"nfs","sizeGb":100}]`,
		"/workflow.schema.json":          `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["jobs"],"properties":{"jobs":{"type":"object","additionalProperties":{"type":"object","required":["steps"]}},"on":{"type":"object"}}}`,
		"/api/sessions": `[{"name":"hello-00001-desktop","user":"alice","type":"desktop","status":"running","workflowRun":{"slug":"hello-00001","name":"hello"}},` +
			`{"name":"viz-board","user":"alice","type":"tunnel","status":"stopped"},{"name":"study-hello-00002","user":"bob","status":"running"}]`,
	}
}

func startPlatformGolden(t *testing.T) (*goldenEnv, string) {
	// Node starts with none of its caches; so does this run.
	sessions.ResetCache()
	cliDir := filepath.Join(t.TempDir(), "pwcli")
	os.MkdirAll(cliDir, 0o755)
	os.WriteFile(filepath.Join(cliDir, "pw"), []byte(fakePwCLI), 0o755)
	for _, f := range platformFixtures() {
		k := cliKey(f.args)
		os.WriteFile(filepath.Join(cliDir, k+".out"), []byte(f.out), 0o644)
		if f.stderr != "" {
			os.WriteFile(filepath.Join(cliDir, k+".err"), []byte(f.stderr), 0o644)
		}
		if f.code != "" {
			os.WriteFile(filepath.Join(cliDir, k+".code"), []byte(f.code), 0o644)
		}
	}
	g := startGoldenEnv(t, nil, map[string]string{"PW_CLI": filepath.Join(cliDir, "pw"), "STUDIO_WORKSPACE_RETRY_MS": "50",
		// A platform's gateway, so the platform tools are offered.
		"PW_GATEWAY_URL": "{gateway}/api/openai/v1"},
		func(mux *http.ServeMux, a *app.App) {
			p := platform.Of(a)
			// As the platform track sets it (internal/platformtrack).
			a.Providers.PlatformAvailable = func(r *http.Request, _ string) bool {
				v, err := p.ViewerOf(r)
				return err == nil && p.PlatformAvailable(v)
			}
			workflows.Register(mux, a, p)
			tasks.Register(mux, a, p)
		})
	g.gw.mu.Lock()
	g.gw.api = platformAPI()
	g.gw.mu.Unlock()
	return g, cliDir
}

func platformScenarios() []scenario {
	long := strings.Repeat("The platform answered and the reply sums it up. ", 5)
	j := func(s string) string { return s }
	return []scenario{
		{name: "workflows and help", text: "what workflows do I have?", replies: []reply{call("list_workflows", `{}`), call("pw_help", `{"command":"workflows run; rm"}`),
			call("pw_help", `{"command":"workflows"}`), call("workflow_runs", `{"limit":2}`), text(long)}},
		{name: "clusters and environments", text: "what systems can I use?", replies: []reply{call("list_clusters", `{}`), call("hpc_environments", `{"cluster":"vega"}`),
			call("hpc_environments", `{}`), call("cluster_command", `{"cluster":"vega","command":"squeue -u alice"}`), call("cluster_command", `{"cluster":"vega; rm","command":"ls"}`), text(long)}},
		{name: "a workflow and its configurations", text: "show me hello", replies: []reply{call("get_workflow", `{"name":"hello"}`), call("workflow_configs", `{"name":"hello"}`),
			call("workflow_configs", `{"name":"hello","config":"default-vega"}`), call("get_workflow", `{"name":"nope"}`), text(long)}},
		{name: "run detail and watching", text: "how did my run go?", replies: []reply{call("workflow_run_detail", `{"run":"hello-00001"}`), call("watch_run", `{"run":"hello-00001","wait":0}`),
			call("watch_run", `{"run":"r1","wait":0}`), call("watch_run", `{"run":"missing-9","wait":0}`), text(long)}},
		{name: "launching asks for a configuration", text: "run hello", replies: []reply{call("run_workflow", `{"name":"hello"}`), text(long)}},
		{name: "a dry run on a named system", text: "check hello on vega", replies: []reply{call("run_workflow", j(`{"name":"hello","resource":"vega","inputs":"{\"batch_size\":4,\"greeting\":\"hi\"}"}`)), text(long)}},
		{name: "a launch from a configuration", text: "launch hello", replies: []reply{call("run_workflow", `{"name":"hello","config":"default-vega","dry_run":false}`),
			call("run_workflow", `{"name":"hello","config":"default-vega","inputs":"{\"greeting\":\"yo\"}","dry_run":false}`), text(long)}},
		{name: "a launch missing a value", text: "train on vega", replies: []reply{call("run_workflow", `{"name":"train","resource":"orion"}`), text(long)}},
		{name: "sessions", text: "what sessions are open?", replies: []reply{call("list_sessions", `{}`), call("list_sessions", `{"all":true}`), call("list_sessions", `{"run":"hello-00002"}`),
			call("list_sessions", `{"run":"hello-00009"}`), call("list_sessions", `{"match":"viz|nothing","all":true}`), call("list_sessions", `{"match":"zzz"}`), text(long)}},
		// No task starts here: an agent a task starts calls the gateway and
		// the CLI on its own time.
		{name: "delegation refused and tasks", text: "split this up", replies: []reply{call("delegate", `{}`),
			call("delegate", `{"objective":"survey","subtasks":[{"objective":"a"},{"objective":"b"},{"objective":"c"},{"objective":"d"},{"objective":"e"},{"objective":"f"},{"persona":"analyst","objective":"g"}]}`),
			call("task_status", `{}`), call("task_status", `{"id":"nope"}`), text(long)}},
		{name: "status, composing and GitHub", text: "is the system up?", replies: []reply{call("hpc_status", `{}`), call("compose_workflow", `{"workflows":"hello"}`),
			call("compose_workflow", `{"workflows":[{"uses":"hello"},{"uses":"nope","needs":["hello"]}]}`), call("compose_workflow", `{"workflows":[]}`), call("list_github_workflows", `{"repo":"not a repo"}`), text(long)}},
		{name: "storage and held calls", text: "storage, then stop vega", replies: []reply{call("list_storage", `{}`), call("stop_cluster", `{"name":"vega"}`), text(long)}},
		{name: "agent sites", text: "where can agents run?", replies: []reply{call("agent_sites", `{}`), call("agent_sessions", `{"site":"vega"}`), call("agent_status", `{"site":"vega","session":"s1"}`), text(long)}},
		{name: "validating and serving", text: "check this yaml", replies: []reply{call("validate_workflow", j(`{"yaml":"jobs:\n  main:\n    steps:\n      - run: echo hi\n"}`)),
			call("validate_workflow", j(`{"yaml":"on:\n  execute: {}\n"}`)), call("serve_model", `{"engine":"vllm","model":"m1"}`), text(long)}},
	}
}

// calls is the CLI's call log without the workflow list Node refreshes
// in the background.
func calls(path string) string {
	b, _ := os.ReadFile(path)
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "workflows ls -o json" {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func TestGoldenPlatformToolsMatchNode(t *testing.T) {
	g, cliDir := startPlatformGolden(t)
	log := filepath.Join(cliDir, "calls.log")
	// Each tool here runs in Go, not through Node's execute.
	var mu sync.Mutex
	ranInGo := map[string]bool{}
	saved := map[string]func(*Module, *http.Request, map[string]any, ToolCtx) (*Outcome, error){}
	for name, fn := range goExecutors {
		saved[name] = fn
		goExecutors[name] = func(m *Module, r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
			out, err := fn(m, r, args, ctx)
			if err != errNotHere {
				mu.Lock()
				ranInGo[name] = true
				mu.Unlock()
			}
			return out, err
		}
	}
	t.Cleanup(func() {
		for name, fn := range saved {
			goExecutors[name] = fn
		}
	})
	called := map[string]bool{}
	for i, sc := range platformScenarios() {
		var nodeCalls string
		n := 0
		sc.setup = func(*goldenEnv) {
			if n == 1 {
				nodeCalls = calls(log)
			}
			os.Remove(log)
			n++
		}
		t.Run(sc.name, func(t *testing.T) {
			runScenario(t, g, 100+i, sc)
			if goCalls := calls(log); nodeCalls != goCalls {
				t.Errorf("pw CLI calls differ\nnode:\n%s\ngo:\n%s", nodeCalls, goCalls)
			}
		})
		for _, rp := range sc.replies {
			for _, f := range rp.frames {
				if name, ok := jsonPathString(f, "choices", 0, "delta", "tool_calls", 0, "function", "name"); ok {
					called[name] = true
				}
			}
		}
	}
	// The chat route holds a call that changes the platform for approval.
	delete(called, "stop_cluster")
	for name := range called {
		if !ranInGo[name] {
			t.Errorf("%s did not run in Go", name)
		}
	}
}

// jsonPathString reads a string at a path of map keys and slice indexes.
func jsonPathString(v any, path ...any) (string, bool) {
	for _, k := range path {
		switch key := k.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return "", false
			}
			v = m[key]
		case int:
			a, ok := v.([]any)
			if !ok || key >= len(a) {
				return "", false
			}
			v = a[key]
		}
	}
	s, ok := v.(string)
	return s, ok
}
