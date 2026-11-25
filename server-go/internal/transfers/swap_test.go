//go:build !windows

package transfers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

func shaOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// eachWriter runs a test through the shell helper and through the write
// helper.
func eachWriter(t *testing.T, f func(*testing.T, func(context.Context) WriteTool)) {
	t.Run("shell", func(t *testing.T) { f(t, nil) })
	t.Run("helper", func(t *testing.T) { f(t, testWriter(t)) })
}

func left(t *testing.T, dir string) {
	t.Helper()
	if l, _ := filepath.Glob(filepath.Join(dir, ".studio-*")); len(l) > 0 {
		t.Errorf("left behind: %v", l)
	}
}

func TestSwapTakesAFilesPlaceOnlyAsItWas(t *testing.T) { eachWriter(t, swapTakesAFilesPlaceOnlyAsItWas) }

func swapTakesAFilesPlaceOnlyAsItWas(t *testing.T, writer func(context.Context) WriteTool) {
	ctx := context.Background()
	root := t.TempDir()
	fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "space", Private: true, Writer: writer}
	d := filepath.Join(root, "d")
	f := filepath.Join(d, "-run me.sh")
	write(t, f, "one")
	if err := os.Chmod(f, 0o754); err != nil {
		t.Fatal(err)
	}
	attr := exec.Command("setfattr", "-n", "user.note", "-v", "kept", f).Run() == nil
	acl := exec.Command("setfacl", "-m", "u:65534:r", f).Run() == nil

	st, err := fs.Swap(ctx, "d/-run me.sh", []byte("two"), shaOf("one"))
	if err != nil || !st.Done || read(t, f) != "two" {
		t.Fatalf("%+v %v %q", st, err, read(t, f))
	}
	left(t, d)
	if p := perm(t, f); p&0o700 != 0o700 || p&0o004 == 0 {
		t.Errorf("mode %o", p)
	}
	if attr {
		if b, _ := exec.Command("getfattr", "--only-values", "-n", "user.note", f).Output(); string(b) != "kept" {
			t.Errorf("attribute %q", b)
		}
	}
	if acl {
		if b, _ := exec.Command("getfacl", "-pn", f).Output(); !bytes.Contains(b, []byte("user:65534:r--")) {
			t.Errorf("acl %s", b)
		}
	}

	st, err = fs.Swap(ctx, "d/-run me.sh", []byte("three"), shaOf("one"))
	if err != nil || st.Done || st.Version != shaOf("two") || read(t, f) != "two" {
		t.Fatalf("%+v %v %q", st, err, read(t, f))
	}
	// Gone, or its folder is: nothing is made again.
	for _, p := range []string{"d/none", "e/none", "none"} {
		if st, err = fs.Swap(ctx, p, []byte("x"), shaOf("one")); err != nil || !st.Missing {
			t.Fatalf("%s: %+v %v", p, st, err)
		}
		gone(t, filepath.Join(root, p))
	}
	gone(t, filepath.Join(root, "e"))
	// Empty, and larger than goes over a kept session.
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<16)
	for _, step := range [][2][]byte{{[]byte("two"), big}, {big, nil}} {
		if st, err = fs.Swap(ctx, "d/-run me.sh", step[1], shaOf(string(step[0]))); err != nil || !st.Done || read(t, f) != string(step[1]) {
			t.Fatalf("%+v %v", st, err)
		}
	}
	if st, err = fs.Swap(ctx, "d", []byte("x"), shaOf("one")); err == nil || st.Done {
		t.Fatalf("a folder: %+v", st)
	}
	if err := os.Chmod(f, 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if st, err = fs.Swap(ctx, "d/-run me.sh", []byte("x"), shaOf("")); err == nil || st.Done || read(t, f) != "" {
			t.Fatalf("a file that may not be written was: %+v", st)
		}
	}
	left(t, d)
	left(t, root)
}

// cutRunner is a connection that ends before the input does.
type cutRunner struct{ localRunner }

func (c *cutRunner) RunOnce(ctx context.Context, site, key string, argv []string, timeout time.Duration, stdin io.Reader) (remote.Result, error) {
	return c.localRunner.RunOnce(ctx, site, key, argv, timeout, io.LimitReader(stdin, 3))
}

func TestSwapKeepsTheFileWhenTheChangeDoesNotArriveWhole(t *testing.T) { eachWriter(t, swapKeepsTheFileWhenTheChangeDoesNotArriveWhole) }

func swapKeepsTheFileWhenTheChangeDoesNotArriveWhole(t *testing.T, writer func(context.Context) WriteTool) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.md"), "one")
	fs := &RemoteFS{R: &cutRunner{}, SiteID: "h", Dir: root, Name: "space", Private: true, Writer: writer}
	if st, err := fs.Swap(context.Background(), "a.md", []byte("a longer text"), shaOf("one")); err == nil || st.Done {
		t.Fatalf("%+v", st)
	}
	if read(t, filepath.Join(root, "a.md")) != "one" {
		t.Fatal("cut")
	}
	left(t, root)
}

func TestSwapFollowsNoLink(t *testing.T) { eachWriter(t, swapFollowsNoLink) }

func swapFollowsNoLink(t *testing.T, writer func(context.Context) WriteTool) {
	ctx := context.Background()
	for name, mk := range map[string]func(root string) *RemoteFS{
		"private": func(root string) *RemoteFS {
			return &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "space", Private: true, Writer: writer}
		},
		"own": func(root string) *RemoteFS {
			return &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "space", NoLinks: true, Writer: writer}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, out := t.TempDir(), t.TempDir()
			write(t, filepath.Join(root, "private", "diary.txt"), "mine")
			write(t, filepath.Join(out, "secret.txt"), "outside")
			write(t, filepath.Join(root, "team", "note.txt"), "note")
			for at, to := range map[string]string{
				"team/to-file": filepath.Join(root, "private", "diary.txt"),
				"team/to-out":  filepath.Join(out, "secret.txt"),
				"team/door":    filepath.Join(root, "private"),
				"team/away":    out,
			} {
				if err := os.Symlink(to, filepath.Join(root, at)); err != nil {
					t.Fatal(err)
				}
			}
			fs := mk(root)
			for _, c := range [][2]string{
				{"team/to-file", "mine"},
				{"team/to-out", "outside"},
				{"team/door/diary.txt", "mine"},
				{"team/away/secret.txt", "outside"},
				{"../" + filepath.Base(out) + "/secret.txt", "outside"},
			} {
				if st, err := fs.Swap(ctx, c[0], []byte("over"), shaOf(c[1])); err == nil || st.Done {
					t.Errorf("%v was taken: %+v", c, st)
				}
				if b, err := fs.Text(ctx, c[0], 100); err == nil {
					t.Errorf("%v was opened: %q", c, b)
				}
			}
			if b, err := fs.Text(ctx, "team/note.txt", 100); err != nil || string(b) != "note" {
				t.Errorf("%q %v", b, err)
			}
			if b, err := fs.Text(ctx, "team/note.txt", 3); err != nil || string(b) != "not" {
				t.Errorf("%q %v", b, err)
			}
			// Through a link, what is there answers as what is not.
			_, x := fs.Text(ctx, "team/away/secret.txt", 100)
			_, y := fs.Text(ctx, "team/away/no-such.txt", 100)
			if x == nil || y == nil || strings.ReplaceAll(x.Error(), "secret", "no-such") != y.Error() {
				t.Errorf("%v / %v", x, y)
			}
			_, a := fs.Swap(ctx, "team/away/secret.txt", []byte("over"), shaOf("x"))
			_, b := fs.Swap(ctx, "team/away/no-such.txt", []byte("over"), shaOf("x"))
			if a == nil || b == nil || strings.ReplaceAll(a.Error(), "secret", "no-such") != b.Error() {
				t.Errorf("%v / %v", a, b)
			}
			if read(t, filepath.Join(root, "private", "diary.txt")) != "mine" || read(t, filepath.Join(out, "secret.txt")) != "outside" {
				t.Fatal("a link was followed")
			}
			left(t, out)
			left(t, filepath.Join(root, "private"))
			left(t, filepath.Join(root, "team"))
		})
	}
}
