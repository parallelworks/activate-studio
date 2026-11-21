//go:build !windows

package transfers

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// keptRig is a location reached through a real remote.Pool whose sessions
// run here, counting the sessions started.
func keptRig(t *testing.T) (*remote.Pool, *atomic.Int32) {
	var started atomic.Int32
	p := remote.New(remote.Options{IndexBase: t.TempDir(), Spawn: func(site, key, command string) *exec.Cmd {
		started.Add(1)
		return exec.Command("bash", "-c", command)
	}})
	t.Cleanup(p.Close)
	return p, &started
}

// TestKeptSessionCarriesTheHelper: over a kept-open session, the helper's
// commands and their lists reach the host whole, whatever the names hold,
// and only one session is started, until a file too large to send as an
// argument is written.
func TestKeptSessionCarriesTheHelper(t *testing.T) {
	ctx := context.Background()
	p, started := keptRig(t)
	root := t.TempDir()
	fs := &RemoteFS{R: PoolRunner{P: p}, SiteID: "cluster-a", KeyText: "k", Dir: root, Name: "space", Private: true, Kept: true}
	names := []string{"a b.txt", "new\nline.txt", "-dash.txt", `quo'te".txt`, "ü.txt", "$(touch pwned).txt", "back\\slash.txt", "tab\there.txt"}
	for _, n := range names {
		if _, err := fs.Write(ctx, "in/"+n, strings.NewReader("body of "+n), 0, 0o644); err != nil {
			t.Fatalf("write %q: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "in", "pwned")); err == nil {
		t.Fatal("a name ran as a command")
	}
	for _, n := range names {
		if b, _ := os.ReadFile(filepath.Join(root, "in", n)); string(b) != "body of "+n {
			t.Fatalf("%q holds %q", n, b)
		}
	}
	list, _, err := fs.List(ctx, "in", 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range list {
		got = append(got, e.Name)
	}
	want := append([]string{}, names...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("listed %q, want %q", got, want)
	}
	rels := make([]string, len(names))
	for i, n := range names {
		rels[i] = "in/" + n
	}
	st, err := fs.Stat(ctx, append(rels, "in/missing"))
	if err != nil || len(st) != len(names) {
		t.Fatalf("stat %d of %d: %v", len(st), len(names), err)
	}
	var pairs []LandPair
	for _, n := range names {
		pairs = append(pairs, LandPair{Staged: "in/" + n, Final: "out/" + n})
	}
	done, err := fs.Land(ctx, pairs)
	if err != nil || len(done) != len(names) {
		t.Fatalf("landed %q: %v", done, err)
	}
	if err := fs.Mkdir(ctx, "made/deep"); err != nil {
		t.Fatal(err)
	}
	gone, err := fs.RemoveTrees(ctx, []string{"out/" + names[1], "made"})
	if err != nil || len(gone) != 2 {
		t.Fatalf("removed %q: %v", gone, err)
	}
	if n := started.Load(); n != 1 {
		t.Fatalf("%d sessions started, want the one kept open", n)
	}
	// An empty file, and one just over what goes as an argument.
	if _, err := fs.Write(ctx, "empty.txt", strings.NewReader(""), 0, 0o644); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("0123456789abcdef"), smallWrite/16+1)
	if n, err := fs.Write(ctx, "big.bin", bytes.NewReader(big), 0, 0o644); err != nil || n != int64(len(big)) {
		t.Fatalf("big write %d: %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "big.bin")); !bytes.Equal(b, big) {
		t.Fatal("the large file was not written whole")
	}
	if fi, err := os.Stat(filepath.Join(root, "empty.txt")); err != nil || fi.Size() != 0 {
		t.Fatalf("empty file: %v", err)
	}
	if n := started.Load(); n != 2 {
		t.Fatalf("%d sessions started, want one more for the large file", n)
	}
	// A list longer than goes as arguments takes a session of its own.
	var long []string
	for i := 0; len(strings.Join(long, "x")) <= keptMax; i++ {
		long = append(long, strings.Repeat("n", 200)+string(rune('a'+i%26)))
	}
	if _, err := fs.Stat(ctx, long); err != nil {
		t.Fatal(err)
	}
	if n := started.Load(); n != 3 {
		t.Fatalf("%d sessions started after a long list", n)
	}
	// A failing command still says why, with its status.
	if _, _, err := fs.List(ctx, "nowhere", 10); err == nil || !strings.Contains(err.Error(), "no such folder") {
		t.Fatalf("listing a missing folder: %v", err)
	}
}

// TestKeptPrivateWritesDoNotFollowLinks is the link checks of a private
// location, over a kept-open session.
func TestKeptPrivateWritesDoNotFollowLinks(t *testing.T) {
	p, _ := keptRig(t)
	privateLinks(t, func(root string, private bool) *RemoteFS {
		return &RemoteFS{R: PoolRunner{P: p}, SiteID: "h", KeyText: "k", Dir: root, Name: "space", Private: private, Kept: true}
	})
}
