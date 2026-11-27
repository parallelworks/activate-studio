package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// existingStudio is setupStudio with Bob's machine an existing cluster,
// labcluster, listed with owner, and his ACTIVATE cluster listing clusters
// (asked with the key it records in asked).
func existingStudio(t *testing.T, owner string, clusters []bridgeCluster, listErr error) (*Module, *fakeMachine, *[]string) {
	t.Helper()
	m, f, _ := setupStudio(t)
	lab := bridgeMach{ID: "m-lab", Kind: "existing", Name: "labcluster", Owner: owner, Status: "connected"}
	m.bridgeMachineFn = func(r *http.Request, id string) (bridgeMach, error) {
		if id == lab.ID {
			return lab, nil
		}
		return bridgeMach{}, platform.KbError(404, "not yours")
	}
	asked := &[]string{}
	m.bridgeClustersFn = func(r *http.Request, key string) ([]bridgeCluster, error) {
		*asked = append(*asked, key)
		return clusters, listErr
	}
	return m, f, asked
}

// pw ssh finds no cluster by its bare name, so an existing cluster is
// always named pw://<owner>/<name>: the owner the pw code listing gives,
// else the one the person's own cluster listing gives for the name, and
// only then the person.
func TestAnExistingClusterIsNamedWithItsOwner(t *testing.T) {
	other := []bridgeCluster{
		{Name: "labcluster", Type: "aws-slurm", User: "erin"}, // a cloud cluster of the same name
		{Name: "othercluster", Type: "existing", User: "frank"},
	}
	for _, c := range []struct {
		name, owner string
		clusters    []bridgeCluster
		listErr     error
		site        string
		lookedUp    bool
	}{
		{name: "the listing gives the owner", owner: "bob", site: "pw://bob/labcluster"},
		{name: "shared by an administrator", owner: "carol", site: "pw://carol/labcluster"},
		{name: "one cluster of that name", clusters: append(other, bridgeCluster{Name: "labcluster", Type: "existing", User: "carol"}),
			site: "pw://carol/labcluster", lookedUp: true},
		{name: "one, named by its URI", clusters: append(other, bridgeCluster{Name: "labcluster", Type: "existing", URI: "pw://dan/labcluster"}),
			site: "pw://dan/labcluster", lookedUp: true},
		{name: "none of that name", clusters: other, site: "pw://bob/labcluster", lookedUp: true},
		{name: "the listing cannot be read", listErr: errors.New("HTTP 502"), site: "pw://bob/labcluster", lookedUp: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, f, asked := existingStudio(t, c.owner, c.clusters, c.listErr)
			code, out := setupAs(t, m, bobProxy, `{"machineId":"m-lab"}`)
			if code != 200 || out["worked"] != true {
				t.Fatalf("%d %v", code, out)
			}
			for _, call := range f.calls {
				if call != c.site+" pwt_bobs.key" {
					t.Errorf("ran at %q, not %s with Bob's own key", call, c.site)
				}
			}
			if len(f.calls) == 0 {
				t.Error("nothing ran")
			}
			if c.lookedUp != (len(*asked) > 0) {
				t.Errorf("cluster listing asked %v", *asked)
			}
			for _, k := range *asked {
				if k != "pwt_bobs.key" {
					t.Errorf("the cluster listing was asked with %q, not Bob's own key", k)
				}
			}
		})
	}
}

// Two clusters of the name: the Studio does not guess, runs nothing, and
// names both.
func TestTwoClustersOfTheNameAreNotGuessedBetween(t *testing.T) {
	m, f, _ := existingStudio(t, "", []bridgeCluster{
		{Name: "labcluster", Type: "existing", User: "carol"},
		{Name: "labcluster", Type: "existing", URI: "pw://dan/labcluster"},
	}, nil)
	code, out := setupAs(t, m, bobProxy, `{"machineId":"m-lab"}`)
	msg, _ := out["error"].(string)
	if code != 409 || !strings.Contains(msg, "pw://carol/labcluster") || !strings.Contains(msg, "pw://dan/labcluster") {
		t.Fatalf("%d %v", code, out)
	}
	if len(f.calls) != 0 {
		t.Errorf("ran %v", f.calls)
	}
}

// When pw ssh does not reach the machine, the Studio says which site it
// tried.
func TestAFailedReachSaysTheSiteItTried(t *testing.T) {
	m, _, _ := existingStudio(t, "", nil, nil)
	m.bridgeRunFn = func(_ context.Context, site, key string, argv []string, _ time.Duration) (remote.Result, error) {
		return remote.Result{}, errors.New("[ERROR] Cluster not found")
	}
	code, out := setupAs(t, m, bobProxy, `{"machineId":"m-lab"}`)
	msg, _ := out["error"].(string)
	if code != 502 || !strings.Contains(msg, "at pw://bob/labcluster") || !strings.Contains(msg, "Cluster not found") {
		t.Fatalf("%d %v", code, out)
	}
}

// The command the card gives to paste in a terminal on the machine adds the
// same entry Set up for me does, with the pw found there, and no key.
func TestThePasteCommandAddsTheSameEntry(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	got := filepath.Join(dir, "args")
	// A pw that writes the arguments it was given, each ended by a NUL.
	fake := "#!/bin/sh\nprintf '%s\\000' \"$@\" >" + got + "\n"
	if err := os.WriteFile(filepath.Join(dir, "pw"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	url := "https://platform.example/me/session/ada/studio/api/mcp"
	cmd := exec.Command("sh", "-c", bridgePaste(url, ""))
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "HOME=" + dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	want := []string{"code", "mcp", "add", "--scope", "user", "--force", studioMCPName, "--", "sh", "-c", bridgeShim, bridgeShimName, url, filepath.Join(dir, "pw")}
	if len(args) != len(want) {
		t.Fatalf("args %q", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("arg %d: %q, want %q", i, args[i], want[i])
		}
	}
	e := &bridgeEntry{}
	e.Config.Command, e.Config.Args = args[8], args[9:]
	if at, ours, current := e.target(); at != url || !ours || !current {
		t.Errorf("entry %v %v %v", at, ours, current)
	}
	if strings.Contains(bridgePaste(url, ""), bridgeKeyHole) {
		t.Error("the paste command asks for a key")
	}
}

// The card has the paste command.
func TestTheCardHasThePasteCommand(t *testing.T) {
	m, _, id := setupStudio(t)
	r := httptest.NewRequest(http.MethodGet, "/api/mcp/bridge?conversation="+id, nil)
	r.Header.Set("X-Forwarded-Host", "platform.example")
	r = asViewer(r, bobProxy)
	w := httptest.NewRecorder()
	if err := m.bridgeRoute(w, r); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if p, _ := out["paste"].(string); !strings.HasPrefix(p, `sh -c "$(pw auth token --print`) || strings.Contains(p, "\n") {
		t.Errorf("paste %q", out["paste"])
	}
}
