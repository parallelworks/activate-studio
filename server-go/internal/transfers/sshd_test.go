package transfers

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// Transfers over real ssh: two sshd processes run by this user on
// loopback ports, each login forced into a HOME of its own, reached through
// the server's own ssh pool with saved locations. Skipped unless
// STUDIO_TEST_SSHD=1; the ports come from STUDIO_TEST_SSHD_PORTS (default
// 5990-5999), as in internal/userlibs.
//
//	STUDIO_TEST_SSHD=1 go test -run TestOverRealSSH ./internal/transfers/

func freePort(t *testing.T, next *int) int {
	first, last := 5990, 5999
	if v := os.Getenv("STUDIO_TEST_SSHD_PORTS"); v != "" {
		a, b, _ := strings.Cut(v, "-")
		first, _ = strconv.Atoi(a)
		last, _ = strconv.Atoi(b)
	}
	if *next < first {
		*next = first
	}
	for ; *next <= last; *next++ {
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(*next)); err == nil {
			l.Close()
			*next++
			return *next - 1
		}
	}
	t.Fatal("no free port for an sshd")
	return 0
}

func TestOverRealSSH(t *testing.T) {
	if os.Getenv("STUDIO_TEST_SSHD") != "1" {
		t.Skip("set STUDIO_TEST_SSHD=1 to start local sshd processes")
	}
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		t.Skip("no sshd here")
	}
	dir := t.TempDir()
	// Short, for ssh's control sockets.
	run, _ := os.MkdirTemp("", "trssh-")
	t.Cleanup(func() { os.RemoveAll(run) })
	for _, f := range []string{"id", "host"} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, f)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "id.pub"))
	os.WriteFile(filepath.Join(dir, "authorized_keys"), b, 0o600)
	u, _ := user.Current()
	next := 0
	// force is sshd's ForceCommand: "" for a login script that runs what it
	// is asked, else as given (%h is the host's home).
	host := func(name, banner, force string) (int, string) {
		h := filepath.Join(dir, name)
		home := filepath.Join(h, "home")
		os.MkdirAll(home, 0o755)
		login := filepath.Join(h, "login.sh")
		os.WriteFile(login, []byte("#!/bin/bash\nexport HOME="+home+"\n"+banner+"\nif [ -n \"$SSH_ORIGINAL_COMMAND\" ]; then exec bash -c \"$SSH_ORIGINAL_COMMAND\"; fi\nexec bash\n"), 0o755)
		if force == "" {
			force = login
		}
		force = strings.ReplaceAll(force, "%h", home)
		port := freePort(t, &next)
		conf := filepath.Join(h, "sshd.conf")
		os.WriteFile(conf, []byte(fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nStrictModes no\nLogLevel ERROR\nSubsystem sftp internal-sftp\nForceCommand %s\n",
			port, filepath.Join(dir, "host"), filepath.Join(h, "sshd.pid"), filepath.Join(dir, "authorized_keys"), force)), 0o644)
		d := exec.Command(sshd, "-D", "-e", "-f", conf)
		if err := d.Start(); err != nil {
			t.Fatal(err)
		}
		// Only the daemon this test started is stopped.
		t.Cleanup(func() { d.Process.Kill(); d.Wait() })
		for i := 0; i < 100; i++ {
			if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
				c.Close()
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return port, home
	}
	portA, homeA := host("host-a", "echo 'Welcome to host A'", "")
	portB, homeB := host("host-b", "", "")
	// Host C takes SFTP only, as an upload drop is set up.
	portC, homeC := host("host-c", "", "internal-sftp -d %h")
	opts := fmt.Sprintf("-i %s -o IdentitiesOnly=yes -o UserKnownHostsFile=%s -o StrictHostKeyChecking=no", filepath.Join(dir, "id"), filepath.Join(dir, "kh"))
	env := map[string]string{"XDG_RUNTIME_DIR": run, "PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")}
	pool := remote.New(remote.Options{IndexBase: filepath.Join(dir, "ix"), Getenv: func(k string) string { return env[k] }})
	t.Cleanup(pool.Close)
	runner := PoolRunner{pool}
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	ix := &recIndex{}
	base := DefaultSettings()
	set := &MemorySettings{S: base}
	set.S.SavedLocations = []SavedLocation{
		{ID: "a", Label: "Host A", Host: u.Username + "@127.0.0.1", Port: portA, SSHOptions: opts, Root: homeA},
		{ID: "b", Label: "Host B", Host: u.Username + "@127.0.0.1", Port: portB, SSHOptions: opts, Root: homeB},
		{ID: "c", Label: "Host C", Host: u.Username + "@127.0.0.1", Port: portC, SSHOptions: opts, Root: homeC},
	}
	s := New(Deps{IndexBase: filepath.Join(dir, "ix"), Getenv: func(k string) string { return env[k] }, Runner: runner, Shell: runner.RemoteShell,
		Libraries: func(context.Context, Viewer) []LibraryLoc {
			return []LibraryLoc{{ID: "kb", Label: "Knowledge base", Writable: true, Indexed: true, FS: &LocalFS{Dir: kb}}}
		},
		Settings: set, Index: ix})
	r := &rig{t: t, s: s, ix: ix}

	write(t, filepath.Join(homeA, "data", "it's $(touch pwned) .txt"), "odd")
	write(t, filepath.Join(homeA, "data", "deep", "big.bin"), strings.Repeat("x", 3<<20))
	write(t, filepath.Join(homeA, "data", ".hidden"), "h")

	// Host A to the library: host A's login prints a banner, which rsync
	// cannot pass, so the plan says so and studio-copy reads past it.
	p := r.mustPlan(single, req(ActionCopy, "saved:a", []string{"data"}, "library:kb", ""))
	if p.Mover != MoverCopy || !strings.Contains(p.Movers[1].Why, "prints text") {
		t.Fatalf("plan with a banner %+v", p.Movers)
	}
	tr := r.run(single, req(ActionCopy, "saved:a", []string{"data"}, "library:kb", ""))
	if tr.State != StateSucceeded || tr.SkippedCount != 1 {
		t.Fatalf("pull %+v", tr)
	}
	if len(read(t, filepath.Join(kb, "data", "deep", "big.bin"))) != 3<<20 || read(t, filepath.Join(kb, "data", "it's $(touch pwned) .txt")) != "odd" {
		t.Fatal("pull landed wrong")
	}
	gone(t, filepath.Join(kb, "data", ".hidden"))
	for _, h := range []string{homeA, filepath.Join(homeA, "data"), kb} {
		gone(t, filepath.Join(h, "pwned"))
	}
	// Host B's login is clean: rsync pulls from it over the real connection.
	write(t, filepath.Join(homeB, "pulled", "f.txt"), "from b")
	tr = r.run(single, req(ActionCopy, "saved:b", []string{"pulled"}, "library:kb", ""))
	if tr.State != StateSucceeded || tr.Mover != MoverRsync || read(t, filepath.Join(kb, "pulled", "f.txt")) != "from b" {
		t.Fatalf("rsync pull %+v", tr)
	}
	os.RemoveAll(filepath.Join(homeB, "pulled"))
	// The library to host B as a move: rsync pushes, sha256 is checked,
	// the originals go and the index hears.
	tr = r.run(single, req(ActionMove, "library:kb", []string{"data"}, "saved:b", ""))
	if tr.State != StateSucceeded || tr.Mover != MoverRsync {
		t.Fatalf("push %+v", tr)
	}
	gone(t, filepath.Join(kb, "data"))
	if len(ix.removed) != 2 || read(t, filepath.Join(homeB, "data", "it's $(touch pwned) .txt")) != "odd" {
		t.Fatalf("index %v", ix.removed)
	}
	// Host B to host A, the same data: two hosts with this computer's ssh
	// options, so studio-copy carries it.
	os.RemoveAll(filepath.Join(homeA, "data"))
	q := req(ActionCopy, "saved:b", []string{"data"}, "saved:a", "")
	q.Options.Checksum = yes
	tr = r.run(single, q)
	if tr.State != StateSucceeded || tr.Mover != MoverCopy {
		t.Fatalf("host to host %+v", tr)
	}
	if len(read(t, filepath.Join(homeA, "data", "deep", "big.bin"))) != 3<<20 {
		t.Fatal("host to host landed wrong")
	}
	noStaging(t, homeA)
	noStaging(t, homeB)
	// rsync named for the same, with studio-copy's reason in the plan.
	q.Options.Mover = MoverRsync
	q.Destination.Path = "data"
	if _, err := r.plan(single, q); statusOf(err) != 400 || !strings.Contains(messageOf(err), "ssh options") {
		t.Fatalf("rsync between hosts: %v", err)
	}
	// The settings test over the real connection: host B is clean and has
	// rsync; host A's login prints, which the result says.
	res, err := s.TestSettings(context.Background(), single, TestRequest{Host: u.Username + "@127.0.0.1", Port: portB, SSHOptions: opts, Root: homeB})
	if err != nil || !res.OK || res.Rsync == nil || !*res.Rsync || res.RsyncVersion == "" || res.Writable == nil || !*res.Writable || res.RoundTripMs == nil || res.Why != "" {
		t.Fatalf("test B %+v %v", res, err)
	}
	t.Logf("host B: rsync %s, round trip %d ms", res.RsyncVersion, *res.RoundTripMs)
	res, err = s.TestSettings(context.Background(), single, TestRequest{Host: u.Username + "@127.0.0.1", Port: portA, SSHOptions: opts, Root: homeA})
	if err != nil || !res.OK || !strings.Contains(res.Why, "prints text") {
		t.Fatalf("test A %+v %v", res, err)
	}
	noStaging(t, homeA)
	noStaging(t, homeB)

	// Host C takes SFTP only: browsing, a push and a move back go over SFTP
	// with the same ssh options, through studio-copy.
	write(t, filepath.Join(kb, "drop", "one.txt"), "one")
	write(t, filepath.Join(kb, "drop", "sub", "two.bin"), strings.Repeat("y", 2<<20))
	if _, err := s.Browse(context.Background(), single, "saved:c", nil); err != nil {
		t.Fatalf("browse C %v", err)
	}
	p = r.mustPlan(single, req(ActionCopy, "library:kb", []string{"drop"}, "saved:c", ""))
	if p.Mover != MoverCopy || !strings.Contains(p.Movers[1].Why, "SFTP only") {
		t.Fatalf("plan to C %+v", p.Movers)
	}
	tr = r.run(single, req(ActionCopy, "library:kb", []string{"drop"}, "saved:c", ""))
	if tr.State != StateSucceeded || len(read(t, filepath.Join(homeC, "drop", "sub", "two.bin"))) != 2<<20 {
		t.Fatalf("push to C %+v", tr)
	}
	os.RemoveAll(filepath.Join(kb, "drop"))
	q = req(ActionMove, "saved:c", []string{"drop"}, "library:kb", "")
	q.Options.Checksum = yes
	tr = r.run(single, q)
	if tr.State != StateSucceeded || read(t, filepath.Join(kb, "drop", "one.txt")) != "one" {
		t.Fatalf("move from C %+v", tr)
	}
	gone(t, filepath.Join(homeC, "drop"))
	noStaging(t, homeC)
	res, err = s.TestSettings(context.Background(), single, TestRequest{Host: u.Username + "@127.0.0.1", Port: portC, SSHOptions: opts, Root: homeC})
	if err != nil || !res.OK || !res.SFTPOnly || res.Writable == nil || !*res.Writable || res.FreeBytes == nil {
		t.Fatalf("test C %+v %v", res, err)
	}
}
