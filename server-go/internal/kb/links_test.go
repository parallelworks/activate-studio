package kb

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

type linkHarness struct {
	t        *testing.T
	m        *Module
	mux      *http.ServeMux
	kb, base string
	user     *auth.User
	log      bytes.Buffer
}

func newLinkHarness(t *testing.T, signIn bool) *linkHarness {
	t.Helper()
	h := &linkHarness{t: t, kb: filepath.Join(t.TempDir(), "library"), base: t.TempDir()}
	os.MkdirAll(h.kb, 0o755)
	env := map[string]string{"KB_ROOT": h.kb, "INDEX_BASE": h.base, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(h.base, "no-gufi")}
	if signIn {
		env["OIDC_ISSUER"], env["OIDC_CLIENT_ID"] = "https://id.example.com", "studio"
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	h.m = New(app.New(cfg, slog.New(slog.NewTextHandler(&h.log, nil)), nil, nil))
	h.m.Files.Indexer = &slowIndexer{}
	h.mux = http.NewServeMux()
	h.m.Routes(h.mux)
	// As the server's: what the two tracks set.
	r, l, s, mw := LibraryResolver, Links, SpaceFiles, MainWriter
	LibraryResolver, Links, SpaceFiles, MainWriter = nil, nil, nil, nil
	t.Cleanup(func() { LibraryResolver, Links, SpaceFiles, MainWriter = r, l, s, mw })
	return h
}

func (h *linkHarness) write(rel, text string) {
	p := filepath.Join(h.kb, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *linkHarness) do(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if h.user != nil {
		r = auth.WithUser(r, h.user)
	}
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, r)
	out := map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (h *linkHarness) link(path, library string) string {
	h.t.Helper()
	code, out := h.do("POST", "/api/links", map[string]any{"path": path, "library": library})
	if code != 200 {
		h.t.Fatalf("link to %s: %d %v", path, code, out)
	}
	return out["id"].(string)
}

func (h *linkHarness) open(id string) (int, map[string]any) {
	h.t.Helper()
	return h.do("GET", "/api/links/"+id, nil)
}

func (h *linkHarness) audit() string {
	var b strings.Builder
	files, _ := filepath.Glob(filepath.Join(h.base, "link-log", "*.jsonl"))
	for _, f := range files {
		x, _ := os.ReadFile(f)
		b.Write(x)
	}
	return b.String()
}

func TestLinksFollowWhatTheLibraryDoes(t *testing.T) {
	h := newLinkHarness(t, false)
	h.write("plans/budget.md", "one")
	h.write("plans/notes.md", "n")
	os.MkdirAll(filepath.Join(h.kb, "archive"), 0o755)
	id := h.link("plans/budget.md", "kb")
	if h.link("/plans/budget.md", "") != id || h.link("kb/plans/budget.md", "all") != id {
		t.Error("one file has more than one ID")
	}
	dir := h.link("plans", "kb")
	code, out := h.open(id)
	if code != 200 || out["path"] != "plans/budget.md" || out["library"] != "kb" || out["type"] != "file" || out["replaced"] != false {
		t.Fatalf("%d %v", code, out)
	}
	if code, out = h.open(dir); code != 200 || out["type"] != "dir" || out["path"] != "plans" {
		t.Fatalf("%d %v", code, out)
	}
	if code, out = h.open(h.link("", "kb")); code != 200 || out["type"] != "dir" || out["path"] != "" {
		t.Fatalf("the library itself: %d %v", code, out)
	}

	// Renamed, its folder moved, written over by an upload.
	if c, o := h.do("POST", "/api/kb/rename", map[string]any{"path": "plans/budget.md", "name": "2027.md"}); c != 200 {
		t.Fatal(o)
	}
	if c, o := h.do("POST", "/api/kb/move", map[string]any{"paths": []string{"plans"}, "dest": "archive"}); c != 200 {
		t.Fatal(o)
	}
	code, out = h.open(id)
	if code != 200 || out["path"] != "archive/plans/2027.md" || out["replaced"] != false {
		t.Fatalf("moved: %d %v", code, out)
	}
	if code, out = h.open(dir); code != 200 || out["path"] != "archive/plans" {
		t.Fatalf("moved: %d %v", code, out)
	}
	abs := filepath.Join(h.kb, "archive/plans/2027.md")
	done := h.m.Files.rewriting("archive/plans/2027.md", abs)
	if err := saveLimited(abs, strings.NewReader("two"), 100); err != nil {
		t.Fatal(err)
	}
	done()
	if code, out = h.open(id); code != 200 || out["replaced"] != false {
		t.Errorf("an upload over it: %d %v", code, out)
	}

	// Another file put there by something else: opened, and said.
	os.WriteFile(abs+".new", []byte("other"), 0o644)
	os.Rename(abs+".new", abs)
	code, out = h.open(id)
	if code != 200 || out["replaced"] != true || out["path"] != "archive/plans/2027.md" {
		t.Errorf("replaced: %d %v", code, out)
	}
	other := h.link("archive/plans/2027.md", "kb")
	if code, out = h.open(other); other == id || code != 200 || out["replaced"] != false {
		t.Errorf("a link to the file there now: %d %v", code, out)
	}

	// Deleted through the Studio: said, and what comes there later is not it.
	if c, o := h.do("POST", "/api/kb/delete", map[string]any{"paths": []string{"archive/plans/2027.md"}}); c != 200 {
		t.Fatal(o)
	}
	for range 2 {
		if code, out = h.open(other); code != 410 || out["reason"] != "deleted" || strings.Contains(out["error"].(string), "2027") {
			t.Errorf("deleted: %d %v", code, out)
		}
		h.write("archive/plans/2027.md", "three")
	}
	// Deleted outside the Studio, with the folder it was in.
	n := h.link("archive/plans/notes.md", "kb")
	os.RemoveAll(filepath.Join(h.kb, "archive"))
	if code, out = h.open(n); code != 410 || out["reason"] != "deleted" {
		t.Errorf("gone: %d %v", code, out)
	}
	if c, _ := h.do("POST", "/api/links", map[string]any{"library": "kb"}); c != 400 {
		t.Error("a link to no path was made")
	}
	if c, _ := h.do("POST", "/api/links", map[string]any{"path": "archive", "library": "kb"}); c != 404 {
		t.Errorf("a link to nothing: %d", c)
	}
	for _, bad := range []string{"../x", "a/../../x", ".git/config"} {
		if c, _ := h.do("POST", "/api/links", map[string]any{"path": bad, "library": "kb"}); c != 400 && c != 404 {
			t.Errorf("%s: %d", bad, c)
		}
	}
}

func TestLinksSayTheSameOfWhatIsNotOpen(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions")
	}
	h := newLinkHarness(t, false)
	h.write("closed/secret.md", "s")
	h.write("half/secret.md", "s")
	h.write("plain.md", "p")
	inClosed, inHalf, closed := h.link("closed/secret.md", "kb"), h.link("half/secret.md", "kb"), h.link("closed", "kb")
	os.Chmod(filepath.Join(h.kb, "closed"), 0)
	os.Chmod(filepath.Join(h.kb, "half/secret.md"), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(h.kb, "closed"), 0o755) })
	_, none := h.open("AAAAAAAAAAAAAAAAAAAAAA")
	for what, id := range map[string]string{"in a closed folder": inClosed, "a closed file": inHalf, "a closed folder": closed, "not an ID": "x", "with a deployment": "site." + inHalf} {
		code, out := h.open(id)
		if code != 404 || out["error"] != none["error"] || out["reason"] != "none" || len(out) != len(none) {
			t.Errorf("%s: %d %v", what, code, out)
		}
	}
	if c, _ := h.do("POST", "/api/links", map[string]any{"path": "half/secret.md", "library": "kb"}); c != 404 {
		t.Errorf("a link to a closed file: %d", c)
	}
	// Deleted in a folder that cannot be listed: not said.
	h.m.Links.Deleted("", "kb", "closed/secret.md")
	if code, out := h.open(inClosed); code != 404 || out["error"] != none["error"] {
		t.Errorf("%d %v", code, out)
	}
	// The record has the IDs and no name.
	log := h.audit()
	if strings.Contains(log, "secret") || strings.Contains(log, "closed") || strings.Contains(log, "plain") || !strings.Contains(log, `"id":"`+inHalf+`","user":"single","outcome":"404"`) || !strings.Contains(log, `"id":"invalid"`) {
		t.Errorf("%s", log)
	}
	if strings.Contains(h.log.String(), "secret") {
		t.Errorf("%s", h.log.String())
	}
}

func TestLinksAskForSignIn(t *testing.T) {
	h := newLinkHarness(t, true)
	h.write("plan.md", "p")
	if c, o := h.do("POST", "/api/links", map[string]any{"path": "plan.md"}); c != 401 || o["signIn"] != auth.LoginPath {
		t.Errorf("%d %v", c, o)
	}
	h.user = &auth.User{ID: "u1", Username: "ada"}
	id := h.link("plan.md", "kb")
	h.user = nil
	code, out := h.open(id)
	code2, out2 := h.open("AAAAAAAAAAAAAAAAAAAAAA")
	if code != 401 || out["reason"] != "signed-out" || code2 != code || out2["error"] != out["error"] {
		t.Errorf("%d %v, %d %v", code, out, code2, out2)
	}
	h.user = &auth.User{ID: "u2", Username: "bob"}
	if code, out = h.open(id); code != 200 {
		t.Errorf("%d %v", code, out)
	}
	if log := h.audit(); !strings.Contains(log, `"what":"made","id":"`+id+`","user":"ada","outcome":"made"`) ||
		!strings.Contains(log, `"what":"opened","id":"`+id+`","outcome":"signed-out"`) || !strings.Contains(log, `"what":"opened","id":"`+id+`","user":"bob","outcome":"opened"`) {
		t.Errorf("%s", log)
	}
}

// bucketStub lists a bucket library's index.
type bucketStub struct {
	remoteStub
	list map[string][]Entry
	err  error
}

func (b *bucketStub) Library(_ *http.Request, id string) (Library, error) {
	return Library{}, Error(404, "no library named %s", id)
}
func (b *bucketStub) Tree(_ *http.Request, _ Library, rel string) ([]Entry, error) {
	if b.err != nil {
		return nil, b.err
	}
	l, ok := b.list[rel]
	if !ok {
		return nil, Error(404, "no such folder: %s", rel)
	}
	return l, nil
}

func TestLinksInALibraryOnlyItsIndexLists(t *testing.T) {
	h := newLinkHarness(t, false)
	os.WriteFile(filepath.Join(h.base, "libraries.json"), []byte(`[{"id":"scans","label":"Scans","indexRoot":"/nowhere","bucket":{"uri":"s3://example"}}]`), 0o644)
	b := &bucketStub{list: map[string][]Entry{
		"":  {{Name: "a", Path: "a", Type: "dir"}},
		"a": {{Name: "x.tif", Path: "a/x.tif", Type: "file"}, {Name: "y.tif", Path: "a/y.tif", Type: "file"}},
	}}
	LibraryResolver = b
	id := h.link("a/x.tif", "scans")
	dir := h.link("a", "scans")
	if code, out := h.open(id); code != 200 || out["path"] != "a/x.tif" || out["library"] != "scans" || out["type"] != "file" || out["replaced"] != false {
		t.Fatalf("%d %v", code, out)
	}
	if code, out := h.open(dir); code != 200 || out["type"] != "dir" {
		t.Fatalf("%d %v", code, out)
	}
	// A move in a bucket is a copy and a delete: deleted.
	b.list["a"] = b.list["a"][1:]
	if code, out := h.open(id); code != 410 {
		t.Errorf("%d %v", code, out)
	}
	delete(b.list, "a")
	b.list[""] = []Entry{{Name: "b", Path: "b", Type: "dir"}}
	if code, out := h.open(id); code != 410 {
		t.Errorf("its folder gone: %d %v", code, out)
	}
	// A folder that lists empty may be one that cannot be read.
	b.list["a"] = []Entry{}
	if code, out := h.open(id); code != 404 {
		t.Errorf("%d %v", code, out)
	}
	b.err = Error(502, "a/x.tif: the bucket did not answer")
	if code, out := h.open(id); code != 502 || out["reason"] != "unreachable" || strings.Contains(out["error"].(string), "x.tif") {
		t.Errorf("%d %v", code, out)
	}
	h.m.Links.DropLibrary("", "scans")
	if code, out := h.open(id); code != 404 {
		t.Errorf("removed: %d %v", code, out)
	}
}
