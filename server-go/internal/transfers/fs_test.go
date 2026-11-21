package transfers

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// localRunner runs a "remote" command on this computer, as the ssh pool
// would on a host: the argv quoted into one command line for bash.
type localRunner struct {
	calls int
	// home is HOME for the commands, "" for this process's.
	home string
}

func (l *localRunner) env(cmd *exec.Cmd) {
	if l.home != "" {
		cmd.Env = append(os.Environ(), "HOME="+l.home)
	}
}

func (l *localRunner) RunOnce(ctx context.Context, site, key string, argv []string, timeout time.Duration, input io.Reader) (remote.Result, error) {
	l.calls++
	cmd := exec.CommandContext(ctx, "bash", "-c", remote.DirectCommand(argv))
	l.env(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if input != nil {
		cmd.Stdin = input
	}
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			return remote.Result{}, err
		}
	}
	return remote.Result{Code: code, Stdout: out.String(), Raw: out.Bytes(), Stderr: errb.String()}, nil
}

func (l *localRunner) Command(site, key string, argv []string) (Cmd, error) {
	// A login that prints first, as some do: the marker must skip it.
	cmd := exec.Command("bash", "-c", "echo 'welcome to the host'; "+remote.DirectCommand(argv))
	l.env(cmd)
	return ExecCmd(cmd), nil
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tree is a folder with odd names, hidden entries and links.
func tree(t *testing.T) (root, outside string) {
	base := t.TempDir()
	root, outside = filepath.Join(base, "root"), filepath.Join(base, "outside")
	write(t, filepath.Join(outside, "secret.txt"), "secret")
	write(t, filepath.Join(root, "docs", "a.txt"), "alpha")
	write(t, filepath.Join(root, "docs", "sub dir", "b $(touch pwned).txt"), "bravo!")
	write(t, filepath.Join(root, "docs", "it's \"quoted\".md"), "q")
	write(t, filepath.Join(root, "docs", ".hidden", "h.txt"), "h")
	write(t, filepath.Join(root, "docs", ".dot"), "d")
	write(t, filepath.Join(root, "top.txt"), "top")
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "docs", "link.txt"))
	os.Symlink(outside, filepath.Join(root, "escape"))
	return root, outside
}

func bothFS(t *testing.T, root string) map[string]FS {
	return map[string]FS{
		"local":  &LocalFS{Dir: root},
		"remote": &RemoteFS{R: &localRunner{}, SiteID: "ssh://host-a", Dir: root, Name: "host-a"},
	}
}

func TestWalkSkipsHiddenAndLinks(t *testing.T) {
	root, _ := tree(t)
	for name, fs := range bothFS(t, root) {
		t.Run(name, func(t *testing.T) {
			w, err := fs.Walk(context.Background(), []string{"docs"}, 100)
			if err != nil {
				t.Fatal(err)
			}
			var files []string
			for _, f := range w.Files {
				files = append(files, f.Rel)
			}
			want := []string{"docs/a.txt", "docs/it's \"quoted\".md", "docs/sub dir/b $(touch pwned).txt"}
			sort.Strings(want)
			if strings.Join(files, "|") != strings.Join(want, "|") {
				t.Fatalf("files %q, want %q", files, want)
			}
			if w.Skipped.Hidden != 2 || w.Skipped.Links != 1 {
				t.Fatalf("skipped %+v, want 2 hidden and 1 link", w.Skipped)
			}
			if _, err := os.Stat(filepath.Join(root, "docs", "sub dir", "pwned")); err == nil {
				t.Fatal("a name ran as a command")
			}
			for _, f := range w.Files {
				if f.Rel == "docs/a.txt" && f.Size != 5 {
					t.Fatalf("size %d", f.Size)
				}
			}
		})
	}
}

func TestPathsThatLeaveTheRoot(t *testing.T) {
	root, _ := tree(t)
	for name, fs := range bothFS(t, root) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := fs.Walk(ctx, []string{"escape/secret.txt"}, 10); statusOf(err) != 400 {
				t.Fatalf("a path through a link out of the root: %v", err)
			}
			if _, err := fs.Walk(ctx, []string{"escape"}, 10); statusOf(err) != 400 {
				t.Fatalf("a link named as an item: %v", err)
			}
			if _, err := fs.Read(ctx, "escape/secret.txt"); err == nil {
				t.Fatal("read through a link out of the root")
			}
			if _, err := fs.Read(ctx, "docs/link.txt"); err == nil {
				t.Fatal("read a link")
			}
			if _, _, err := fs.List(ctx, "escape", 10); statusOf(err) != 400 {
				t.Fatalf("listed through a link: %v", err)
			}
			if _, err := fs.Walk(ctx, []string{"nope"}, 10); statusOf(err) != 404 {
				t.Fatalf("missing: %v", err)
			}
			if _, err := fs.Write(ctx, "escape/new.txt", strings.NewReader("x"), 0, 0o644); err == nil {
				t.Fatal("wrote through a link out of the root")
			}
		})
	}
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../b", ".ssh", "docs/.hidden/h.txt", "a\x00b"} {
		if _, err := CleanRel(bad); statusOf(err) != 400 {
			t.Fatalf("CleanRel(%q) = %v, want 400", bad, err)
		}
	}
	if c, err := CleanRel("/docs//sub/"); err == nil {
		t.Fatalf("absolute accepted as %q", c)
	}
	if c, _ := CleanRel("docs//sub/./x/"); c != "docs/sub/x" {
		t.Fatalf("clean %q", c)
	}
}

func TestListReadWriteLand(t *testing.T) {
	root, _ := tree(t)
	for name, fs := range bothFS(t, root) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			es, more, err := fs.List(ctx, "docs", 2)
			if err != nil || !more || len(es) != 2 || es[0].Name != "a.txt" {
				t.Fatalf("list %v %v %v", es, more, err)
			}
			es, _, _ = fs.List(ctx, "", 100)
			kinds := map[string]string{}
			for _, e := range es {
				kinds[e.Name] = e.Type
			}
			if kinds["escape"] != "link" || kinds["docs"] != "dir" || kinds["top.txt"] != "file" {
				t.Fatalf("kinds %v", kinds)
			}
			r, err := fs.Read(ctx, "docs/sub dir/b $(touch pwned).txt")
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r)
			if err := r.Close(); err != nil || string(b) != "bravo!" {
				t.Fatalf("read %q %v", b, err)
			}
			stage := ".studio-transfer-" + name
			n, err := fs.Write(ctx, stage+"/x/it's.txt", strings.NewReader("hello"), 1700000000, 0o644)
			if err != nil || n != 5 {
				t.Fatalf("write %d %v", n, err)
			}
			if _, err := fs.Write(ctx, stage+"/x/it's.txt", strings.NewReader("again"), 0, 0o644); err == nil {
				t.Fatal("wrote over a file")
			}
			st, _ := fs.Stat(ctx, []string{stage + "/x/it's.txt"})
			if st[stage+"/x/it's.txt"].Mtime != 1700000000 {
				t.Fatalf("mtime %+v", st)
			}
			sums, err := fs.Sha256(ctx, []string{stage + "/x/it's.txt"})
			if err != nil || sums[stage+"/x/it's.txt"] != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
				t.Fatalf("sha %v %v", sums, err)
			}
			// A file that is not there does not land, and says so.
			if done, err := fs.Land(ctx, []LandPair{{Staged: "docs/no-such.txt", Final: "docs/moved.txt"}}); err == nil || len(done) != 0 {
				t.Fatalf("landed a file that is not there: %v %v", done, err)
			}
			if _, err := os.Lstat(filepath.Join(root, "docs", "moved.txt")); err == nil {
				t.Fatal("a missing file left a name behind")
			}
			// Landing over a name not listed fails; listed, it replaces.
			if _, err := fs.Land(ctx, []LandPair{{Staged: stage + "/x/it's.txt", Final: "top.txt"}}); statusOf(err) != 409 {
				t.Fatalf("landed over an unlisted file: %v", err)
			}
			done, err := fs.Land(ctx, []LandPair{{Staged: stage + "/x/it's.txt", Final: "new/it's.txt"}})
			if err != nil || len(done) != 1 {
				t.Fatalf("land %v %v", done, err)
			}
			got, _ := os.ReadFile(filepath.Join(root, "new", "it's.txt"))
			if string(got) != "hello" {
				t.Fatalf("landed %q", got)
			}
			if free, ok := fs.Free(ctx, ""); !ok || free <= 0 {
				t.Fatalf("free %d %v", free, ok)
			}
			removed, err := fs.Remove(ctx, []string{"new/it's.txt", "docs/link.txt"}, []string{"new"})
			if err != nil || len(removed) != 1 {
				t.Fatalf("remove %v %v", removed, err)
			}
			if _, err := os.Lstat(filepath.Join(root, "docs", "link.txt")); err != nil {
				t.Fatal("a link was removed as a file")
			}
			if _, err := os.Stat(filepath.Join(root, "new")); err == nil {
				t.Fatal("the emptied folder is still there")
			}
			if err := fs.RemoveStaging(ctx, stage); err != nil {
				t.Fatal(err)
			}
			if err := fs.RemoveStaging(ctx, "docs"); err == nil {
				t.Fatal("removed a folder that is not a staging folder")
			}
		})
	}
}

// A location rooted at / (a cluster) reaches everything under it.
func TestRemoteRootedAtSlash(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sub", "f.txt"), "f")
	fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: "/", Name: "h"}
	rel := strings.TrimPrefix(dir, "/")
	es, _, err := fs.List(context.Background(), rel+"/sub", 10)
	if err != nil || len(es) != 1 || es[0].Name != "f.txt" {
		t.Fatalf("%v %v", es, err)
	}
	rc, err := fs.Read(context.Background(), rel+"/sub/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "f" {
		t.Fatalf("read %q", b)
	}
}
