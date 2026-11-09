package userlibs

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// A remote build is followed with the owner's key as it is at each poll,
// as Node's follower asks keyOf(owner) each time: a key rotated during the
// build is the one the next poll uses, and with the key cleared the
// follower stops after five misses and says why.
func TestFollowUsesTheOwnersKeyNow(t *testing.T) {
	h, _ := buildHarness(t)
	h.auth = true
	h.user = &auth.User{ID: "ada", Username: "ada"}
	var mu sync.Mutex
	keys := map[string]string{"ada": "key-one"}
	h.m.OwnerKey = func(owner, _ string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		k, ok := keys[owner]
		return k, ok
	}
	setKey := func(k string) {
		mu.Lock()
		defer mu.Unlock()
		if k == "" {
			delete(keys, "ada")
		} else {
			keys["ada"] = k
		}
	}
	// gufi_dir2index waits while the hold file exists, so the build is
	// still running when the key changes.
	gufi := needGufi(t)
	hold := filepath.Join(h.dir, "hold")
	bin := filepath.Join(h.dir, "slow-gufi")
	exe(t, filepath.Join(bin, "gufi_dir2index"), "#!/bin/sh\nn=0\nif [ $# -gt 1 ]; then while [ -f "+hold+" ] && [ $n -lt 1200 ]; do sleep 0.05; n=$((n+1)); done; fi\nexec "+gufi+"/gufi_dir2index \"$@\"\n")
	exe(t, filepath.Join(bin, "gufi_query"), "#!/bin/sh\nexec "+gufi+"/gufi_query \"$@\"\n")
	waitSession := func(key string) {
		t.Helper()
		for i := 0; i < 400; i++ {
			if h.sawSession("cluster-a", key) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("no session with %s", key)
	}

	write(t, hold, "")
	b := h.post("/api/user-libraries/pw/build", map[string]any{"cluster": "cluster-a", "sourceRoot": "~/work", "depth": "names", "gufiBin": bin})
	if b.Status != 200 {
		t.Fatalf("%d %v", b.Status, b.Body)
	}
	id := b.Body["id"].(string)
	waitSession("key-one")
	setKey("key-two")
	waitSession("key-two")
	os.Remove(hold)
	got := h.waitFor(id)
	eq(t, []any{got["state"], got["error"], got["files"]}, []any{"ready", nil, 1}, "followed with the new key")

	write(t, hold, "")
	again := h.post("/api/user-libraries/"+id+"/refresh", nil)
	eq(t, []any{again.Status, again.Body["state"]}, []any{200, "updating"}, "rebuild")
	setKey("")
	var l map[string]any
	for i := 0; i < 400; i++ {
		if l = h.library(id); l != nil && l["error"] != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	os.Remove(hold)
	match(t, l["error"], `^The build could not be followed: This library is reached with your own platform key`, "cleared")
}
