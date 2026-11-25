package transfers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// sftpRunner stands in for an account that takes SFTP only: every command
// is refused with OpenSSH's line, and the sftp subsystem is pkg/sftp's own
// server over in-process pipes, serving this computer's files.
type sftpRunner struct {
	home     string
	probes   atomic.Int32
	sessions atomic.Int32
	open     atomic.Int32
}

func (s *sftpRunner) RunOnce(ctx context.Context, site, key string, argv []string, timeout time.Duration, input io.Reader) (remote.Result, error) {
	s.probes.Add(1)
	return remote.Result{Code: 1, Stdout: sftpOnlyLine + "\n", Raw: []byte(sftpOnlyLine + "\n")}, nil
}

func (s *sftpRunner) Command(site, key string, argv []string) (Cmd, error) {
	return nil, errors.New("commands are refused")
}

func (s *sftpRunner) Subsystem(site, key, name string) (Cmd, error) {
	if name != "sftp" {
		return nil, errors.New("no subsystem " + name)
	}
	s.sessions.Add(1)
	return &pipeCmd{run: s}, nil
}

// pipeCmd is an sftp server on pipes, as an ssh process would be.
type pipeCmd struct {
	run       *sftpRunner
	inR, outR *io.PipeReader
	inW, outW *io.PipeWriter
	done      chan struct{}
	killed    sync.Once
}

func (p *pipeCmd) StdinPipe() (io.WriteCloser, error) {
	p.inR, p.inW = io.Pipe()
	return p.inW, nil
}

func (p *pipeCmd) StdoutPipe() (io.ReadCloser, error) {
	p.outR, p.outW = io.Pipe()
	return p.outR, nil
}

func (p *pipeCmd) SetStderr(io.Writer) {}

type rwc struct {
	io.Reader
	io.WriteCloser
}

func (p *pipeCmd) Start() error {
	srv, err := sftp.NewServer(rwc{p.inR, p.outW}, sftp.WithServerWorkingDirectory(p.run.home))
	if err != nil {
		return err
	}
	p.done = make(chan struct{})
	p.run.open.Add(1)
	go func() {
		srv.Serve()
		p.outW.Close()
		p.run.open.Add(-1)
		close(p.done)
	}()
	return nil
}

func (p *pipeCmd) Wait() error {
	<-p.done
	return nil
}

func (p *pipeCmd) Kill() {
	p.killed.Do(func() {
		p.inW.Close()
		p.inR.Close()
	})
}

var siteSeq atomic.Int32

// sftpFS is a location rooted at a fresh folder, on a site of its own.
func sftpFS(t *testing.T) (*RemoteFS, *sftpRunner, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	run := &sftpRunner{home: root}
	site, _ := remote.SSHSite("drop@sftp-"+itoa(int(siteSeq.Add(1)))+".example.com", 0, nil)
	return &RemoteFS{R: run, SiteID: site, Dir: root, Name: "Drop"}, run, dir
}

func TestSFTPDetect(t *testing.T) {
	ctx := context.Background()
	fs, run, _ := sftpFS(t)
	if !fs.SFTPOnly(ctx) || !fs.SFTPOnly(ctx) {
		t.Fatal("an account that answers with the SFTP-only line is SFTP only")
	}
	if n := run.probes.Load(); n != 1 {
		t.Fatalf("the answer is kept: %d probes", n)
	}
	// A login that runs commands is not, and the answer is kept too.
	loc := &localRunner{}
	site, _ := remote.SSHSite("user@shell.example.com", 0, nil)
	shell := &RemoteFS{R: shellSub{loc}, SiteID: site, Dir: t.TempDir(), Name: "Shell"}
	if shell.SFTPOnly(ctx) || shell.SFTPOnly(ctx) || loc.calls.Load() != 1 {
		t.Fatalf("a shell host: %d calls", loc.calls.Load())
	}
	// A runner that cannot start a subsystem, or a pw ssh site, is never
	// probed.
	loc2 := &localRunner{}
	if (&RemoteFS{R: loc2, SiteID: site}).SFTPOnly(ctx) || (&RemoteFS{R: shellSub{loc2}, SiteID: "gpu-cluster"}).SFTPOnly(ctx) || loc2.calls.Load() != 0 {
		t.Fatalf("probed %d times", loc2.calls.Load())
	}
	// A helper's answer that turns out to be the SFTP-only line marks the
	// site, so the next call uses SFTP.
	run2 := &sftpRunner{home: t.TempDir()}
	site2, _ := remote.SSHSite("drop@changed.example.com", 0, nil)
	markSFTP(site2, "", false)
	changed := &RemoteFS{R: run2, SiteID: site2, Dir: run2.home, Name: "Changed"}
	if _, _, err := changed.List(ctx, "", 10); err == nil || !strings.Contains(messageOf(err), "SFTP only") {
		t.Fatalf("first list %v", err)
	}
	if _, _, err := changed.List(ctx, "", 10); err != nil {
		t.Fatalf("second list %v", err)
	}
	// Home is where the SFTP session starts.
	if h, err := Home(ctx, run, fs.SiteID, ""); err != nil || h != run.home {
		t.Fatalf("home %q %v", h, err)
	}
}

// shellSub is a runner that could start a subsystem but runs commands.
type shellSub struct{ *localRunner }

func (shellSub) Subsystem(site, key, name string) (Cmd, error) {
	return nil, errors.New("not used")
}

func TestSFTPFS(t *testing.T) {
	ctx := context.Background()
	fs, run, dir := sftpFS(t)
	root := fs.Dir
	write(t, filepath.Join(root, "a.txt"), "alpha")
	write(t, filepath.Join(root, ".hidden"), "h")
	write(t, filepath.Join(root, "dir", "b.txt"), "beta")
	write(t, filepath.Join(root, "dir", ".h", "x"), "x")
	write(t, filepath.Join(dir, "outside", "secret"), "secret")
	os.Symlink("a.txt", filepath.Join(root, "link"))
	os.Symlink("../outside", filepath.Join(root, "out"))
	os.Symlink("dir", filepath.Join(root, "in"))

	// List: hidden entries left out, cut at max, links not followed.
	es, more, err := fs.List(ctx, "", 10)
	if err != nil || more {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name+":"+e.Type)
	}
	if strings.Join(names, " ") != "a.txt:file dir:dir in:link link:link out:link" {
		t.Fatalf("list %v", names)
	}
	if es, more, _ := fs.List(ctx, "", 2); len(es) != 2 || !more {
		t.Fatalf("cut %v %v", es, more)
	}
	for rel, want := range map[string]int{"missing": 404, "a.txt": 400, "out": 400, "in": 400} {
		if _, _, err := fs.List(ctx, rel, 10); statusOf(err) != want {
			t.Fatalf("list %s: %v", rel, err)
		}
	}
	if _, _, err := fs.List(ctx, "dir", 10); err != nil {
		t.Fatal(err)
	}

	// Stat: links as links, missing paths left out, paths through a link
	// that leaves the root refused, and the location named in errors.
	st, err := fs.Stat(ctx, []string{"a.txt", "link", "nope", "dir"})
	if err != nil || len(st) != 3 || st["a.txt"].Type != 'f' || st["a.txt"].Size != 5 || st["link"].Type != 'l' || st["dir"].Type != 'd' {
		t.Fatalf("stat %+v %v", st, err)
	}
	if _, err := fs.Stat(ctx, []string{"out/secret"}); statusOf(err) != 400 || !strings.HasPrefix(messageOf(err), "Drop: ") {
		t.Fatalf("stat outside %v", err)
	}
	// A link that stays inside the root may be named through.
	if st, err := fs.Stat(ctx, []string{"in/b.txt"}); err != nil || st["in/b.txt"].Type != 'f' {
		t.Fatalf("stat inside %v %v", st, err)
	}

	// Walk: hidden entries counted and not gone into, links counted, a
	// link named is a 400, a missing path a 404.
	w, err := fs.Walk(ctx, []string{"dir"}, 0)
	if err != nil || len(w.Files) != 1 || w.Files[0].Rel != "dir/b.txt" || w.Files[0].Size != 4 || w.Skipped.Hidden != 1 || len(w.Dirs) != 1 {
		t.Fatalf("walk dir %+v %v", w, err)
	}
	w, err = fs.Walk(ctx, []string{""}, 0)
	if err != nil || len(w.Files) != 2 || w.Skipped.Hidden != 2 || w.Skipped.Links != 3 {
		t.Fatalf("walk root %+v %v", w, err)
	}
	if _, err := fs.Walk(ctx, []string{"link"}, 0); statusOf(err) != 400 {
		t.Fatalf("walk link %v", err)
	}
	if _, err := fs.Walk(ctx, []string{"nope"}, 0); statusOf(err) != 404 {
		t.Fatalf("walk missing %v", err)
	}
	if _, err := fs.Walk(ctx, []string{""}, 1); err == nil {
		t.Fatal("walk past its limit")
	}
	if _, err := fs.Walk(ctx, []string{"out/secret"}, 0); statusOf(err) != 400 {
		t.Fatalf("walk outside %v", err)
	}

	// Sha256 is the file's hash, of plain files only.
	h, err := fs.Sha256(ctx, []string{"a.txt", "dir/b.txt"})
	sum := sha256.Sum256([]byte("alpha"))
	if err != nil || h["a.txt"] != hex.EncodeToString(sum[:]) || len(h) != 2 {
		t.Fatalf("sha %v %v", h, err)
	}
	if _, err := fs.Sha256(ctx, []string{"link"}); statusOf(err) != 400 {
		t.Fatalf("sha link %v", err)
	}

	// Read: plain files inside the root.
	rc, err := fs.Read(ctx, "dir/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	if rc.Close(); string(b) != "beta" {
		t.Fatalf("read %q", b)
	}
	for _, rel := range []string{"link", "out/secret", "dir"} {
		if _, err := fs.Read(ctx, rel); statusOf(err) != 400 {
			t.Fatalf("read %s: %v", rel, err)
		}
	}
	if _, err := fs.Read(ctx, "nope"); statusOf(err) != 404 {
		t.Fatalf("read missing %v", err)
	}

	// Write: folders made, never over a file, time and mode set, nothing
	// through a link that leaves the root.
	mt := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC).Unix()
	n, err := fs.Write(ctx, "new/deep/f.sh", strings.NewReader("#!/bin/sh\n"), mt, 0o755)
	if err != nil || n != 10 {
		t.Fatalf("write %d %v", n, err)
	}
	fi, _ := os.Stat(filepath.Join(root, "new", "deep", "f.sh"))
	if fi.ModTime().Unix() != mt || fi.Mode()&0o100 == 0 || read(t, filepath.Join(root, "new", "deep", "f.sh")) != "#!/bin/sh\n" {
		t.Fatalf("written %v %v", fi.ModTime(), fi.Mode())
	}
	if _, err := fs.Write(ctx, "a.txt", strings.NewReader("over"), 0, 0o644); err == nil || read(t, filepath.Join(root, "a.txt")) != "alpha" {
		t.Fatalf("write over a file: %v", err)
	}
	if _, err := fs.Write(ctx, "link", strings.NewReader("over"), 0, 0o644); err == nil || read(t, filepath.Join(root, "a.txt")) != "alpha" {
		t.Fatalf("write over a link: %v", err)
	}
	if _, err := fs.Write(ctx, "out/planted", strings.NewReader("x"), 0, 0o644); statusOf(err) != 400 {
		t.Fatalf("write outside %v", err)
	}
	gone(t, filepath.Join(dir, "outside", "planted"))

	// Mkdir.
	if err := fs.Mkdir(ctx, "m/n"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(root, "m", "n")); err != nil || !fi.IsDir() {
		t.Fatal("mkdir")
	}
	if err := fs.Mkdir(ctx, "out/z"); statusOf(err) != 400 {
		t.Fatalf("mkdir outside %v", err)
	}
	gone(t, filepath.Join(dir, "outside", "z"))

	// Land: a new name, a name that is there and not listed, a listed one
	// replaced, a folder never replaced.
	stage := stagingPrefix + "t1"
	for _, f := range []string{"one", "two", "three", "four"} {
		if _, err := fs.Write(ctx, stage+"/"+f, strings.NewReader("new "+f), 0, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done, err := fs.Land(ctx, []LandPair{{Staged: stage + "/one", Final: "landed/one"}})
	if err != nil || len(done) != 1 || read(t, filepath.Join(root, "landed", "one")) != "new one" {
		t.Fatalf("land %v %v", done, err)
	}
	gone(t, filepath.Join(root, stage, "one"))
	done, err = fs.Land(ctx, []LandPair{{Staged: stage + "/two", Final: "a.txt"}})
	if statusOf(err) != 409 || len(done) != 0 || read(t, filepath.Join(root, "a.txt")) != "alpha" {
		t.Fatalf("land over %v %v", done, err)
	}
	done, err = fs.Land(ctx, []LandPair{{Staged: stage + "/two", Final: "a.txt", Replace: true}, {Staged: stage + "/three", Final: "dir", Replace: true}})
	if statusOf(err) != 409 || len(done) != 1 || read(t, filepath.Join(root, "a.txt")) != "new two" {
		t.Fatalf("land replace %v %v", done, err)
	}
	if _, err := fs.Land(ctx, []LandPair{{Staged: stage + "/four", Final: "out/four"}}); statusOf(err) != 400 {
		t.Fatalf("land outside %v", err)
	}
	gone(t, filepath.Join(dir, "outside", "four"))

	// Remove: plain files (never links), then empty folders deepest first.
	write(t, filepath.Join(root, "r", "s", "f"), "f")
	got, err := fs.Remove(ctx, []string{"r/s/f", "link", "nope"}, []string{"r", "r/s", "dir"})
	sort.Strings(got)
	if err != nil || strings.Join(got, ",") != "r/s/f" {
		t.Fatalf("remove %v %v", got, err)
	}
	gone(t, filepath.Join(root, "r"))
	if _, err := os.Lstat(filepath.Join(root, "link")); err != nil {
		t.Fatal("a link was removed")
	}
	if _, err := os.Stat(filepath.Join(root, "dir", "b.txt")); err != nil {
		t.Fatal("a folder with files was removed")
	}
	if _, err := fs.Remove(ctx, []string{"out/secret"}, nil); statusOf(err) != 400 || read(t, filepath.Join(dir, "outside", "secret")) != "secret" {
		t.Fatalf("remove outside %v", err)
	}

	// RemoveStaging: only a staging folder's name, never a link, all in it.
	if err := fs.RemoveStaging(ctx, "dir"); statusOf(err) != 400 {
		t.Fatalf("not staging %v", err)
	}
	os.Symlink("../outside", filepath.Join(root, stagingPrefix+"link"))
	if err := fs.RemoveStaging(ctx, stagingPrefix+"link"); statusOf(err) != 400 || read(t, filepath.Join(dir, "outside", "secret")) != "secret" {
		t.Fatalf("staging link %v", err)
	}
	os.Symlink("../../outside", filepath.Join(root, stage, "back"))
	if err := fs.RemoveStaging(ctx, stage); err != nil {
		t.Fatal(err)
	}
	gone(t, filepath.Join(root, stage))
	if read(t, filepath.Join(dir, "outside", "secret")) != "secret" {
		t.Fatal("a link in a staging folder was followed")
	}
	if err := fs.RemoveStaging(ctx, stagingPrefix+"none"); err != nil {
		t.Fatal(err)
	}

	// Free, from statvfs@openssh.com.
	if free, ok := fs.Free(ctx, ""); !ok || free <= 0 {
		t.Fatalf("free %d %v", free, ok)
	}

	// The root itself through a link is fine; its real path is what paths
	// are checked against.
	os.Symlink(root, filepath.Join(dir, "rootlink"))
	via := &RemoteFS{R: run, SiteID: fs.SiteID, Dir: filepath.Join(dir, "rootlink"), Name: "Via"}
	if _, err := via.Stat(ctx, []string{"in/b.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := via.Stat(ctx, []string{"out/secret"}); statusOf(err) != 400 {
		t.Fatalf("through a linked root %v", err)
	}
	if _, _, err := (&RemoteFS{R: run, SiteID: fs.SiteID, Dir: filepath.Join(dir, "none"), Name: "None"}).List(ctx, "", 1); statusOf(err) != 502 {
		t.Fatalf("missing root %v", err)
	}

	// Every session was closed.
	for i := 0; i < 100 && run.open.Load() != 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := run.open.Load(); n != 0 {
		t.Fatalf("%d sessions left open", n)
	}
}

// Transfers to and from an SFTP-only saved location: studio-copy carries
// them, rsync says why it cannot.
func TestSFTPTransfers(t *testing.T) {
	_, run, dir := sftpFS(t)
	drop := filepath.Join(dir, "drop")
	os.MkdirAll(drop, 0o755)
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	write(t, filepath.Join(kb, "batch", "one.txt"), "one")
	write(t, filepath.Join(kb, "batch", "sub", "two.txt"), "two")
	write(t, filepath.Join(kb, "batch", ".skip"), "s")
	set := DefaultSettings()
	set.SavedLocations = []SavedLocation{{ID: "drop", Label: "Drop", Host: "drop@files.example.com", Root: drop}}
	ix := &recIndex{}
	shell := func(site, key string) ([]string, []string, error) { return []string{"false"}, nil, nil }
	s := New(Deps{IndexBase: filepath.Join(dir, "ix"), Getenv: func(string) string { return "" }, Runner: run, Shell: shell,
		Libraries: func(context.Context, Viewer) []LibraryLoc {
			return []LibraryLoc{{ID: "kb", Label: "Knowledge base", Writable: true, Reindexed: true, FS: &LocalFS{Dir: kb}}}
		},
		Settings: &MemorySettings{S: set}, Index: ix})
	r := &rig{t: t, s: s, ix: ix}

	ls, err := s.Browse(context.Background(), single, "saved:drop", nil)
	if err != nil || len(ls.Entries) != 0 {
		t.Fatalf("browse %+v %v", ls, err)
	}
	p := r.mustPlan(single, req(ActionCopy, "library:kb", []string{"batch"}, "saved:drop", ""))
	if p.Mover != MoverCopy {
		t.Fatalf("mover %s", p.Mover)
	}
	for _, m := range p.Movers {
		if m.Name == MoverRsync && !strings.Contains(m.Why, "takes SFTP only") {
			t.Fatalf("rsync's reason %q", m.Why)
		}
		if m.Name == MoverCopy && !strings.Contains(m.Why, "over SFTP") {
			t.Fatalf("studio-copy's reason %q", m.Why)
		}
	}
	q := req(ActionCopy, "library:kb", []string{"batch"}, "saved:drop", "")
	q.Options.Mover = MoverRsync
	if _, err := r.plan(single, q); err == nil {
		t.Fatal("rsync named for an SFTP-only location")
	}
	tr := r.run(single, req(ActionCopy, "library:kb", []string{"batch"}, "saved:drop", ""))
	if tr.State != StateSucceeded || tr.Mover != MoverCopy || read(t, filepath.Join(drop, "batch", "sub", "two.txt")) != "two" {
		t.Fatalf("push %+v", tr)
	}
	gone(t, filepath.Join(drop, "batch", ".skip"))
	noStaging(t, drop)
	// A move back, checked with sha256: the files leave the drop.
	os.RemoveAll(filepath.Join(kb, "batch"))
	q = req(ActionMove, "saved:drop", []string{"batch"}, "library:kb", "")
	q.Options.Checksum = yes
	tr = r.run(single, q)
	if tr.State != StateSucceeded || read(t, filepath.Join(kb, "batch", "one.txt")) != "one" {
		t.Fatalf("move back %+v", tr)
	}
	gone(t, filepath.Join(drop, "batch"))
	noStaging(t, kb)

	// The settings test says the account takes SFTP only.
	res, err := s.TestSettings(context.Background(), single, TestRequest{Host: "drop@files.example.com", Root: drop})
	if err != nil || !res.OK || !res.SFTPOnly || res.Rsync == nil || *res.Rsync || res.Writable == nil || !*res.Writable || res.FreeBytes == nil {
		t.Fatalf("test %+v %v", res, err)
	}
	noStaging(t, drop)
}
