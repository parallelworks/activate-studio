package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A deployment with an ACTIVATE credential of its own (PW_API_KEY) and a
// pw CLI signed in, for the two rules: a person on a Studio several people
// use never gets it, and Stop using the pw CLI leaves a Studio of one
// with none.
func deploymentWithCredential(t *testing.T, auth bool) (*Platform, string) {
	t.Helper()
	creds := t.TempDir()
	writeStore(t, creds, map[string]any{
		"currentIdentity": "work",
		"identities":      map[string]any{"work": map[string]any{"server": "platform.example", "apikey": "cli-key"}},
	})
	cli, calls := fakeCLI(t, "")
	env := map[string]string{"PW_CREDENTIALS_DIR": creds, "PW_CLI": cli}
	if !auth {
		env["PW_API_KEY"] = ""
	}
	p := New(Options{IndexBase: t.TempDir(), Home: t.TempDir(), AuthEnabled: func() bool { return auth },
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok && v != "" }})
	return p, calls
}

func envOf(list []string) map[string]string {
	out := map[string]string{}
	for _, kv := range list {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

func TestSharedStudioNeverLendsTheDeploymentCredential(t *testing.T) {
	p, _ := deploymentWithCredential(t, true)
	if p.GatewayKey() != "cli-key" {
		t.Fatal("the deployment has its credential, for its housekeeping")
	}
	nobody := &Viewer{AuthEnabled: true, User: &User{ID: "u1", Username: "alex"}}
	if k, own := p.KeyFor(nobody); k != "" || own {
		t.Fatalf("a person without a credential of their own got %q", k)
	}
	// The viewer may not say sign-in is on (Node's viewer); the Studio's own setting decides too.
	if k, _ := p.KeyFor(&Viewer{User: &User{ID: "u1"}}); k != "" {
		t.Fatalf("got %q", k)
	}
	if p.PlatformAvailable(nobody) {
		t.Fatal("ACTIVATE offered to a person without a credential of their own")
	}
	if k, own := p.KeyFor(&Viewer{AuthEnabled: true, SignIn: "alex-token"}); k != "alex-token" || !own {
		t.Fatal("the person's own sign-in")
	}
	// A pw CLI run without the person's key has no sign-in at all.
	t.Setenv("PW_API_KEY", "process-key")
	e := envOf(p.Environ(nil))
	if e["PW_API_KEY"] != "" || e["PW_CONTEXT"] != "" || e["HOME"] == "" || e["HOME"] != e["PW_CREDENTIALS_DIR"] || e["HOME"] == os.Getenv("HOME") {
		t.Fatalf("a run for nobody: %v", e)
	}
	if entries, _ := os.ReadDir(e["HOME"]); len(entries) != 0 {
		t.Fatal("the no sign-in home is not empty")
	}
	// With the person's key, theirs.
	if e := envOf(p.Environ(map[string]string{"PW_API_KEY": "alex-key"})); e["PW_API_KEY"] != "alex-key" {
		t.Fatal(e)
	}
	// The Studio's housekeeping keeps the deployment's sign-in.
	if e := envOf(p.EnvironAsDeployment(nil)); e["PW_API_KEY"] != "process-key" || e["PW_CREDENTIALS_DIR"] == e["HOME"] {
		t.Fatal(e)
	}
}

func TestStopUsingThePwCLI(t *testing.T) {
	p, calls := deploymentWithCredential(t, false)
	if p.GatewayKey() != "cli-key" || p.CredentialContextName() != "work" {
		t.Fatal("a Studio of one uses the pw CLI by itself")
	}
	if err := p.ChooseContext("work"); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(p.IndexBase(), "access.json"))
	if string(b) != "{\n \"pwContext\": \"work\",\n \"stopped\": true\n}" {
		t.Fatalf("%q", b)
	}
	owner := &Viewer{}
	if p.GatewayKey() != "" || p.CredentialContextName() != "" || p.CLICredential() != "" {
		t.Fatal("still reading the CLI's sign-in")
	}
	if k, _ := p.KeyFor(owner); k != "" || p.PlatformAvailable(owner) {
		t.Fatal("ACTIVATE still in use")
	}
	// Any CLI run now has no sign-in, and nothing signs the CLI out.
	if _, err := p.Exec(context.Background(), []string{"workflows", "ls"}, ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(calls)
	if strings.Contains(string(got), "logout") {
		t.Fatal("the CLI was signed out")
	}
	if e := envOf(p.Environ(nil)); e["PW_CREDENTIALS_DIR"] == "" || e["PW_CREDENTIALS_DIR"] == p.CredentialsFile() {
		t.Fatal(e)
	}
	// Read again from the file, as after a restart.
	q := New(Options{IndexBase: p.IndexBase(), Home: t.TempDir(), LookupEnv: func(string) (string, bool) { return "", false }})
	if !q.Stopped() {
		t.Fatal("not kept")
	}
	if err := p.Stop(false); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(p.IndexBase(), "access.json"))
	if string(b) != "{\n \"pwContext\": \"work\"\n}" || p.GatewayKey() != "cli-key" {
		t.Fatalf("%q", b)
	}
}
