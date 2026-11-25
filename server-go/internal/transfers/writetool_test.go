//go:build !windows

package transfers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/writehelper"
)

var (
	writerOnce sync.Once
	writerErr  error
)

// testWriter is the write helper as a library on this computer has it; nil
// where none is built for it.
func testWriter(t *testing.T) func(context.Context) WriteTool {
	t.Helper()
	if runtime.GOOS != "linux" {
		return nil
	}
	writerOnce.Do(func() {
		if testWriterDir, writerErr = os.MkdirTemp("", "studio-write-"); writerErr != nil {
			return
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(testWriterDir, "studio-write"), "../../cmd/studio-write")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			writerErr = fmt.Errorf("%v: %s", err, b)
		}
	})
	if writerErr != nil {
		t.Fatal(writerErr)
	}
	return func(context.Context) WriteTool { return WriteTool{Path: filepath.Join(testWriterDir, "studio-write")} }
}

func needWriter(t *testing.T) func(context.Context) WriteTool {
	t.Helper()
	w := testWriter(t)
	if w == nil {
		t.Skip("no write helper for this system")
	}
	return w
}

// A folder other accounts can write in, or move, takes every kind of write
// through the helper; a link in it takes none of them elsewhere.
func TestAFolderOthersCanWriteInIsWrittenThroughTheHelper(t *testing.T) {
	ctx := context.Background()
	w := needWriter(t)
	for name, fs := range map[string]*RemoteFS{"umask": {NoLinks: true}, "own": {Own: true}, "space": {Private: true}} {
		base := t.TempDir()
		parent, out := filepath.Join(base, "parent"), filepath.Join(base, "out")
		root := filepath.Join(parent, "lib")
		write(t, filepath.Join(root, "keep", "a.txt"), "a")
		write(t, filepath.Join(root, ".studio-transfer-x", "b.txt"), "b")
		write(t, filepath.Join(root, ".studio-upload-0", "data"), "")
		os.Chmod(filepath.Join(root, ".studio-upload-0"), 0o700)
		write(t, filepath.Join(out, "a.txt"), "private")
		os.Chmod(parent, 0o777)
		os.Chmod(root, 0o777)
		fs.R, fs.SiteID, fs.Dir, fs.Name, fs.Writer = &localRunner{}, "h", root, "lib", w
		for op, err := range writes(ctx, fs) {
			if err != nil {
				t.Errorf("%s, %s: %v", name, op, err)
			}
		}
		for p, want := range map[string]bool{"keep": false, ".studio-transfer-x": false, "new": true, "new.txt": true, "landed.txt": true} {
			if _, err := os.Lstat(filepath.Join(root, p)); (err == nil) != want {
				t.Errorf("%s, %s there: %v", name, p, err == nil)
			}
		}
		os.Symlink(out, filepath.Join(root, "door"))
		if _, err := fs.Write(ctx, "door/x.txt", bytes.NewReader([]byte("x")), 0, 0o644); err == nil || !strings.Contains(err.Error(), "goes through a link") {
			t.Errorf("%s, a write through a link: %v", name, err)
		}
		if _, err := fs.Land(ctx, []LandPair{{Staged: "new.txt", Final: "door/a.txt", Replace: true}}); err == nil {
			t.Errorf("%s: landed through a link", name)
		}
		if _, err := fs.Read(ctx, "door/a.txt"); err == nil {
			t.Errorf("%s: read through a link", name)
		}
		if es, _ := os.ReadDir(out); len(es) != 1 || read(t, filepath.Join(out, "a.txt")) != "private" {
			t.Errorf("%s: what is outside changed: %v", name, es)
		}
		if why := fs.Loose(ctx); !strings.Contains(why, "can be written by every account") {
			t.Errorf("%s: who else can write: %q", name, why)
		}
		// A command the helper does not know is refused, not run in a shell.
		if _, err := fs.run(ctx, nil, "chmod", "777", "new"); err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A folder put in the place of the library's is not written in; a helper
// that went is looked for again, and the write goes on.
func TestThePinAndAHelperThatWent(t *testing.T) {
	ctx := context.Background()
	w := needWriter(t)
	root := filepath.Join(t.TempDir(), "lib")
	write(t, filepath.Join(root, "keep", "a.txt"), "a")
	fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "lib", NoLinks: true, Writer: w}
	pin, err := fs.Identity(ctx)
	if err != nil || !strings.HasSuffix(pin, fmt.Sprintf(":%d", os.Getuid())) {
		t.Fatalf("%q %v", pin, err)
	}
	asked, missing := 0, 0
	fs.Writer = func(ctx context.Context) WriteTool {
		asked++
		t := w(ctx)
		t.Pin = pin
		t.Missing = func() { missing++ }
		return t
	}
	if err := fs.Mkdir(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	os.Rename(root, root+".was")
	write(t, filepath.Join(root, "keep", "a.txt"), "other")
	for op, err := range writes(ctx, fs) {
		if err == nil || !strings.Contains(err.Error(), "is not the folder this library was added for") {
			t.Errorf("%s: %v", op, err)
		}
	}
	if missing == 0 {
		t.Error("the folder was not read again")
	}
	if read(t, filepath.Join(root, "keep", "a.txt")) != "other" {
		t.Error("the other folder changed")
	}
	os.RemoveAll(root)
	os.Rename(root+".was", root)

	found := false
	fs.Writer = func(ctx context.Context) WriteTool {
		if found {
			return w(ctx)
		}
		return WriteTool{Path: "/nonexistent/studio-write", Missing: func() { found = true }}
	}
	if err := fs.Mkdir(ctx, "two"); err != nil || !found {
		t.Errorf("a helper that went: %v %v", err, found)
	}
	// What cannot be sent twice says to try again.
	found = false
	if _, err := fs.Write(ctx, "s.txt", bytes.NewReader([]byte("x")), 0, 0o644); err == nil || statusOf(err) != 503 || !found {
		t.Errorf("a write: %v", err)
	}
	if _, err := fs.Write(ctx, "s.txt", bytes.NewReader([]byte("x")), 0, 0o644); err != nil {
		t.Errorf("again: %v", err)
	}
	found = false
	if _, err := fs.Read(ctx, "s.txt"); err == nil || !found {
		t.Errorf("a read: %v", err)
	}
}

// libRig is a rig with a library on host A that other accounts can write
// in, whose writes go through the helper.
func libRig(t *testing.T) (r *rig, root string) {
	r = newRig(t)
	root = filepath.Join(r.hostA, "team")
	os.MkdirAll(root, 0o755)
	os.Chmod(root, 0o777)
	os.Mkdir(filepath.Join(root, "in"), 0o755)
	os.Chmod(filepath.Join(root, "in"), 0o777)
	r.more = []LibraryLoc{{ID: "team", Label: "Team", Writable: true,
		FS: &RemoteFS{R: r.runner, SiteID: "ssh://user@host-a", Dir: root, Name: "host-a", NoLinks: true, Writer: needWriter(t)}}}
	return r, root
}

// Every way rsync lands files in a library, and studio-copy, work in a
// folder others can write in.
func TestTransfersIntoALibraryOthersCanWriteIn(t *testing.T) {
	for _, mover := range []string{MoverRsync, MoverCopy} {
		if _, err := exec.LookPath("rsync"); err != nil && mover == MoverRsync {
			t.Skip("no rsync here")
		}
		r, root := libRig(t)
		write(t, filepath.Join(r.kb, "here", "a b.txt"), "from here")
		write(t, filepath.Join(r.hostA, "there", "b.txt"), "from its host")
		for src, want := range map[[2]string]string{{"library:kb", "here"}: "here/a b.txt", {"saved:a", "there"}: "there/b.txt"} {
			q := req(ActionCopy, src[0], []string{src[1]}, "library:team", "in")
			q.Options.Mover = mover
			tr := r.run(single, q)
			if tr.State != StateSucceeded || tr.Mover != mover {
				t.Fatalf("%s from %s: %s by %s: %v %v", mover, src[0], tr.State, tr.Mover, tr.Error, tr.Events)
			}
			if _, err := os.Stat(filepath.Join(root, "in", want)); err != nil {
				t.Errorf("%s: %v", mover, err)
			}
		}
		noStaging(t, root)
	}
}

// Another account puts a link in the place of a transfer's staging folder,
// or of the folder it goes to, at any moment: nothing lands elsewhere.
func TestATransferIntoALibraryIsNotSentElsewhere(t *testing.T) {
	for _, mover := range []string{MoverRsync, MoverCopy} {
		if _, err := exec.LookPath("rsync"); err != nil && mover == MoverRsync {
			t.Skip("no rsync here")
		}
		for _, what := range []string{stagingPrefix, "in"} {
			r, root := libRig(t)
			out := filepath.Join(r.dir, "private")
			write(t, filepath.Join(out, "a.txt"), "private")
			for i := range 40 {
				write(t, filepath.Join(r.kb, "here", fmt.Sprintf("f%d.txt", i)), strings.Repeat("x", 5000))
			}
			stop := make(chan struct{})
			var done sync.WaitGroup
			done.Add(1)
			swapped := 0
			go func() {
				defer done.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					for _, at := range []string{root, filepath.Join(root, "in")} {
						es, _ := os.ReadDir(at)
						for _, e := range es {
							if !strings.HasPrefix(e.Name(), what) || strings.Contains(e.Name(), ".was") || e.Type()&os.ModeSymlink != 0 {
								continue
							}
							p := filepath.Join(at, e.Name())
							// The folder it goes to once something is staged there,
							// and either a little later each time.
							if staged, _ := filepath.Glob(filepath.Join(p, stagingPrefix+"*")); what == "in" && len(staged) == 0 {
								continue
							}
							time.Sleep(time.Duration(swapped%12) * 4 * time.Millisecond)
							if os.Rename(p, fmt.Sprintf("%s.was%d", p, swapped)) == nil && os.Symlink(out, p) == nil {
								swapped++
							}
						}
					}
					time.Sleep(50 * time.Microsecond)
				}
			}()
			moved := 0
			for range 6 {
				q := req(ActionCopy, "library:kb", []string{"here"}, "library:team", "in")
				q.Options.Mover = mover
				if p, err := r.plan(single, q); err == nil {
					if tr, err := r.s.Start(context.Background(), single, p.ID, ByUser, ""); err == nil {
						if r.wait(single, tr.ID).State == StateSucceeded {
							moved++
						}
					}
				}
				os.Remove(filepath.Join(root, "in"))
				os.Mkdir(filepath.Join(root, "in"), 0o777)
			}
			close(stop)
			done.Wait()
			t.Logf("%s, %s: %d swaps, %d of 6 transfers done", mover, what, swapped, moved)
			if swapped == 0 {
				t.Errorf("%s, %s: nothing was swapped", mover, what)
			}
			es, _ := os.ReadDir(out)
			st, _ := os.Stat(filepath.Join(out, "a.txt"))
			if len(es) != 1 || read(t, filepath.Join(out, "a.txt")) != "private" || st.Sys().(*syscall.Stat_t).Nlink != 1 {
				t.Errorf("%s, %s: what is outside changed: %v", mover, what, es)
			}
		}
	}
}

// CONDUIT writes by path, as a service of its own: it does not carry a
// transfer into a folder others can change.
func TestConduitDoesNotWriteWhereOthersCan(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib")
	os.MkdirAll(root, 0o700)
	fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "lib", NoLinks: true, Writer: noWriter}
	if why := fs.Loose(context.Background()); why != "" {
		t.Errorf("closed: %q", why)
	}
	os.Chmod(root, 0o777)
	if why := fs.Loose(context.Background()); !strings.HasSuffix(why, "can be written by every account") {
		t.Errorf("open: %q", why)
	}
	if why := (&RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root}).Loose(context.Background()); why != "" {
		t.Errorf("a place that is no library: %q", why)
	}
}

func noWriter(context.Context) WriteTool { return writehelper.Tool{Why: "none here"} }

// Large files go in parts through the helper as they do without it.
func TestPartsThroughTheHelper(t *testing.T) {
	partsWriter = needWriter(t)
	defer func() { partsWriter = nil }()
	for name, run := range map[string]func(*testing.T){
		"AreWrittenAtTheirPlaceAndLandedWhole":  TestPartsAreWrittenAtTheirPlaceAndLandedWhole,
		"ThatDoNotFitOrHaveNoPlaceAreRefused":   TestPartsThatDoNotFitOrHaveNoPlaceAreRefused,
		"DoNotFollowWhatAnotherPutInTheirPlace": TestPartsDoNotFollowWhatAnotherPutInTheirPlace,
		"NeedAnAccountThatRunsCommands":         TestPartsNeedAnAccountThatRunsCommands,
	} {
		t.Run(name, run)
	}
}
