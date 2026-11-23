package workflows

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/runs"
)

// The person-facing workflow routes, each asked by someone with no
// ACTIVATE credential of their own.
var personRoutes = [][2]string{
	{"GET", "/api/workflows"},
	{"GET", "/api/workflows/hello"},
	{"GET", "/api/platform/clusters"},
	{"GET", "/api/platform/partitions?cluster=c1"},
	{"GET", "/api/platform/buckets"},
	// The Studio's own record of runs: answered here, asking nothing.
	{"GET", "/api/workflows/runs"},
	{"POST", "/api/workflows/item/run"},
	{"POST", "/api/workflows/item/install"},
}

// noActivateCall asks every person-facing route and checks that none is
// answered, and that nothing reached the platform: no API request, no pw
// CLI run (through the routes or directly), no run watched.
func noActivateCall(t *testing.T, f *fixture, net *hosts, cliLog string) {
	t.Helper()
	var watched []string
	f.r.Runs.CLI = func(_ context.Context, args []string, key string) (string, error) {
		watched = append(watched, strings.Join(args, " ")+" "+key)
		return "", nil
	}
	for _, c := range personRoutes {
		var body any
		if c[0] == "POST" {
			body = map[string]any{"entry": "hello", "inputs": map[string]any{}}
		}
		if a := f.do(c[0], c[1], body); a.status < 400 && c[1] != "/api/workflows/runs" {
			t.Errorf("%s %s answered %d without a credential: %s", c[0], c[1], a.status, a.body)
		}
	}
	f.r.Runs.RecordRun(runs.RecordInput{Slug: "run-1", Workflow: "hello", Owner: sp("alex"), OwnerID: sp("u1")})
	f.r.Runs.Stop()
	if calls := f.cliCalls(); len(calls) != 0 {
		t.Errorf("the pw CLI ran: %v", calls)
	}
	if seen := net.reset(); len(seen) != 0 {
		t.Errorf("the platform was asked: %v", seen)
	}
	if b, _ := os.ReadFile(cliLog); len(b) != 0 {
		t.Errorf("the pw CLI ran directly: %s", b)
	}
	if len(watched) != 0 {
		t.Errorf("a run was watched: %v", watched)
	}
}

func sp(s string) *string { return &s }

func cliScript(t *testing.T) (string, string) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "pw")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755)
	return script, log
}

func TestSharedStudioWorkflowsNeverUseTheDeploymentCredential(t *testing.T) {
	net := fakeNet(t)
	cli, log := cliScript(t)
	f := newFixture(t, map[string]string{"AUTH_ON": "1", "PW_API_KEY": "deploy-key", "PW_CLI": cli}, "hello")
	f.user = &auth.User{ID: "u1", Username: "alex", Name: "Alex"}
	if f.p.GatewayKey() != "deploy-key" {
		t.Fatal("the deployment has a credential")
	}
	noActivateCall(t, f, net, log)
}

func TestStoppedStudioOfOneWorkflowsMakeNoActivateCall(t *testing.T) {
	net := fakeNet(t)
	cli, log := cliScript(t)
	f := newFixture(t, map[string]string{"PW_API_KEY": "owner-key", "PW_CLI": cli}, "hello")
	if err := f.p.Stop(true); err != nil {
		t.Fatal(err)
	}
	noActivateCall(t, f, net, log)
}
