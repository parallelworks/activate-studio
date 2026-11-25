package writehelper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// world is a library's folder and, beside it, a folder of the same person's
// that no write in the library may change.
type world struct {
	t         *testing.T
	root, out string
	before    string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	old := unix.Umask(0o022)
	t.Cleanup(func() { unix.Umask(old) })
	dir := t.TempDir()
	w := &world{t: t, root: filepath.Join(dir, "lib"), out: filepath.Join(dir, "private")}
	w.fill(w.root, "in")
	w.fill(w.out, "out")
	os.Mkdir(filepath.Join(w.root, stagePrefix+"1"), 0o700)
	w.file(filepath.Join(w.root, stagePrefix+"1", "s.txt"), "staged")
	w.file(filepath.Join(w.root, uploadPrefix+"1"), "upload")
	w.before = snapshot(t, w.out)
	return w
}

func (w *world) file(p, body string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func sumText(s string) string {
	h, _ := sumOf(strings.NewReader(s))
	return h
}

// fill puts the same names in a folder, so a write led there would find
// what it looks for.
func (w *world) fill(dir, what string) {
	for _, n := range []string{"d/a.txt", "d/sub/b.txt", "e/c.txt", "top.txt", stagePrefix + "9/z.txt", "d/" + uploadPrefix + "9/data", "d/" + stagePrefix + "9/z.txt"} {
		w.file(filepath.Join(dir, n), what+" "+n)
	}
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		b := []byte{}
		if fi.Mode().IsRegular() {
			b, _ = os.ReadFile(p)
		}
		out = append(out, fmt.Sprintf("%s %v %d %q", strings.TrimPrefix(p, dir), fi.Mode(), st.Nlink, b))
		return nil
	})
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func (w *world) unchanged(what string) {
	w.t.Helper()
	if now := snapshot(w.t, w.out); now != w.before {
		w.t.Fatalf("%s changed what is outside the library:\n%s\nwas\n%s", what, now, w.before)
	}
}

// call runs the helper on the library.
func (w *world) call(stdin string, args ...string) (string, int, string) {
	return callAt(w.root, stdin, args...)
}

func callAt(root, stdin string, args ...string) (string, int, string) {
	var opts []string
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		opts, args = append(opts, args[0]), args[1:]
	}
	var out, errb bytes.Buffer
	code := Main(append(append(opts, "MARK", args[0], root), args[1:]...), strings.NewReader(stdin), &out, &errb)
	if strings.Contains(out.String(), "out d/") {
		panic("what is outside the library was read: " + out.String())
	}
	return strings.TrimPrefix(out.String(), "MARK\n"), code, errb.String()
}

// ops is every command, on names under the folder d and at the top.
var ops = []struct {
	name  string
	stdin string
	args  []string
}{
	{"write in a folder", "new", []string{"write", "d/new.txt", "", ""}},
	{"write over a name", "new", []string{"write", "d/a.txt", "", ""}},
	{"write in a folder to make", "new", []string{"write", "d/sub/deep/new.txt", "", "x"}},
	{"write at the top", "new", []string{"write", "top-new.txt", "", ""}},
	{"mkdir", "", []string{"mkdir", "d/sub/new/more"}},
	{"mkdir of a staging folder", "", []string{"mkdir", "d/" + stagePrefix + "2"}},
	{"land from staging", stagePrefix + "1/s.txt\x00d/s.txt\x00n\x00", []string{"land"}},
	{"land over", uploadPrefix + "1\x00d/a.txt\x00r\x00", []string{"land"}},
	{"land from a folder", "d/a.txt\x00moved.txt\x00n\x00", []string{"land"}},
	{"move a folder", "d\x00e/d\x00n\x00", []string{"land"}},
	{"move into a folder", "top.txt\x00d/sub/top.txt\x00n\x00", []string{"land"}},
	{"remove a file", "f\x00d/a.txt\x00", []string{"remove"}},
	{"remove a folder", "d\x00d/sub\x00", []string{"remove"}},
	{"remove a tree", "d/sub\x00", []string{"rmtree"}},
	{"remove a tree at the top", "d\x00", []string{"rmtree"}},
	{"remove staging", "", []string{"rmstage", stagePrefix + "1"}},
	{"remove staging in a folder", "", []string{"rmstage", "d/" + stagePrefix + "9"}},
	{"read", "", []string{"read", "d/a.txt"}},
	{"start parts", "", []string{"pstart", "d/" + uploadPrefix + "2", "d/big", "1"}},
	{"a part", "part", []string{"part", "d/" + uploadPrefix + "9", "0", "4"}},
	{"land parts", "d/" + uploadPrefix + "9/data\x00d/a.txt\x00r\x00", []string{"land"}},
	{"drop parts", "d/" + uploadPrefix + "9\x00", []string{"rmtree"}},
	{"text", "", []string{"text", "d/a.txt", "100"}},
	{"swap", "new", []string{"swap", "d/a.txt", sumText("in d/a.txt"), sumText("new"), uploadPrefix + "3"}},
	{"share", "", []string{"share", "everyone", "", "", "r", "d", ""}},
	{"unshare", "", []string{"unshare", "everyone", "", "", "r", "d", ""}},
}

// both runs fn with the kernel's own resolution and with the walk used
// where the kernel has none.
func both(t *testing.T, fn func(t *testing.T)) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "openat2", true: "walk"}[old], func(t *testing.T) {
			noOpenat2 = old
			t.Cleanup(func() { noOpenat2, beforeUse = false, func(string) {} })
			fn(t)
		})
	}
}

// What every command does with nothing in its way.
func TestCommands(t *testing.T) {
	both(t, func(t *testing.T) {
		w := newWorld(t)
		is := func(p, want string) {
			t.Helper()
			b, err := os.ReadFile(filepath.Join(w.root, p))
			if want == "" {
				if err == nil {
					t.Errorf("%s is still there", p)
				}
			} else if string(b) != want {
				t.Errorf("%s: %q, want %q", p, b, want)
			}
		}
		ok := func(out string, code int, errs string) string {
			t.Helper()
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errs)
			}
			return out
		}
		fails := func(want int, says string, out string, code int, errs string) {
			t.Helper()
			if code != want || !strings.Contains(errs, says) {
				t.Errorf("exit %d %q, want %d %q", code, errs, want, says)
			}
		}
		ok(w.call("body", "write", "-x/y z/new.txt", "202401020304.05", "x"))
		is("-x/y z/new.txt", "body")
		st, _ := os.Stat(filepath.Join(w.root, "-x/y z/new.txt"))
		if st.Mode().Perm() != 0o744 || !st.ModTime().Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
			t.Errorf("mode and time: %v %v", st.Mode(), st.ModTime().UTC())
		}
		o, c, e := w.call("again", "write", "top.txt", "", "")
		fails(8, "something is there already", o, c, e)
		is("top.txt", "in top.txt")
		ok(w.call("", "mkdir", "new/deep"))
		ok(w.call("", "mkdir", "new/deep"))
		if out := ok(w.call(stagePrefix+"1/s.txt\x00f/s.txt\x00n\x00"+uploadPrefix+"1\x00top.txt\x00r\x00", "land")); out != "f/s.txt\x00top.txt\x00" {
			t.Errorf("landed %q", out)
		}
		is("f/s.txt", "staged")
		is("top.txt", "upload")
		o, c, e = w.call("e/c.txt\x00top.txt\x00n\x00", "land")
		fails(10, "already there", o, c, e)
		o, c, e = w.call("e/c.txt\x00d/sub\x00r\x00", "land")
		fails(10, "already there", o, c, e)
		o, c, e = w.call("e/none\x00x\x00n\x00", "land")
		fails(4, "is not there", o, c, e)
		ok(w.call("d\x00e/d\x00n\x00", "land"))
		is("e/d/sub/b.txt", "in d/sub/b.txt")
		if out := ok(w.call("f\x00e/c.txt\x00d\x00e/d\x00f\x00gone/x\x00", "remove")); out != "e/c.txt\x00" {
			t.Errorf("removed %q", out)
		}
		is("e/d/sub/b.txt", "in d/sub/b.txt")
		if out := ok(w.call("e\x00gone\x00", "rmtree")); out != "e\x00" {
			t.Errorf("removed %q", out)
		}
		is("e/d/sub/b.txt", "")
		ok(w.call("", "rmstage", stagePrefix+"1"))
		is(stagePrefix+"1/s.txt", "")
		ok(w.call("", "rmstage", stagePrefix+"none"))
		o, c, e = w.call("", "rmstage", "f")
		fails(5, "not a staging folder", o, c, e)
		if out := ok(w.call("", "read", "top.txt")); out != "upload" {
			t.Errorf("read %q", out)
		}
		for _, p := range []string{"..", "../private/top.txt", "f/../../private/top.txt", "/../x"} {
			o, c, e = w.call("x", "write", p, "", "")
			fails(6, "leads outside", o, c, e)
		}
		for _, p := range []string{"", ".", "./", "/"} {
			o, c, e = w.call(p+"\x00", "rmtree")
			fails(6, "refusing the location itself", o, c, e)
		}
		o, c, e = w.call("", "nothing")
		fails(2, "unknown command", o, c, e)
		w.unchanged("a command")
	})
}

// A link anywhere on the way, or in the place of what is written over,
// moved or removed, is not followed.
func TestNoLinkIsFollowed(t *testing.T) {
	both(t, func(t *testing.T) {
		for _, op := range ops {
			for _, link := range []string{"d", "d/sub", "d/a.txt", "top-new.txt", "d/new.txt", "d/s.txt", uploadPrefix + "1", stagePrefix + "1", stagePrefix + "1/s.txt", "d/" + stagePrefix + "2", "d/" + stagePrefix + "9", "moved.txt", "d/" + uploadPrefix + "9", "d/" + uploadPrefix + "9/data", "d/" + uploadPrefix + "2"} {
				for _, rel := range []bool{false, true} {
					w := newWorld(t)
					p := filepath.Join(w.root, link)
					to := filepath.Join(w.out, link)
					switch {
					case strings.HasPrefix(link, uploadPrefix), link == stagePrefix+"1/s.txt":
						to = filepath.Join(w.out, "top.txt")
					case strings.HasPrefix(link, stagePrefix):
						to = filepath.Join(w.out, "e")
					}
					if rel {
						to, _ = filepath.Rel(filepath.Dir(p), to)
					}
					os.RemoveAll(p)
					if err := os.Symlink(to, p); err != nil {
						t.Fatal(err)
					}
					w.call(op.stdin, op.args...)
					w.unchanged(op.name + " with " + link + " a link")
				}
			}
		}
	})
}

// A name swapped for a link between its being looked at and its being used,
// at every such moment of every command.
func TestASwapBetweenCheckAndUse(t *testing.T) {
	both(t, func(t *testing.T) {
		for _, op := range ops {
			for _, link := range []string{"d", "d/sub", "d/a.txt", "e", stagePrefix + "1", "d/" + stagePrefix + "9", uploadPrefix + "1", "d/" + uploadPrefix + "9", "d/" + uploadPrefix + "9/data"} {
				// The moments are counted first, then each is taken in turn.
				n := 0
				beforeUse = func(string) { n++ }
				newWorld(t).call(op.stdin, op.args...)
				for at := range n {
					w := newWorld(t)
					k := 0
					beforeUse = func(string) {
						if k++; k-1 != at {
							return
						}
						p := filepath.Join(w.root, link)
						to := filepath.Join(w.out, link)
						if _, err := os.Lstat(to); err != nil {
							to = filepath.Join(w.out, "e")
						}
						if os.Rename(p, p+".was") == nil {
							os.Symlink(to, p)
						}
					}
					w.call(op.stdin, op.args...)
					beforeUse = func(string) {}
					w.unchanged(fmt.Sprintf("%s with %s swapped at moment %d", op.name, link, at))
				}
			}
		}
	})
}

// Another account swaps a folder for a link and back as fast as it can
// while every command runs.
func TestARaceWithAnotherAccount(t *testing.T) {
	both(t, func(t *testing.T) {
		w := newWorld(t)
		os.Symlink(w.out, filepath.Join(w.root, "trap"))
		os.Symlink(filepath.Join(w.out, "d"), filepath.Join(w.root, "trap-d"))
		fd, err := unix.Open(w.root, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		if err := unix.Renameat2(fd, "e", fd, "trap", unix.RENAME_EXCHANGE); err != nil {
			t.Skip("this file system does not exchange names")
		}
		ctx, stop := context.WithCancel(context.Background())
		var swaps atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ctx.Err() == nil {
				unix.Renameat2(fd, "d", fd, "trap-d", unix.RENAME_EXCHANGE)
				unix.Renameat2(fd, "e", fd, "trap", unix.RENAME_EXCHANGE)
				swaps.Add(1)
			}
		}()
		tries, hit := 0, 0
		for until := time.Now().Add(1500 * time.Millisecond); time.Now().Before(until); tries++ {
			op := ops[tries%len(ops)]
			if _, code, _ := w.call(op.stdin, op.args...); code == 6 {
				hit++
			}
			// Put back what a command took, inside the library.
			os.Mkdir(filepath.Join(w.root, stagePrefix+"1"), 0o700)
			os.WriteFile(filepath.Join(w.root, stagePrefix+"1", "s.txt"), []byte("staged"), 0o644)
			os.WriteFile(filepath.Join(w.root, uploadPrefix+"1"), []byte("upload"), 0o644)
		}
		stop()
		<-done
		w.unchanged("a race")
		t.Logf("%d commands, %d refused at a link, %d swaps", tries, hit, swaps.Load())
		if hit == 0 || swaps.Load() < 100 {
			t.Errorf("the race did not meet: %d refused, %d swaps", hit, swaps.Load())
		}
	})
}

// The folder itself moved away and another, or a link, put in its place.
func TestTheFolderItselfReplaced(t *testing.T) {
	w := newWorld(t)
	id, code, _ := w.call("", "id")
	if code != 0 {
		t.Fatal(id)
	}
	pin := "--pin=" + strings.TrimSpace(id)
	if _, code, errs := w.call("x", pin, "write", "ok.txt", "", ""); code != 0 {
		t.Fatal(errs)
	}
	os.Rename(w.root, w.root+".was")
	for _, how := range []func(){
		func() { os.Symlink(w.out, w.root) },
		func() { os.Mkdir(w.root, 0o777) },
		func() { os.Rename(w.out, w.root) },
	} {
		how()
		for _, op := range ops {
			_, code, errs := w.call(op.stdin, append([]string{pin}, op.args...)...)
			if code != ExitMoved || !strings.Contains(errs, "not the folder this library was added for") {
				t.Errorf("%s: %d %s", op.name, code, errs)
			}
		}
		if fi, _ := os.Lstat(w.root); fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(w.root)
		} else if _, err := os.Stat(w.out); err != nil {
			os.Rename(w.root, w.out)
		} else {
			os.Remove(w.root)
		}
	}
	w.unchanged("a command on another folder")
	// The folder back, however it is reached.
	os.Rename(w.root+".was", w.root)
	os.Symlink(w.root, w.root+"-link")
	if _, code, errs := callAt(w.root+"-link", "x", pin, "write", "ok2.txt", "", ""); code != 0 {
		t.Error(errs)
	}
	uid := strings.TrimSpace(id[strings.Index(id, ":")+1:])
	for p, want := range map[string]int{"--pin=0:" + uid: 0, "--pin=0:4294967294": ExitMoved, "--pin=x": exitUsage} {
		if _, code, _ := w.call("", p, "id"); code != want {
			t.Errorf("%s: %d, want %d", p, code, want)
		}
	}
}

// Pipes and devices in the place of a file do not hold a command up.
func TestWhatIsNotAPlainFile(t *testing.T) {
	w := newWorld(t)
	if err := unix.Mkfifo(filepath.Join(w.root, "pipe"), 0o666); err != nil {
		t.Skip(err)
	}
	end := make(chan struct{})
	go func() {
		defer close(end)
		for _, p := range []string{"pipe", "d"} {
			if _, code, errs := w.call("", "read", p); code != exitNotPlain {
				t.Errorf("read %s: %d %s", p, code, errs)
			}
		}
		if _, code, _ := w.call(uploadPrefix+"1\x00pipe\x00r\x00", "land"); code != exitTaken {
			t.Errorf("land over a pipe: %d", code)
		}
	}()
	select {
	case <-end:
	case <-time.After(20 * time.Second):
		t.Fatal("a pipe held the helper up")
	}
}

// What a write makes is closed to other accounts as the shell helper's is.
func TestModes(t *testing.T) {
	for _, c := range []struct {
		opts            []string
		folder          os.FileMode
		umask           int
		file, dir, land os.FileMode
	}{
		{nil, 0o700, 0o022, 0o644, 0o755, 0o664},
		{[]string{"--priv=1"}, 0o700, 0o002, 0o600, 0o700, 0o600},
		{[]string{"--priv=1"}, 0o755, 0o002, 0o644, 0o755, 0o664},
		{[]string{"--priv=1"}, 0o750, 0o002, 0o600, 0o700, 0o600},
		{[]string{"--priv=2"}, 0o750, 0o002, 0o664, 0o775, 0o664},
		{[]string{"--priv=2"}, 0o700, 0o002, 0o600, 0o700, 0o600},
	} {
		w := newWorld(t)
		unix.Umask(c.umask)
		d := filepath.Join(w.root, "d")
		os.Chmod(d, c.folder)
		in := func(stdin string, args ...string) {
			t.Helper()
			if _, code, errs := w.call(stdin, append(append([]string{}, c.opts...), args...)...); code != 0 {
				t.Errorf("%v %o: %s", c.opts, c.folder, errs)
			}
		}
		in("x", "write", "d/made/f.txt", "", "")
		in("", "mkdir", "d/"+stagePrefix+"7")
		in("x", "write", "d/"+stagePrefix+"7/g.txt", "", "")
		os.Chmod(filepath.Join(w.root, uploadPrefix+"1"), 0o664)
		in(uploadPrefix+"1\x00d/l.txt\x00n\x00", "land")
		for p, want := range map[string]os.FileMode{"d/made": c.dir, "d/made/f.txt": c.file, "d/" + stagePrefix + "7": 0o700, "d/" + stagePrefix + "7/g.txt": c.file, "d/l.txt": c.land} {
			if st, err := os.Stat(filepath.Join(w.root, p)); err != nil || st.Mode().Perm() != want {
				t.Errorf("%v in a folder of mode %o: %s is %v, want %o", c.opts, c.folder, p, st.Mode(), want)
			}
		}
	}
}

// Between file systems, an entry is copied beside its name, then renamed.
func TestAnotherFileSystem(t *testing.T) {
	far, err := os.MkdirTemp("/dev/shm", "studio-write-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(far)
	w := newWorld(t)
	var a, b unix.Stat_t
	if unix.Stat(far, &a) != nil || unix.Stat(w.root, &b) != nil || a.Dev == b.Dev {
		t.Skip("one file system")
	}
	w.file(filepath.Join(far, "d/x.txt"), "far")
	os.Symlink(w.out, filepath.Join(far, "d/link"))
	os.Chmod(filepath.Join(far, "d/x.txt"), 0o755)
	tr, err := openTree(w.root, pin{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	from, err := openDir(unix.AT_FDCWD, far, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(from)
	st, _ := statAt(from, "d")
	if err := tr.copyIn(from, "d", "d", tr.fd, "far", st, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(w.root, "far/x.txt")); string(b) != "far" {
		t.Errorf("copied %q", b)
	}
	if l, _ := os.Readlink(filepath.Join(w.root, "far/link")); l != w.out {
		t.Errorf("the link is %q", l)
	}
	if _, err := os.Lstat(filepath.Join(far, "d")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the original stayed")
	}
	st, _ = statAt(tr.fd, "top.txt")
	if err := tr.copyIn(tr.fd, "top.txt", "top.txt", tr.fd, "far", st, false); !errors.Is(err, unix.EEXIST) {
		t.Errorf("over a name that is taken: %v", err)
	}
	w.unchanged("a copy")
}

// A program run in a staging folder writes in that folder wherever its
// name leads by then, and in one that is closed and this account's only.
func TestAProgramInAStagingFolder(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "studio-write")
	cmd := exec.Command("go", "build", "-o", exe, "../../cmd/studio-write")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	w := newWorld(t)
	stage := stagePrefix + "1"
	p := filepath.Join(w.root, stage)
	run := func(args ...string) (string, int) {
		c := exec.Command(exe, append([]string{"-", "in", w.root, stage}, args...)...)
		b, _ := c.CombinedOutput()
		return string(b), c.ProcessState.ExitCode()
	}
	// The name is swapped for a link once the program runs.
	out, code := run("sh", "-c", `mv "$1" "$1.was" && ln -s "$2" "$1" && cd "$3" && echo in > "$3"f.txt && mkdir "$3"sub && cat "$3"f.txt`, "sh", p, w.out, InDir)
	if code != 0 || strings.TrimSpace(out) != "in" {
		t.Fatalf("%d %s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(p+".was", "f.txt")); string(b) != "in\n" {
		t.Errorf("wrote %q", b)
	}
	w.unchanged("a program in a staging folder")
	if out, code := run("true"); code != exitOutside {
		t.Errorf("a link: %d %s", code, out)
	}
	os.Remove(p)
	os.Rename(p+".was", p)
	os.Chmod(p, 0o770)
	if out, code := run("true"); code != ExitOpen {
		t.Errorf("a folder that is open: %d %s", code, out)
	}
	os.Chmod(p, 0o700)
	if out, code := run("no-such-program-there"); code != 127 {
		t.Errorf("no such program: %d %s", code, out)
	}
	stage = "d"
	if out, code := run("true"); code != exitUsage {
		t.Errorf("another folder: %d %s", code, out)
	}
}

func TestACLCodec(t *testing.T) {
	a := acl{{tagOther, 0, noID}, {tagGroup, 5, 70}, {tagUser, 7, 9}, {tagUserObj, 7, noID}, {tagUser, 4, 3}, {tagGroupObj, 5, noID}}
	a.remask()
	b, err := decodeACL(a.encode())
	if err != nil {
		t.Fatal(err)
	}
	want := acl{{tagUserObj, 7, noID}, {tagUser, 4, 3}, {tagUser, 7, 9}, {tagGroupObj, 5, noID}, {tagGroup, 5, 70}, {tagMask, 7, noID}, {tagOther, 0, noID}}
	if fmt.Sprint(b) != fmt.Sprint(want) {
		t.Errorf("%v", b)
	}
	b.set(tagMask, noID, 4)
	b.pin()
	if g, _ := b.get(tagGroupObj, noID); g != 4 {
		t.Errorf("the group was pinned at %d", g)
	}
	for _, bad := range [][]byte{nil, {1, 0, 0, 0}, {2, 0, 0, 0, 1}} {
		if _, err := decodeACL(bad); err == nil {
			t.Errorf("%v was read", bad)
		}
	}
}

// Parts: a folder closed to other accounts, whole parts at their place, and
// only while the folder is the one this account made.
func TestParts(t *testing.T) {
	w := newWorld(t)
	dir := uploadPrefix + "7"
	if _, code, errs := w.call("", "pstart", dir, "big", "1"); code != 0 {
		t.Fatal(errs)
	}
	if st, _ := os.Stat(filepath.Join(w.root, dir)); st.Mode().Perm() != 0o700 {
		t.Errorf("%v", st.Mode())
	}
	if _, code, errs := w.call("bb", "part", dir, "1", "2"); code != 0 {
		t.Fatal(errs)
	}
	if _, code, errs := w.call("aaaa", "part", dir, "0", "4"); code != 0 {
		t.Fatal(errs)
	}
	b, _ := os.ReadFile(filepath.Join(w.root, dir, "data"))
	if len(b) != 1<<20+2 || string(b[:4]) != "aaaa" || string(b[1<<20:]) != "bb" {
		t.Errorf("%d bytes", len(b))
	}
	for _, c := range []struct {
		code int
		args []string
	}{
		{exitRead, []string{"part", dir, "0", "9"}},
		{exitMissing, []string{"part", uploadPrefix + "8", "0", "4"}},
		{exitNotPlain, []string{"part", "d", "0", "4"}},
		{exitNotPlain, []string{"pstart", "d", "big", "1"}},
		{exitTaken, []string{"pstart", uploadPrefix + "6", "e", "1"}},
		{exitNoSpace, []string{"pstart", "new/" + uploadPrefix + "6", "new/big", "4503599627370496"}},
		{exitUsage, []string{"part", dir, "-1", "4"}},
	} {
		if _, code, errs := w.call("aaaa", c.args...); code != c.code {
			t.Errorf("%v: %d %s, want %d", c.args, code, errs, c.code)
		}
	}
	if _, err := os.Stat(filepath.Join(w.root, "new")); err == nil {
		t.Error("a refused file made its folder")
	}
	os.Chmod(filepath.Join(w.root, dir), 0o755)
	if _, code, _ := w.call("aaaa", "--priv=1", "part", dir, "0", "4"); code != exitMissing {
		t.Errorf("a folder of parts open to others: %d", code)
	}
}

// An edit: kept from a file that changed, and the file's mode, ACL and
// attributes kept on one that did not.
func TestSwap(t *testing.T) {
	w := newWorld(t)
	p := filepath.Join(w.root, "d/a.txt")
	os.Chmod(p, 0o751)
	unix.Setxattr(p, "user.note", []byte("kept"), 0)
	if out, code, errs := w.call("", "text", "d/a.txt", "4"); code != 0 || out != "in d" {
		t.Errorf("%q %d %s", out, code, errs)
	}
	if out, code, _ := w.call("new", "swap", "d/a.txt", sumText("other"), sumText("new"), uploadPrefix+"3"); code != exitChanged || strings.TrimSpace(out) != sumText("in d/a.txt") {
		t.Errorf("%q %d", out, code)
	}
	if _, code, _ := w.call("ne", "swap", "d/a.txt", sumText("in d/a.txt"), sumText("new"), uploadPrefix+"3"); code != exitRead {
		t.Errorf("not whole: %d", code)
	}
	if _, code, errs := w.call("new", "swap", "d/a.txt", sumText("in d/a.txt"), sumText("new"), uploadPrefix+"3"); code != 0 {
		t.Fatal(errs)
	}
	st, _ := os.Stat(p)
	if b, _ := os.ReadFile(p); string(b) != "new" || st.Mode().Perm() != 0o751 {
		t.Errorf("%q %v", b, st.Mode())
	}
	buf := make([]byte, 10)
	if n, err := unix.Getxattr(p, "user.note", buf); err == nil && string(buf[:n]) != "kept" {
		t.Errorf("attribute %q", buf[:n])
	}
	for _, c := range []struct {
		code int
		name string
	}{{exitGone, "d/none"}, {exitGone, "none/x"}, {exitNotPlain, "d/sub"}, {exitOutside, "../private/top.txt"}} {
		if _, code, errs := w.call("new", "swap", c.name, sumText("x"), sumText("new"), uploadPrefix+"3"); code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, code, errs, c.code)
		}
	}
	if _, code, _ := w.call("new", "swap", "top.txt", sumText("in top.txt"), sumText("new"), "../x"); code != exitUsage {
		t.Errorf("a staged name elsewhere: %d", code)
	}
	if os.Geteuid() != 0 {
		os.Chmod(p, 0o444)
		for _, args := range [][]string{{"text", "d/a.txt", "9"}, {"swap", "d/a.txt", sumText("new"), sumText("x"), uploadPrefix + "3"}} {
			if _, code, errs := w.call("x", args...); code != exitNotYours {
				t.Errorf("%v: %d %s", args, code, errs)
			}
		}
	}
	if left, _ := filepath.Glob(filepath.Join(w.root, "d", uploadPrefix+"3*")); len(left) > 0 {
		t.Errorf("left %v", left)
	}
	// Written where it is, as another account's file would be.
	tr, _ := openTree(w.root, pin{}, 0)
	f, _ := os.OpenFile(filepath.Join(w.root, "top.txt"), os.O_RDWR, 0)
	s, _ := os.CreateTemp(t.TempDir(), "s")
	s.WriteString("x")
	if err := tr.writeInPlace(tr.fd, "top.txt", uploadPrefix+"5", f, s); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(w.root, "top.txt")); string(b) != "x" {
		t.Errorf("%q", b)
	}
	w.unchanged("an edit")
}
