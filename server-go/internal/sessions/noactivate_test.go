package sessions

import (
	"os"
	"strings"
	"testing"
)

// Sessions and desktops: nothing is asked of the platform for a person
// without an ACTIVATE credential of their own (the deployment has one,
// deploy-key), and nothing after Stop using the pw CLI on a Studio of one.
func checkNoActivateCall(t *testing.T, f *fixture) {
	t.Helper()
	var body map[string]any
	if st := f.get("/api/sessions", &body); st != 409 || !strings.HasPrefix(body["error"].(string), "No platform credential: sign in with ACTIVATE") {
		t.Fatal(st, body)
	}
	if res, _ := f.studio.Client().Get(f.studio.URL + "/api/session-desktop/alex/desk/websockify"); res.StatusCode < 400 {
		t.Fatal(res.Status)
	}
	f.mu.Lock()
	asked := append([]string{}, f.auth...)
	f.mu.Unlock()
	if len(asked) != 0 {
		t.Fatalf("the platform was asked: %q", asked)
	}
	if b, _ := os.ReadFile(f.cliLog); len(b) != 0 {
		t.Fatalf("the pw CLI ran: %s", b)
	}
}

func TestSharedStudioSessionsNeverUseTheDeploymentCredential(t *testing.T) {
	checkNoActivateCall(t, newFixtureWith(t, true))
}

func TestStoppedStudioOfOneSessionsMakeNoActivateCall(t *testing.T) {
	f := newFixture(t)
	if err := f.p.Stop(true); err != nil {
		t.Fatal(err)
	}
	checkNoActivateCall(t, f)
}
