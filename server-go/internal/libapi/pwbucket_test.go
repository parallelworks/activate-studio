package libapi

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// A platform with one bucket each for two people, as the pw CLI and S3
// answer: pw knows a person by the credential it is run with, and gives a
// token of theirs; the store takes only tokens the bucket's owner was given,
// each for a number of requests.
type fakePlatform struct {
	t    *testing.T
	dir  string
	base string
	mu   sync.Mutex
	log  []string
	left map[string]int // token: requests it still serves, below 0 no end
	uses int            // of each new token
}

func newFakePlatform(t *testing.T) *fakePlatform {
	p := &fakePlatform{t: t, dir: t.TempDir(), left: map[string]int{}, uses: -1}
	ts := httptest.NewServer(p)
	t.Cleanup(ts.Close)
	p.base = ts.URL
	put(t, filepath.Join(p.dir, "pw"), `#!/bin/sh
D='`+p.dir+`'
while [ $# -gt 0 ]; do case "$1" in --context|--platform-host) shift 2;; *) break;; esac; done
shift; cmd=$1; for a; do last=$a; done
case "$PW_API_KEY" in key-*) who=${PW_API_KEY#key-};; *) echo "$cmd as '$PW_API_KEY'" >> "$D/strangers"; echo "x [ERROR] unauthorized" >&2; exit 1;; esac
echo "$who $cmd $last" >> "$D/runs"
env >> "$D/envs"; echo "$HOME" >> "$D/homes"; echo cached-token > "$HOME/credentials"
own() { case "$1" in pw://$who/*) ;; *) echo "x [ERROR] storage not found: ${1##*/}" >&2; exit 1;; esac; }
case "$cmd" in
get-token) own "$last"; [ -f "$D/no-token" ] && { echo "x [ERROR] no token for you" >&2; exit 1; }
  curl -s "`+p.base+`/mint?who=$who";;
ls) if [ "$last" = json ]; then echo '[{"name":"papers","namespace":"'$who'","displayName":"Papers of '$who'","csp":"'$(cat "$D/csp" 2>/dev/null || echo aws)'","region":"us-east-1","bucketName":"store-'$who'","uri":"pw://'$who'/papers","status":"provisioned"},{"name":"old","namespace":"'$who'","csp":"oracle","uri":"pw://'$who'/old","status":"provisioned"}]'
  else own "$last"; curl -s "`+p.base+`/cli-ls?who=$who&at=$last"; fi;;
cat) own "$last"; curl -sf "`+p.base+`/cli-cat?who=$who&at=$last" || { echo "x [ERROR] no such object" >&2; exit 1; };;
esac
`)
	os.Chmod(filepath.Join(p.dir, "pw"), 0o755)
	t.Setenv("PW_CLI", filepath.Join(p.dir, "pw"))
	t.Setenv("OPENAI_BASE_URL", "https://platform.example/api/openai/v1")
	t.Setenv("OPENAI_API_KEY", "")
	return p
}

// object puts an object in a person's bucket.
func (p *fakePlatform) object(who, key, body string) {
	put(p.t, filepath.Join(p.dir, "stores", who, filepath.FromSlash(key)), body)
}

func (p *fakePlatform) keys(who string) []string {
	root := filepath.Join(p.dir, "stores", who)
	var out []string
	filepath.WalkDir(root, func(f string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, f)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func (p *fakePlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := r.URL.Query()
	if r.URL.Path == "/mint" {
		tok := fmt.Sprintf("tok-%s-%d", q.Get("who"), len(p.left))
		p.left[tok] = p.uses
		fmt.Fprintf(w, "export BUCKET_URI='s3://store-%s'\nexport AWS_ACCESS_KEY_ID='AKIAEXAMPLE'\nexport AWS_SECRET_ACCESS_KEY='s'\nexport AWS_SESSION_TOKEN='%s'\n", q.Get("who"), tok)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/cli-") {
		who := q.Get("who")
		at := strings.TrimPrefix(q.Get("at"), "pw://"+who+"/papers/")
		if r.URL.Path == "/cli-cat" {
			http.ServeFile(w, r, filepath.Join(p.dir, "stores", who, at))
			return
		}
		type e struct {
			URI       string `json:"uri"`
			Size      int64  `json:"size"`
			Modified  string `json:"modified"`
			Directory bool   `json:"directory,omitempty"`
		}
		out, seen := []e{}, map[string]bool{}
		for _, k := range p.keys(who) {
			rest, ok := strings.CutPrefix(k, at)
			if !ok {
				continue
			}
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				if d := at + rest[:i+1]; !seen[d] {
					seen[d] = true
					out = append(out, e{URI: "pw://" + who + "/papers/" + d, Directory: true})
				}
				continue
			}
			st, _ := os.Stat(filepath.Join(p.dir, "stores", who, k))
			out = append(out, e{URI: "pw://" + who + "/papers/" + k, Size: st.Size(), Modified: st.ModTime().UTC().Format(time.RFC3339)})
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	tok := r.Header.Get("X-Amz-Security-Token")
	p.log = append(p.log, tok+" "+bucket+"/"+key+" "+r.Header.Get("Range"))
	who := strings.TrimPrefix(bucket, "store-")
	fail := func(status int, code string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
	}
	left, ok := p.left[tok]
	switch {
	case !ok || !strings.HasPrefix(tok, "tok-"+who+"-") || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 "):
		fail(403, "AccessDenied")
		return
	case left == 0:
		fail(400, "ExpiredToken")
		return
	}
	p.left[tok] = left - 1
	if key != "" {
		http.ServeFile(w, r, filepath.Join(p.dir, "stores", who, key))
		return
	}
	type content struct {
		Key          string
		Size         int64
		LastModified string
	}
	type prefix struct{ Prefix string }
	var x struct {
		CommonPrefixes        []prefix
		XMLName               xml.Name `xml:"ListBucketResult"`
		EncodingType          string
		IsTruncated           bool
		NextContinuationToken string
		Contents              []content
	}
	x.EncodingType = "url"
	start := q.Get("start-after")
	if t := q.Get("continuation-token"); t != "" {
		start = strings.TrimPrefix(t, tok+"/")
		if start == t {
			fail(400, "InvalidArgument")
			return
		}
	}
	const page = 3
	if q.Get("delimiter") != "" {
		x.EncodingType = ""
		var seen []string
		for _, k := range p.keys(who) {
			rest, ok := strings.CutPrefix(k, q.Get("prefix"))
			if d, _, sub := strings.Cut(rest, "/"); ok && sub && !slices.Contains(seen, d) {
				seen = append(seen, d)
				x.CommonPrefixes = append(x.CommonPrefixes, prefix{q.Get("prefix") + d + "/"})
			} else if ok && !sub {
				x.Contents = append(x.Contents, content{k, 1, time.Now().UTC().Format(time.RFC3339)})
			}
		}
		xml.NewEncoder(w).Encode(x)
		return
	}
	for _, k := range p.keys(who) {
		if k <= start && start != "" || !strings.HasPrefix(k, q.Get("prefix")) {
			continue
		}
		if len(x.Contents) == page {
			x.IsTruncated, x.NextContinuationToken = true, tok+"/"+start
			break
		}
		st, _ := os.Stat(filepath.Join(p.dir, "stores", who, k))
		x.Contents = append(x.Contents, content{url.QueryEscape(k), st.Size(), st.ModTime().UTC().Format(time.RFC3339)})
		start = k
	}
	xml.NewEncoder(w).Encode(x)
}

func (p *fakePlatform) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.log)
}

func (p *fakePlatform) file(name string) string {
	b, _ := os.ReadFile(filepath.Join(p.dir, name))
	return string(b)
}

// clean checks that no run was made as anyone but ada or bob, that every
// HOME of a run is gone, and that nothing under dir holds a credential.
func (p *fakePlatform) clean(dir string) {
	p.t.Helper()
	if s := p.file("strangers"); s != "" {
		p.t.Errorf("pw was run with another credential: %s", s)
	}
	for _, h := range strings.Fields(p.file("homes")) {
		if _, err := os.Stat(h); !os.IsNotExist(err) {
			p.t.Errorf("%s was left behind", h)
		}
	}
	filepath.WalkDir(dir, func(f string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		b, _ := os.ReadFile(f)
		for _, secret := range []string{"tok-ada", "tok-bob", "tok-me", "key-ada", "key-bob", "key-me", "AKIAEXAMPLE"} {
			if strings.Contains(string(b), secret) {
				p.t.Errorf("%s holds %s", f, secret)
			}
		}
		return nil
	})
}

func (f *buildFixture) extractBins() {
	exe, _ := os.Executable()
	bin := filepath.Join(f.dir, "bin", "index-extract")
	put(f.t, bin, "#!/bin/sh\nLIBAPI_TEST_CHILD=index-extract exec '"+exe+"' \"$@\"\n")
	os.Chmod(bin, 0o755)
	f.t.Setenv("STUDIO_INDEX_EXTRACT_BIN", bin)
}

func needTrace(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(testGufi, "gufi_trace2index")); err != nil {
		t.Skip("GUFI has no gufi_trace2index")
	}
}

// An ACTIVATE bucket on a Studio of one, end to end: offered and listed with
// the Studio's pw sign-in, with no rclone; indexed from a flat listing, a
// request a page, though its credential ends on the way; opened by ranges;
// its text read through a socket; its source recorded with the index; no
// credential written anywhere.
func TestPWBucketLibrary(t *testing.T) {
	needTrace(t)
	p := newFakePlatform(t)
	t.Setenv("PW_API_KEY", "key-me")
	f := newBuildFixture(t)
	f.extractBins()
	kb.Register(f.mux, f.a)
	f.s.builds.pwb.base = p.base
	p.object("me", "readme.txt", "top")
	p.object("me", "projects/alpha/summary.txt", "alpha results: the marmoset trials")
	p.object("me", "projects/beta/plan.md", "# Beta plan\n"+strings.Repeat("wind tunnel runs\n", 50000))
	for i := range 8 {
		p.object("me", "projects/beta/raw/zephyr-run"+strconv.Itoa(i)+".bin", strings.Repeat("x", 100+i))
	}
	p.uses = 2

	if _, b := f.call("GET", "/api/libraries", nil); b["buckets"] != true {
		t.Fatalf("buckets are offered: %v", b["buckets"])
	}
	code, b := f.call("GET", "/api/libraries/buckets", nil)
	list := b["buckets"].([]any)
	if code != 200 || b["available"] != true || len(list) != 2 || list[0].(map[string]any)["label"] != "Papers of me" || list[1].(map[string]any)["why"] == nil {
		t.Fatalf("buckets: %d %v", code, b)
	}
	code, b = f.call("POST", "/api/libraries/buckets/folders", map[string]any{"uri": "pw://me/papers", "path": "projects"})
	if code != 200 || fmt.Sprint(b["dirs"]) != "[alpha beta]" || b["parent"] != "" || b["objects"] != float64(0) {
		t.Fatalf("folders: %d %v", code, b)
	}
	for body, want := range map[string]int{`{"bucket":"pw://me/papers/x"}`: 400, `{"bucket":"pw://ada/papers"}`: 404, `{"bucket":"pw://me/old"}`: 400, `{"bucket":"pw://me/papers","path":"../x"}`: 400, `{"bucket":"pw://me/papers","every":7}`: 400} {
		var m map[string]any
		json.Unmarshal([]byte(body), &m)
		if code, b := f.call("POST", "/api/libraries/bucket", m); code != want || f.library("papers-of-me") != nil {
			t.Errorf("%s: %d %v", body, code, b)
		}
	}

	p.mu.Lock()
	p.log, p.left = nil, map[string]int{}
	p.mu.Unlock()
	f.s.builds.pwb.forget("")
	code, b = f.call("POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://me/papers", "every": 60})
	if code != 200 {
		t.Fatalf("add: %d %v", code, b)
	}
	id := b["library"].(map[string]any)["id"].(string)
	l := f.waitReady(id)
	bk := l["bucket"].(map[string]any)
	if id != "papers-of-me" || bk["objects"] != float64(11) || bk["route"] != "direct" || bk["provider"] != "AWS" || bk["location"] != "pw://me/papers" || bk["error"] != nil {
		t.Fatalf("%s: %v", id, l)
	}
	// The folder looked at to add it; then four pages of three, and the
	// two refusals of a credential that ended.
	if bk["requests"] != float64(len(p.requests())-1) || len(p.requests()) > 8 {
		t.Errorf("requests %v: %v", bk["requests"], p.requests())
	}
	if code, b := f.call("POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://me/papers"}); code != 409 {
		t.Errorf("the same again: %d %v", code, b)
	}
	if l["access"].(map[string]any)["readAs"] != "you" {
		t.Errorf("access %v", l["access"])
	}
	if h := f.hits("zephyr-run3", id); !slices.Equal(h, []string{"projects/beta/raw/zephyr-run3.bin"}) {
		t.Fatalf("hits %v", h)
	}

	// Opening a file reads what is shown, and no more.
	p.mu.Lock()
	p.log, p.uses = nil, -1
	p.mu.Unlock()
	code, b = f.call("GET", "/api/kb/file?library="+id+"&path="+url.QueryEscape("projects/beta/plan.md"), nil)
	if code != 200 || b["truncated"] != true || b["size"] != float64(850012) {
		t.Fatalf("preview: %d %v", code, b["size"])
	}
	for _, q := range p.requests() {
		if !strings.Contains(q, " bytes=0-") {
			t.Errorf("a whole object was asked for: %s", q)
		}
	}
	if code, b := f.call("GET", "/api/kb/file?library="+id+"&path=gone.txt", nil); code != 404 {
		t.Errorf("missing: %d %v", code, b)
	}

	// Its text, through the socket, with the credential ending on the way.
	p.mu.Lock()
	p.uses = 2
	p.mu.Unlock()
	if code, b := f.call("PUT", "/api/libraries/"+id+"/bucket", map[string]any{"text": true}); code != 200 {
		t.Fatalf("text: %d %v", code, b)
	}
	bk = f.waitText(id)
	pass, _ := bk["textPass"].(map[string]any)
	if pass == nil || pass["error"] != nil || pass["files"] != float64(3) || bk["fsid"] != "s3.us-east-1.amazonaws.com_store-me" {
		t.Fatalf("text: %v", bk)
	}
	if h := f.hits("marmoset", id); !slices.Equal(h, []string{"projects/alpha/summary.txt"}) {
		t.Fatalf("by its words: %v", h)
	}

	// Where the index came from is kept with it.
	var m struct {
		Source string
		Bucket map[string]any
	}
	raw, _ := os.ReadFile(filepath.Join(f.base, "libraries", id, "index", manifestName))
	json.Unmarshal(raw, &m)
	if m.Bucket["uri"] != "pw://me/papers/" || m.Bucket["provider"] != "AWS" || m.Bucket["region"] != "us-east-1" || m.Bucket["bucket"] != "store-me" || m.Bucket["listedAt"] == nil || m.Bucket["objects"] != float64(11) {
		t.Errorf("recorded %v", m)
	}
	p.clean(f.dir)

	// A store that refuses the token is read through the pw CLI.
	put(t, filepath.Join(p.dir, "no-token"), "")
	if code, b := f.call("POST", "/api/libraries/"+id+"/rebuild", nil); code != 200 {
		t.Fatalf("again: %d %v", code, b)
	}
	bk = f.waitText(id)
	if bk["route"] != "pw" || bk["objects"] != float64(11) || bk["routeWhy"] != "no token for you" || bk["error"] != nil {
		t.Errorf("through the CLI: %v", bk)
	}
	copies := filepath.Join(f.a.Cfg.IndexBase, "remote-previews", id)
	if err := os.MkdirAll(copies, 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := os.Stat(copies); err == nil {
			t.Error("the copies of files opened stayed after the library was removed")
		}
	}()
	if code, b = f.call("DELETE", "/api/libraries/"+id, nil); code != 200 {
		t.Errorf("remove: %d %v", code, b)
	}
}

// signInFixture is a Studio where people sign in: calls are made as a
// person, whose credential is in the key store (keys).
type signInFixture struct {
	// origin is the page a request comes from (as).
	origin string
	*buildFixture
	keys map[string]string
	logs *lockedLog
}

// lockedLog is a log that is read while it is written.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(b)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newSignInFixture(t *testing.T, p *fakePlatform) *signInFixture {
	t.Helper()
	needTrace(t)
	// The deployment's own sign-in, which nothing here may use.
	t.Setenv("PW_API_KEY", "deployment")
	dir := t.TempDir()
	f := &signInFixture{buildFixture: &buildFixture{t: t, dir: dir, kb: filepath.Join(dir, "kb"), base: filepath.Join(dir, "ix")}, keys: map[string]string{}, logs: &lockedLog{}}
	put(t, filepath.Join(f.kb, "main.md"), "# main\n")
	t.Setenv("HOME", dir)
	env := map[string]string{"KB_ROOT": f.kb, "INDEX_BASE": f.base, "GUFI_BIN": testGufi, "STUDIO_NODE": "off", "SWEEP_INTERVAL_SEC": "0", "AUTH_JWKS_URL": "http://127.0.0.1:1/jwks", "AUTH_REQUIRED": "1"}
	// The strict posture: everyone signs in and brings their own credential.
	t.Setenv("REQUIRE_PERSONAL_KEY", "1")
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	f.a = app.New(cfg, slog.New(slog.NewTextHandler(f.logs, nil)), nil, nil)
	f.mux = http.NewServeMux()
	index.Register(f.mux, f.a)
	Register(f.mux, f.a)
	kb.Register(f.mux, f.a)
	f.s = New(f.a)
	f.a.Providers.SignInKey = func(sub string) string { return f.keys[sub] }
	f.s.builds.pwb.base = p.base
	t.Cleanup(func() { f.s.builds.stop() })
	return f
}

// as makes a call as a person; browser is a key their browser carries that
// the Studio does not keep.
func (f *signInFixture) as(who, browser, method, u string, body any) (int, map[string]any) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	r := httptest.NewRequest(method, u, rd)
	r.Header.Set("Content-Type", "application/json")
	v := &platform.Viewer{AuthEnabled: true}
	if who != "" {
		r = auth.WithUser(r, &auth.User{ID: who, Username: who})
		v.User, v.SignIn = &platform.User{ID: who, Username: who}, f.keys[who]
		if browser != "" {
			v.Cred = &platform.Cred{Key: browser}
		}
	}
	r = platform.WithViewer(r, v)
	if f.origin != "" {
		r.Header.Set("Origin", f.origin)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func (f *signInFixture) row(who, id string) map[string]any {
	_, b := f.as(who, "", "GET", "/api/libraries", nil)
	ls, _ := b["libraries"].([]any)
	for _, l := range ls {
		if m := l.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

func (f *signInFixture) wait(who, id string) map[string]any {
	f.t.Helper()
	for range 600 {
		if l := f.row(who, id); l != nil && f.s.builds.jobOf(id) == nil {
			if st := l["build"].(map[string]any)["state"]; st == "ready" || st == "failed" {
				return l
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.t.Fatal("the build did not end")
	return nil
}

// Two people on one Studio, each with a bucket. Each sees, searches, opens,
// changes and removes their own alone, read with their own sign-in alone;
// nothing of one is in what the other, or the Studio's logs and shared
// files, can show.
func TestPWBucketsOfTwoPeople(t *testing.T) {
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	f.extractBins()
	f.keys["ada"], f.keys["bob"] = "key-ada", "key-bob"
	p.object("ada", "plans/secret-otter.txt", "the otter launches in april")
	p.object("bob", "plans/bob-heron.txt", "the heron stays")

	// rclone's route stays off, and nobody not signed in gets anywhere.
	if code, _ := f.as("ada", "", "POST", "/api/libraries/bucket", map[string]any{"location": "rclone:store"}); code != 403 {
		t.Errorf("an rclone remote with sign-in on: %d", code)
	}
	for _, c := range [][2]string{{"GET", "/api/libraries/buckets"}, {"POST", "/api/libraries/buckets/folders"}, {"POST", "/api/libraries/bucket"}} {
		if code, b := f.as("", "", c[0], c[1], map[string]any{"bucket": "pw://ada/papers"}); code != 401 {
			t.Errorf("%s %s not signed in: %d %v", c[0], c[1], code, b)
		}
	}
	// Without a platform sign-in of their own a person has no buckets: the
	// deployment's is not lent.
	if code, b := f.as("cy", "", "GET", "/api/libraries/buckets", nil); code != 200 || b["available"] != false || !strings.Contains(b["why"].(string), "Sign in with ACTIVATE") {
		t.Errorf("cy: %d %v", code, b)
	}
	if code, b := f.as("cy", "", "GET", "/api/libraries", nil); code != 200 || b["buckets"] != true {
		t.Errorf("cy is not told of buckets: %d %v", code, b["buckets"])
	}
	if code, b := f.as("cy", "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers"}); code != 409 {
		t.Errorf("cy adds: %d %v", code, b)
	}
	if code, b := f.as("bob", "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers"}); code != 404 {
		t.Errorf("bob adds ada's: %d %v", code, b)
	}

	for _, e := range []string{"https://169.254.169.254", "https://127.0.0.1:8443", "http://store.example", "https://vpce-0example.s3.us-east-1.vpce.amazonaws.com", "https://storage-example.p.googleapis.com"} {
		if code, b := f.as("ada", "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers", "endpoint": e}); code != 400 {
			t.Errorf("endpoint %s: %d %v", e, code, b)
		}
		if code, b := f.as("ada", "", "POST", "/api/libraries/buckets/folders", map[string]any{"uri": "pw://ada/papers", "endpoint": e}); code != 400 {
			t.Errorf("looking at endpoint %s: %d %v", e, code, b)
		}
	}
	add := func(who string) string {
		code, b := f.as(who, "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://" + who + "/papers"})
		if code != 200 {
			t.Fatalf("%s adds: %d %v", who, code, b)
		}
		id := b["library"].(map[string]any)["id"].(string)
		f.wait(who, id)
		if code, b := f.as(who, "", "PUT", "/api/libraries/"+id+"/bucket", map[string]any{"text": true}); code != 200 {
			t.Fatalf("%s text: %d %v", who, code, b)
		}
		for range 600 {
			l := f.row(who, id)
			if bk := l["bucket"].(map[string]any); bk["textPass"] != nil && f.s.builds.jobOf(id) == nil {
				return id
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("no text")
		return ""
	}
	if code, b := f.as("ada", "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers", "path": "palns"}); code != 404 {
		t.Errorf("a folder that is not there: %d %v", code, b)
	}
	idA, idB := add("ada"), add("bob")
	// A page of another site changes nothing of hers.
	f.origin = "https://elsewhere.example"
	for _, c := range []struct {
		m, u string
		body any
	}{{"POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers", "path": "plans"}}, {"PUT", "/api/libraries/" + idA + "/bucket", map[string]any{"every": 0}},
		{"POST", "/api/libraries/" + idA + "/bucket/text/stop", nil}, {"POST", "/api/libraries/" + idA + "/rebuild", nil}, {"DELETE", "/api/libraries/" + idA, nil}} {
		if code, b := f.as("ada", "", c.m, c.u, c.body); code != 403 {
			t.Errorf("another site's %s %s: %d %v", c.m, c.u, code, b)
		}
	}
	f.origin = ""
	if code, _ := f.as("ada", "", "PUT", "/api/libraries/"+idA+"/bucket", map[string]any{"endpoint": "https://vpce-0example.s3.us-east-1.vpce.amazonaws.com"}); code != 400 {
		t.Errorf("an endpoint the administrator does not list: %d", code)
	}
	bk := f.row("ada", idA)["bucket"].(map[string]any)
	if bk["route"] != "direct" || bk["objects"] != float64(1) || bk["textPass"].(map[string]any)["files"] != float64(1) {
		t.Fatalf("ada's: %v", bk)
	}
	hits := func(who, q, scope string) (int, string) {
		code, b := f.as(who, "", "GET", "/api/search?q="+q+"&library="+scope, nil)
		return code, fmt.Sprint(b["hits"])
	}
	for who, id := range map[string]string{"ada": idA, "bob": idB} {
		word := map[string]string{"ada": "otter", "bob": "heron"}[who]
		for _, scope := range []string{id, "all"} {
			if code, h := hits(who, word, scope); code != 200 || !strings.Contains(h, word) {
				t.Errorf("%s in %s: %d %s", who, scope, code, h)
			}
		}
		if code, b := f.as(who, "", "GET", "/api/kb/file?library="+id+"&path="+map[string]string{"ada": "plans/secret-otter.txt", "bob": "plans/bob-heron.txt"}[who], nil); code != 200 || !strings.Contains(fmt.Sprint(b["content"]), word) {
			t.Errorf("%s opens: %d %v", who, code, b)
		}
	}

	// What bob, an administrator, or nobody can ask of ada's.
	_, jobs := f.as("ada", "", "POST", "/api/libraries/"+idA+"/rebuild", nil)
	job := fmt.Sprint(jobs["job"].(map[string]any)["id"])
	for _, who := range []string{"bob", "cy", ""} {
		for _, c := range [][2]string{
			{"GET", "/api/search?q=otter&library=" + idA}, {"GET", "/api/kb/file?library=" + idA + "&path=plans/secret-otter.txt"},
			{"GET", "/api/kb/tree?library=" + idA + "&path="}, {"GET", "/api/kb/details?library=" + idA + "&path=plans"},
			{"GET", "/api/libraries/" + idA + "/bucket/text"}, {"POST", "/api/libraries/" + idA + "/bucket/text/stop"},
			{"PUT", "/api/libraries/" + idA + "/bucket"}, {"POST", "/api/libraries/" + idA + "/rebuild"}, {"DELETE", "/api/libraries/" + idA},
			{"PUT", "/api/libraries/" + idA + "/depth"}, {"PUT", "/api/libraries/" + idA + "/meaning"},
			{"GET", "/api/libraries/" + idA + "/topology"}, {"GET", "/api/index/job/" + job}, {"POST", "/api/index/job/" + job + "/cancel"},
		} {
			code, b := f.as(who, "", c[0], c[1], map[string]any{"text": false, "depth": "names", "level": "off"})
			if code < 400 || strings.Contains(fmt.Sprint(b), "otter") || strings.Contains(fmt.Sprint(b), "Papers") {
				t.Errorf("%q %s %s: %d %v", who, c[0], c[1], code, b)
			}
		}
		if _, h := hits(who, "otter", "all"); strings.Contains(h, "otter") {
			t.Errorf("%q finds it in all: %s", who, h)
		}
		_, b := f.as(who, "", "GET", "/api/libraries", nil)
		if s := fmt.Sprint(b); strings.Contains(s, idA) || strings.Contains(s, "pw://ada") {
			t.Errorf("%q is listed it: %s", who, s)
		}
		_, b = f.as(who, "", "GET", "/api/index/status", nil)
		if s := fmt.Sprint(b); strings.Contains(s, idA) || strings.Contains(s, "Papers") {
			t.Errorf("%q sees its job: %s", who, s)
		}
	}
	f.wait("ada", idA)
	// Her own rebuild is hers to follow.
	if code, _ := f.as("ada", "", "GET", "/api/index/job/"+job, nil); code != 200 {
		t.Errorf("ada's own job: %d", code)
	}

	// On disk: in her folder, closed to other accounts, and nowhere else.
	var outside []string
	filepath.WalkDir(f.base, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(f.base, q)
		if d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(q)
		if s := string(b) + rel; (strings.Contains(s, "otter") || strings.Contains(s, "pw://ada")) && !strings.HasPrefix(rel, "users/") {
			outside = append(outside, rel)
		}
		return nil
	})
	if len(outside) > 0 {
		t.Errorf("outside her folder: %v", outside)
	}
	for _, q := range []string{"users", filepath.Dir(filepath.Dir(f.s.Reg.BuiltLibraryDir(idA))), filepath.Dir(f.s.Reg.BuiltLibraryDir(idA)), f.s.Reg.BuiltLibraryDir(idA)} {
		if !filepath.IsAbs(q) {
			q = filepath.Join(f.base, q)
		}
		if st, err := os.Stat(q); err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", q, st.Mode(), err)
		}
	}
	if f.s.Reg.BuiltLibraryDir(idA) == f.s.Reg.BuiltLibraryDir(idB) || !strings.HasPrefix(f.s.Reg.BuiltLibraryDir(idA), filepath.Join(f.base, "users")) {
		t.Errorf("%s and %s", f.s.Reg.BuiltLibraryDir(idA), f.s.Reg.BuiltLibraryDir(idB))
	}
	if s := f.logs.String(); strings.Contains(s, "otter") || strings.Contains(s, "pw://") || strings.Contains(s, "Papers of") {
		t.Errorf("the log says too much: %s", s)
	}
	// Each person's runs and requests were their own.
	for _, l := range strings.Split(strings.TrimSpace(p.file("runs")), "\n") {
		who := strings.Fields(l)[0]
		if strings.Contains(l, "pw://") && !strings.Contains(l, "pw://"+who+"/") && !strings.Contains(l, "bob") {
			t.Errorf("run %s", l)
		}
	}
	for _, q := range p.requests() {
		if who := strings.Split(q, "-")[1]; !strings.Contains(q, "store-"+who+"/") {
			t.Errorf("request %s", q)
		}
	}
	p.clean(f.dir)

	// Signed out, her files are not opened, though a credential was just
	// used for them, and her library waits.
	f.keys["ada"] = ""
	f.a.Providers.CredentialChanged("ada")
	if code, b := f.as("ada", "", "GET", "/api/kb/file?library="+idA+"&path=plans/secret-otter.txt", nil); code == 200 {
		t.Errorf("opened after signing out: %v", b)
	}
	if code, b := f.as("ada", "", "DELETE", "/api/libraries/"+idA, nil); code != 200 {
		t.Errorf("ada removes hers: %d %v", code, b)
	}
	if f.row("ada", idA) != nil || f.row("bob", idB) == nil {
		t.Error("removing hers left it, or took his")
	}
	if strings.Contains(fmt.Sprint(filepath.Glob(filepath.Join(f.base, "users", "*", "buckets", idA))), idA) {
		t.Error("its index is still there")
	}
}

// A person whose key is in their browser alone: their bucket is listed while
// they are here, waits when a listing is due and they are not, says so, and
// is listed when they open the Studio again.
func TestPWBucketWaitsForItsOwner(t *testing.T) {
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	p.object("ada", "a.txt", "a")
	code, b := f.as("ada", "key-ada", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers", "every": 60})
	if code != 200 {
		t.Fatalf("add: %d %v", code, b)
	}
	id := b["library"].(map[string]any)["id"].(string)
	if l := f.wait("ada", id); l["build"].(map[string]any)["state"] != "ready" {
		t.Fatalf("%v", l)
	}
	// Away, and due.
	f.s.builds.pwb.forget("ada")
	p.object("ada", "b.txt", "b")
	due := func(d time.Duration) {
		f.s.builds.startDue(*f.s.Reg.OwnedDef("ada", id), time.Now().Add(d))
	}
	due(30 * time.Minute)
	if f.s.Reg.BucketState(id).Waits {
		t.Error("it waits before it is due")
	}
	due(2 * time.Hour)
	if !f.s.Reg.BucketState(id).Waits || f.s.builds.jobOf(id) != nil {
		t.Errorf("it does not wait: %+v", f.s.Reg.BucketState(id))
	}
	if l := f.row("ada", id); l["bucket"].(map[string]any)["waits"] != true {
		t.Errorf("its row does not say so: %v", l["bucket"])
	}
	// Someone else opening the Studio changes nothing.
	f.as("bob", "key-bob", "GET", "/api/libraries", nil)
	if f.s.builds.jobOf(id) != nil || !f.s.Reg.BucketState(id).Waits {
		t.Error("bob's visit listed it")
	}
	f.as("ada", "key-ada", "GET", "/api/libraries", nil)
	l := f.wait("ada", id)
	if bk := l["bucket"].(map[string]any); bk["objects"] != float64(2) || bk["waits"] == true {
		t.Errorf("after she came: %v", bk)
	}
	p.clean(f.dir)
}

// An index made while sign-in was off has no person whose sign-in reads it.
func TestPWBucketFromBeforeSignIn(t *testing.T) {
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	_, err := f.s.pwStoreOf("", nil, true)
	if err == nil || !strings.Contains(err.Error(), "sign-in off") {
		t.Errorf("%v", err)
	}
	if s := p.file("runs") + p.file("strangers"); s != "" {
		t.Errorf("pw ran: %s", s)
	}
}

// With sign-in off, no page of another site can use the person's buckets: no
// route that has a token fetched, lists, reads, rebuilds or stops answers it,
// pw is not run for it, and an address it names is asked nothing.
func TestPWBucketOtherSites(t *testing.T) {
	needTrace(t)
	for _, csp := range []string{"aws", "google"} {
		t.Run(csp, func(t *testing.T) {
			p := newFakePlatform(t)
			t.Setenv("PW_API_KEY", "key-me")
			f := newBuildFixture(t)
			kb.Register(f.mux, f.a)
			f.s.builds.pwb.base = p.base
			p.object("me", "readme.txt", "top")
			code, b := f.call("POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://me/papers"})
			if code != 200 {
				t.Fatalf("add: %d %v", code, b)
			}
			id := b["library"].(map[string]any)["id"].(string)
			f.waitReady(id)
			put(t, filepath.Join(p.dir, "csp"), csp)
			f.s.builds.pwb.forget("")
			f.s.builds.pwb.base = ""
			var asked []string
			named := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = append(asked, r.URL.Path+" "+r.Header.Get("Authorization"))
				w.WriteHeader(403)
			}))
			defer named.Close()
			at := url.QueryEscape(named.URL)
			runs, requests := p.file("runs"), len(p.requests())
			for _, c := range [][3]string{
				{"GET", "/api/libraries/buckets", ""},
				{"GET", "/api/libraries/buckets/folders?uri=pw://me/papers&endpoint=" + at, ""},
				{"POST", "/api/libraries/buckets/folders", `{"uri":"pw://me/papers"}`},
				{"POST", "/api/libraries/buckets/folders?uri=pw://me/papers&endpoint=" + at, `{"uri":"pw://me/papers","endpoint":"` + named.URL + `"}`},
				{"POST", "/api/libraries/bucket", `{"bucket":"pw://me/papers","path":"x","endpoint":"` + named.URL + `"}`},
				{"PUT", "/api/libraries/" + id + "/bucket", `{"endpoint":"` + named.URL + `"}`},
				{"GET", "/api/libraries/" + id + "/bucket/text", ""},
				{"POST", "/api/libraries/" + id + "/bucket/text/stop", ""},
				{"POST", "/api/libraries/" + id + "/rebuild", ""},
				{"GET", "/api/kb/file?library=" + id + "&path=readme.txt", ""},
				{"GET", "/api/kb/download?library=" + id + "&path=readme.txt", ""},
				{"DELETE", "/api/libraries/" + id, ""},
			} {
				for _, from := range []map[string]string{
					{"Origin": "https://elsewhere.example", "Sec-Fetch-Site": "cross-site"}, {"Sec-Fetch-Site": "cross-site"}, {"Sec-Fetch-Site": "same-site"},
					{"Origin": "https://elsewhere.example"}, {"Origin": "null"}, {"Referer": "http://elsewhere.example/page"},
				} {
					for _, kind := range []string{"application/json", "text/plain"} {
						r := httptest.NewRequest(c[0], c[1], strings.NewReader(c[2]))
						r.Header.Set("Content-Type", kind)
						for k, v := range from {
							r.Header.Set(k, v)
						}
						w := httptest.NewRecorder()
						f.mux.ServeHTTP(w, r)
						if w.Code != 403 && w.Code != 404 && w.Code != 405 || strings.Contains(w.Body.String(), "top") {
							t.Errorf("%s %s from %v: %d %s", c[0], c[1], from, w.Code, w.Body)
						}
					}
				}
			}
			// The old way to look at folders names nothing, from here either.
			if code, b := f.call("GET", "/api/libraries/buckets/folders?uri=pw://me/papers&endpoint="+at, nil); code == 200 {
				t.Errorf("by GET: %v", b)
			}
			// Nor does the Studio's own page send the credential to any host.
			for _, body := range []map[string]any{{"uri": "pw://me/papers", "endpoint": named.URL}, {"uri": "pw://me/papers", "endpoint": "https://collector.example"}} {
				if code, b := f.call("POST", "/api/libraries/buckets/folders", body); code != 400 {
					t.Errorf("%v: %d %v", body, code, b)
				}
			}
			if code, b := f.call("PUT", "/api/libraries/"+id+"/bucket", map[string]any{"endpoint": "https://collector.example"}); code != 400 {
				t.Errorf("its endpoint: %d %v", code, b)
			}
			if len(asked) > 0 || p.file("runs") != runs || len(p.requests()) != requests || f.s.builds.jobOf(id) != nil {
				t.Errorf("asked %v; runs %q", asked, strings.TrimPrefix(p.file("runs"), runs))
			}
			// The Studio's own pages are answered, whatever names them.
			f.s.builds.pwb.base = p.base
			for _, from := range []map[string]string{{"Sec-Fetch-Site": "same-origin"}, {"Sec-Fetch-Site": "none"}, {"Origin": "http://example.com"}, {"Referer": "http://example.com/library"}, {}} {
				r := httptest.NewRequest("GET", "/api/libraries/buckets", nil)
				for k, v := range from {
					r.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Errorf("from %v: %d %s", from, w.Code, w.Body)
				}
			}
		})
	}
}

// Another site's request for the list of libraries does not start listings
// that waited for their owner.
func TestPWBucketVisitIsHers(t *testing.T) {
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	p.object("ada", "a.txt", "a")
	_, b := f.as("ada", "key-ada", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers", "every": 60})
	id := b["library"].(map[string]any)["id"].(string)
	f.wait("ada", id)
	f.s.builds.pwb.forget("ada")
	f.s.Reg.WriteBucketState(id, libraries.BucketState{Waits: true})
	f.origin = "https://elsewhere.example"
	f.as("ada", "key-ada", "GET", "/api/libraries", nil)
	if f.s.builds.jobOf(id) != nil || !f.s.Reg.BucketState(id).Waits {
		t.Error("another site had it listed")
	}
	f.origin = ""
	f.as("ada", "key-ada", "GET", "/api/libraries", nil)
	if f.wait("ada", id); f.s.Reg.BucketState(id).Waits {
		t.Error("she came, and it waits")
	}
}

// Keys no path can name: the listing goes on without them and counts them,
// and no one of them opens, or gives its text to, another object.
func TestPWBucketKeysNoPathNames(t *testing.T) {
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	f.extractBins()
	f.keys["ada"] = "key-ada"
	var mu sync.Mutex
	var gets []string
	hostile := []string{"notes\n.txt", "nl\n99\x1e1\x1e40\x1edocs/ok.txt", "cut\x1eoff.txt", "cr\r.txt", "del\x7f.txt", "bell\a/in.txt",
		"/report.txt", "//double//slash.txt", "docs//ok.txt", "docs//", "/"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/"); key != "" {
			mu.Lock()
			gets = append(gets, key)
			mu.Unlock()
			if key == "gone.txt" {
				w.WriteHeader(404)
				return
			}
			fmt.Fprintf(w, "zebrafinch %s", key)
			return
		}
		fmt.Fprint(w, "<ListBucketResult><EncodingType>url</EncodingType>")
		for _, k := range append([]string{"docs/ok.txt", "plain.txt", "report.txt"}, hostile...) {
			fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>40</Size><LastModified>2026-11-01T00:00:00Z</LastModified></Contents>", url.QueryEscape(k))
		}
		fmt.Fprint(w, "</ListBucketResult>")
	}))
	defer ts.Close()
	f.s.builds.pwb.base = ts.URL
	code, b := f.as("ada", "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers"})
	if code != 200 {
		t.Fatalf("add: %d %v", code, b)
	}
	id := b["library"].(map[string]any)["id"].(string)
	l := f.wait("ada", id)
	bk := l["bucket"].(map[string]any)
	if l["build"].(map[string]any)["state"] != "ready" || bk["error"] != nil || bk["objects"] != float64(3) || bk["skipped"] != float64(len(hostile)) {
		t.Fatalf("%v", l)
	}
	if code, b := f.as("ada", "", "PUT", "/api/libraries/"+id+"/bucket", map[string]any{"text": true}); code != 200 {
		t.Fatalf("text: %d %v", code, b)
	}
	for range 600 {
		if bk, _ := f.row("ada", id)["bucket"].(map[string]any); bk["textPass"] != nil && f.s.builds.jobOf(id) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, b := f.as("ada", "", "GET", "/api/kb/file?library="+id+"&path=report.txt", nil); code != 200 || b["content"] != "zebrafinch report.txt" {
		t.Errorf("%d %v", code, b)
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(gets)
	if got := slices.Compact(slices.Clone(gets)); !slices.Equal(got, []string{"docs/ok.txt", "plain.txt", "report.txt"}) {
		t.Errorf("asked for %q", got)
	}
}

// A listing that fails of its own (a panic) is a failed listing: the server
// stays up, the library says so, and the next listing works.
func TestPWBucketListingPanics(t *testing.T) {
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	f.keys["ada"] = "key-ada"
	p.object("ada", "a.txt", "a")
	_, b := f.as("ada", "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers"})
	id := b["library"].(map[string]any)["id"].(string)
	f.wait("ada", id)
	var once sync.Once
	f.a.Providers.SignInKey = func(sub string) string {
		once.Do(func() { panic("of the listing's own") })
		return f.keys[sub]
	}
	if _, err := f.s.builds.start(id); err != nil {
		t.Fatal(err)
	}
	l := f.wait("ada", id)
	if e := fmt.Sprint(l["bucket"].(map[string]any)["error"]); l["build"].(map[string]any)["state"] != "ready" || !strings.Contains(e, "internal error") {
		t.Fatalf("%v", l)
	}
	if !strings.Contains(f.logs.String(), "panicked") {
		t.Error("the log does not say so")
	}
	f.s.builds.start(id)
	if l = f.wait("ada", id); l["bucket"].(map[string]any)["error"] != nil {
		t.Errorf("%v", l)
	}
}

// Where people sign in, an endpoint is one the administrator lists, the form
// is given the list, and what its connection fails of is in the log alone.
func TestPWBucketListedEndpoint(t *testing.T) {
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	defer ts.Close()
	listed := strings.TrimPrefix(ts.URL, "https://")
	t.Setenv("STUDIO_BUCKET_ENDPOINTS", listed+", s3.gateway.example.com")
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	f.s.builds.pwb.base = ""
	f.keys["ada"] = "key-ada"
	p.object("ada", "a.txt", "a")
	if _, b := f.as("ada", "", "GET", "/api/libraries", nil); fmt.Sprint(b["bucketEndpoints"]) != "["+ts.URL+" https://s3.gateway.example.com]" {
		t.Errorf("%v", b["bucketEndpoints"])
	}
	code, b := f.as("ada", "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers", "endpoint": ts.URL})
	if code != 200 {
		t.Fatalf("%d %v", code, b)
	}
	l := f.wait("ada", b["library"].(map[string]any)["id"].(string))
	if bk := l["bucket"].(map[string]any); bk["route"] != "pw" || !strings.Contains(fmt.Sprint(bk["routeWhy"]), "could not be reached") {
		t.Errorf("%v", bk)
	}
	if s := fmt.Sprint(l); strings.Contains(s, "x509") || strings.Contains(s, "certificate") {
		t.Errorf("she is told: %s", s)
	}
	if !strings.Contains(f.logs.String(), "certificate") {
		t.Errorf("the administrator is not: %s", f.logs)
	}
}

// pw runs for a bucket are given none of the Studio's own cloud credentials.
func TestPWBucketNoCloudCredentials(t *testing.T) {
	needTrace(t)
	p := newFakePlatform(t)
	t.Setenv("PW_API_KEY", "key-me")
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_WEB_IDENTITY_TOKEN_FILE", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_ACCESS_TOKEN", "AZURE_CLIENT_SECRET", "AZURE_STORAGE_KEY"} {
		t.Setenv(k, "studio-secret")
	}
	f := newBuildFixture(t)
	f.s.builds.pwb.base = p.base
	p.object("me", "a.txt", "a")
	code, b := f.call("POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://me/papers"})
	if code != 200 {
		t.Fatalf("%d %v", code, b)
	}
	f.waitReady(b["library"].(map[string]any)["id"].(string))
	if e := p.file("envs"); e == "" || strings.Contains(e, "studio-secret") || !strings.Contains(e, "AWS_EC2_METADATA_DISABLED=true") {
		t.Errorf("%s", e)
	}
}

// What the Studio does for buckets as it starts.
func TestPWBucketStart(t *testing.T) {
	t.Setenv("AWS_ENDPOINT_URL", "http://[::1")
	t.Setenv("TMPDIR", t.TempDir())
	left := filepath.Join(os.TempDir(), "studio-pw-run-1234")
	put(t, filepath.Join(left, "credentials"), "a credential")
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	// The empty HOME of pw runs without a sign-in, which the Studios of an
	// account share.
	none := ""
	for _, kv := range platform.Of(f.a).EnvironFor(nil, false) {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			none = v
		}
	}
	defer func() {
		if st, err := os.Stat(none); err != nil || !st.IsDir() || filepath.Dir(none) != os.TempDir() {
			t.Errorf("%s: %v", none, err)
		}
	}()
	ctx, stop := context.WithCancel(context.Background())
	defer func() {
		stop()
		f.a.Wait(10 * time.Second)
	}()
	f.a.Start(ctx)
	for range 100 {
		if _, err := os.Stat(left); os.IsNotExist(err) && strings.Contains(f.logs.String(), "AWS_ENDPOINT_URL") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("%v; %s", os.IsNotExist(func() error { _, err := os.Stat(left); return err }()), f.logs)
}

// Copy link in a person's own bucket: her link leads her there, says to
// anyone else what a link that is none says, and goes with the library.
func TestPWBucketLinks(t *testing.T) {
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	f.keys["ada"], f.keys["bob"] = "key-ada", "key-bob"
	p.object("ada", "runs/otter.txt", "a")
	p.object("bob", "runs/otter.txt", "b")
	add := func(who string) string {
		_, b := f.as(who, "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://" + who + "/papers"})
		id := b["library"].(map[string]any)["id"].(string)
		f.wait(who, id)
		return id
	}
	lib := add("ada")
	add("bob")
	link := map[string]any{"library": lib, "path": "runs/otter.txt"}
	if st, b := f.as("bob", "", "POST", "/api/links", link); st < 400 {
		t.Errorf("bob made one: %d %v", st, b)
	}
	st, b := f.as("ada", "", "POST", "/api/links", link)
	id, _ := b["id"].(string)
	if st != 200 || id == "" {
		t.Fatal(st, b)
	}
	if st, b := f.as("ada", "", "GET", "/api/links/"+id, nil); st != 200 || b["library"] != lib || b["path"] != "runs/otter.txt" {
		t.Errorf("hers: %d %v", st, b)
	}
	none, nb := f.as("bob", "", "GET", "/api/links/"+strings.Repeat("A", 22), nil)
	if st, b := f.as("bob", "", "GET", "/api/links/"+id, nil); st != 404 || st != none || !reflect.DeepEqual(b, nb) {
		t.Errorf("to bob: %d %v, a link that is none: %d %v", st, b, none, nb)
	}
	if st, _ := f.as("", "", "GET", "/api/links/"+id, nil); st != 401 {
		t.Errorf("signed out: %d", st)
	}
	if strings.Contains(f.logs.String(), "otter") {
		t.Error("the log names the file")
	}
	if st, b := f.as("ada", "", "DELETE", "/api/libraries/"+lib, nil); st != 200 {
		t.Fatal(st, b)
	}
	if st, _ := f.as("ada", "", "GET", "/api/links/"+id, nil); st != 404 {
		t.Errorf("removed: %d", st)
	}
	if kept, _ := os.ReadFile(filepath.Join(f.base, "links.json")); bytes.Contains(kept, []byte("otter")) {
		t.Error("the link is kept")
	}
}

// A tab in a key: kept in the trace, the index, the list, names and words
// found, and it opens its own object.
func TestPWBucketTabs(t *testing.T) {
	p := newFakePlatform(t)
	f := newSignInFixture(t, p)
	f.extractBins()
	f.keys["ada"] = "key-ada"
	keys := []string{"tab\tdir/run\tone.txt", "two\ttabs\there.txt", "\tfirst.txt"}
	for _, k := range keys {
		p.object("ada", k, "zebrafinch in "+k)
	}
	p.object("ada", "run one.txt", "another")
	_, b := f.as("ada", "", "POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://ada/papers"})
	id := b["library"].(map[string]any)["id"].(string)
	l := f.wait("ada", id)
	if bk := l["bucket"].(map[string]any); bk["error"] != nil || bk["objects"] != float64(len(keys)+1) || bk["skipped"] != float64(0) {
		t.Fatalf("%v", l)
	}
	if code, b := f.as("ada", "", "PUT", "/api/libraries/"+id+"/bucket", map[string]any{"text": true}); code != 200 {
		t.Fatal(code, b)
	}
	for range 600 {
		if bk, _ := f.row("ada", id)["bucket"].(map[string]any); bk["textPass"] != nil && f.s.builds.jobOf(id) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, k := range keys {
		code, b := f.as("ada", "", "GET", "/api/kb/file?library="+id+"&path="+url.QueryEscape(k), nil)
		if code != 200 || b["content"] != "zebrafinch in "+k || b["path"] != k {
			t.Errorf("%q opens %d %v", k, code, b)
		}
		dir := path.Dir(k)
		if dir == "." {
			dir = ""
		}
		_, b = f.as("ada", "", "GET", "/api/kb/tree?library="+id+"&path="+url.QueryEscape(dir), nil)
		if !strings.Contains(fmt.Sprintf("%q", b), strings.Trim(fmt.Sprintf("%q", k), `"`)) {
			t.Errorf("%q is not listed: %q", k, b)
		}
		name := path.Base(k)
		for _, q := range []string{name, strings.Split(strings.TrimSpace(name), "\t")[0], "zebrafinch"} {
			code, b = f.as("ada", "", "GET", "/api/search?q="+url.QueryEscape(q)+"&library="+id, nil)
			if code != 200 || !strings.Contains(fmt.Sprintf("%q", b), strings.Trim(fmt.Sprintf("%q", k), `"`)) {
				t.Errorf("%q does not find %q: %d %q", q, k, code, b)
			}
		}
	}
}

// Behind a proxy that rewrites the host and does not say which it was, a
// browser that says no more than the page's origin is told by the Studio's
// own address, and by nothing near it.
func TestPWBucketOwnAddress(t *testing.T) {
	needTrace(t)
	p := newFakePlatform(t)
	t.Setenv("PW_API_KEY", "key-me")
	f := newBuildFixture(t)
	f.s.builds.pwb.base = p.base
	p.object("me", "a.txt", "a")
	_, b := f.call("POST", "/api/libraries/bucket", map[string]any{"bucket": "pw://me/papers"})
	id := b["library"].(map[string]any)["id"].(string)
	f.waitReady(id)
	ask := func(method, path string, h map[string]string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"every":0}`))
		r.Host = "10.0.0.5:4180"
		r.Header.Set("Content-Type", "application/json")
		for k, v := range h {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w.Code
	}
	own := func(origin string) (int, int) {
		return ask("PUT", "/api/libraries/"+id+"/bucket", map[string]string{"Origin": origin}), ask("GET", "/api/libraries/buckets", map[string]string{"Referer": origin + "/library"})
	}
	oidc := &f.a.Cfg.OIDC
	for _, c := range []struct{ self, redirect, origin string }{
		{"http://studio.example.com", "", "http://studio.example.com"},
		{"http://Studio.example.com:80/", "", "http://studio.example.com"},
		{"https://studio.example.com:8443/studio", "", "https://studio.example.com:8443"},
		{"", "https://studio.example.com/api/auth/callback", "https://studio.example.com"},
		{"http://127.0.0.1:4180", "https://studio.example.com/api/auth/callback", "https://studio.example.com"},
	} {
		t.Setenv("STUDIO_SELF_URL", "")
		oidc.RedirectURI = ""
		if put, get := own(c.origin); put != 403 || get != 403 {
			t.Errorf("%+v without it: %d %d", c, put, get)
		}
		t.Setenv("STUDIO_SELF_URL", c.self)
		oidc.RedirectURI = c.redirect
		if put, get := own(c.origin); put != 200 || get != 200 {
			t.Errorf("%+v: %d %d", c, put, get)
		}
	}
	t.Setenv("STUDIO_SELF_URL", "http://studio.example.com")
	oidc.RedirectURI = "https://signin.example.com/api/auth/callback"
	for _, other := range []string{"https://studio.example.com", "http://studio.example.com:8080", "http://studio.example.com.elsewhere.example", "http://x.studio.example.com",
		"http://studio.example.com@elsewhere.example", "http://elsewhere.example", "https://signin.example.com:444", "file://studio.example.com"} {
		if put, get := own(other); put != 403 || get != 403 {
			t.Errorf("%s: %d %d", other, put, get)
		}
	}
	for _, h := range []map[string]string{{"Origin": "null"}, {"Origin": "http://studio.example.com", "Sec-Fetch-Site": "cross-site"}, {"Origin": "http://studio.example.com", "Sec-Fetch-Site": "same-site"},
		{"Origin": "http://elsewhere.example", "Referer": "http://studio.example.com/"}} {
		if st := ask("PUT", "/api/libraries/"+id+"/bucket", h); st != 403 {
			t.Errorf("%v: %d", h, st)
		}
	}
	// A Studio with neither has no address to be taken for.
	t.Setenv("STUDIO_SELF_URL", "")
	oidc.RedirectURI = ""
	if st := ask("PUT", "/api/libraries/"+id+"/bucket", map[string]string{"Origin": "null"}); st != 403 {
		t.Error(st)
	}
}

// Over plain http on a host name, behind a proxy that rewrites the host and
// does not say which it was, the Studio's own page is known by the header it
// sends; a form of another site, which cannot send it, is refused, and the
// refusal says what to set where the page was the Studio's after all.
func TestOwnRequestBehindProxy(t *testing.T) {
	needTrace(t)
	f := newBuildFixture(t)
	remotes := filepath.Join(f.dir, "remotes")
	put(t, filepath.Join(remotes, "store", "research", "readme.txt"), "top")
	exe, _ := os.Executable()
	bin := filepath.Join(f.dir, "rclone-bin")
	put(t, filepath.Join(bin, "rclone"), "#!/bin/sh\nLIBAPI_TEST_CHILD=rclone exec '"+exe+"' \"$@\"\n")
	os.Chmod(filepath.Join(bin, "rclone"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_RCLONE_DIR", remotes)
	t.Setenv("FAKE_RCLONE_LOG", filepath.Join(f.dir, "rclone.log"))
	t.Setenv("STUDIO_SELF_URL", "")
	src := filepath.Join(f.dir, "notes")
	put(t, filepath.Join(src, "a.txt"), "aardvark")
	if code, b := f.call("POST", "/api/libraries/build", map[string]any{"sourceRoot": src, "label": "Notes"}); code != 200 {
		t.Fatalf("folder library: %d %v", code, b)
	}
	f.waitReady("notes")
	ask := func(method, path, kind, body string, h map[string]string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "10.0.0.5:4180"
		if kind != "" {
			r.Header.Set("Content-Type", kind)
		}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	const js, form = "application/json", "application/x-www-form-urlencoded"
	add := `{"location":"rclone:store","path":"research"}`
	refused := func(what string, code int, body string) {
		t.Helper()
		if code != 403 || !strings.Contains(body, "STUDIO_SELF_URL") || !strings.Contains(body, "X-Forwarded-Host") {
			t.Errorf("%s: %d %s", what, code, body)
		}
	}
	calls := func(id string) [][4]string {
		return [][4]string{
			{"GET", "/api/libraries/buckets", "", ""},
			{"PUT", "/api/libraries/" + id + "/bucket", js, `{"every":360}`},
			{"GET", "/api/libraries/" + id + "/bucket/text", "", ""},
			{"POST", "/api/libraries/" + id + "/bucket/text/stop", "", ""},
			{"POST", "/api/libraries/" + id + "/rebuild", js, `{}`},
			{"POST", "/api/libraries/notes/rebuild", js, `{}`},
			{"DELETE", "/api/libraries/" + id, "", ""},
		}
	}
	page := map[string]string{"Origin": "http://studio.example.com", "Referer": "http://studio.example.com/"}
	withHeader := map[string]string{"X-Studio-Request": "1", "Origin": "http://studio.example.com", "Referer": "http://studio.example.com/"}

	code, b := ask("POST", "/api/libraries/bucket", js, add, page)
	refused("add without the header", code, b)
	if code, b = ask("POST", "/api/libraries/bucket", js, add, withHeader); code != 200 {
		t.Fatalf("add: %d %s", code, b)
	}
	f.waitReady("research")
	other := func(h map[string]string, more ...string) map[string]string {
		out := map[string]string{"Origin": "https://elsewhere.example", "Referer": "https://elsewhere.example/page"}
		for k, v := range h {
			out[k] = v
		}
		for i := 0; i+1 < len(more); i += 2 {
			out[more[i]] = more[i+1]
		}
		return out
	}
	own := map[string]string{"X-Studio-Request": "1"}
	for _, c := range calls("research") {
		what := c[0] + " " + c[1]
		code, b := ask(c[0], c[1], c[2], c[3], page)
		refused(what+" without the header", code, b)
		// A form of another site.
		code, b = ask(c[0], c[1], form, "every=360", other(nil))
		refused(what+" from a form of another site", code, b)
		code, b = ask(c[0], c[1], form, "every=360", other(nil, "Sec-Fetch-Site", "cross-site"))
		refused(what+" from a form of another site, as the browser says", code, b)
		// No form sends the header; whatever did, the browser's word stands.
		for _, site := range []string{"cross-site", "same-site"} {
			code, b = ask(c[0], c[1], c[2], c[3], other(own, "Sec-Fetch-Site", site))
			refused(what+" with the header, "+site, code, b)
		}
		// The header has one value.
		for _, v := range []string{"", "0", "true", "1, 1"} {
			code, b = ask(c[0], c[1], c[2], c[3], other(nil, "X-Studio-Request", v))
			refused(what+" with the header "+v, code, b)
		}
		// A preflight is granted nothing.
		r := httptest.NewRequest("OPTIONS", c[1], nil)
		r.Header.Set("Origin", "https://elsewhere.example")
		r.Header.Set("Access-Control-Request-Method", c[0])
		r.Header.Set("Access-Control-Request-Headers", "x-studio-request")
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		for k := range w.Header() {
			if strings.HasPrefix(k, "Access-Control-") {
				t.Errorf("%s: a preflight has %s", what, k)
			}
		}
		if code, b = ask(c[0], c[1], c[2], c[3], withHeader); code != 200 {
			t.Errorf("%s with the header: %d %s", what, code, b)
		}
		if strings.HasSuffix(c[1], "/rebuild") {
			f.waitReady(strings.Split(c[1], "/")[3])
		}
	}
}
