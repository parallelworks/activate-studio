package transfers

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestBucketListsWithOneFastList(t *testing.T) {
	r, dir, log := rcloneRig(t)
	t.Setenv("FAKE_RCLONE_PROVIDERS", "s=AWS")
	ctx := context.Background()
	os.MkdirAll(filepath.Join(dir, "s", "research", "projects", "beta"), 0o755)
	os.WriteFile(filepath.Join(dir, "s", "research", "readme.txt"), []byte("top"), 0o644)
	os.WriteFile(filepath.Join(dir, "s", "research", "projects", "beta", "plan.md"), []byte("# Beta plan\nwind tunnel runs\n"), 0o644)

	if _, err := r.s.Bucket(ctx, single, "saved:x"); err == nil {
		t.Fatal("a location other than an rclone remote is refused")
	}
	b, err := r.s.Bucket(ctx, single, "rclone:s")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := b.Walk(ctx, "research", func(e BucketEntry) error {
		if !e.IsDir {
			got = append(got, e.Path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "projects/beta/plan.md,readme.txt" {
		t.Errorf("listing %v", got)
	}
	var walked []string
	for _, l := range fakeLog(t, log) {
		if slices.Contains(l, "lsjson") && slices.Contains(l, "-R") {
			walked = l
		}
	}
	if !slices.Contains(walked, "--fast-list") || !slices.Contains(walked, "--no-mimetype") || !slices.Contains(walked, "--use-server-modtime") {
		t.Errorf("the listing should be one fast list from the listing alone: %v", walked)
	}

	st, err := b.Stat(ctx, "research/projects/beta/plan.md")
	if err != nil || st.Size != 29 || st.IsDir {
		t.Errorf("stat %+v %v", st, err)
	}
	// As S3 answers: a key that is not there is a prefix, a folder.
	if st, err := b.Stat(ctx, "research/missing.txt"); err != nil || !st.IsDir {
		t.Errorf("a missing object stats as a folder: %+v %v", st, err)
	}
	head, err := b.Read(ctx, "research/projects/beta/plan.md", 6)
	if err != nil || string(head) != "# Beta" {
		t.Errorf("first bytes %q %v", head, err)
	}
	var ranged []string
	for _, l := range fakeLog(t, log) {
		if slices.Contains(l, "cat") {
			ranged = l
		}
	}
	if i := slices.Index(ranged, "--count"); i < 0 || ranged[i+1] != "6" {
		t.Errorf("a preview asks the store for its range alone: %v", ranged)
	}
	all, err := b.Read(ctx, "research/readme.txt", -1)
	if err != nil || string(all) != "top" {
		t.Errorf("whole object %q %v", all, err)
	}
	if p := b.Provider(ctx); p != "AWS" {
		t.Errorf("provider %q", p)
	}
	if p := NewBucket(b.fs.Bin, "a", "alias").Provider(ctx); p != "" {
		t.Errorf("a remote with no provider says none, got %q", p)
	}
}

func TestBucketNeedsSignInOff(t *testing.T) {
	r, _, _ := rcloneRig(t)
	if _, err := r.s.Bucket(context.Background(), Viewer{AuthOn: true}, "rclone:s"); err == nil {
		t.Fatal("with sign-in on, the Studio's rclone configuration is not lent")
	}
}

func TestRedactedValue(t *testing.T) {
	cfg := "[x]\ntype = s3\nprovider = Ceph\nsecret_access_key = XXX\n[y]\nprovider = AWS\n"
	if v := redactedValue([]byte(cfg), "provider"); v != "Ceph" {
		t.Errorf("provider %q", v)
	}
	if v := redactedValue([]byte(cfg), "endpoint"); v != "" {
		t.Errorf("endpoint %q", v)
	}
}
