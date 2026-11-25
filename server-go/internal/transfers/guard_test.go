//go:build !windows

package transfers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// A guarded location takes no write of any kind where another account
// could change its folder or one above it, and so send the write
// elsewhere; nothing is changed, reading goes on, and closing the folder
// turns writing back on.
func TestAGuardedLocationTakesNoWriteAnotherAccountCouldRedirect(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		open func(parent, root string) (undo func())
		said string
	}{
		{"every account can write in it", func(_, root string) func() { os.Chmod(root, 0o777); return func() { os.Chmod(root, 0o700) } }, "can be written by every account"},
		// The sticky bit keeps others from moving its entries, not from
		// making links in it.
		{"every account can write in it, sticky", func(_, root string) func() {
			os.Chmod(root, 0o777|os.ModeSticky)
			return func() { os.Chmod(root, 0o700) }
		}, "can be written by every account"},
		{"a folder above it can be moved", func(parent, _ string) func() { os.Chmod(parent, 0o777); return func() { os.Chmod(parent, 0o755) } }, "can be written by every account"},
	} {
		t.Run(c.name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			root := filepath.Join(parent, "lib")
			write(t, filepath.Join(root, "keep", "a.txt"), "a")
			write(t, filepath.Join(root, ".studio-transfer-x", "b.txt"), "b")
			write(t, filepath.Join(root, ".studio-upload-0", "data"), "")
			os.Chmod(filepath.Join(root, ".studio-upload-0"), 0o700)
			os.Chmod(root, 0o700)
			undo := c.open(parent, root)
			for _, fs := range []*RemoteFS{
				{R: &localRunner{}, SiteID: "h", Dir: root, Name: "lib", Guard: true, NoLinks: true},
				{R: &localRunner{}, SiteID: "h", Dir: root, Name: "lib", Guard: true, Own: true},
				{R: &localRunner{}, SiteID: "h", Dir: root, Name: "lib", Guard: true, Private: true},
			} {
				for op, err := range writes(ctx, fs) {
					if err == nil || !strings.Contains(err.Error(), c.said) || !strings.Contains(err.Error(), "nothing was changed") {
						t.Errorf("%s: %v", op, err)
					}
				}
				if es, _, err := fs.List(ctx, "keep", 0); err != nil || len(es) != 1 {
					t.Errorf("reading: %v %v", es, err)
				}
			}
			for p, want := range map[string]bool{"keep/a.txt": true, ".studio-transfer-x/b.txt": true, "new": false, "new.txt": false, "landed.txt": false} {
				if _, err := os.Lstat(filepath.Join(root, p)); (err == nil) != want {
					t.Errorf("%s there: %v", p, err == nil)
				}
			}
			undo()
			fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "lib", Guard: true, Own: true}
			for op, err := range writes(ctx, fs) {
				if err != nil {
					t.Errorf("closed again, %s: %v", op, err)
				}
			}
		})
	}
}

// writes is every kind of write a location takes, and what each answered.
func writes(ctx context.Context, fs *RemoteFS) map[string]error {
	out := map[string]error{}
	_, out["write"] = fs.Write(ctx, "new.txt", bytes.NewReader([]byte("x")), 0, 0o644)
	out["mkdir"] = fs.Mkdir(ctx, "new")
	_, out["open a text to edit"] = fs.Text(ctx, "keep/a.txt", 100)
	st, err := fs.Swap(ctx, "keep/a.txt", []byte("edited"), shaOf("a"))
	if err == nil && !st.Done {
		err = fmt.Errorf("answered %+v", st)
	}
	out["save a text"] = err
	// Changed since is not what a guarded folder answers.
	if st, err := fs.Swap(ctx, "keep/a.txt", []byte("edited"), shaOf("other")); err != nil && !strings.Contains(err.Error(), "nothing was changed") || err == nil && st.Version == "" {
		out["save over a change"] = fmt.Errorf("answered %+v %v", st, err)
	}
	_, out["land"] = fs.Land(ctx, []LandPair{{Staged: "keep/a.txt", Final: "landed.txt"}})
	_, out["remove a tree"] = fs.RemoveTrees(ctx, []string{"keep"})
	out["start a file in parts"] = fs.StartParts(ctx, ".studio-upload-1", "large", mb, mb)
	out["write a part"] = fs.WritePart(ctx, ".studio-upload-0", 0, 1, strings.NewReader("x"))
	out["remove staging"] = fs.RemoveStaging(ctx, ".studio-transfer-x")
	return out
}

// Under a sticky folder such as /tmp, others cannot move what the person
// owns, so a folder of theirs there is written in.
func TestAGuardedLocationUnderAStickyFolder(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "scratch")
	root := filepath.Join(parent, "lib")
	os.MkdirAll(root, 0o700)
	os.Chmod(parent, 0o777|os.ModeSticky)
	fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "lib", Guard: true, Own: true}
	if err := fs.Mkdir(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
}

// loose runs the check on p with lines before it, and what it says.
func loose(t *testing.T, before, p string, self bool) string {
	t.Helper()
	arg := ""
	if self {
		arg = "self"
	}
	out, err := exec.Command("bash", "-c", LooseSh+before+"\nloose \"$1\" $2", "_", p, arg).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestHowAnotherAccountCouldChangeAFolder(t *testing.T) {
	d := t.TempDir()
	at := func(mode os.FileMode) string {
		p := filepath.Join(d, mode.String())
		os.MkdirAll(p, 0o700)
		os.Chmod(p, mode)
		return p
	}
	for _, c := range []struct {
		mode   os.FileMode
		before string
		self   bool
		want   string
	}{
		{0o700, "", true, ""},
		{0o755, "", true, ""},
		{0o770, "", true, "group"},
		// A group of the person's own, as many systems give each account.
		{0o770, `lz_user=$(lz_stat %G %Sg "$1")`, true, ""},
		{0o777, "", true, "other"},
		{0o777 | os.ModeSticky, "", true, "other"},
		{0o777 | os.ModeSticky, "", false, ""},
		{0o700, "lz_uid=$((lz_uid+1))", true, "owner"},
		{0o700, "lz_uid=$((lz_uid+1))", false, "owner"},
	} {
		// The test's account may have a group of its own name.
		before := "lz_user=nobody-else\n" + c.before
		if got := loose(t, before, at(c.mode), c.self); got != c.want {
			t.Errorf("%v %q self %v: %q, want %q", c.mode, c.before, c.self, got, c.want)
		}
	}
	// root owns what is above; a folder written in itself is the person's.
	if got := loose(t, "", "/", false); got != "" && os.Getuid() != 0 {
		t.Errorf("/ above: %q", got)
	}
	if got := loose(t, "", "/", true); got != "owner" && os.Getuid() != 0 {
		t.Errorf("/ itself: %q", got)
	}
	if got := loose(t, "", filepath.Join(d, "not there"), true); got != "unknown" {
		t.Errorf("missing: %q", got)
	}
}

// With an ACL, the group bits of a mode are its mask: what counts is who
// the entries let write, less those the owner shared it with.
func TestAnACLThatLetsAnotherAccountWrite(t *testing.T) {
	d := filepath.Join(t.TempDir(), "lib")
	os.MkdirAll(d, 0o700)
	if out, err := exec.Command("setfacl", "-m", "u:nobody:rwx", d).CombinedOutput(); err != nil {
		t.Skipf("no ACLs here: %v %s", err, out)
	}
	if got := loose(t, "", d, true); got != "acl" {
		t.Errorf("another account's entry: %q", got)
	}
	if got := loose(t, "\n"+LooseOK([]string{"user:nobody"}), d, true); got != "" {
		t.Errorf("one it is shared with: %q", got)
	}
	if got := loose(t, "\n"+LooseOK([]string{"user:nobody2", "group:nobody"}), d, true); got != "acl" {
		t.Errorf("shared with others: %q", got)
	}
	exec.Command("setfacl", "-m", "u:nobody:r-x", d).Run()
	if got := loose(t, "", d, true); got != "" {
		t.Errorf("an entry for reading: %q", got)
	}
	// A grant is a name and nothing a shell would read.
	if got := LooseOK([]string{"user:a'; touch x; '", "group:$(id)", "user:ok"}); got != "LOOSE_OK='user:ok'\n" {
		t.Errorf("%q", got)
	}
}

// What lands in a folder of the person's own follows the folder: closed
// where it is closed, the person's umask where its group reads it, open
// where every account does.
func TestWritesInAFolderOfOnesOwnFollowTheFolder(t *testing.T) {
	ctx := context.Background()
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	for _, c := range []struct{ dir, file os.FileMode }{{0o700, 0o600}, {0o750, 0o644}, {0o755, 0o644}} {
		root := filepath.Join(t.TempDir(), "lib")
		os.MkdirAll(root, 0o700)
		os.Chmod(root, c.dir)
		fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "lib", Own: true}
		if _, err := fs.Write(ctx, "a.txt", bytes.NewReader([]byte("x")), 0, 0o644); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Join(root, "a.txt"))
		if err != nil || fi.Mode().Perm() != c.file {
			t.Errorf("in %v: %v %v", c.dir, fi.Mode().Perm(), err)
		}
		os.Symlink(root, filepath.Join(root, "door"))
		if err := fs.Mkdir(ctx, "door/sub"); err == nil {
			t.Error("a folder was made through a link")
		}
	}
}

// A library where another account could send a write elsewhere takes no
// transfer, though it is reached with the viewer's own key; one of their own
// is written through the guarded helper, never SFTP.
func TestLibraryLocationsFollowTheFoldersCheck(t *testing.T) {
	why := "Every account there can write in /data/open, so it is read only here."
	open := libraries.Library{ID: "open", IndexRoot: "remote:open", ReadOnlyWhy: why}
	if got := libraryReadOnly(open, Viewer{}); got != why {
		t.Errorf("open: %q", got)
	}
	if got := libraryReadOnly(libraries.Library{ID: "kb", Primary: true, ReadOnlyWhy: why}, Viewer{MainReadOnly: "admins only"}); got != "admins only" {
		t.Errorf("main: %q", got)
	}
	own := libraries.Library{ID: "own", IndexRoot: "remote:own", Mine: true, Own: true}
	if got := libraryReadOnly(own, Viewer{}); got != "" {
		t.Errorf("own: %q", got)
	}
	asked := ""
	fs := userFS(&localRunner{}, &libraries.Target{Site: "cluster-a", Key: "k", SourceRoot: "/data/own", Def: libraries.UserDef{Guard: true}}, own,
		func(root string) []string { asked = root; return []string{"user:bob"} })
	if !fs.Own || !fs.Guard || len(fs.Shared) != 1 || asked != "remote:own" || fs.SFTPOnly(context.Background()) {
		t.Errorf("own: %+v, asked %q", fs, asked)
	}
	// One added before the check is as it was.
	fs = userFS(&localRunner{}, &libraries.Target{Site: "cluster-a", Key: "k", SourceRoot: "/data/old"}, libraries.Library{Mine: true}, nil)
	if fs.Guard || fs.Own || !fs.NoLinks {
		t.Errorf("as it was: %+v", fs)
	}
}
