//go:build !windows

package transfers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

func partStatus(err error) int {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func partsFS(root string, private bool) *RemoteFS {
	return &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "space", Private: private}
}

const mb = 1 << 20

// The parts of a file are written at their place in any order, again after
// one failed, and take the file's name in one rename with the modes a file
// made there has.
func TestPartsAreWrittenAtTheirPlaceAndLandedWhole(t *testing.T) {
	ctx := context.Background()
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	for _, c := range []struct {
		name         string
		private      bool
		folder, file os.FileMode
	}{
		{"a closed folder of a space", true, 0o700, 0o600},
		{"a folder of a space open to others", true, 0o755, 0o644},
		{"a library elsewhere", false, 0o755, 0o644},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0o700)
			os.Mkdir(filepath.Join(root, "it's in"), 0o700)
			os.Chmod(filepath.Join(root, "it's in"), c.folder)
			fs := partsFS(root, c.private)
			data := make([]byte, 5*mb+7)
			for i := range data {
				data[i] = byte(i * 31 >> 8)
			}
			const dir, final = "it's in/.studio-upload-1", "it's in/da ta.tar"
			if err := fs.StartParts(ctx, dir, final, int64(len(data)), int64(len(data))); err != nil {
				t.Fatal(err)
			}
			if st, _ := os.Stat(filepath.Join(root, dir)); st.Mode().Perm() != 0o700 {
				t.Fatalf("the parts are open to others: %v", st.Mode())
			}
			if err := fs.WritePart(ctx, dir, 4*mb, mb+7, bytes.NewReader(data[4*mb:])); err != nil {
				t.Fatal(err)
			}
			// One that does not arrive whole, then whole. A reader that gives
			// a byte or so at a time makes no difference to where it goes.
			if err := fs.WritePart(ctx, dir, 0, 2*mb, bytes.NewReader(make([]byte, mb))); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("a short part: %v", err)
			}
			if err := fs.LandParts(ctx, dir, final, int64(len(data))+1); partStatus(err) != 409 {
				t.Fatalf("the wrong size: %v", err)
			}
			if err := fs.WritePart(ctx, dir, 0, 2*mb, &dribble{data[:2*mb]}); err != nil {
				t.Fatal(err)
			}
			if err := fs.WritePart(ctx, dir, 2*mb, 2*mb, bytes.NewReader(data[2*mb:4*mb])); err != nil {
				t.Fatal(err)
			}
			if err := fs.WritePart(ctx, dir, 100, 10, bytes.NewReader(data)); partStatus(err) != 400 {
				t.Fatalf("an odd place: %v", err)
			}
			os.WriteFile(filepath.Join(root, final), []byte("before"), 0o644)
			if err := fs.LandParts(ctx, dir, final, int64(len(data))); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(filepath.Join(root, final)); !bytes.Equal(got, data) {
				t.Fatal("not what was sent")
			}
			if st, _ := os.Stat(filepath.Join(root, final)); st.Mode().Perm() != c.file {
				t.Errorf("modes %v, want %v", st.Mode(), c.file)
			}
			if left, _ := filepath.Glob(filepath.Join(root, "*", ".studio-upload-*")); len(left) > 0 {
				t.Errorf("left %v", left)
			}
			if err := fs.WritePart(ctx, dir, 0, 1, strings.NewReader("x")); partStatus(err) != 404 || !strings.Contains(err.Error(), "no longer there") {
				t.Errorf("a part after: %v", err)
			}
		})
	}
}

type dribble struct{ b []byte }

func (d *dribble) Read(p []byte) (int, error) {
	if len(d.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, d.b[:min(len(d.b), 777)])
	d.b = d.b[n:]
	return n, nil
}

func TestPartsThatDoNotFitOrHaveNoPlaceAreRefused(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fs := partsFS(root, true)
	err := fs.StartParts(ctx, "new/.studio-upload-1", "new/big", 1<<52, 1<<52)
	if partStatus(err) != 507 || !strings.Contains(err.Error(), "needs 4.0 PB") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); err == nil {
		t.Error("a refused file made its folder")
	}
	write(t, filepath.Join(root, "taken", "x"), "x")
	if err := fs.StartParts(ctx, ".studio-upload-1", "taken", mb, mb); partStatus(err) != 409 {
		t.Errorf("over a folder: %v", err)
	}
	if err := fs.StartParts(ctx, "other", "y", mb, mb); partStatus(err) != 400 {
		t.Errorf("a folder of another name: %v", err)
	}
	if err := fs.DropParts(ctx, "taken"); partStatus(err) != 400 {
		t.Errorf("dropped another folder: %v", err)
	}
	if err := fs.StartParts(ctx, "../.studio-upload-1", "../y", mb, mb); err == nil {
		t.Error("outside the space")
	}
	// A disk that fills while a part is written says so.
	if err := fs.StartParts(ctx, ".studio-upload-2", "z", mb, mb); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	write(t, filepath.Join(bin, "dd"), "#!/bin/sh\necho \"dd: writing to './data': $SAYS\" >&2\nexit 1\n")
	os.Chmod(filepath.Join(bin, "dd"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	for says, want := range map[string]int{"No space left on device": 507, "Disk quota exceeded": 507, "Input/output error": 502} {
		t.Setenv("SAYS", says)
		if err := fs.WritePart(ctx, ".studio-upload-2", 0, mb, bytes.NewReader(make([]byte, mb))); partStatus(err) != want {
			t.Errorf("%s: %v", says, err)
		}
	}
}

// A person a folder is shared with may rename what is in it. Parts are
// written only in the folder this account made, and nowhere a link leads.
func TestPartsDoNotFollowWhatAnotherPutInTheirPlace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "private", "data"), "mine")
	os.MkdirAll(filepath.Join(root, "team"), 0o700)
	fs := partsFS(root, true)
	part := func(dir string) error { return fs.WritePart(ctx, dir, 0, 3, strings.NewReader("new")) }
	const dir = "team/.studio-upload-1"
	if err := fs.StartParts(ctx, dir, "team/f", mb, mb); err != nil {
		t.Fatal(err)
	}
	at := filepath.Join(root, dir)
	swap := func(put func()) {
		t.Helper()
		os.RemoveAll(at)
		put()
		if err := part(dir); err == nil {
			t.Error("a part was written in what took its place")
		}
	}
	swap(func() { os.Symlink(filepath.Join(root, "private"), at) })
	// Folders another account can write in, or a file in it that is a link.
	swap(func() {
		os.Remove(at)
		os.Mkdir(at, 0o755)
		write(t, filepath.Join(at, "data"), "x")
		os.Chmod(at, 0o777)
	})
	swap(func() {
		os.Mkdir(at, 0o700)
		os.Symlink(filepath.Join(root, "private", "data"), filepath.Join(at, "data"))
	})
	// The way there through a link, in a space.
	os.Symlink(filepath.Join(root, "team"), filepath.Join(root, "door"))
	if err := fs.StartParts(ctx, "door/.studio-upload-2", "door/f", mb, mb); err == nil {
		t.Error("parts through a link")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "private", "data")); string(b) != "mine" {
		t.Fatalf("private is now %q", b)
	}
	// Dropping removes a link put there, not what it names.
	os.RemoveAll(at)
	os.Symlink(filepath.Join(root, "private"), at)
	if err := fs.DropParts(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "private", "data")); err != nil {
		t.Fatal("dropping followed a link")
	}
}

// A cancelled part ends its command and does not wait for its bytes.
func TestAPartThatIsCancelledEnds(t *testing.T) {
	root := t.TempDir()
	fs := partsFS(root, false)
	if err := fs.StartParts(context.Background(), ".studio-upload-1", "f", mb, mb); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- fs.WritePart(ctx, ".studio-upload-1", 0, mb, r) }()
	w.Write([]byte("some"))
	cancel()
	w.CloseWithError(context.Canceled)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPartsNeedAnAccountThatRunsCommands(t *testing.T) {
	fs, _, _ := sftpFS(t)
	if err := fs.StartParts(context.Background(), ".studio-upload-1", "f", mb, mb); partStatus(err) != 409 {
		t.Fatalf("%v", err)
	}
}
