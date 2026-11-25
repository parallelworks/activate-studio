package pwbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore answers as AWS S3 (and, for Google, its JSON interface) does,
// with credentials that end: a token is good for uses requests.
type fakeStore struct {
	mu    sync.Mutex
	keys  []string
	body  map[string]string
	valid map[string]int // token: requests left; below 0 is no end
	// busy answers the next n requests with 503; deny answers every one 403.
	busy   int
	deny   bool
	region string
	log    []string
	// noToken refuses a page token another credential made, as a store may.
	pages map[string]string
}

func newStore(keys ...string) *fakeStore {
	f := &fakeStore{body: map[string]string{}, valid: map[string]int{}, pages: map[string]string{}}
	for _, k := range keys {
		f.body[k] = "body of " + k
		f.keys = append(f.keys, k)
	}
	sort.Strings(f.keys)
	return f
}

func (f *fakeStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, r.URL.String()+" "+r.Header.Get("Range"))
	fail := func(status int, code, msg string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `<?xml version="1.0"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
	}
	google := strings.HasPrefix(r.URL.Path, "/storage/v1/")
	tok := r.Header.Get("X-Amz-Security-Token")
	if google {
		tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	} else if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") {
		fail(403, "AccessDenied", "not signed")
		return
	}
	if f.deny {
		fail(403, "AccessDenied", "Access Denied")
		return
	}
	if f.region != "" && !strings.Contains(r.Header.Get("Authorization"), "/"+f.region+"/s3/") {
		w.Header().Set("X-Amz-Bucket-Region", f.region)
		fail(301, "PermanentRedirect", "elsewhere")
		return
	}
	left, ok := f.valid[tok]
	if !ok || left == 0 {
		if google {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"Invalid Credentials"}}`)
			return
		}
		fail(400, "ExpiredToken", "The provided token has expired.")
		return
	}
	f.valid[tok] = left - 1
	if f.busy > 0 {
		f.busy--
		fail(503, "SlowDown", "Please reduce your request rate.")
		return
	}
	q := r.URL.Query()
	key := strings.TrimPrefix(r.URL.Path, "/examplebucket/")
	if google {
		esc := strings.TrimPrefix(r.URL.EscapedPath(), "/storage/v1/b/examplebucket/o")
		key, _ = url.PathUnescape(strings.TrimPrefix(esc, "/"))
	}
	if q.Get("list-type") == "2" || google && key == "" {
		token, start := q.Get("continuation-token"), q.Get("start-after")
		if google {
			token, start = q.Get("pageToken"), q.Get("startOffset")
		}
		if token != "" {
			if f.pages[token] != tok {
				fail(400, "InvalidArgument", "The continuation token provided is incorrect")
				return
			}
			start = strings.TrimPrefix(token, "t:")
		}
		var out []string
		more := false
		if q.Get("delimiter") == "/" {
			f.folder(w, google, q)
			return
		}
		for _, k := range f.keys {
			if !strings.HasPrefix(k, q.Get("prefix")) || k < start || k == start && !(google && token == "") {
				continue
			}
			if len(out) == 1000 {
				more = true
				break
			}
			out = append(out, k)
		}
		next := ""
		if more {
			next = "t:" + out[len(out)-1]
			f.pages[next] = tok
		}
		if google {
			items := []map[string]string{}
			for _, k := range out {
				items = append(items, map[string]string{"name": k, "size": fmt.Sprint(len(f.body[k])), "updated": "2026-10-01T12:00:00Z"})
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items, "nextPageToken": next})
			return
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><EncodingType>url</EncodingType><IsTruncated>%v</IsTruncated>`, more)
		if more {
			fmt.Fprintf(w, `<NextContinuationToken>%s</NextContinuationToken>`, next)
		}
		for _, k := range out {
			size := len(f.body[k])
			if strings.HasSuffix(k, "/") {
				size = 0
			}
			fmt.Fprintf(w, `<Contents><Key>%s</Key><LastModified>2026-10-01T12:00:00.000Z</LastModified><Size>%d</Size></Contents>`, url.QueryEscape(k), size)
		}
		io.WriteString(w, `</ListBucketResult>`)
		return
	}
	body, ok := f.body[key]
	if !ok {
		fail(404, "NoSuchKey", "The specified key does not exist.")
		return
	}
	http.ServeContent(w, r, "", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), strings.NewReader(body))
}

// folder answers a listing of what is directly under a prefix.
func (f *fakeStore) folder(w http.ResponseWriter, google bool, q url.Values) {
	dirs, objects := []string{}, []map[string]string{}
	for _, k := range f.keys {
		rest, ok := strings.CutPrefix(k, q.Get("prefix"))
		if !ok {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			d := q.Get("prefix") + rest[:i+1]
			if len(dirs) == 0 || dirs[len(dirs)-1] != d {
				dirs = append(dirs, d)
			}
			continue
		}
		objects = append(objects, map[string]string{"name": k, "size": fmt.Sprint(len(f.body[k])), "updated": "2026-10-01T12:00:00Z"})
	}
	if google {
		json.NewEncoder(w).Encode(map[string]any{"items": objects, "prefixes": dirs})
		return
	}
	io.WriteString(w, `<ListBucketResult><EncodingType>url</EncodingType>`)
	for _, o := range objects {
		fmt.Fprintf(w, `<Contents><Key>%s</Key><LastModified>2026-10-01T12:00:00.000Z</LastModified><Size>%s</Size></Contents>`, url.QueryEscape(o["name"]), o["size"])
	}
	for _, d := range dirs {
		fmt.Fprintf(w, `<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>`, url.QueryEscape(d))
	}
	io.WriteString(w, `</ListBucketResult>`)
}

func (f *fakeStore) requests() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.log) }

// fakePW writes a pw that gives numbered tokens, lists and prints from dir,
// and records how it was run.
func fakePW(t *testing.T, csp string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "pw")
	script := `#!/bin/sh
D='` + dir + `'
echo "$@" >> "$D/runs"
env | sort > "$D/env"
echo "$HOME" >> "$D/homes"
echo secret > "$HOME/left"
while [ $# -gt 0 ]; do case "$1" in --context|--platform-host) shift 2;; *) break;; esac; done
shift
[ -f "$D/fail-$1" ] && { cat "$D/fail-$1" >&2; exit 1; }
case "$1" in
get-token)
  n=$(cat "$D/n" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$D/n"
  echo "export BUCKET_URI='s3://examplebucket'"
  [ -f "$D/region" ] && echo "export AWS_REGION='$(cat "$D/region")'"
  if [ '` + csp + `' = google ]; then echo "export CLOUDSDK_AUTH_ACCESS_TOKEN='tok$n'"
  else echo "export AWS_ACCESS_KEY_ID='AKIAEXAMPLE'"; echo "export AWS_SECRET_ACCESS_KEY='a secret'"; echo "export AWS_SESSION_TOKEN='tok$n'"; fi
  echo; echo "To use the AWS CLI with these variables";;
ls)
  for a; do last=$a; done
  if [ "$last" = json ]; then cat "$D/buckets.json"; else f=$(echo "$last" | sed 's#^pw://demo/b##;s#/#,#g'); cat "$D/ls$f" 2>/dev/null || echo '[]'; fi;;
cat)
  for a; do last=$a; done
  f=$(echo "$last" | sed 's#^pw://demo/b/##;s#/#,#g'); cat "$D/obj,$f" 2>/dev/null || { echo '2026-11-25T14:10:07Z [ERROR] no such object' >&2; exit 1; };;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testCLI(bin string) *CLI {
	return &CLI{Bin: bin, Host: "platform.example", Context: "studio",
		Key: func(context.Context) (string, error) { return "pwt_person", nil },
		Environ: func(set map[string]string) []string {
			env := []string{"PATH=/usr/bin:/bin", "PW_API_KEY=deployment", "PW_CONTEXT=work"}
			for k, v := range set {
				env = append(env, k+"="+v)
			}
			return env
		}}
}

func setup(t *testing.T, csp string, f *fakeStore) (*Store, string) {
	t.Helper()
	pause = time.Millisecond
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	bin, dir := fakePW(t, csp)
	return &Store{CLI: testCLI(bin), Base: ts.URL, B: Bucket{URI: "pw://demo/b", CSP: csp, Region: "us-east-1", Store: "examplebucket"}}, dir
}

func walk(t *testing.T, s *Store, dir string) (objects, dirs []string) {
	t.Helper()
	if err := s.Walk(context.Background(), dir, func(e Entry) error {
		if e.IsDir {
			dirs = append(dirs, e.Path)
		} else {
			objects = append(objects, e.Path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func many(n int) []string {
	var keys []string
	for i := range n {
		keys = append(keys, fmt.Sprintf("d%02d/f%05d.txt", i%7, i))
	}
	return keys
}

func TestWalkDirect(t *testing.T) {
	for _, csp := range []string{AWS, Google} {
		t.Run(csp, func(t *testing.T) {
			odd := []string{"sp ace/a+b&c.txt", "uni/café.txt", "pct/%41.txt", "empty-dir/", "top.txt"}
			f := newStore(append(many(2500), odd...)...)
			f.valid["tok1"] = -1
			s, _ := setup(t, csp, f)
			got, dirs := walk(t, s, "")
			if len(got) != 2504 || fmt.Sprint(dirs) != "[empty-dir]" {
				t.Fatalf("%d objects, folders %v", len(got), dirs)
			}
			for _, k := range odd {
				if !strings.HasSuffix(k, "/") && !strings.Contains("\n"+strings.Join(got, "\n")+"\n", "\n"+k+"\n") {
					t.Errorf("%s is not in the listing", k)
				}
			}
			if f.requests() != 3 || s.Requests.Load() != 3 {
				t.Errorf("%d requests, counted %d", f.requests(), s.Requests.Load())
			}
			if r, why := s.Route(); r != Direct || why != nil {
				t.Errorf("route %s, %v", r, why)
			}
			got, _ = walk(t, s, "uni")
			if fmt.Sprint(got) != "[café.txt]" {
				t.Errorf("a folder listed %v", got)
			}
		})
	}
}

// A credential that ends part way through a listing is renewed, and the
// listing goes on where it was: nothing twice, nothing missed, no build
// failed. The store here does not take the old credential's page token.
func TestTokenEndsMidListing(t *testing.T) {
	for _, csp := range []string{AWS, Google} {
		t.Run(csp, func(t *testing.T) {
			f := newStore(many(5200)...)
			f.valid["tok1"], f.valid["tok2"], f.valid["tok3"] = 2, 2, -1
			s, dir := setup(t, csp, f)
			got, _ := walk(t, s, "")
			seen := map[string]bool{}
			for _, k := range got {
				if seen[k] {
					t.Fatalf("%s is listed twice", k)
				}
				seen[k] = true
			}
			if len(got) != 5200 {
				t.Fatalf("%d of 5200 objects", len(got))
			}
			b, _ := os.ReadFile(filepath.Join(dir, "n"))
			if strings.TrimSpace(string(b)) != "3" {
				t.Errorf("%s tokens were asked for, not 3", b)
			}
			// Six pages, and the two refusals.
			if n := f.requests(); n < 8 || n > 9 {
				t.Errorf("%d requests", n)
			}
		})
	}
}

// One that ends at once, over and over, fails the listing with what the
// store said, and does not ask without end.
func TestTokenNeverGood(t *testing.T) {
	f := newStore(many(10)...)
	s, dir := setup(t, AWS, f)
	put(t, dir, "fail-ls", "2026-11-25T14:10:08Z [ERROR] storage not found: b\n")
	err := s.Walk(context.Background(), "", func(Entry) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "ExpiredToken") || !strings.Contains(err.Error(), "storage not found: b") {
		t.Fatalf("err %v", err)
	}
	if f.requests() > 4 {
		t.Errorf("%d requests", f.requests())
	}
	for _, secret := range []string{"a secret", "tok1", "pwt_person"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error holds %q: %v", secret, err)
		}
	}
}

func TestBusyStoreAndOtherRegion(t *testing.T) {
	f := newStore(many(1500)...)
	f.valid["tok1"], f.busy, f.region = -1, 3, "eu-west-2"
	s, _ := setup(t, AWS, f)
	if got, _ := walk(t, s, ""); len(got) != 1500 {
		t.Fatalf("%d objects", len(got))
	}
}

func TestReadsOnlyWhatIsShown(t *testing.T) {
	for _, csp := range []string{AWS, Google} {
		t.Run(csp, func(t *testing.T) {
			f := newStore("big.txt", "none.txt", "a b/c+d.txt")
			f.valid["tok1"] = -1
			f.body["big.txt"], f.body["none.txt"] = strings.Repeat("x", 5000), ""
			s, _ := setup(t, csp, f)
			ctx := context.Background()
			e, err := s.Stat(ctx, "big.txt")
			if err != nil || e.Size != 5000 || e.ModTime.Unix() != 1790856000 {
				t.Fatalf("stat %+v, %v", e, err)
			}
			if b, err := s.Read(ctx, "big.txt", 100); err != nil || len(b) != 100 {
				t.Fatalf("read %d, %v", len(b), err)
			}
			f.mu.Lock()
			log := strings.Join(f.log, "\n")
			f.mu.Unlock()
			if !strings.Contains(log, "bytes=0-0") || !strings.Contains(log, "bytes=0-99") {
				t.Errorf("ranges:\n%s", log)
			}
			if b, err := s.Read(ctx, "a b/c+d.txt", -1); err != nil || string(b) != "body of a b/c+d.txt" {
				t.Errorf("read %q, %v", b, err)
			}
			if e, err := s.Stat(ctx, "none.txt"); err != nil || e.Size != 0 {
				t.Errorf("empty: %+v, %v", e, err)
			}
			if b, err := s.Read(ctx, "none.txt", 100); err != nil || len(b) != 0 {
				t.Errorf("empty: %q, %v", b, err)
			}
			var nf *NotFound
			if _, err := s.Stat(ctx, "gone.txt"); !errors.As(err, &nf) {
				t.Errorf("gone: %v", err)
			}
			// A credential that ended is renewed on a read too.
			f.mu.Lock()
			f.valid["tok1"], f.valid["tok2"] = 0, -1
			f.mu.Unlock()
			if b, err := s.Read(ctx, "big.txt", 10); err != nil || len(b) != 10 {
				t.Errorf("after the end: %d, %v", len(b), err)
			}
		})
	}
}

// A store this package does not read, one whose credential cannot be had,
// and one that refuses the credential are read through the CLI.
func TestFallsBackToTheCLI(t *testing.T) {
	for _, why := range []string{"azure", "token", "denied"} {
		t.Run(why, func(t *testing.T) {
			f := newStore("x")
			csp := AWS
			if why == "azure" {
				csp = Azure
			}
			s, dir := setup(t, csp, f)
			switch why {
			case "token":
				put(t, dir, "fail-get-token", "2026-11-25T14:10:08Z [ERROR] not now\n")
			case "denied":
				f.valid["tok1"], f.deny = -1, true
			}
			put(t, dir, "ls,", `[{"uri":"pw://demo/b/top.txt","size":3,"modified":"2026-10-01T12:00:00Z"},{"uri":"pw://demo/b/notes/","directory":true},{"uri":"pw://demo/b/empty/","directory":true}]`)
			put(t, dir, "ls,notes,", `[{"uri":"pw://demo/b/notes/","size":0,"modified":"2026-10-01T12:00:00Z"},{"uri":"pw://demo/b/notes/r.md","size":5,"modified":"2026-10-01T12:00:00Z"},{"uri":"pw://demo/b/notes/deep/","directory":true}]`)
			put(t, dir, "ls,notes,deep,", `[{"uri":"pw://demo/b/notes/deep/x.bin","size":9,"modified":"2026-10-01T12:00:00Z"}]`)
			put(t, dir, "ls,notes,r.md", `[{"uri":"pw://demo/b/notes/r.md","size":5,"modified":"2026-10-01T12:00:00Z"},{"uri":"pw://demo/b/notes/r.md.old","size":5,"modified":"2026-10-01T12:00:00Z"}]`)
			put(t, dir, "obj,notes,r.md", "hello")
			got, dirs := walk(t, s, "")
			sort.Strings(got)
			sort.Strings(dirs)
			if fmt.Sprint(got) != "[notes/deep/x.bin notes/r.md top.txt]" || fmt.Sprint(dirs) != "[empty notes notes/deep]" {
				t.Fatalf("%v %v", got, dirs)
			}
			if got, _ := walk(t, s, "notes"); len(got) != 2 {
				t.Errorf("a folder: %v", got)
			}
			if r, w := s.Route(); r != ThroughCLI || w == nil {
				t.Errorf("route %s, %v", r, w)
			}
			e, err := s.Stat(context.Background(), "notes/r.md")
			if err != nil || e.Size != 5 {
				t.Errorf("stat %+v, %v", e, err)
			}
			if b, err := s.Read(context.Background(), "notes/r.md", 3); err != nil || string(b) != "hel" {
				t.Errorf("read %q, %v", b, err)
			}
			if b, err := s.Read(context.Background(), "notes/r.md", -1); err != nil || string(b) != "hello" {
				t.Errorf("read %q, %v", b, err)
			}
			var nf *NotFound
			if _, err := s.Read(context.Background(), "notes/none", -1); !errors.As(err, &nf) {
				t.Errorf("missing: %v", err)
			}
		})
	}
}

// Every pw run is the person's: their credential in the environment, no
// other sign-in, the context and host named, and a HOME of its own that is
// gone afterwards with what pw left in it. Nothing of it is in the
// arguments.
func TestRunsAsThePersonAndLeavesNothing(t *testing.T) {
	f := newStore("a.txt")
	f.valid["tok1"] = -1
	s, dir := setup(t, AWS, f)
	walk(t, s, "")
	put(t, dir, "buckets.json", `[{"name":"b","namespace":"demo","csp":"aws","region":"us-east-1","bucketName":"examplebucket","uri":"pw://demo/b","status":"provisioned","displayName":"Example"},
		{"name":"o","namespace":"demo","csp":"oracle","uri":"pw://demo/o","status":"provisioned"},{"name":"../x","namespace":"demo","uri":"pw://demo/../x"}]`)
	list, err := s.CLI.Buckets(context.Background())
	if err != nil || len(list) != 2 || list[0].Label != "Example" || list[0].Why != "" || list[1].Why == "" {
		t.Fatalf("%+v, %v", list, err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	for _, want := range []string{"PW_API_KEY=pwt_person\n", "PW_PLATFORM_HOST=platform.example\n", "PW_CONTEXT=\n", "PW_CREDENTIALS_DIR=\n"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("the environment has no %q", want)
		}
	}
	runs, _ := os.ReadFile(filepath.Join(dir, "runs"))
	for _, l := range strings.Split(strings.TrimSpace(string(runs)), "\n") {
		if !strings.HasPrefix(l, "--context studio --platform-host platform.example buckets ") {
			t.Errorf("run as %q", l)
		}
		if strings.Contains(l, "pwt_person") {
			t.Errorf("the credential is in the arguments: %q", l)
		}
	}
	homes, _ := os.ReadFile(filepath.Join(dir, "homes"))
	for _, h := range strings.Fields(string(homes)) {
		if _, err := os.Stat(h); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", h)
		}
	}
}

func TestNoSignIn(t *testing.T) {
	f := newStore("a.txt")
	s, _ := setup(t, AWS, f)
	s.CLI.Key = func(context.Context) (string, error) { return "", nil }
	if err := s.Walk(context.Background(), "", func(Entry) error { return nil }); !errors.Is(err, ErrNoSignIn) {
		t.Errorf("%v", err)
	}
	if s.CLI.Runs.Load() != 0 || f.requests() != 0 {
		t.Error("something was asked without a sign-in")
	}
}

// A listing stopped part way ends at once, leaving nothing.
func TestStopped(t *testing.T) {
	f := newStore(many(3000)...)
	f.valid["tok1"] = -1
	s, _ := setup(t, AWS, f)
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	err := s.Walk(ctx, "", func(Entry) error {
		if n++; n == 10 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
	stop := errors.New("enough")
	s2, _ := setup(t, AWS, f)
	if err := s2.Walk(context.Background(), "", func(Entry) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("%v", err)
	}
}

func TestHosts(t *testing.T) {
	for _, c := range []struct{ bucket, region, env, want string }{
		{"examplebucket", "us-east-1", "", "https://examplebucket.s3-fips.us-east-1.amazonaws.com/a%20b/c"},
		{"examplebucket", "us-gov-west-1", "", "https://examplebucket.s3-fips.us-gov-west-1.amazonaws.com/a%20b/c"},
		{"examplebucket", "eu-west-1", "", "https://examplebucket.s3.eu-west-1.amazonaws.com/a%20b/c"},
		{"examplebucket", "eu-west-1", "true", "https://examplebucket.s3-fips.eu-west-1.amazonaws.com/a%20b/c"},
		{"examplebucket", "us-east-1", "false", "https://examplebucket.s3.us-east-1.amazonaws.com/a%20b/c"},
		{"example.bucket", "us-east-1", "", "https://s3.us-east-1.amazonaws.com/example.bucket/a%20b/c"},
		{"example.bucket", "us-gov-west-1", "", "https://s3.us-gov-west-1.amazonaws.com/example.bucket/a%20b/c"},
		{"examplebucket", "us-gov-east-1", "", "https://examplebucket.s3-fips.us-gov-east-1.amazonaws.com/a%20b/c"},
		{"examplebucket", "us-gov-west-1", "false", "https://examplebucket.s3.us-gov-west-1.amazonaws.com/a%20b/c"},
		{"examplebucket", "us-iso-east-1", "", "https://examplebucket.s3.us-iso-east-1.c2s.ic.gov/a%20b/c"},
		{"examplebucket", "cn-north-1", "", "https://examplebucket.s3.cn-north-1.amazonaws.com.cn/a%20b/c"},
	} {
		t.Setenv("AWS_USE_FIPS_ENDPOINT", c.env)
		if got := newS3(nil, c.bucket, c.region, "").url("a b/c").String(); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}

// What a bucket is asked at and signed for is the region the platform
// says, else the credential's; without either no host is asked. A store
// never moves a bucket to another partition.
func TestRegionIsNeverGuessed(t *testing.T) {
	signed := func(s *Store) string { c := s.client.(*s3Client); return c.region() }
	f := newStore("a.txt")
	f.valid["tok1"] = -1
	s, dir := setup(t, AWS, f)
	s.B.Region = "us-gov-west-1"
	f.region = "us-gov-east-1"
	if got, _ := walk(t, s, ""); len(got) != 1 || signed(s) != "us-gov-east-1" {
		t.Errorf("within GovCloud: %v %s", got, signed(s))
	}
	s2, _ := setup(t, AWS, f)
	f.valid["tok1"], f.region = -1, "us-east-1"
	s2.B.Region = "us-gov-west-1"
	err := s2.Walk(context.Background(), "", func(Entry) error { return nil })
	if signed(s2) != "us-gov-west-1" {
		t.Errorf("moved out of GovCloud: %s, %v", signed(s2), err)
	}
	f.region = ""
	s3, _ := setup(t, AWS, f)
	s3.B.Region = ""
	n := f.requests()
	walk(t, s3, "")
	if r, why := s3.Route(); r != ThroughCLI || why == nil || !strings.Contains(why.Error(), "region") || f.requests() != n {
		t.Errorf("no region: %s, %v", r, why)
	}
	_ = dir
	if c := (&gcsClient{bucket: "b", base: "https://storage-x.p.googleapis.com/"}); c.at("", nil) != "https://storage-x.p.googleapis.com/storage/v1/b/b/o?" {
		t.Error(c.at("", nil))
	}
}

func TestTokenNamesTheRegion(t *testing.T) {
	f := newStore("a.txt")
	f.valid["tok1"] = -1
	s, dir := setup(t, AWS, f)
	s.B.Region = ""
	put(t, dir, "region", "us-gov-west-1")
	if got, _ := walk(t, s, ""); len(got) != 1 || s.client.(*s3Client).region() != "us-gov-west-1" {
		t.Errorf("%v", got)
	}
}

func TestBrowse(t *testing.T) {
	for _, csp := range []string{AWS, Google} {
		f := newStore("a.txt", "proj/", "proj/one/x", "proj/two/y", "proj/z.txt", "sp ace/q")
		f.valid["tok1"] = -1
		s, _ := setup(t, csp, f)
		names := func(dir string) string {
			list, more, err := s.Browse(context.Background(), dir, 100)
			if err != nil || more {
				t.Fatalf("%s %q: %v %v", csp, dir, more, err)
			}
			var out []string
			for _, e := range list {
				n := e.Path
				if e.IsDir {
					n += "/"
				}
				out = append(out, n)
			}
			return strings.Join(out, " ")
		}
		if got := names(""); got != "proj/ sp ace/ a.txt" {
			t.Errorf("%s top: %s", csp, got)
		}
		if got := names("proj"); got != "one/ two/ z.txt" {
			t.Errorf("%s proj: %s", csp, got)
		}
		if f.requests() != 2 {
			t.Errorf("%d requests", f.requests())
		}
	}
}

// A store that cannot be reached from here is given up on soon, and pw is
// asked at the same address.
func TestUnreachableStore(t *testing.T) {
	f := newStore("a.txt")
	s, dir := setup(t, AWS, f)
	s.Base, s.CLI.Endpoint = "http://127.0.0.1:1", "http://127.0.0.1:1"
	put(t, dir, "fail-ls", "2026-11-25T14:10:08Z [ERROR] connection refused\n")
	err := s.Walk(context.Background(), "", func(Entry) error { return nil })
	if err == nil || s.Requests.Load() != 2 || !strings.Contains(err.Error(), "127.0.0.1:1") || !strings.Contains(err.Error(), "through pw buckets ls: connection refused") {
		t.Errorf("%d requests, %v", s.Requests.Load(), err)
	}
	if env, _ := os.ReadFile(filepath.Join(dir, "env")); !strings.Contains(string(env), "AWS_ENDPOINT_URL=http://127.0.0.1:1\n") {
		t.Error("pw is not asked at the endpoint")
	}
}

func TestCheckEndpoint(t *testing.T) {
	t.Setenv("STUDIO_BUCKET_ENDPOINTS", "s3.gateway.example.com, store.example.com:9443")
	for _, c := range []struct {
		in     string
		shared bool
		want   string
	}{
		{"", true, ""},
		{"bucket.vpce-0example-abcd.s3.us-gov-west-1.vpce.amazonaws.com", true, "https://bucket.vpce-0example-abcd.s3.us-gov-west-1.vpce.amazonaws.com"},
		{"https://vpce-0example.s3.us-east-1.vpce.amazonaws.com/", true, "https://vpce-0example.s3.us-east-1.vpce.amazonaws.com"},
		{"https://S3.gateway.example.com", true, "https://s3.gateway.example.com"},
		{"https://store.example.com:9443", true, "https://store.example.com:9443"},
		{"https://store.example.com", true, "!"},
		{"https://169.254.169.254", true, "!"},
		{"http://169.254.169.254", false, "!"},
		{"https://10.0.0.5:8443", true, "!"},
		{"https://10.0.0.5:8443", false, "https://10.0.0.5:8443"},
		{"http://127.0.0.1:9000", true, "!"},
		{"http://127.0.0.1:9000", false, "http://127.0.0.1:9000"},
		{"http://s3.gateway.example.com", true, "!"},
		{"https://vpce-0example.s3.us-east-1.vpce.amazonaws.com.evil.example", true, "!"},
		{"https://vpce-0example.s3.us-east-1.vpce.amazonaws.com:22", true, "!"},
		{"https://user:pass@vpce-0example.s3.us-east-1.vpce.amazonaws.com", true, "!"},
		{"https://vpce-0example.s3.us-east-1.vpce.amazonaws.com/x?y", true, "!"},
		{"https://vpce-0example.s3.us-east-1.vpce.amazonaws.com/x", true, "!"},
		{"file:///etc/passwd", false, "!"},
	} {
		got, err := CheckEndpoint(c.in, c.shared)
		if err != nil {
			got = "!"
		}
		if got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
}

// The store is asked through the environment's proxy, and what the rest of
// the Studio trusts is trusted.
func TestProxyAndTrust(t *testing.T) {
	tr := newHTTP().Transport.(*http.Transport)
	if tr.Proxy == nil || tr.TLSClientConfig != nil && tr.TLSClientConfig.RootCAs != nil {
		t.Error("the proxy or the roots are its own")
	}
}

func TestParseURI(t *testing.T) {
	for _, ok := range []string{"pw://demo/b", "pw://demo/b/", " pw://a.b/c_d-1 "} {
		if _, _, err := ParseURI(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "pw://", "pw://demo", "pw://demo/b/c", "s3://demo/b", "pw://demo/../b", "pw://-x/b", "pw://demo/--context", "pw://de mo/b"} {
		if _, _, err := ParseURI(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

func TestExports(t *testing.T) {
	v := exports("export A='it'\\''s'\nexport B=\"x \\\" y\"\nexport C=plain\nnot D=1\n")
	if v["A"] != "it's" || v["B"] != `x " y` || v["C"] != "plain" || len(v) != 3 {
		t.Errorf("%q", v)
	}
}

// A credential pw prints in an error is not in what is shown.
func TestErrorsHoldNoCredential(t *testing.T) {
	got := plain("2026-11-25T14:10:08Z [ERROR] bad key pwt_abc.def for ASIAEXAMPLE123456 and my-own-key", nil, "my-own-key")
	if strings.Contains(got, "pwt_abc") || strings.Contains(got, "ASIA") || strings.Contains(got, "my-own-key") || !strings.HasPrefix(got, "bad key") {
		t.Errorf("%q", got)
	}
}

// Readings at once share one credential.
func TestManyReaders(t *testing.T) {
	f := newStore(many(20)...)
	f.valid["tok1"], f.valid["tok2"] = 10, -1
	s, dir := setup(t, AWS, f)
	var wg sync.WaitGroup
	var bad atomic.Int32
	for _, k := range f.keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b, err := s.Read(context.Background(), k, -1); err != nil || string(b) != "body of "+k {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(dir, "n"))
	if bad.Load() > 0 || strings.TrimSpace(string(b)) != "2" {
		t.Errorf("%d readings failed; %s tokens", bad.Load(), b)
	}
}
