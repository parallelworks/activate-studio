package kb

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

type textRig struct {
	root string
	mux  *http.ServeMux
}

func newTextRig(t *testing.T) *textRig {
	t.Helper()
	root, ix := t.TempDir(), t.TempDir()
	env := map[string]string{"KB_ROOT": root, "INDEX_BASE": ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(ix, "no-gufi")}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	m := New(app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	mux := http.NewServeMux()
	m.Routes(mux)
	return &textRig{root: root, mux: mux}
}

func (r *textRig) do(method string, q map[string]string, body string) (int, map[string]any) {
	v := url.Values{}
	for k, x := range q {
		v.Set(k, x)
	}
	req := httptest.NewRequest(method, "/api/kb/text?"+v.Encode(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (r *textRig) open(p string) (int, map[string]any) {
	return r.do("GET", map[string]string{"path": p}, "")
}
func (r *textRig) save(p, base, body string) (int, map[string]any) {
	return r.do("PUT", map[string]string{"path": p, "base": base}, body)
}
func (r *textRig) put(t *testing.T, p, body string) string {
	t.Helper()
	abs := filepath.Join(r.root, p)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}
func (r *textRig) text(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.root, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestANewTextFileIsMadeAndNeverOverOneThere(t *testing.T) {
	r := newTextRig(t)
	c, b := r.save("notes/Untitled.md", NoBase, "")
	if c != 200 || b["path"] != "notes/Untitled.md" || b["version"] != TextVersion(nil) || b["deferred"] != true {
		t.Fatalf("%d %v", c, b)
	}
	if r.text(t, "notes/Untitled.md") != "" {
		t.Fatal("not empty")
	}
	r.put(t, "notes/Untitled.md", "kept")
	if c, b = r.save("notes/Untitled.md", NoBase, "other"); c != 409 || b["conflict"] != TextExists {
		t.Fatalf("%d %v", c, b)
	}
	if r.text(t, "notes/Untitled.md") != "kept" {
		t.Fatal("written over")
	}
	// A folder of that name too, and a file where a folder would be.
	if c, b = r.save("notes", NoBase, ""); c != 409 || b["conflict"] != TextExists {
		t.Fatalf("%d %v", c, b)
	}
	if c, b = r.save("notes/Untitled.md/x.md", NoBase, ""); c != 409 {
		t.Fatalf("%d %v", c, b)
	}
	// The name is made usable as an upload's is; a folder's own is kept.
	if err := os.Mkdir(filepath.Join(r.root, "résumés"), 0o755); err != nil {
		t.Fatal(err)
	}
	if c, b = r.save("résumés/a:b.txt", NoBase, "x"); c != 200 || b["path"] != "résumés/a_b.txt" {
		t.Fatalf("%d %v", c, b)
	}
	for _, bad := range []string{"", "notes/", ".hidden", "notes/.env", ".git/config", "../out.md", "notes/../../out.md", "build/x.md", strings.Repeat("n", 300) + ".md"} {
		if c, b := r.save(bad, NoBase, "x"); c != 400 {
			t.Errorf("%q: %d %v", bad, c, b)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.root), "out.md")); err == nil {
		t.Fatal("left the library")
	}
}

func TestASaveNeverWritesOverAChangeMadeSince(t *testing.T) {
	r := newTextRig(t)
	abs := r.put(t, "plan.md", "one\n")
	if err := os.Chmod(abs, 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(abs)
	c, b := r.open("plan.md")
	if c != 200 || b["content"] != "one\n" || b["version"] != TextVersion([]byte("one\n")) {
		t.Fatalf("%d %v", c, b)
	}
	v := b["version"].(string)
	if c, b = r.save("plan.md", v, "two\n"); c != 200 || b["version"] != TextVersion([]byte("two\n")) {
		t.Fatalf("%d %v", c, b)
	}
	after, _ := os.Stat(abs)
	if !os.SameFile(before, after) || runtime.GOOS != "windows" && after.Mode() != before.Mode() {
		t.Errorf("the file was replaced, so its labels and owner were not kept: %v %v", before.Mode(), after.Mode())
	}
	// Changed since, with the same size: the hash tells.
	r.put(t, "plan.md", "2wo\n")
	if c, b = r.save("plan.md", TextVersion([]byte("two\n")), "mine\n"); c != 409 || b["conflict"] != TextChanged || b["version"] != TextVersion([]byte("2wo\n")) {
		t.Fatalf("%d %v", c, b)
	}
	if r.text(t, "plan.md") != "2wo\n" {
		t.Fatal("written over")
	}
	// Overwriting names the version the person was told of.
	if c, b = r.save("plan.md", TextVersion([]byte("2wo\n")), "mine\n"); c != 200 || r.text(t, "plan.md") != "mine\n" {
		t.Fatalf("%d %v", c, b)
	}
	// Shorter text leaves none of the old behind.
	if c, _ = r.save("plan.md", TextVersion([]byte("mine\n")), "m"); c != 200 || r.text(t, "plan.md") != "m" {
		t.Fatalf("%d %q", c, r.text(t, "plan.md"))
	}
	os.Remove(abs)
	if c, b = r.save("plan.md", TextVersion([]byte("m")), "again"); c != 409 || b["conflict"] != TextMissing {
		t.Fatalf("%d %v", c, b)
	}
	if _, err := os.Stat(abs); err == nil {
		t.Fatal("a deleted file came back")
	}
	if c, _ = r.open("plan.md"); c != 404 {
		t.Fatal(c)
	}
	for _, base := range []string{"", "latest", strings.Repeat("z", 64)} {
		if c, b := r.save("plan.md", base, "x"); c != 400 {
			t.Errorf("base %q: %d %v", base, c, b)
		}
	}
}

func TestOfTwoSavesFromOneVersionOneIsTaken(t *testing.T) {
	r := newTextRig(t)
	for round := range 20 {
		v := TextVersion([]byte("base"))
		r.put(t, "a.md", "base")
		var wg sync.WaitGroup
		codes := make([]int, 6)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _ = r.save("a.md", v, fmt.Sprintf("from %d", i))
			}()
		}
		wg.Wait()
		ok, win := 0, -1
		for i, c := range codes {
			if c == 200 {
				ok, win = ok+1, i
			} else if c != 409 {
				t.Fatalf("round %d: %v", round, codes)
			}
		}
		if ok != 1 || r.text(t, "a.md") != fmt.Sprintf("from %d", win) {
			t.Fatalf("round %d: %v %q", round, codes, r.text(t, "a.md"))
		}
	}
}

func TestOnlyTextUpToTheLimitIsEdited(t *testing.T) {
	r := newTextRig(t)
	at := strings.Repeat("a", MaxEditBytes)
	if c, b := r.save("at.txt", NoBase, at); c != 200 {
		t.Fatalf("%d %v", c, b)
	}
	if c, b := r.open("at.txt"); c != 200 || len(b["content"].(string)) != MaxEditBytes {
		t.Fatalf("%d", c)
	}
	if c, b := r.save("at.txt", TextVersion([]byte(at)), at+"b"); c != 413 || r.text(t, "at.txt") != at {
		t.Fatalf("%d %v", c, b)
	}
	r.put(t, "over.txt", at+"b")
	if c, b := r.open("over.txt"); c != 413 || !strings.Contains(b["error"].(string), "1 MB") {
		t.Fatalf("%d %v", c, b)
	}
	r.put(t, "latin1.txt", "caf\xe9")
	r.put(t, "nul.txt", "a\x00b")
	for _, p := range []string{"latin1.txt", "nul.txt"} {
		if c, b := r.open(p); c != 415 {
			t.Errorf("%s: %d %v", p, c, b)
		}
	}
	if c, _ := r.save("bad.txt", NoBase, "caf\xe9"); c != 415 {
		t.Error(c)
	}
	// CRLF, a byte order mark and no last newline come and go as they are.
	odd := "\ufeffone\r\ntwo\r\nthree"
	r.put(t, "odd.txt", odd)
	if c, b := r.open("odd.txt"); c != 200 || b["content"] != odd {
		t.Fatalf("%d %q", c, b["content"])
	}
}

func TestATextEditStaysInTheLibrary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need a privilege there")
	}
	r := newTextRig(t)
	out := t.TempDir()
	secret := filepath.Join(out, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.put(t, "in.txt", "inside")
	for at, to := range map[string]string{"link.txt": secret, "door": out, "near.txt": "in.txt"} {
		if err := os.Symlink(to, filepath.Join(r.root, at)); err != nil {
			t.Fatal(err)
		}
	}
	v := TextVersion([]byte("outside"))
	for _, p := range []string{"link.txt", "door/secret.txt", "../" + filepath.Base(out) + "/secret.txt"} {
		if c, b := r.open(p); c < 400 || strings.Contains(fmt.Sprint(b["content"]), "outside") {
			t.Errorf("open %s: %d %v", p, c, b)
		}
		if c, b := r.save(p, v, "owned"); c < 400 || c >= 500 {
			t.Errorf("save %s: %d %v", p, c, b)
		}
	}
	for _, p := range []string{"door/new.txt", "door/sub/new.txt"} {
		if c, b := r.save(p, NoBase, "x"); c != 403 {
			t.Error(p, c, b)
		}
	}
	// A folder reached through a link that stays in the library is one.
	r.put(t, "dir/a.txt", "a")
	if err := os.Symlink("dir", filepath.Join(r.root, "alias")); err != nil {
		t.Fatal(err)
	}
	if c, b := r.save("alias/new.txt", NoBase, "x"); c != 200 || r.text(t, "dir/new.txt") != "x" {
		t.Error(c, b)
	}
	if c, _ := r.save("near.txt", TextVersion([]byte("inside")), "x"); c != 409 || r.text(t, "in.txt") != "inside" {
		t.Error(c)
	}
	if c, b := r.save("link.txt", NoBase, "x"); c != 409 || b["conflict"] != TextExists {
		t.Error(c, b)
	}
	if b, _ := os.ReadFile(secret); string(b) != "outside" {
		t.Fatalf("%q", b)
	}
	if list, _ := os.ReadDir(out); len(list) != 1 {
		t.Fatal(list)
	}
}

func TestTextIsNotEditedWhereTheLibraryIsReadOnly(t *testing.T) {
	r := newTextRig(t)
	r.put(t, "a.md", "a")
	old := MainWriter
	MainWriter = func(*http.Request) (bool, string) { return false, "only its administrators change it" }
	defer func() { MainWriter = old }()
	v := TextVersion([]byte("a"))
	for _, x := range []func() (int, map[string]any){
		func() (int, map[string]any) { return r.open("a.md") },
		func() (int, map[string]any) { return r.save("a.md", v, "b") },
		func() (int, map[string]any) { return r.save("n.md", NoBase, "b") },
	} {
		if c, b := x(); c != 403 || b["error"] != "only its administrators change it" {
			t.Errorf("%d %v", c, b)
		}
	}
	if r.text(t, "a.md") != "a" {
		t.Fatal("changed")
	}
}

// textStub is a person's library whose text is kept in memory.
type textStub struct {
	spaceStub
}

func (s *textStub) Library(r *http.Request, id string) (Library, error) {
	if id == "lent" {
		return Library{ID: "lent", Label: "Lent", IndexRoot: "remote:abc:lent", Site: "cluster", Space: true}, nil
	}
	return s.spaceStub.Library(r, id)
}
func (s *textStub) OpenText(_ *http.Request, _ Library, rel string, _ int) ([]byte, error) {
	b, ok := s.files[rel]
	if !ok {
		return nil, Error(404, "no such file")
	}
	s.note("open " + rel)
	return []byte(b), nil
}
func (s *textStub) CreateText(_ *http.Request, _ Library, rel string, b []byte) error {
	if _, ok := s.files[rel]; ok {
		return &TextConflict{State: TextExists}
	}
	s.files[rel] = string(b)
	s.note("create " + rel)
	return nil
}
func (s *textStub) SaveText(_ *http.Request, _ Library, rel string, b []byte, base string) error {
	if v := TextVersion([]byte(s.files[rel])); v != base {
		return &TextConflict{State: TextChanged, Version: v}
	}
	s.files[rel] = string(b)
	s.note("save " + rel)
	return nil
}

func TestTextInAPersonsLibraryIsWrittenAsThem(t *testing.T) {
	r := newTextRig(t)
	stub := &textStub{spaceStub{files: map[string]string{}}}
	oldR, oldS, oldT := LibraryResolver, SpaceFiles, SpaceText
	LibraryResolver, SpaceFiles, SpaceText = stub, stub, stub
	defer func() { LibraryResolver, SpaceFiles, SpaceText = oldR, oldS, oldT }()
	mine := func(method, p, base, body string) (int, map[string]any) {
		return r.do(method, map[string]string{"library": "mine", "path": p, "base": base}, body)
	}
	if c, b := mine("PUT", "n/a.md", NoBase, "a"); c != 200 || b["reindexing"] != true {
		t.Fatalf("%d %v", c, b)
	}
	if c, b := mine("PUT", "n/a.md", NoBase, "a"); c != 409 || b["conflict"] != TextExists {
		t.Fatalf("%d %v", c, b)
	}
	c, b := mine("GET", "n/a.md", "", "")
	if c != 200 || b["content"] != "a" {
		t.Fatalf("%d %v", c, b)
	}
	v := b["version"].(string)
	if c, b := mine("PUT", "n/a.md", v, "b"); c != 200 {
		t.Fatalf("%d %v", c, b)
	}
	if c, b := mine("PUT", "n/a.md", v, "c"); c != 409 || b["version"] != TextVersion([]byte("b")) {
		t.Fatalf("%d %v", c, b)
	}
	if got := strings.Join(stub.calls, "; "); got != "create n/a.md; changed n/a.md; open n/a.md; save n/a.md; changed n/a.md" {
		t.Error(got)
	}
	// Named in the combined tree, the library is the path's first part.
	if c, b := r.do("GET", map[string]string{"library": "all", "path": "mine/n/a.md"}, ""); c != 200 || b["content"] != "b" {
		t.Errorf("%d %v", c, b)
	}
	// A folder shared to read only, and a library that is not written here.
	for _, lib := range []string{"lent", "far"} {
		if c, _ := r.do("PUT", map[string]string{"library": lib, "path": "x.md", "base": NoBase}, "x"); c != 403 {
			t.Errorf("%s: %d", lib, c)
		}
	}
	if list, _ := os.ReadDir(r.root); len(list) != 0 {
		t.Errorf("this host was written: %v", list)
	}
}
