//go:build !windows

package userlibs

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// TestSharedWithMeNamesSystemsNotReached: the systems shares are on are
// asked side by side, each for a bounded time, and one that fails or does
// not answer is named with its reason rather than read as nothing shared.
func TestSharedWithMeNamesSystemsNotReached(t *testing.T) {
	h, homes := buildHarness(t)
	h.auth = true
	h.m.OwnerKey = func(owner, _ string) (string, bool) { return "key-" + owner, true }
	h.user = &auth.User{ID: "bob", Username: "bob"}
	// cluster-slow answers, but only after a minute.
	slow := filepath.Join(h.dir, "slow")
	exe(t, filepath.Join(slow, "bin", "id"), "#!/bin/sh\nsleep 60\n")
	h.homes["cluster-slow"] = homes["cluster-a"]
	h.path["cluster-slow"] = filepath.Join(slow, "bin")
	h.failing["cluster-down"] = "ssh: connect to host cluster-down port 22: Connection refused"
	old := sharedReachWait
	sharedReachWait = 2 * time.Second
	t.Cleanup(func() { sharedReachWait = old })
	shares := []Share{
		{ID: "s1", Owner: "ada", OwnerAccount: "ada", Site: "cluster-a", Folder: "/home/ada/studio/a", Kind: "everyone", Access: "read"},
		{ID: "s2", Owner: "ada", OwnerAccount: "ada", Site: "cluster-down", Folder: "/home/ada/studio/b", Kind: "everyone", Access: "read"},
		{ID: "s3", Owner: "ada", OwnerAccount: "ada", Site: "cluster-slow", Folder: "/home/ada/studio/c", Kind: "everyone", Access: "read"},
		{ID: "s4", Owner: "ada", OwnerAccount: "ada", Site: "cluster-down", Folder: "/home/ada/studio/d", Kind: "everyone", Access: "read"},
	}
	b, _ := json.Marshal(shares)
	if err := os.WriteFile(h.m.sharesFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	got := h.do(http.MethodGet, "/api/user-libraries/shared-with-me", nil)
	took := time.Since(t0)
	if took > 8*time.Second {
		t.Fatalf("the list waited %v: the systems were not asked side by side, or not for a bounded time", took)
	}
	list, _ := got.Body["shares"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != "s1" {
		t.Fatalf("shares: %v", got.Body)
	}
	un, _ := got.Body["unreached"].([]any)
	why := map[string]string{}
	for _, u := range un {
		u := u.(map[string]any)
		why[u["site"].(string)] = u["why"].(string)
	}
	if len(un) != 2 || why["cluster-down"] == "" {
		t.Fatalf("unreached: %v", got.Body["unreached"])
	}
	if !strings.Contains(why["cluster-slow"], "did not answer within 2 seconds") {
		t.Fatalf("a system that did not answer: %q", why["cluster-slow"])
	}
}
