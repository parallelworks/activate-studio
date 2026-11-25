//go:build !windows

package userlibs

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// A folder of a person's library is found by their own session on its
// system, for nobody else, and only where they may write.
func TestAFolderOfAPersonsLibrary(t *testing.T) {
	h := newHarness(t, map[string]string{"PW_API_KEY": "studio-key"})
	h.auth = true
	keys := map[string]string{"ada": "key-ada", "bob": "key-bob"}
	h.m.OwnerKey = func(owner, _ string) (string, bool) { return keys[owner], true }
	root := filepath.Join(h.dir, "cluster", "studio")
	h.homes["pw://ada/cluster-a"] = filepath.Join(h.dir, "cluster")
	write(t, filepath.Join(root, "proj", "sub", "plan.md"), "p")
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "away"))
	uid := os.Getuid()
	def := func(id, folder string) libraries.UserDef {
		return libraries.UserDef{ID: id, Label: id, Site: "pw://ada/cluster-a", State: "ready", SourceRoot: &folder, IndexRoot: &folder, GufiBin: &folder}
	}
	ada := def("u-space", root)
	ada.Space = &libraries.SpaceInfo{Account: "ada", UID: uid}
	other := ada
	other.ID, other.Space = "u-moved", &libraries.SpaceInfo{Account: "ada", UID: uid + 1}
	win, through := def("u-win", root), def("u-through", root)
	osName := "windows"
	win.OS = &osName
	through.ReadThrough = &libraries.ReadThrough{}
	ssh := def("u-ssh", root)
	ssh.Transport, ssh.Destination = "ssh", &[]string{"ada@host"}[0]
	bob := def("u-shared-proj", filepath.Join(root, "proj"))
	bob.Shared = &libraries.SharedInfo{ShareID: "s-1", From: "ada", Access: "write", UID: uid}
	read := def("u-shared-read", filepath.Join(root, "proj"))
	read.Shared = &libraries.SharedInfo{ShareID: "s-2", From: "ada", Access: "read", UID: uid}
	h.m.Users.Save("ada", []libraries.UserDef{ada, other, win, through, ssh})
	h.m.Users.Save("bob", []libraries.UserDef{bob, read})
	h.m.Users.Save("admin", nil)

	s := spaceFiles{h.m}
	at := func(who, owner, id, rel string) (kb.FolderAt, int, string) {
		t.Helper()
		r := auth.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), &auth.User{ID: who, Username: who})
		f, err := s.Folder(r, kb.Library{ID: id, Label: id, IndexRoot: libraries.RemoteRootOf(owner, id), Site: "pw://ada/cluster-a"}, rel)
		var ae *apierr.Error
		if errors.As(err, &ae) {
			return f, ae.Status, ae.Message
		}
		if err != nil {
			t.Fatal(err)
		}
		return f, 200, ""
	}
	f, code, why := at("ada", "ada", "u-space", "proj/sub")
	if code != 200 || f.Dir != root+"/proj/sub" || f.Root != root || f.Site != "pw://ada/cluster-a" || f.File != "" || f.Ino == "" || f.SSH {
		t.Fatalf("%d %s %+v", code, why, f)
	}
	if f, code, why = at("ada", "ada", "u-space", "proj/sub/plan.md"); code != 200 || f.Dir != root+"/proj/sub" || f.Rel != "proj/sub" || f.File != "proj/sub/plan.md" {
		t.Fatalf("%d %s %+v", code, why, f)
	}
	if f, code, why = at("bob", "bob", "u-shared-proj", ""); code != 200 || f.Dir != root+"/proj" {
		t.Fatalf("%d %s %+v", code, why, f)
	}
	if !h.sawSession("pw://ada/cluster-a", "key-ada") || !h.sawSession("pw://ada/cluster-a", "key-bob") || h.sawSession("pw://ada/cluster-a", "studio-key") {
		t.Errorf("looked as %v", h.sessions)
	}

	// Nobody else's, an administrator's included, and nothing is asked.
	n := len(h.sessions)
	none := kb.ErrFolderNone().Error()
	for _, who := range []string{"bob", "admin", "nobody"} {
		if _, code, why := at(who, "ada", "u-space", "proj"); code != 404 || why != none {
			t.Errorf("%s: %d %s", who, code, why)
		}
	}
	if len(h.sessions) != n {
		t.Errorf("asked as %v", h.sessions[n:])
	}
	for _, rel := range []string{"gone", "gone/x.md", "away"} {
		if _, code, why := at("ada", "ada", "u-space", rel); code != 404 || why != none {
			t.Errorf("%s: %d %s", rel, code, why)
		}
	}
	for id, word := range map[string]string{"u-moved": "account", "u-win": "cannot look", "u-through": "another host", "u-ssh": ""} {
		if _, code, why := at("ada", "ada", id, "proj"); code == 200 || !strings.Contains(why, word) {
			t.Errorf("%s: %d %s", id, code, why)
		}
	}
	if _, code, why := at("bob", "bob", "u-shared-read", ""); code != 403 || !strings.Contains(why, "to read") {
		t.Errorf("read: %d %s", code, why)
	}
	keys["ada"] = ""
	if _, code, why := at("ada", "ada", "u-space", "proj"); code == 200 || !strings.Contains(strings.ToLower(why), "sign in") {
		t.Errorf("no sign-in: %d %s", code, why)
	}
	keys["ada"] = "key-ada"
	if os.Geteuid() != 0 {
		os.Chmod(filepath.Join(root, "proj"), 0o555)
		defer os.Chmod(filepath.Join(root, "proj"), 0o755)
		if _, code, why := at("ada", "ada", "u-space", "proj"); code != 403 || !strings.Contains(why, "cannot write") {
			t.Errorf("read only: %d %s", code, why)
		}
	}
}

// Over ssh, on a Studio of one, the host's own pw code says which machine
// it is; one that runs none says nothing.
func TestAFolderOnAHostOverSSHNamesItsPWCode(t *testing.T) {
	h := newHarness(t, nil)
	h.homes["me@box"] = filepath.Join(h.dir, "box")
	root := filepath.Join(h.homes["me@box"], "work")
	write(t, filepath.Join(root, "runs", "a.md"), "a")
	dest := "me@box"
	h.m.Users.Save(single, []libraries.UserDef{{ID: "u-box", Label: "Box", Transport: "ssh", Destination: &dest, State: "ready", SourceRoot: &root, IndexRoot: &root, GufiBin: &root}})
	at := func() kb.FolderAt {
		t.Helper()
		f, err := (spaceFiles{h.m}).Folder(httptest.NewRequest(http.MethodGet, "/", nil), kb.Library{ID: "u-box", Label: "Box", IndexRoot: libraries.RemoteRootOf(single, "u-box"), Site: "ssh://me@box"}, "runs")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := at(); !f.SSH || f.Daemon != "" || f.Dir != root+"/runs" {
		t.Errorf("%+v", f)
	}
	// It is asked for its status, and starts nothing.
	exe(t, filepath.Join(h.homes["me@box"], ".local/bin/pw"), "#!/bin/sh\n[ \"$*\" = \"code daemon proxy\" ] || exit 1\nread -r l\ncase \"$l\" in \"GET /v1/status HTTP/1.1\"*) printf 'HTTP/1.1 200 OK\\r\\n\\r\\n{\"hostname\":\"box\",\"machineId\":\"d-7\"}' ;; esac\n")
	if f := at(); f.Daemon != "d-7" {
		t.Errorf("%+v", f)
	}
}
