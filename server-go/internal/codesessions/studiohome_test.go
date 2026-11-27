package codesessions

import (
	"encoding/json"
	"testing"
)

func listed(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var body struct {
		Machines []map[string]any `json:"machines"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.Machines
}

// The cluster the Studio runs beside is marked as the Studio's and listed
// first; a cluster of the same name of someone else's, a workspace and a
// computer are not.
func TestTheStudiosClusterIsMarkedAndFirst(t *testing.T) {
	t.Setenv("STUDIO_HOME_CLUSTER", "pw://ops/my-cluster")
	raw := []byte(`{"machines":[` +
		`{"id":"workspace","name":"workspace","kind":"workspace","owner":"ada"},` +
		`{"id":"c2","name":"my-cluster","kind":"existing","owner":"cy"},` +
		`{"id":"l1","name":"my-cluster","kind":"local","owner":"ada"},` +
		`{"id":"c1","name":"my-cluster","kind":"existing","owner":"ops"}],"sessions":[]}`)
	ms := listed(t, studioHomeListing(raw, true, "ada", nil))
	if len(ms) != 4 || ms[0]["id"] != "c1" || ms[0][StudioHome] != true {
		t.Fatalf("%v", ms)
	}
	for _, m := range ms[1:] {
		if m[StudioHome] != nil {
			t.Errorf("%v marked", m["id"])
		}
	}
	// A bare name matches by name.
	t.Setenv("STUDIO_HOME_CLUSTER", "my-cluster")
	if ms := listed(t, studioHomeListing(raw, true, "ada", nil)); ms[0]["id"] != "c2" || ms[0][StudioHome] != true {
		t.Fatalf("bare: %v", ms)
	}
}

// With sign-in on and no home cluster known, an entry says why nothing
// starts on the Studio's own machine; without sign-in the listing is as it
// came.
func TestNoHomeClusterIsSaidInTheListing(t *testing.T) {
	t.Setenv("STUDIO_HOME_CLUSTER", "")
	t.Setenv("PW_USER", "")
	t.Setenv("PW_POOL_NAME", "")
	raw := []byte(`{"machines":[{"id":"c1","name":"other","kind":"cloud","owner":"ada"}],"sessions":[]}`)
	ms := listed(t, studioHomeListing(raw, true, "ada", nil))
	if len(ms) != 2 || ms[0]["id"] != "studio" || ms[0][StudioHome] != true || ms[0]["status"] != "unreachable" || ms[0]["error"] != NoHomeCluster {
		t.Fatalf("%v", ms)
	}
	if got := studioHomeListing(raw, false, "ada", nil); string(got) != string(raw) {
		t.Errorf("without sign-in: %s", got)
	}
}

// The marked machine says whom pw code there runs as: the viewer, never
// the cluster's owner, which is the Studio's operator.
func TestTheStudiosMachineRunsAsTheViewerNotItsOwner(t *testing.T) {
	t.Setenv("STUDIO_HOME_CLUSTER", "pw://ops/my-cluster")
	raw := []byte(`{"machines":[{"id":"c1","name":"my-cluster","kind":"existing","owner":"ops","runsAs":"ops"}],"sessions":[]}`)
	ms := listed(t, studioHomeListing(raw, true, "ada", nil))
	if ms[0][StudioHome] != true || ms[0][RunsAs] != "ada" || ms[0]["owner"] != "ops" {
		t.Fatalf("%v", ms)
	}
	if ms := listed(t, studioHomeListing(raw, true, "", nil)); ms[0][RunsAs] != nil {
		t.Errorf("no viewer: %v", ms)
	}
}

// Without sign-in or a home cluster, the Studio drives this computer's pw
// code, so the machine whose daemon is here is marked: by the daemon's id,
// or by hostname where the listing has no id. Nothing is marked when the
// daemon does not answer, and nothing with sign-in on.
func TestWithoutSignInThisComputerIsMarked(t *testing.T) {
	t.Setenv("STUDIO_HOME_CLUSTER", "")
	t.Setenv("PW_USER", "")
	t.Setenv("PW_POOL_NAME", "")
	here := &Machine{ID: "d1", Kind: "local", Hostname: "laptop.local", DaemonID: "d1"}
	raw := []byte(`{"machines":[` +
		`{"id":"workspace","name":"workspace","kind":"workspace","owner":"ada","daemonId":"d9"},` +
		`{"id":"m2","name":"other","kind":"local","owner":"ada","daemonId":"d2","hostname":"laptop.local"},` +
		`{"id":"m1","name":"laptop","kind":"local","owner":"ada","daemonId":"d1"}],"sessions":[]}`)
	ms := listed(t, studioHomeListing(raw, false, "ada", here))
	if ms[0]["id"] != "m1" || ms[0][StudioHome] != true || ms[0][RunsAs] != "ada" {
		t.Fatalf("%v", ms)
	}
	for _, m := range ms[1:] {
		if m[StudioHome] != nil {
			t.Errorf("%v marked", m["id"])
		}
	}
	byHost := []byte(`{"machines":[{"id":"m3","name":"laptop","kind":"local","owner":"ada","hostname":"LAPTOP.local"}],"sessions":[]}`)
	if ms := listed(t, studioHomeListing(byHost, false, "ada", here)); ms[0][StudioHome] != true {
		t.Errorf("by hostname: %v", ms)
	}
	if got := studioHomeListing(raw, false, "ada", nil); string(got) != string(raw) {
		t.Errorf("no daemon: %s", got)
	}
	if ms := listed(t, studioHomeListing(raw, true, "ada", here)); ms[1][StudioHome] != nil || ms[2][StudioHome] != nil || ms[0]["id"] != "studio" {
		t.Errorf("with sign-in: %v", ms)
	}
}
