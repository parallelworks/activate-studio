package libapi

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// The fake rclone: this test binary again (LIBAPI_TEST_CHILD=rclone),
// serving each folder of FAKE_RCLONE_DIR as an s3 remote, as an object
// store answers: a folder is a prefix, a missing key stats as a folder.
// Each command line goes to FAKE_RCLONE_LOG.
func fakeRclone(args []string) int {
	if lf := os.Getenv("FAKE_RCLONE_LOG"); lf != "" {
		if f, err := os.OpenFile(lf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			b, _ := json.Marshal(args)
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	var cmd, pos []string
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		switch x := args[i]; {
		case x == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case x == "--count":
			flags[x] = args[i+1]
			i++
		case strings.HasPrefix(x, "-"):
			flags[x] = "true"
		case len(cmd) == 0:
			cmd = append(cmd, x)
		default:
			pos = append(pos, x)
		}
	}
	root := os.Getenv("FAKE_RCLONE_DIR")
	local := func(spec string) string {
		name, p, _ := strings.Cut(spec, ":")
		return filepath.Join(root, name, filepath.FromSlash(p))
	}
	type entry struct {
		Path    string
		Name    string
		Size    int64
		ModTime string
		IsDir   bool
	}
	at := func(fi fs.FileInfo) string { return fi.ModTime().UTC().Format(time.RFC3339Nano) }
	switch cmd[0] {
	case "version":
		fmt.Println("rclone v1.76.0")
	case "listremotes":
		es, _ := os.ReadDir(root)
		for _, e := range es {
			fmt.Printf("%s: s3\n", e.Name())
		}
	case "backend":
		fmt.Print(`{"Name":"s3","Hashes":["md5"],"Features":{"About":false,"CanHaveEmptyDirectories":false,"Copy":true,"Move":false}}`)
	case "config":
		name := strings.TrimSuffix(pos[1], ":")
		fmt.Printf("[%s]\ntype = s3\nprovider = Minio\nsecret_access_key = XXX\n", name)
	case "cat":
		f, err := os.Open(local(pos[0]))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR : object not found")
			return 4
		}
		var r io.Reader = f
		if c := flags["--count"]; c != "" {
			n, _ := strconv.ParseInt(c, 10, 64)
			r = io.LimitReader(f, n)
		}
		io.Copy(os.Stdout, r)
	case "lsjson":
		p := local(pos[0])
		if flags["--stat"] != "" {
			fi, err := os.Stat(p)
			e := entry{Size: -1, ModTime: time.Now().UTC().Format(time.RFC3339Nano), IsDir: true}
			if err == nil && !fi.IsDir() {
				e = entry{Path: fi.Name(), Name: fi.Name(), Size: fi.Size(), ModTime: at(fi)}
			}
			b, _ := json.Marshal(e)
			fmt.Println(string(b))
			return 0
		}
		var out []entry
		if flags["-R"] != "" {
			var dirs []entry
			filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
				if err != nil || f == p {
					return nil
				}
				rel, _ := filepath.Rel(p, f)
				fi, _ := d.Info()
				if d.IsDir() {
					dirs = append(dirs, entry{Path: filepath.ToSlash(rel), Name: d.Name(), Size: -1, ModTime: time.Now().UTC().Format(time.RFC3339Nano), IsDir: true})
				} else {
					out = append(out, entry{Path: filepath.ToSlash(rel), Name: d.Name(), Size: fi.Size(), ModTime: at(fi)})
				}
				return nil
			})
			// As rclone's fast list gives them: the files, then the folders.
			out = append(out, dirs...)
		} else {
			es, _ := os.ReadDir(p)
			for _, d := range es {
				fi, _ := d.Info()
				e := entry{Path: d.Name(), Name: d.Name(), Size: fi.Size(), ModTime: at(fi), IsDir: d.IsDir()}
				if d.IsDir() {
					e.Size = -1
				}
				out = append(out, e)
			}
		}
		if out == nil {
			out = []entry{}
		}
		b, _ := json.Marshal(out)
		fmt.Println(string(b))
	default:
		fmt.Fprintln(os.Stderr, "unknown command "+cmd[0])
		return 1
	}
	return 0
}

func fakeRcloneLog(t *testing.T, log string) [][]string {
	t.Helper()
	raw, _ := os.ReadFile(log)
	var out [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var a []string
		if json.Unmarshal([]byte(l), &a) == nil {
			out = append(out, a)
		}
	}
	return out
}

// A bucket library end to end, against a fake s3 remote: refusals before
// anything is kept; the first listing is one fast list, built into an
// index with gufi_trace2index; search finds objects by name; the Library
// lists its folders from the index; a text object's preview reads its
// first bytes alone; a download reads one object; the depth stays at
// names; the interval decides when it is listed again; a new listing
// finds a new object; removing it removes its index. Needs GUFI with
// gufi_trace2index.
func TestBucketLibrary(t *testing.T) {
	if _, err := os.Stat(filepath.Join(testGufi, "gufi_trace2index")); err != nil {
		t.Skip("GUFI has no gufi_trace2index")
	}
	f := newBuildFixture(t)
	kb.Register(f.mux, f.a)
	remotes := filepath.Join(f.dir, "remotes")
	bucket := filepath.Join(remotes, "store", "research")
	put(t, filepath.Join(bucket, "readme.txt"), "top")
	put(t, filepath.Join(bucket, "projects", "alpha", "summary.txt"), "alpha results summary")
	put(t, filepath.Join(bucket, "projects", "beta", "plan.md"), "# Beta plan\n"+strings.Repeat("wind tunnel runs\n", 50000))
	put(t, filepath.Join(bucket, "projects", "beta", "raw", "zephyr-run1.bin"), strings.Repeat("x", 4096))
	exe, _ := os.Executable()
	bin := filepath.Join(f.dir, "rclone-bin")
	put(t, filepath.Join(bin, "rclone"), "#!/bin/sh\nLIBAPI_TEST_CHILD=rclone exec '"+exe+"' \"$@\"\n")
	os.Chmod(filepath.Join(bin, "rclone"), 0o755)
	log := filepath.Join(f.dir, "rclone.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_RCLONE_DIR", remotes)
	t.Setenv("FAKE_RCLONE_LOG", log)

	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"location": "computer:Downloads"}, "location: an rclone remote"},
		{map[string]any{"location": "rclone:store", "path": "research", "every": 5}, "every: 0, 60, 360, 1440 minutes"},
		{map[string]any{"location": "rclone:store", "path": "../x"}, ""},
		{map[string]any{"location": "rclone:nowhere", "path": "research"}, ""},
	} {
		code, b := f.call("POST", "/api/libraries/bucket", c.body)
		if code < 400 || (c.want != "" && !strings.Contains(fmt.Sprint(b["error"]), c.want)) {
			t.Fatalf("%v: %d %v", c.body, code, b)
		}
	}

	code, b := f.call("POST", "/api/libraries/bucket", map[string]any{"location": "rclone:store", "path": "research", "every": 60})
	if code != 200 {
		t.Fatalf("add: %d %v", code, b)
	}
	id := b["library"].(map[string]any)["id"].(string)
	if id != "research" {
		t.Fatalf("id %s", id)
	}
	l := f.waitReady(id)
	if l["build"].(map[string]any)["state"] != "ready" {
		t.Fatalf("build %v", l["build"])
	}
	bk := l["bucket"].(map[string]any)
	if bk["objects"] != float64(4) || bk["requests"] != float64(1) || bk["provider"] != "Minio" || bk["listedAt"] == nil || bk["error"] != nil || bk["listing"] != false {
		t.Fatalf("bucket after the first listing: %v", bk)
	}
	if l["bucket"] == nil || l["sourceRoot"] != nil || l["writable"] != false {
		t.Fatalf("a bucket library has no folder here and is read only: %v", l)
	}
	var lists [][]string
	for _, a := range fakeRcloneLog(t, log) {
		if a[0] == "lsjson" && slices.Contains(a, "-R") {
			lists = append(lists, a)
		}
	}
	if len(lists) != 1 || !slices.Contains(lists[0], "--fast-list") {
		t.Fatalf("one fast list: %v", lists)
	}
	for _, a := range fakeRcloneLog(t, log) {
		if a[0] == "cat" {
			t.Fatalf("indexing read an object: %v", a)
		}
	}

	if h := f.hits("zephyr", id); !slices.Equal(h, []string{"projects/beta/raw/zephyr-run1.bin"}) {
		t.Fatalf("hits %v", h)
	}

	// The Library lists its folders from the index.
	code, b = f.call("GET", "/api/kb/tree?library="+id+"&path=projects/beta", nil)
	if code != 200 {
		t.Fatalf("tree: %d %v", code, b)
	}
	var names []string
	for _, e := range b["entries"].([]any) {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "raw,plan.md" {
		t.Fatalf("tree %v", names)
	}

	// The Details card says where it was listed from.
	if code, b := f.call("GET", "/api/kb/details?library="+id+"&path=projects/alpha", nil); code != 200 || b["library"].(map[string]any)["kind"] != "Indexed from a listing of store, research" {
		t.Fatalf("details: %d %v", code, b["library"])
	}

	// A text object's preview reads its first bytes alone.
	code, b = f.call("GET", "/api/kb/file?library="+id+"&path="+url.QueryEscape("projects/beta/plan.md"), nil)
	if code != 200 || !strings.HasPrefix(fmt.Sprint(b["content"]), "# Beta plan") || b["truncated"] != true || b["source"] != "raw" {
		t.Fatalf("file: %d %v", code, b["truncated"])
	}
	var cat []string
	for _, a := range fakeRcloneLog(t, log) {
		if a[0] == "cat" {
			cat = a
		}
	}
	if i := slices.Index(cat, "--count"); i < 0 || cat[i+1] != fmt.Sprint(512*1024+1) || cat[len(cat)-1] != "store:research/projects/beta/plan.md" {
		t.Fatalf("the preview's read: %v", cat)
	}
	if code, b = f.call("GET", "/api/kb/file?library="+id+"&path=nowhere.txt", nil); code != 404 {
		t.Fatalf("a missing object: %d %v", code, b)
	}

	// A download reads the object whole.
	r := httptest.NewRequest("GET", "/api/kb/download?library="+id+"&path="+url.QueryEscape("projects/alpha/summary.txt"), nil)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "alpha results summary" {
		t.Fatalf("download: %d %q", w.Code, w.Body.String())
	}

	// The assistant's read_original reads the object, and says which
	// bucket it tried for one that is not there.
	lib, err := f.s.Reg.Get(id, "", libraries.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	o, err := f.s.OpenOriginal(t.Context(), lib, "projects/alpha/summary.txt", libraries.Deployment)
	if content, _ := o.File.Get("content"); err != nil || o.Where != "store, research" || content != "alpha results summary" {
		t.Fatalf("read_original: %v %+v", err, o)
	}
	if _, err := f.s.OpenOriginal(t.Context(), lib, "gone.txt", libraries.Deployment); err == nil || !strings.Contains(err.Error(), "store, research") {
		t.Fatalf("read_original of a missing object: %v", err)
	}
	if name, body, err := f.s.DownloadOriginal(t.Context(), lib, "projects/beta/raw/zephyr-run1.bin", libraries.Deployment); err != nil || name != "zephyr-run1.bin" || len(body) != 4096 {
		t.Fatalf("download original: %v %s %d", err, name, len(body))
	}

	// Names only, for now.
	if code, b := f.call("PUT", "/api/libraries/"+id+"/depth", map[string]any{"depth": "text"}); code != 409 || !strings.Contains(fmt.Sprint(b["error"]), "listing") {
		t.Fatalf("depth: %d %v", code, b)
	}

	// The interval decides when it is listed again.
	st := f.s.Reg.BucketState(id)
	at, _ := time.Parse(time.RFC3339Nano, *st.ListedAt)
	bc := f.s.Reg.AddedDef(id).Bucket
	if bucketDue(bc, st, at.Add(59*time.Minute)) || !bucketDue(bc, st, at.Add(60*time.Minute)) {
		t.Fatal("bucketDue does not follow the interval")
	}
	if bucketDue(&libraries.BucketOf{Every: 0}, libraries.BucketState{}, at) {
		t.Fatal("every 0 is only when asked")
	}
	if code, b := f.call("PUT", "/api/libraries/"+id+"/bucket", map[string]any{"every": 0}); code != 200 || b["library"].(map[string]any)["bucket"].(map[string]any)["every"] != float64(0) {
		t.Fatalf("every: %d %v", code, b)
	}

	// A new object shows after the next listing, asked for with a rebuild.
	put(t, filepath.Join(bucket, "notes", "quokka-notes.txt"), "later")
	if code, b := f.call("POST", "/api/libraries/"+id+"/rebuild", nil); code != 200 {
		t.Fatalf("list again: %d %v", code, b)
	}
	f.waitReady(id)
	if h := f.hits("quokka", id); !slices.Equal(h, []string{"notes/quokka-notes.txt"}) {
		t.Fatalf("hits after listing again %v", h)
	}
	if f.s.Reg.BucketState(id).Objects != 5 {
		t.Fatalf("state %+v", f.s.Reg.BucketState(id))
	}

	// Removing it removes its index.
	if code, b := f.call("DELETE", "/api/libraries/"+id, nil); code != 200 {
		t.Fatalf("remove: %d %v", code, b)
	}
	if _, err := os.Stat(f.s.Reg.BuiltLibraryDir(id)); err == nil {
		t.Fatal("its folder is still there")
	}
}

func TestListRequests(t *testing.T) {
	for n, want := range map[int64]int64{0: 1, 1: 1, 1000: 1, 1001: 2, 100_000_000: 100_000} {
		if got := listRequests(n); got != want {
			t.Errorf("listRequests(%d) = %d, want %d", n, got, want)
		}
	}
}
