package libapi

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/extdb"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

func TestBucketFsid(t *testing.T) {
	for _, c := range [][3]string{
		{"s3.us-east-1.amazonaws.com", "research-data/projects", "s3.us-east-1.amazonaws.com_research-data_projects"},
		{"127.0.0.1:39500", "Research", "127.0.0.1_39500_research"},
		{"", "", "bucket"},
	} {
		if got := BucketFsid(c[0], c[1]); got != c[2] {
			t.Errorf("BucketFsid(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	long := BucketFsid("store.example.com", strings.Repeat("deep/", 40))
	if len(long) != 80 || long == BucketFsid("store.example.com", strings.Repeat("deep/", 41)) {
		t.Errorf("a long name is cut and told apart by its hash: %q", long)
	}
}

// waitText waits for a bucket library's text pass to end.
func (f *buildFixture) waitText(id string) map[string]any {
	f.t.Helper()
	for i := 0; i < 600; i++ {
		l := f.waitReady(id)
		bk := l["bucket"].(map[string]any)
		if bk["reading"] == false && bk["listing"] == false && f.s.builds.jobOf(id) == nil {
			return bk
		}
		time.Sleep(100 * time.Millisecond)
	}
	f.t.Fatal("the text pass did not end")
	return nil
}

// Reading a bucket library's text, against a fake s3 remote: off by
// default; the estimate counts what a pass would read; turning it on
// lists the bucket and reads each readable object once, through
// index-extract; search finds the objects by their words; a new listing
// keeps the text, reads only what is new or changed, and drops the text
// of an object that went. Needs GUFI with gufi_trace2index.
func TestBucketText(t *testing.T) {
	if _, err := os.Stat(filepath.Join(testGufi, "gufi_trace2index")); err != nil {
		t.Skip("GUFI has no gufi_trace2index")
	}
	f := newBuildFixture(t)
	kb.Register(f.mux, f.a)
	remotes := filepath.Join(f.dir, "remotes")
	bucket := filepath.Join(remotes, "store", "research")
	put(t, filepath.Join(bucket, "readme.txt"), "top")
	put(t, filepath.Join(bucket, "projects", "alpha", "summary.txt"), "alpha results: the marmoset trials")
	put(t, filepath.Join(bucket, "projects", "beta", "plan.md"), "# Beta plan\n\nwind tunnel runs with the narwhal rig\n")
	put(t, filepath.Join(bucket, "projects", "beta", "raw", "run1.bin"), strings.Repeat("x", 4096))
	put(t, filepath.Join(bucket, "gone.txt"), "the axolotl notes")
	exe, _ := os.Executable()
	bin := filepath.Join(f.dir, "bin")
	put(t, filepath.Join(bin, "rclone"), "#!/bin/sh\nLIBAPI_TEST_CHILD=rclone exec '"+exe+"' \"$@\"\n")
	put(t, filepath.Join(bin, "index-extract"), "#!/bin/sh\nLIBAPI_TEST_CHILD=index-extract exec '"+exe+"' \"$@\"\n")
	os.Chmod(filepath.Join(bin, "rclone"), 0o755)
	os.Chmod(filepath.Join(bin, "index-extract"), 0o755)
	log := filepath.Join(f.dir, "rclone.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_RCLONE_DIR", remotes)
	t.Setenv("FAKE_RCLONE_LOG", log)
	t.Setenv("STUDIO_INDEX_EXTRACT_BIN", filepath.Join(bin, "index-extract"))
	reads := func() []string {
		var out []string
		for _, a := range fakeRcloneLog(t, log) {
			if a[0] == "cat" && !slices.Contains(a, "--count") {
				out = append(out, a[len(a)-1])
			}
		}
		sort := slices.Clone(out)
		slices.Sort(sort)
		return sort
	}

	code, b := f.call("POST", "/api/libraries/bucket", map[string]any{"location": "rclone:store", "path": "research", "every": 0})
	if code != 200 {
		t.Fatalf("add: %d %v", code, b)
	}
	id := b["library"].(map[string]any)["id"].(string)
	bk := f.waitText(id)
	if bk["text"] != false || bk["textPass"] != nil || len(reads()) != 0 {
		t.Fatalf("text is off until asked: %v %v", bk, reads())
	}
	if h := f.hits("marmoset", id); len(h) != 0 {
		t.Fatalf("no text yet: %v", h)
	}

	code, b = f.call("GET", "/api/libraries/"+id+"/bucket/text", nil)
	if code != 200 || b["objects"] != float64(4) || b["other"] != float64(1) || b["done"] != float64(0) || b["provider"] != "Minio" || b["rate"] != float64(0) {
		t.Fatalf("estimate: %d %v", code, b)
	}

	if code, b = f.call("PUT", "/api/libraries/"+id+"/bucket", map[string]any{"text": true}); code != 200 || b["library"].(map[string]any)["bucket"].(map[string]any)["text"] != true {
		t.Fatalf("text on: %d %v", code, b)
	}
	bk = f.waitText(id)
	pass, _ := bk["textPass"].(map[string]any)
	if pass == nil || pass["error"] != nil || pass["files"] != float64(4) || pass["requests"] != float64(4) || pass["read"].(map[string]any)["text"] != float64(4) {
		t.Fatalf("the first pass: %v", bk)
	}
	want := []string{"store:research/gone.txt", "store:research/projects/alpha/summary.txt", "store:research/projects/beta/plan.md", "store:research/readme.txt"}
	if got := reads(); !slices.Equal(got, want) {
		t.Fatalf("each readable object read once: %v", got)
	}
	if bk["fsid"] != "s3_research" {
		t.Fatalf("fsid %v", bk["fsid"])
	}
	if l := f.library(id); l["caps"].(map[string]any)["fullText"] != true {
		t.Fatalf("caps after the first pass: %v", l["caps"])
	}
	for q, p := range map[string]string{"marmoset": "projects/alpha/summary.txt", "narwhal": "projects/beta/plan.md", "axolotl": "gone.txt"} {
		if h := f.hits(q, id); !slices.Equal(h, []string{p}) {
			t.Fatalf("search for %s: %v", q, h)
		}
	}
	if code, b = f.call("GET", "/api/libraries/"+id+"/bucket/text", nil); b["objects"] != float64(0) || b["done"] != float64(4) {
		t.Fatalf("estimate after: %d %v", code, b)
	}

	// A new listing: one object new, one changed, one gone.
	put(t, filepath.Join(bucket, "notes", "quokka.txt"), "the pangolin study")
	put(t, filepath.Join(bucket, "projects", "beta", "plan.md"), "# Beta plan\n\nnow with the okapi rig\n")
	future := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(bucket, "projects", "beta", "plan.md"), future, future)
	os.Remove(filepath.Join(bucket, "gone.txt"))
	os.Truncate(log, 0)
	if code, b := f.call("POST", "/api/libraries/"+id+"/rebuild", nil); code != 200 {
		t.Fatalf("list again: %d %v", code, b)
	}
	bk = f.waitText(id)
	if got := reads(); !slices.Equal(got, []string{"store:research/notes/quokka.txt", "store:research/projects/beta/plan.md"}) {
		t.Fatalf("only what is new or changed is read: %v", got)
	}
	for q, want := range map[string][]string{"marmoset": {"projects/alpha/summary.txt"}, "okapi": {"projects/beta/plan.md"}, "pangolin": {"notes/quokka.txt"}, "narwhal": nil, "axolotl": nil} {
		if h := f.hits(q, id); !slices.Equal(h, want) {
			t.Fatalf("search for %s after listing again: %v", q, h)
		}
	}
	if pass := bk["textPass"].(map[string]any); pass["files"] != float64(4) {
		t.Fatalf("the second pass: %v", pass)
	}

	// Off: nothing more is read; what was read stays searchable.
	if code, b := f.call("PUT", "/api/libraries/"+id+"/bucket", map[string]any{"text": false}); code != 200 || b["library"].(map[string]any)["bucket"].(map[string]any)["text"] != false {
		t.Fatalf("text off: %d %v", code, b)
	}
	put(t, filepath.Join(bucket, "notes", "later.txt"), "the tapir log")
	os.Truncate(log, 0)
	f.call("POST", "/api/libraries/"+id+"/rebuild", nil)
	f.waitText(id)
	if got := reads(); len(got) != 0 {
		t.Fatalf("read with text off: %v", got)
	}
	if h := f.hits("marmoset", id); len(h) != 1 {
		t.Fatalf("the text read before is kept: %v", h)
	}
	if _, err := os.Stat(filepath.Join(f.s.Reg.BuiltLibraryDir(id), "spread", extdb.Prefix)); err != nil {
		t.Fatalf("no spread tree: %v", err)
	}
}
