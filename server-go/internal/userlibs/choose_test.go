//go:build !windows

package userlibs

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// chooseHarness is two systems, one over pw ssh and one over plain ssh,
// with no need of GUFI.
func chooseHarness(t *testing.T) (*harness, map[string]string) {
	h := newHarness(t, map[string]string{"PW_API_KEY": "cli-key"})
	homes := map[string]string{"me@linux-box": filepath.Join(h.dir, "box"), "cluster-a": filepath.Join(h.dir, "cluster")}
	for k, v := range homes {
		h.homes[k] = v
		write(t, filepath.Join(v, "data", "runs", "a.csv"), "1,2\n")
		write(t, filepath.Join(v, "data", "b.CSV"), "12\n")
		write(t, filepath.Join(v, "data", "notes.md"), "# n\n")
	}
	exe(t, filepath.Join(h.dir, "pw"), "#!/bin/sh\nexit 0\n")
	h.env["PW_CLI"] = filepath.Join(h.dir, "pw")
	h.m.P.FindCLI()
	return h, homes
}

// TestSSHHosts: the hosts in the ssh config of the account a Studio of one
// runs as, as names alone; not on a Studio people sign in to.
func TestSSHHosts(t *testing.T) {
	h, _ := chooseHarness(t)
	h.m.Home = filepath.Join(h.dir, "me")
	eq(t, h.do(http.MethodGet, "/api/user-libraries/ssh/hosts", nil).Body, map[string]any{"hosts": []any{}}, "no file")
	write(t, filepath.Join(h.m.Home, ".ssh", "config"), "Host example-box *.example.com\n  HostName 192.0.2.10\n  IdentityFile ~/.ssh/id_example\n  User alex\n")
	eq(t, h.do(http.MethodGet, "/api/user-libraries/ssh/hosts", nil).Body, map[string]any{"hosts": []any{"example-box"}}, "names alone")
	h.auth, h.user = true, &auth.User{ID: "ada"}
	if r := h.do(http.MethodGet, "/api/user-libraries/ssh/hosts", nil); r.Status != 403 {
		t.Errorf("with sign-in on: %v", r)
	}
	if r := h.do(http.MethodGet, "/api/user-libraries/folders?destination=me@linux-box&path=~", nil); r.Status != 403 {
		t.Errorf("a host's folders with sign-in on: %v", r)
	}
}

// TestRemoteFolders: a system's folders are listed and counted as the
// person, over the session a library there is read with.
func TestRemoteFolders(t *testing.T) {
	h, homes := chooseHarness(t)
	for where, home := range map[string]string{"cluster=cluster-a": homes["cluster-a"], "destination=me@linux-box:2222": homes["me@linux-box"]} {
		r := h.do(http.MethodGet, "/api/user-libraries/folders?"+where+"&path=~", nil)
		eq(t, []any{r.Body["path"], r.Body["home"], r.Body["entries"]}, []any{home, home, []any{map[string]any{"name": "data"}}}, where)
		r = h.do(http.MethodGet, "/api/user-libraries/folders?"+where+"&path="+home+"/data&filter=ru", nil)
		eq(t, r.Body["entries"], []any{map[string]any{"name": "runs"}}, "under it")
		r = h.do(http.MethodGet, "/api/user-libraries/folders?"+where+"&path=/none", nil)
		eq(t, r.Status, 400, "none")
		match(t, r.Body["error"], `^/none is not a folder your account there can read\.$`, "none")
		k, v, _ := strings.Cut(where, "=")
		c := h.post("/api/user-libraries/space/count", map[string]any{k: v, "sourceRoot": "~/data"})
		eq(t, c.Body, map[string]any{"count": map[string]any{"files": 3, "bytes": 11, "kinds": []any{map[string]any{"name": "csv", "files": 2}, map[string]any{"name": "md", "files": 1}}}}, "counted")
		c = h.post("/api/user-libraries/space/count", map[string]any{k: v, "sourceRoot": "~/new"})
		eq(t, c.Body, map[string]any{"count": nil}, "nothing there")
	}
	if !h.sawSession("cluster-a", "cli-key") || !h.sawSession("ssh://me@linux-box:2222", "") {
		t.Error("read over the person's own sessions")
	}
	bad := h.do(http.MethodGet, "/api/user-libraries/folders?cluster=-oProxyCommand%3Dx&path=~", nil)
	eq(t, bad.Status, 400, "a name pw ssh would take as an option")

	// A host that does not take the person's key says what to do.
	h.failing["me@shut-box"] = "Permission denied (publickey)."
	for _, r := range []answer{
		h.do(http.MethodGet, "/api/user-libraries/folders?destination=me@shut-box&path=~", nil),
		h.post("/api/user-libraries/space/test", map[string]any{"destination": "me@shut-box", "sourceRoot": "~/data"}),
	} {
		match(t, r.Body["error"], `^me@shut-box did not accept a key .*Set up key access`, "what to do")
	}
}

// TestCheckWarnsInShort: a check's warnings in a few words beside their
// detail, and what search by meaning needs there.
func TestCheckWarnsInShort(t *testing.T) {
	h, homes := buildHarness(t)
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	open := filepath.Join(homes["me@linux-box"], "open")
	write(t, filepath.Join(open, "a.txt"), "a")
	os.Chmod(open, 0o777)
	short := func(r answer, name string) any {
		for _, s := range r.Body["steps"].([]any) {
			if m := s.(map[string]any); m["name"] == name {
				return m["short"]
			}
		}
		return nil
	}
	r := h.post("/api/user-libraries/space/test", map[string]any{"destination": "me@linux-box", "sourceRoot": "~/open"})
	if r.Body["ok"] != true {
		t.Fatalf("%v", r.Body)
	}
	eq(t, []any{short(r, "folder"), r.Body["writable"]}, []any{"Others can write it", true}, "open")
	match(t, warned(r, "folder"), `run chmod go-w`, "what to change")
	if _, ok := r.Body["embedMB"].(float64); !ok && r.Body["embedWhy"] == nil {
		t.Errorf("what search by meaning needs: %v %v", r.Body["embedMB"], r.Body["embedWhy"])
	}
	os.Chmod(open, 0o755)
	r = h.post("/api/user-libraries/space/test", map[string]any{"destination": "me@linux-box", "sourceRoot": "~/open"})
	eq(t, short(r, "folder"), "Others can read it", "readable by others")
	os.Chmod(open, 0o500)
	r = h.post("/api/user-libraries/space/test", map[string]any{"destination": "me@linux-box", "sourceRoot": "~/open"})
	if os.Getuid() != 0 {
		eq(t, []any{r.Body["ok"], r.Body["writable"]}, []any{true, false}, "not the person's to write")
	}
	os.Chmod(open, 0o700)
}
