//go:build !windows

package transfers

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func names(es []Entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// TestListTwoReadsTheFoldersInside: one command answers a folder and the
// folders in it, but never claims to know a folder it could not list or
// one past where the answer was cut off.
func TestListTwoReadsTheFoldersInside(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GNU find")
	}
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "top.txt"), "x")
	write(t, filepath.Join(root, "a", "one.txt"), "1")
	write(t, filepath.Join(root, "a", "deep", "two.txt"), "2")
	write(t, filepath.Join(root, "a", ".hidden"), "h")
	write(t, filepath.Join(root, "b", "line\nbreak.txt"), "3")
	os.MkdirAll(filepath.Join(root, "empty"), 0o755)
	write(t, filepath.Join(root, ".secret", "x.txt"), "s")
	// A hidden folder asked for by its path, below the top and at it: what
	// is in its folders is read, and hidden names in it are still left out.
	write(t, filepath.Join(root, "proj", ".agents", "skills", "one", "SKILL.md"), "s")
	write(t, filepath.Join(root, "proj", ".agents", "skills", ".cache", "x"), "c")
	write(t, filepath.Join(root, ".agents", "skills", "two", "SKILL.md"), "s")
	write(t, filepath.Join(root, "we[i]*rd", ".hid", "x"), "h")
	write(t, filepath.Join(root, "we[i]*rd", "sub", "f"), "f")
	write(t, filepath.Join(root, "a", ".hid2", "kept.txt"), "k")
	write(t, filepath.Join(root, "closed", "c.txt"), "c")
	os.Chmod(filepath.Join(root, "closed"), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "closed"), 0o755) })
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []bool{false, true} {
		var fs *RemoteFS
		if kept {
			p, _ := keptRig(t)
			fs = &RemoteFS{R: PoolRunner{P: p}, SiteID: "h", KeyText: "k", Dir: root, Name: "space", Private: true, Kept: true}
		} else {
			fs = &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "space", Private: true}
		}
		two, err := fs.ListTwo(ctx, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(two.Entries); got != "a,b,closed,empty,link,proj,top.txt,we[i]*rd" {
			t.Fatalf("kept %v: entries %s", kept, got)
		}
		if got := names(two.Children["a"]); got != "deep,one.txt" {
			t.Fatalf("kept %v: a holds %s", kept, got)
		}
		if got := names(two.Children["b"]); got != "line\nbreak.txt" {
			t.Fatalf("kept %v: b holds %q", kept, got)
		}
		if es, ok := two.Children["empty"]; !ok || len(es) != 0 {
			t.Fatalf("kept %v: empty folder %v %v", kept, es, ok)
		}
		if os.Geteuid() != 0 {
			if _, ok := two.Children["closed"]; ok {
				t.Fatalf("kept %v: a folder that cannot be listed was taken as known", kept)
			}
		}
		if _, ok := two.Children["link"]; ok {
			t.Fatalf("kept %v: a link was listed as a folder", kept)
		}
		// A sub folder, and an answer cut off after two entries: only the
		// folders read whole before the cut are known.
		sub, err := fs.ListTwo(ctx, "a", 100)
		if err != nil || names(sub.Entries) != "deep,one.txt" || names(sub.Children["a/deep"]) != "two.txt" {
			t.Fatalf("kept %v: a: %+v %v", kept, sub, err)
		}
		for rel, want := range map[string][2]string{
			"proj/.agents": {"proj/.agents/skills", "one"},
			".agents":      {".agents/skills", "two"},
			"we[i]*rd":     {"we[i]*rd/sub", "f"},
		} {
			h, err := fs.ListTwo(ctx, rel, 100)
			if err != nil {
				t.Fatalf("kept %v: %s: %v", kept, rel, err)
			}
			if got := names(h.Children[want[0]]); got != want[1] {
				t.Fatalf("kept %v: %s read as holding %q, not %q", kept, want[0], got, want[1])
			}
			if _, ok := h.Children["we[i]*rd/.hid"]; ok {
				t.Fatalf("kept %v: a hidden folder was listed", kept)
			}
		}
		// A folder given with a trailing slash: the helper itself leaves out
		// what is in its hidden folders (the Go side would drop it too, as
		// their parent is not listed, but the entries count toward the cap).
		for _, rel := range []string{"a/", "a//"} {
			res, err := fs.runMarked(ctx, marker(), "list2", rel, "100")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(res.Raw), "kept.txt") {
				t.Fatalf("kept %v: list2 %q lists what is in a hidden folder", kept, rel)
			}
		}
		cut, err := fs.ListTwo(ctx, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(cut.Children) >= len(two.Children) {
			t.Fatalf("kept %v: a cut answer knows every folder: %v", kept, cut.Children)
		}
		for d, es := range cut.Children {
			full := names(two.Children[d])
			if names(es) != full {
				t.Fatalf("kept %v: cut answer claims %s holds %s, not %s", kept, d, names(es), full)
			}
		}
		if _, _, err := fs.List(ctx, "../", 10); err == nil {
			t.Fatalf("kept %v: listing outside the location", kept)
		}
		if _, err := fs.ListTwo(ctx, "link", 10); err == nil {
			t.Fatalf("kept %v: a link listed as a folder", kept)
		}
	}
}
