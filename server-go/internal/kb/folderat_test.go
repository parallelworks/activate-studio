package kb

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

func (h *linkHarness) folder(id, rel string) (FolderAt, int, string) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if h.user != nil {
		r = auth.WithUser(r, h.user)
	}
	at, err := h.m.FolderFor(r, id, rel)
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return at, ae.Status, ae.Message
	}
	if err != nil {
		h.t.Fatalf("%s %q: %v", id, rel, err)
	}
	return at, 200, ""
}

func TestTheFolderOfAChatStartedInTheLibrary(t *testing.T) {
	h := newLinkHarness(t, false)
	h.write("runs/case 1/plan.md", "p")
	for _, c := range []struct{ rel, dir, file string }{
		{"", "", ""},
		{"runs", "runs", ""},
		{"/runs/case 1/", "runs/case 1", ""},
		{"runs/case 1/plan.md", "runs/case 1", "runs/case 1/plan.md"},
	} {
		at, code, why := h.folder("kb", c.rel)
		if code != 200 || at.Dir != filepath.Join(h.kb, filepath.FromSlash(c.dir)) || at.Rel != c.dir || at.File != c.file || at.Site != "" || at.Root != h.kb || at.Ino == "" && runtime.GOOS != "windows" {
			t.Errorf("%q: %d %s %+v", c.rel, code, why, at)
		}
	}
	_, _, none := h.folder("nolib", "")
	h.write("node_modules/x/a.js", "x")
	for _, c := range [][2]string{{"kb", "gone"}, {"kb", "gone/plan.md"}, {"nolib", "runs"}, {"all", "kb/runs"}, {"", "runs"}, {"kb", "node_modules/x"}} {
		if _, code, why := h.folder(c[0], c[1]); code != 404 || why != none {
			t.Errorf("%v: %d %s", c, code, why)
		}
	}
	for _, bad := range []string{"../x", "runs/../../x", "a\x00b"} {
		if _, code, _ := h.folder("kb", bad); code == 200 {
			t.Errorf("%q was taken", bad)
		}
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	// One the account cannot open is as one that is not there.
	h.write("closed/a.md", "a")
	os.Chmod(filepath.Join(h.kb, "closed"), 0)
	for _, rel := range []string{"closed", "closed/a.md"} {
		if _, code, why := h.folder("kb", rel); code != 404 || why != none {
			t.Errorf("%s: %d %s", rel, code, why)
		}
	}
	os.Chmod(filepath.Join(h.kb, "closed"), 0o755)
	os.Chmod(filepath.Join(h.kb, "runs"), 0o555)
	if _, code, why := h.folder("kb", "runs"); code != 403 || !strings.Contains(why, "cannot write") {
		t.Errorf("read only: %d %s", code, why)
	}
	os.Chmod(filepath.Join(h.kb, "runs"), 0o755)
}

func TestNoChatStartsInABucketOrAReadThroughLibrary(t *testing.T) {
	h := newLinkHarness(t, false)
	os.WriteFile(filepath.Join(h.base, "libraries.json"), []byte(`[{"id":"scans","label":"Scans","indexRoot":"/nowhere","bucket":{"uri":"s3://example"}}]`), 0o644)
	LibraryResolver = &bucketStub{}
	if _, code, why := h.folder("scans", "a"); code != 409 || !strings.Contains(why, "bucket") {
		t.Errorf("%d %s", code, why)
	}
}

// Where people sign in, the main library is read only to those who may not
// write it, whatever the Studio's account may do, and nobody signed out has
// a folder.
func TestAChatInTheMainLibraryIsForThoseWhoMayWriteIt(t *testing.T) {
	h := newLinkHarness(t, true)
	h.write("runs/plan.md", "p")
	if _, code, _ := h.folder("kb", "runs"); code != 401 {
		t.Errorf("signed out: %d", code)
	}
	MainWriter = func(*http.Request) (bool, string) { return h.user.ID == "cy", "Only administrators change it." }
	h.user = &auth.User{ID: "cy", Username: "cy"}
	if at, code, why := h.folder("kb", "runs"); code != 200 || at.Ino == "" && runtime.GOOS != "windows" {
		t.Errorf("cy: %d %s %+v", code, why, at)
	}
	h.user = &auth.User{ID: "u1", Username: "ada"}
	if _, code, why := h.folder("kb", "runs"); code != 403 || !strings.Contains(why, "read only for you") {
		t.Errorf("ada: %d %s", code, why)
	}
	// Not there, she is told no more than that she may not write.
	if _, code, why := h.folder("kb", "gone"); code != 403 || !strings.Contains(why, "read only for you") {
		t.Errorf("ada: %d %s", code, why)
	}
}

// What is on another system is found by the libraries track, as the viewer.
type folderStub struct{ asked []string }

func (f *folderStub) Folder(r *http.Request, lib Library, rel string) (FolderAt, error) {
	who := ""
	if u := auth.UserOf(r); u != nil {
		who = u.ID
	}
	f.asked = append(f.asked, who+" "+lib.ID+" "+rel)
	return FolderAt{Lib: lib, Rel: rel, Site: lib.Site, Root: "/r", Dir: "/r/" + rel}, nil
}

type elsewhereStub struct{ remoteStub }

func (*elsewhereStub) Library(_ *http.Request, id string) (Library, error) {
	if id != "u-work" {
		return Library{}, Error(404, "no library named %s", id)
	}
	return Library{ID: id, Label: "Work", IndexRoot: remotePrefix + "single/u-work", Site: "pw://ada/cluster"}, nil
}

func TestAFolderOnAnotherSystemIsFoundThere(t *testing.T) {
	h := newLinkHarness(t, false)
	LibraryResolver = &elsewhereStub{}
	prev := Folders
	t.Cleanup(func() { Folders = prev })
	Folders = nil
	if _, code, _ := h.folder("u-work", "runs"); code != 409 {
		t.Errorf("%d", code)
	}
	f := &folderStub{}
	Folders = f
	if at, code, _ := h.folder("u-work", "/runs/"); code != 200 || at.Dir != "/r/runs" || at.Site != "pw://ada/cluster" || len(f.asked) != 1 || f.asked[0] != " u-work runs" {
		t.Errorf("%d %+v %v", code, at, f.asked)
	}
	// What the Library leaves out is not asked for there.
	if _, code, why := h.folder("u-work", "node_modules/x"); code != 404 || why != folderNone || len(f.asked) != 1 {
		t.Errorf("%d %s %v", code, why, f.asked)
	}
}
