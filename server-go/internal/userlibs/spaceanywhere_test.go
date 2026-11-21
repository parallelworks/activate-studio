//go:build !windows

package userlibs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// TestSpaceOnAnySystem: My library is made on a cluster the request
// names, also on a Studio that knows no cluster of its own, and over plain
// ssh on a Studio one person uses, written as them there and not shared.
func TestSpaceOnAnySystem(t *testing.T) {
	h, homes := buildHarness(t)
	t.Setenv("STUDIO_HOME_CLUSTER", "")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())

	st := h.do(http.MethodGet, "/api/user-libraries/space", nil)
	eq(t, []any{st.Body["available"], st.Body["cluster"], st.Body["pw"], st.Body["ssh"]}, []any{true, "", true, true}, "offered without a cluster of its own")
	none := h.post("/api/user-libraries/space/test", map[string]any{"sourceRoot": "~/studio"})
	match(t, none.Body["error"], `Choose one of your systems`, "no system named")
	bad := h.post("/api/user-libraries/space/test", map[string]any{"cluster": "-oProxyCommand=x", "sourceRoot": "~/studio"})
	eq(t, bad.Status, 400, "a name pw ssh would take as an option")

	r := h.post("/api/user-libraries/space/test", map[string]any{"cluster": "cluster-a", "sourceRoot": "~/studio"})
	if r.Body["ok"] != true || r.Body["system"] != "cluster-a" {
		t.Fatalf("%v", r.Body)
	}
	b := h.post("/api/user-libraries/space", map[string]any{"cluster": "cluster-a", "sourceRoot": "~/studio", "create": true, "depth": "names"})
	if b.Status != 200 || b.Body["site"] != "cluster-a" {
		t.Fatalf("%v", b.Body)
	}
	eq(t, perm(t, filepath.Join(homes["cluster-a"], "studio")), os.FileMode(0o700), "made for the person alone")
	eq(t, h.waitFor(b.Body["id"].(string))["state"], "ready", "built")

	// Over plain ssh, with the keys of the account the Studio runs as.
	s := h.post("/api/user-libraries/space", map[string]any{"destination": "me@linux-box", "sourceRoot": "~/mine", "create": true, "depth": "names"})
	if s.Status != 200 {
		t.Fatalf("%v", s.Body)
	}
	id := s.Body["id"].(string)
	eq(t, []any{s.Body["transport"], s.Body["destination"]}, []any{"ssh", "me@linux-box"}, "over ssh")
	eq(t, h.waitFor(id)["state"], "ready", "built over ssh")
	box := homes["me@linux-box"]
	eq(t, perm(t, filepath.Join(box, "mine")), os.FileMode(0o700), "closed to others there too")
	lib := kb.Library{ID: id, Label: "My library", IndexRoot: libraries.RemoteRootOf("single", id), Space: true, Site: "me@linux-box"}
	req := httptest.NewRequest(http.MethodPost, "/api/kb/upload", nil)
	if err := h.m.SpaceFiles().Put(req, lib, "a.txt", strings.NewReader("over ssh"), 100); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(box, "mine", "a.txt")); string(got) != "over ssh" {
		t.Fatalf("written: %q", got)
	}
	eq(t, perm(t, filepath.Join(box, "mine", "a.txt"))&0o077, os.FileMode(0), "closed to others")
	sh := h.post("/api/user-libraries/"+id+"/shares", map[string]any{"kind": "user", "name": "bob", "access": "read"})
	match(t, sh.Body["error"], `nobody here to share it with`, "not shared")

	// With sign-in on, plain ssh has no credential of the person's own.
	if _, err := h.m.spaceSession(who{ID: "ada", Auth: true, Own: "key-ada"}, jsonv.NewObject("destination", "ada@box")); err == nil {
		t.Fatal("a space over ssh with sign-in on")
	}
	if tg, err := h.m.spaceSession(who{ID: "ada", Auth: true, Own: "key-ada"}, jsonv.NewObject("cluster", "cluster-b")); err != nil || tg.key != "key-ada" || tg.transport != "pw" {
		t.Fatalf("a space over pw ssh with their own key: %+v %v", tg, err)
	}
}
