//go:build !windows

package userlibs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// sharedHarness is a Studio with sign-in whose sessions say they are the
// account and in the groups given for a person, as their key's.
type sharedHarness struct {
	*harness
	keys map[string]string
}

func newSharedHarness(t *testing.T) *sharedHarness {
	h := &sharedHarness{newHarness(t, nil), map[string]string{"bob": "key-bob", "cy": "key-cy", "eve": "key-eve"}}
	h.auth = true
	h.m.OwnerKey = func(owner, _ string) (string, bool) { k, ok := h.keys[owner]; return k, ok }
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.homes["cluster-a"] = filepath.Join(h.dir, "cluster")
	os.MkdirAll(h.homes["cluster-a"], 0o755)
	return h
}

// account has the next sessions to site reach an account with its groups.
func (h *sharedHarness) account(site, name, groups string) {
	h.t.Helper()
	dir := filepath.Join(h.dir, strings.NewReplacer(":", "_", "/", "_", " ", "_").Replace("as-"+site+"-"+name+"-"+groups))
	exe(h.t, filepath.Join(dir, "id"), "#!/bin/sh\ncase \"$*\" in -u) echo 2001;; -un) echo "+name+";; -Gn) echo '"+groups+"';; esac\n")
	h.path[site] = dir
}

func (h *sharedHarness) store(list ...Share) {
	h.t.Helper()
	b, _ := json.Marshal(list)
	if err := os.WriteFile(h.m.sharesFile(), b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// now is what is new to a person as it stands, without waiting.
func (h *sharedHarness) now(id string) ([]string, bool) {
	r := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/notices", nil), &auth.User{ID: id, Username: id})
	list, looking := h.m.SharedNew(r)
	out := []string{}
	for _, s := range list {
		out = append(out, s.ID)
	}
	return out, looking
}

// told is what is new to a person once their sessions have answered.
func (h *sharedHarness) told(id string) string {
	h.t.Helper()
	for end := time.Now().Add(20 * time.Second); ; {
		list, looking := h.now(id)
		if !looking {
			return strings.Join(list, " ")
		}
		if time.Now().After(end) {
			h.t.Fatalf("%s: still being asked", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *sharedHarness) folder(name string) string {
	p := filepath.Join(h.homes["cluster-a"], name)
	os.MkdirAll(p, 0o755)
	return p
}

// TestAShareIsNewToWhoItReaches: a share is told to the person or the group
// members it names, as their own session says who they are, from their
// first request on; not to others, the sharer, nor to everyone.
func TestAShareIsNewToWhoItReaches(t *testing.T) {
	h := newSharedHarness(t)
	proj, runs, pub := h.folder("proj"), h.folder("runs"), h.folder("pub")
	user := Share{ID: "s-user", Owner: "ada", OwnerAccount: "ada", Site: "cluster-a", SpaceLabel: "My space", Rel: "proj", Folder: proj, Kind: "user", Name: "bob", Access: "read", CreatedAt: "2026-01-02T00:00:00.000Z"}
	group := Share{ID: "s-group", Owner: "ada", OwnerAccount: "ada", Site: "cluster-a", SpaceLabel: "Runs", Folder: runs, Kind: "group", Name: "team", Access: "write"}
	all := Share{ID: "s-all", Owner: "ada", OwnerAccount: "ada", Site: "cluster-a", Folder: pub, Kind: "everyone", Access: "read"}
	h.store(user, group, all)

	// Nothing is waited for.
	h.account("cluster-a", "bob", "bob team")
	if list, looking := h.now("bob"); len(list) != 0 || !looking {
		t.Fatalf("at once: %v, looking %v", list, looking)
	}
	eq(t, h.told("bob"), "s-user s-group", "bob, in the group")
	h.account("cluster-a", "cy", "cy team")
	eq(t, h.told("cy"), "s-group", "cy, in the group")
	h.account("cluster-a", "eve", "eve")
	eq(t, h.told("eve"), "", "eve")
	h.keys["ada"] = "key-ada"
	h.account("cluster-a", "ada", "ada team")
	eq(t, h.told("ada"), "", "the sharer")
	// Another who reaches the cluster as the account that shared it.
	h.keys["ann"] = "key-ann"
	eq(t, h.told("ann"), "", "the sharer's account")

	r := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/notices", nil), &auth.User{ID: "bob", Username: "bob"})
	list, _ := h.m.SharedNew(r)
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
	eq(t, list[0], SharedNew{ID: "s-user", From: "ada", Label: "proj", Folder: proj, Site: "cluster-a", System: "cluster-a", Access: "read", At: "2026-01-02T00:00:00.000Z"}, "what is said")
	eq(t, []any{list[1].Label, list[1].Group}, []any{"Runs", "team"}, "a group's")

	// Another is known at once, one taken back is gone at once.
	more := Share{ID: "s-more", Owner: "cy", OwnerAccount: "cy", Site: "cluster-a", Folder: h.folder("more"), Kind: "user", Name: "bob", Access: "read"}
	h.store(group, more)
	eq(t, h.told("bob"), "s-group s-more", "another")
	// Taken back and shared again, it is asked anew.
	os.Remove(more.Folder)
	more.ID = "s-more2"
	h.store(group, more)
	eq(t, h.told("bob"), "s-group", "a folder that does not open")

	// Added, it is no longer new; nor once that library is removed.
	group.Adopters = []Adopter{{Owner: "bob", Library: "u-shared-runs"}}
	h.store(group)
	eq(t, h.told("bob"), "", "added")
	eq(t, h.told("cy"), "s-group", "not added by cy")
	h.m.forgetAdopted("bob", &libraries.UserDef{ID: "u-shared-runs", Shared: &libraries.SharedInfo{ShareID: "s-group"}})
	if l := h.m.loadShares(); len(l[0].Adopters) != 0 {
		t.Fatalf("still added: %+v", l)
	}
	eq(t, h.told("bob"), "", "removed by bob")
}

// TestNothingIsNewWithoutTheirOwnKey: what a share says of a person comes
// from their own session, with the key they have now.
func TestNothingIsNewWithoutTheirOwnKey(t *testing.T) {
	h := newSharedHarness(t)
	h.store(Share{ID: "s1", Owner: "ada", OwnerAccount: "ada", Site: "cluster-a", Folder: h.folder("proj"), Kind: "user", Name: "bob", Access: "read"})
	delete(h.keys, "bob")
	h.account("cluster-a", "bob", "bob")
	if list, looking := h.now("bob"); len(list) != 0 || looking {
		t.Fatalf("with no key: %v %v", list, looking)
	}
	if h.sawSession("cluster-a", "") || h.sawSession("cluster-a", "cli-key") {
		t.Fatal("a session was opened with another key")
	}
	h.keys["bob"] = "key-bob"
	eq(t, h.told("bob"), "s1", "with their key")
	// Another key is another session's answer, not the one kept.
	h.keys["bob"] = "key-of-eve"
	h.account("cluster-a", "eve", "eve")
	eq(t, h.told("bob"), "", "with another key")
	delete(h.keys, "bob")
	eq(t, h.told("bob"), "", "with the key cleared")
	h.m.forgetLooks("bob")
	h.m.sharesMu.Lock()
	n := len(h.m.looks)
	h.m.sharesMu.Unlock()
	eq(t, n, 0, "what was kept")
	// No one signed in.
	if list, looking := h.m.SharedNew(httptest.NewRequest(http.MethodGet, "/api/notices", nil)); len(list) != 0 || looking {
		t.Fatalf("signed out: %v", list)
	}
}

// TestNoSessionIsOpenedOnAnothersMachine: a share names its own system, and
// a person's session is opened there without their asking only on the
// Studio's cluster, where they have a library, and on a running cluster of
// their own.
func TestNoSessionIsOpenedOnAnothersMachine(t *testing.T) {
	h := newSharedHarness(t)
	pw := filepath.Join(h.dir, "pw")
	exe(t, pw, `#!/bin/sh
case "$*" in *"clusters ls --status active"*) echo '[{"name":"box","user":"bob"}]';; *) exit 1;; esac
`)
	h.env["PW_CLI"] = pw
	h.m.P.FindCLI()
	var shares []Share
	for i, site := range []string{"pw://eve/box", "other", "ssh://eve@host", "pw://bob/box", "lib-box"} {
		h.homes[site] = h.homes["cluster-a"]
		h.account(site, "bob", "bob")
		shares = append(shares, Share{ID: "s" + string(rune('1'+i)), Owner: "eve", OwnerAccount: "eve", Site: site, Folder: h.folder("proj"), Kind: "user", Name: "bob", Access: "read"})
	}
	if err := h.m.appendDef("bob", libraries.UserDef{ID: "u-a", Label: "A", Site: "lib-box", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	h.store(shares...)
	eq(t, h.told("bob"), "s4 s5", "told of")
	for i, site := range []string{"pw://eve/box", "other", "ssh://eve@host"} {
		if h.sawSession(site, "key-bob") {
			t.Errorf("a session was opened on %s (%d)", site, i)
		}
	}
}

// TestASystemThatDoesNotAnswerIsNotAskedOver: it fails closed, is not asked
// at every read, and what was known is kept while it is away.
func TestASystemThatDoesNotAnswerIsNotAskedOver(t *testing.T) {
	h := newSharedHarness(t)
	h.store(Share{ID: "s1", Owner: "ada", OwnerAccount: "ada", Site: "cluster-a", Folder: h.folder("proj"), Kind: "user", Name: "bob", Access: "read"})
	h.failing["cluster-a"] = "Connection refused"
	eq(t, h.told("bob"), "", "down")
	for range 5 {
		if _, looking := h.now("bob"); looking {
			t.Fatal("asked again at once")
		}
	}
	delete(h.failing, "cluster-a")
	h.account("cluster-a", "bob", "bob")
	age := func(at, tried time.Duration) {
		h.m.sharesMu.Lock()
		defer h.m.sharesMu.Unlock()
		for _, l := range h.m.looks {
			if !l.at.IsZero() {
				l.at = time.Now().Add(-at)
			}
			l.tried = time.Now().Add(-tried)
		}
	}
	age(0, lookAgain+time.Minute)
	eq(t, h.told("bob"), "s1", "back")
	h.failing["cluster-a"] = "Connection refused"
	h.m.Pool.Close()
	age(lookShown+time.Minute, lookShown+time.Minute)
	eq(t, h.told("bob"), "s1", "away for a while")
	age(lookKeep+time.Minute, time.Minute)
	eq(t, h.told("bob"), "", "away for long")
}

// TestAnotherSiteAddsNothing: whoever shared a folder knows the share's ID,
// and a page of theirs cannot add it for someone.
func TestAnotherSiteAddsNothing(t *testing.T) {
	h := newSharedHarness(t)
	h.store(Share{ID: "s1", Owner: "ada", OwnerAccount: "ada", Site: "cluster-a", Folder: h.folder("proj"), Kind: "user", Name: "bob", Access: "read"})
	for _, hdr := range [][2]string{{"Origin", "https://elsewhere.example"}, {"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}} {
		r := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/user-libraries/shared-with-me/s1/add", strings.NewReader("{}")), &auth.User{ID: "bob", Username: "bob"})
		r.Header.Set("Content-Type", "text/plain")
		r.Header.Set(hdr[0], hdr[1])
		w := httptest.NewRecorder()
		h.mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || h.sawSession("cluster-a", "key-bob") {
			t.Errorf("%s: %d %s", hdr[0], w.Code, w.Body.String())
		}
	}
	// The Studio's own page, behind a proxy that rewrites the host: the
	// browser says it is the same origin, the environment has the address, or
	// the page says it is its own.
	t.Setenv("STUDIO_SELF_URL", "https://studio.example")
	for _, hdr := range [][2]string{{"Sec-Fetch-Site", "same-origin"}, {"Origin", "https://studio.example"}, {"X-Studio-Request", "1"}} {
		r := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/user-libraries/shared-with-me/none/add", strings.NewReader("{}")), &auth.User{ID: "bob", Username: "bob"})
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(hdr[0], hdr[1])
		w := httptest.NewRecorder()
		h.mux.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("own page, %s: %d %s", hdr[0], w.Code, w.Body.String())
		}
	}
}
