package kb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// partsBed is a Studio with a main library, its routes, and who a request
// is from in X-Test-Person.
type partsBed struct {
	t        *testing.T
	root, ix string
	m        *Module
	mux      *http.ServeMux
}

func newPartsBed(t *testing.T) *partsBed {
	t.Helper()
	b := &partsBed{t: t, root: t.TempDir(), ix: t.TempDir(), mux: http.NewServeMux()}
	b.m = b.module()
	b.m.Routes(b.mux)
	old := JobOwner
	JobOwner = func(r *http.Request) string { return r.Header.Get("X-Test-Person") }
	t.Cleanup(func() { JobOwner = old })
	return b
}

func (b *partsBed) module() *Module {
	env := map[string]string{"KB_ROOT": b.root, "INDEX_BASE": b.ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(b.ix, "no-gufi")}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		b.t.Fatal(err)
	}
	return New(app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
}

func (b *partsBed) do(person, method, url string, body io.Reader) (int, map[string]any) {
	b.t.Helper()
	req := httptest.NewRequest(method, url, body)
	req.Header.Set("X-Test-Person", person)
	if method == "POST" && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	b.mux.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (b *partsBed) start(person, query, path string, size int64) (int, map[string]any) {
	b.t.Helper()
	return b.do(person, "POST", "/api/kb/upload-parts?"+query, strings.NewReader(fmt.Sprintf(`{"path":%q,"size":%d}`, path, size)))
}

func (b *partsBed) started(person, query, path string, size int64) string {
	b.t.Helper()
	c, o := b.start(person, query, path, size)
	if c != 200 || o["part"] != float64(UploadPart) {
		b.t.Fatalf("start: %d %v", c, o)
	}
	return o["id"].(string)
}

func (b *partsBed) part(person, id string, n int, data []byte) (int, map[string]any) {
	b.t.Helper()
	return b.do(person, "PUT", fmt.Sprintf("/api/kb/upload-parts/%s/%d", id, n), bytes.NewReader(data))
}

// staged is what uploads under way have in the library.
func (b *partsBed) staged() []string {
	var out []string
	filepath.WalkDir(b.root, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".") && p != b.root {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// left fails the test when an upload left anything: in the library, or a
// record of it.
func (b *partsBed) left(what string) {
	b.t.Helper()
	if s := b.staged(); len(s) > 0 {
		b.t.Errorf("%s left %v", what, s)
	}
	if e, _ := os.ReadDir(filepath.Join(b.ix, "uploads")); len(e) > 0 {
		b.t.Errorf("%s left the record %v", what, e)
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(int64(n))).Read(b)
	return b
}

// over200MB is past what one request takes; no more than a few parts of it
// are sent in a test.
const over200MB = 300 << 20

// A file over the old limit goes up in parts, in any order, a part again
// after it failed; it takes its name only once whole, and replaces what
// was there.
func TestALargeFileGoesUpInParts(t *testing.T) {
	b := newPartsBed(t)
	data := randomBytes(2*UploadPart + 12345)
	os.MkdirAll(filepath.Join(b.root, "runs"), 0o755)
	os.WriteFile(filepath.Join(b.root, "runs", "data.tar"), []byte("before"), 0o644)
	id := b.started("", "dir=runs", "data.tar", int64(len(data)))
	dest := filepath.Join(b.root, "runs", "data.tar")

	s := b.staged()
	if len(s) != 1 || filepath.Dir(s[0]) != filepath.Join(b.root, "runs") {
		t.Fatalf("staged: %v", s)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(s[0]); st.Mode().Perm() != 0o700 {
			t.Fatalf("the parts are open to others: %v", st.Mode())
		}
	}
	slice := func(n int) []byte { return data[n*UploadPart : min((n+1)*UploadPart, len(data))] }
	if c, o := b.part("", id, 2, slice(2)); c != 200 || o["left"] != float64(2) {
		t.Fatalf("last part first: %d %v", c, o)
	}
	// A part cut short, one too long, one not in the file.
	if c, o := b.do("", "PUT", "/api/kb/upload-parts/"+id+"/0", io.LimitReader(bytes.NewReader(slice(0)), 1000)); c != 400 && c != 503 {
		t.Fatalf("a short part: %d %v", c, o)
	}
	if c, _ := b.part("", id, 0, append(append([]byte{}, slice(0)...), 'x')); c != 400 {
		t.Fatalf("a long part: %d", c)
	}
	if c, _ := b.part("", id, 3, []byte("x")); c != 400 {
		t.Fatalf("a part past the end: %d", c)
	}
	if c, o := b.do("", "POST", "/api/kb/upload-parts/"+id+"/finish", nil); c != 409 || !strings.Contains(o["error"].(string), "2 parts") {
		t.Fatalf("finished early: %d %v", c, o)
	}
	b.part("", id, 0, slice(0))
	if c, o := b.part("", id, 0, slice(0)); c != 200 || o["left"] != float64(1) {
		t.Fatalf("the same part again: %d %v", c, o)
	}
	b.part("", id, 1, slice(1))
	if got, _ := os.ReadFile(dest); string(got) != "before" {
		t.Fatal("the file was touched before it was whole")
	}
	c, o := b.do("", "POST", "/api/kb/upload-parts/"+id+"/finish", nil)
	if c != 200 || fmt.Sprint(o["saved"]) != "[runs/data.tar]" || o["deferred"] != true {
		t.Fatalf("finish: %d %v", c, o)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("the file is not what was sent")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(dest); st.Mode().Perm() != 0o666&^umask() {
			t.Fatalf("modes: %v", st.Mode())
		}
	}
	// Its answer lost, the page asks again.
	if c, o := b.do("", "POST", "/api/kb/upload-parts/"+id+"/finish", nil); c != 200 || fmt.Sprint(o["saved"]) != "[runs/data.tar]" {
		t.Fatalf("finish again: %d %v", c, o)
	}
	// Dropping it then removes nothing of it.
	b.do("", "DELETE", "/api/kb/upload-parts/"+id, nil)
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("a late drop touched the file")
	}
	b.left("a finished upload")
	if c, _ := b.part("", id, 0, slice(0)); c != 404 {
		t.Fatalf("a part after the end: %d", c)
	}
}

func TestALargeFileThatDoesNotFitIsRefusedBeforeItStarts(t *testing.T) {
	b := newPartsBed(t)
	c, o := b.start("", "dir=new/folder", "big.tar", 1<<52)
	if c != 507 || !strings.Contains(o["error"].(string), "not enough free space") || !strings.Contains(o["error"].(string), "4.0 PB") {
		t.Fatalf("%d %v", c, o)
	}
	if _, err := os.Stat(filepath.Join(b.root, "new")); err == nil {
		t.Error("a refused file made its folder")
	}
	b.left("a refused upload")
	// What is still to come of an upload under way is spoken for.
	free, ok := FreeBytes(b.root)
	if !ok || free < 8<<30 {
		t.Skip("little free space here")
	}
	b.started("", "dir=", "one.tar", free-free/4)
	if c, o := b.start("", "dir=", "two.tar", free-free/4); c != 507 {
		t.Fatalf("both fit: %d %v", c, o)
	}
}

func TestPartsAreTakenOnlyFromThePersonWhoStarted(t *testing.T) {
	b := newPartsBed(t)
	id := b.started("ada", "dir=in", "a.bin", over200MB)
	data := randomBytes(UploadPart)
	for _, who := range []string{"bob", ""} {
		if c, _ := b.part(who, id, 0, data); c != 404 {
			t.Errorf("%q wrote a part: %d", who, c)
		}
		if c, _ := b.do(who, "POST", "/api/kb/upload-parts/"+id+"/finish", nil); c != 404 {
			t.Errorf("%q finished it: %d", who, c)
		}
		if c, _ := b.do(who, "DELETE", "/api/kb/upload-parts/"+id, nil); c != 404 {
			t.Errorf("%q dropped it: %d", who, c)
		}
	}
	if c, _ := b.part("ada", id, 0, data); c != 200 {
		t.Fatalf("ada: %d", c)
	}
	// Ada may no longer change the main library.
	old := MainWriter
	MainWriter = func(r *http.Request) (bool, string) { return false, "read only for you" }
	defer func() { MainWriter = old }()
	if c, _ := b.part("ada", id, 1, data); c != 403 {
		t.Fatalf("after her rights went: %d", c)
	}
	if c, _ := b.start("ada", "dir=in", "b.bin", over200MB); c != 403 {
		t.Fatalf("a new one: %d", c)
	}
	MainWriter = old
	if c, _ := b.do("ada", "DELETE", "/api/kb/upload-parts/"+id, nil); c != 200 {
		t.Fatalf("drop: %d", c)
	}
	b.left("a dropped upload")
}

func TestPartUploadsRefuseWhatAnUploadRefuses(t *testing.T) {
	b := newPartsBed(t)
	os.MkdirAll(filepath.Join(b.root, "taken"), 0o755)
	for _, c := range []struct{ dir, path string }{
		{".git", "a"}, {"..", "a"}, {"a/../..", "a"}, {"x", ".hidden"}, {"x", "a/.b/c"}, {"x", "../../etc/passwd"}, {"x", ""}, {"", "taken"},
	} {
		if code, o := b.start("", "dir="+c.dir, c.path, over200MB); code < 400 || code >= 500 {
			t.Errorf("%q %q: %d %v", c.dir, c.path, code, o)
		}
	}
	for _, size := range []int64{0, -1} {
		if code, _ := b.start("", "dir=", "a", size); code != 400 {
			t.Errorf("size %d: %d", size, code)
		}
	}
	for i := 0; i < maxOpenUploads; i++ {
		b.started("ada", "dir=", fmt.Sprintf("f%d", i), over200MB)
	}
	if code, _ := b.start("ada", "dir=", "more", over200MB); code != 429 {
		t.Errorf("too many: %d", code)
	}
	b.started("bob", "dir=", "bobs", over200MB)
	if !b.m.up.active() {
		t.Error("an update would restart over an upload")
	}
	b.m.stopUploads()
	b.left("a Studio that stopped")
	if b.m.up.active() {
		t.Error("still active")
	}
}

func TestAnUploadThatStopsComingIsRemoved(t *testing.T) {
	b := newPartsBed(t)
	old := UploadIdle
	UploadIdle = 100 * time.Millisecond
	defer func() { UploadIdle = old }()
	id := b.started("", "dir=", "a.bin", over200MB)
	for i := 0; i < 100 && len(b.staged()) > 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	b.left("an upload that stopped coming")
	if c, _ := b.part("", id, 0, randomBytes(UploadPart)); c != 404 {
		t.Fatalf("a part after: %d", c)
	}
}

// What a Studio that was killed left is removed when it starts again, and
// nothing a changed record could name outside the library.
func TestUploadsAnEarlierRunLeftAreRemoved(t *testing.T) {
	b := newPartsBed(t)
	b.started("", "dir=deep/er", "a.bin", over200MB)
	outside := filepath.Join(t.TempDir(), ".studio-upload-x")
	os.MkdirAll(outside, 0o755)
	os.MkdirAll(filepath.Join(b.root, "kept"), 0o755)
	os.WriteFile(filepath.Join(b.ix, "uploads", "o.json"), fmt.Appendf(nil, `{"id":"o","dir":%q}`, outside), 0o600)
	os.WriteFile(filepath.Join(b.ix, "uploads", "k.json"), fmt.Appendf(nil, `{"id":"k","dir":%q}`, filepath.Join(b.root, "kept")), 0o600)
	b.module().clearUploads()
	b.left("an earlier run")
	for _, p := range []string{outside, filepath.Join(b.root, "kept")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("removed %s", p)
		}
	}
}

// A part whose connection hangs is given up for the same part sent again.
func TestAPartSentAgainReplacesOneThatHangs(t *testing.T) {
	b := newPartsBed(t)
	srv := httptest.NewServer(b.mux)
	defer srv.Close()
	id := b.started("", "dir=", "a.bin", UploadPart)
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT /api/kb/upload-parts/%s/0 HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\nhalf a part", id, UploadPart)
	u := b.m.up.open[id]
	for i := 0; i < 200; i++ {
		u.mu.Lock()
		n := len(u.busy)
		u.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data := randomBytes(UploadPart)
	began := time.Now()
	req, _ := http.NewRequest("PUT", srv.URL+"/api/kb/upload-parts/"+id+"/0", bytes.NewReader(data))
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 || time.Since(began) > 20*time.Second {
		t.Fatalf("%v %v after %v", res, err, time.Since(began))
	}
	if c, o := b.do("", "POST", "/api/kb/upload-parts/"+id+"/finish", nil); c != 200 {
		t.Fatalf("finish: %d %v", c, o)
	}
	if got, _ := os.ReadFile(filepath.Join(b.root, "a.bin")); !bytes.Equal(got, data) {
		t.Fatal("not what was sent")
	}
	b.left("it")
}

// A connection cut part way counts for nothing, and the upload goes on.
func TestAPartWhoseConnectionIsCutIsNotCounted(t *testing.T) {
	b := newPartsBed(t)
	srv := httptest.NewServer(b.mux)
	defer srv.Close()
	id := b.started("", "dir=", "a.bin", UploadPart)
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "PUT /api/kb/upload-parts/%s/0 HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", id, UploadPart)
	conn.Write(make([]byte, UploadPart/2))
	conn.Close()
	time.Sleep(200 * time.Millisecond)
	if c, _ := b.do("", "POST", "/api/kb/upload-parts/"+id+"/finish", nil); c != 409 {
		t.Fatalf("finished on half: %d", c)
	}
	data := randomBytes(UploadPart)
	if c, _ := b.part("", id, 0, data); c != 200 {
		t.Fatalf("again: %d", c)
	}
	b.do("", "POST", "/api/kb/upload-parts/"+id+"/finish", nil)
	if got, _ := os.ReadFile(filepath.Join(b.root, "a.bin")); !bytes.Equal(got, data) {
		t.Fatal("not what was sent")
	}
}

// In a person's space the same runs through SpaceFiles, as them.
func TestALargeFileInASpaceRunsThroughSpaceFiles(t *testing.T) {
	b := newPartsBed(t)
	stub := &spaceStub{files: map[string]string{}}
	oldR, oldS := LibraryResolver, SpaceFiles
	LibraryResolver, SpaceFiles = stub, stub
	defer func() { LibraryResolver, SpaceFiles = oldR, oldS }()
	id := b.started("ada", "library=mine&dir=in/box", "sub/a.bin", UploadPart+3)
	b.part("ada", id, 1, []byte("end"))
	if c, _ := b.part("bob", id, 0, randomBytes(UploadPart)); c != 404 {
		t.Fatalf("bob: %d", c)
	}
	b.part("ada", id, 0, randomBytes(UploadPart))
	if c, o := b.do("ada", "POST", "/api/kb/upload-parts/"+id+"/finish", nil); c != 200 || o["reindexing"] != true || fmt.Sprint(o["saved"]) != "[in/box/sub/a.bin]" {
		t.Fatalf("finish: %d %v", c, o)
	}
	gone := b.started("ada", "library=mine&dir=", "b.bin", over200MB)
	b.do("ada", "DELETE", "/api/kb/upload-parts/"+gone, nil)
	if c, _ := b.start("ada", "library=far&dir=", "x", over200MB); c != 403 {
		t.Fatalf("a library written by no one: %d", c)
	}
	if c, _ := b.start("ada", "library=mine&dir=../x", "x", over200MB); c != 400 {
		t.Fatalf("out of the space: %d", c)
	}
	want := fmt.Sprintf("start in/box/sub/a.bin %d %d|part %d 3|part 0 %d|land in/box/sub/a.bin %d|changed in/box/sub/a.bin|start b.bin %d %d|drop",
		UploadPart+3, UploadPart+3, UploadPart, UploadPart, UploadPart+3, over200MB, over200MB)
	if got := strings.Join(stub.calls, "|"); got != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
	}
	b.left("uploads in a space")
	if e, _ := os.ReadDir(b.root); len(e) > 0 {
		t.Fatalf("this host's folder was written: %v", e)
	}

	// One an earlier run left there is removed, as her, when she next adds
	// a large file.
	b.started("ada", "library=mine&dir=", "c.bin", over200MB)
	again := b.module()
	again.clearUploads()
	mux := http.NewServeMux()
	again.Routes(mux)
	b.m, b.mux = again, mux
	stub.calls = nil
	b.started("bob", "library=mine&dir=", "d.bin", over200MB)
	if len(stub.calls) != 1 {
		t.Fatalf("bob's upload cleaned up hers: %v", stub.calls)
	}
	b.started("ada", "library=mine&dir=", "e.bin", over200MB)
	for i := 0; i < 100; i++ {
		if strings.Contains(strings.Join(stub.noted(), "|"), "drop") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("not removed: %v", stub.noted())
}

// A part refused before its body is read is still read to its end, so the
// page hears why and not a connection that dropped.
func TestARefusedPartIsAnsweredNotCutOff(t *testing.T) {
	b := newPartsBed(t)
	srv := httptest.NewServer(b.mux)
	defer srv.Close()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	fmt.Fprintf(c, "PUT /api/kb/upload-parts/%s/0 HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", strings.Repeat("0", 32), UploadPart)
	if n, err := c.Write(make([]byte, UploadPart)); err != nil {
		t.Fatalf("cut off after %d bytes: %v", n, err)
	}
	line := make([]byte, 12)
	if _, err := io.ReadFull(c, line); err != nil || string(line) != "HTTP/1.1 404" {
		t.Fatalf("answered %q, %v", line, err)
	}
}

// What has arrived of a file is read by no one, the administrator included,
// until it has its name.
func TestAFileStillArrivingIsNotRead(t *testing.T) {
	b := newPartsBed(t)
	id := b.started("ada", "dir=", "big.bin", UploadPart+9)
	if c, o := b.do("ada", "PUT", "/api/kb/upload-parts/"+id+"/0", bytes.NewReader(bytes.Repeat([]byte("a"), UploadPart))); c != 200 {
		t.Fatal(c, o)
	}
	m, _ := filepath.Glob(filepath.Join(b.root, partsPrefix+"*"))
	if len(m) != 1 {
		t.Fatal(m)
	}
	rel := filepath.Base(m[0]) + "/data"
	for _, u := range []string{"file", "raw", "download", "details"} {
		for _, p := range []string{rel, "./" + rel, "x/../" + rel} {
			if c, _ := b.do("ada", "GET", "/api/kb/"+u+"?path="+p, nil); c != 404 {
				t.Errorf("%s %s answers %d", u, p, c)
			}
		}
	}
	if c, _ := b.do("ada", "GET", "/api/kb/dir?path="+filepath.Base(m[0]), nil); c == 200 {
		t.Error("its folder is listed")
	}
}

// slowSpace takes a part at its own pace, and what came of it.
type slowSpace struct {
	SpaceFS
	wait time.Duration
	err  error
}

func (s *slowSpace) StartParts(*http.Request, Library, string, int64, int64) (string, error) {
	return partsPrefix + "0", nil
}
func (s *slowSpace) DropParts(*http.Request, Library, string) error { return nil }
func (s *slowSpace) WritePart(_ *http.Request, _ Library, _ string, _, n int64, src io.Reader) error {
	half := make([]byte, n/2)
	io.ReadFull(src, half)
	time.Sleep(s.wait)
	if s.err != nil {
		return s.err
	}
	_, err := io.CopyN(io.Discard, src, n-n/2)
	return err
}

// A part that is slow to write begins its answer, for what waits in front of
// the Studio not to give up on it; its body says how it ended.
func TestAPartSlowToWriteBeginsItsAnswer(t *testing.T) {
	oldWait := partWait
	partWait = 50 * time.Millisecond
	defer func() { partWait = oldWait }()
	for _, tc := range []struct {
		name string
		wait time.Duration
		err  error
		code int
		want string
	}{
		{"quick", 0, nil, 200, `{"part":0,"left":1}`},
		{"quick, refused", 0, Error(403, "no"), 403, `{"error":"no"}`},
		{"slow", 400 * time.Millisecond, nil, 200, `{"part":0,"left":1}`},
		{"slow, refused", 400 * time.Millisecond, Error(403, "no"), 200, `{"error":"no","status":403}`},
		{"slow, cut", 400 * time.Millisecond, io.ErrUnexpectedEOF, 200, `{"error":"the part did not arrive whole","status":503}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newPartsBed(t)
			stub := &spaceStub{files: map[string]string{}}
			oldR, oldS := LibraryResolver, SpaceFiles
			LibraryResolver, SpaceFiles = stub, &slowSpace{SpaceFS: stub, wait: tc.wait, err: tc.err}
			defer func() { LibraryResolver, SpaceFiles = oldR, oldS }()
			id := b.started("ada", "library=mine&dir=", "big.bin", UploadPart+9)
			srv := httptest.NewServer(b.mux)
			defer srv.Close()
			req, _ := http.NewRequest("PUT", srv.URL+"/api/kb/upload-parts/"+id+"/0", bytes.NewReader(make([]byte, UploadPart)))
			req.Header.Set("X-Test-Person", "ada")
			began := time.Now()
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			head := time.Since(began)
			body, err := io.ReadAll(res.Body)
			if err != nil || res.StatusCode != tc.code || strings.TrimSpace(string(body)) != tc.want {
				t.Fatalf("%d %q %v", res.StatusCode, body, err)
			}
			if tc.wait > 0 && head >= tc.wait {
				t.Fatalf("the answer began after %s", head)
			}
			if tc.wait > 0 && body[0] != ' ' {
				t.Fatal("did not begin early")
			}
		})
	}
}
