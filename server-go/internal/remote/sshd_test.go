package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestStopOverRealSSH stops a command over a real ssh session, to a
// user-run sshd on a loopback port, and finds it gone on that host: the
// session's end reaches the loop as the end of its input. Skipped unless
// STUDIO_TEST_SSHD=1 (port STUDIO_TEST_SSHD_PORT, default 5989).
//
//	STUDIO_TEST_SSHD=1 go test -run TestStopOverRealSSH ./internal/remote/
func TestStopOverRealSSH(t *testing.T) {
	if os.Getenv("STUDIO_TEST_SSHD") != "1" {
		t.Skip("set STUDIO_TEST_SSHD=1 to start a local sshd")
	}
	sshd := ""
	for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
		if _, err := os.Stat(p); err == nil {
			sshd = p
		}
	}
	if sshd == "" {
		t.Skip("no sshd on this host")
	}
	dir := t.TempDir()
	for _, f := range []string{"id", "host"} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, f)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	pub, _ := os.ReadFile(filepath.Join(dir, "id.pub"))
	os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600)
	port := 5989
	if v := os.Getenv("STUDIO_TEST_SSHD_PORT"); v != "" {
		port, _ = strconv.Atoi(v)
	}
	conf := filepath.Join(dir, "sshd.conf")
	os.WriteFile(conf, []byte(fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nStrictModes no\nLogLevel ERROR\n",
		port, filepath.Join(dir, "host"), filepath.Join(dir, "sshd.pid"), filepath.Join(dir, "authorized_keys"))), 0o600)
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
	run, err := os.MkdirTemp("", "rmssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	env := map[string]string{"XDG_RUNTIME_DIR": run, "HOME": os.Getenv("HOME"), "PATH": os.Getenv("PATH")}
	p := New(Options{IndexBase: dir, Getenv: func(k string) string { return env[k] }})
	t.Cleanup(p.Close)
	opts := []string{"-i", filepath.Join(dir, "id"), "-o", "IdentitiesOnly=yes", "-o", "UserKnownHostsFile=" + filepath.Join(dir, "kh"), "-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=10"}
	user := os.Getenv("USER")
	site, err := SSHSite(user+"@127.0.0.1", port, opts)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := p.Run(context.Background(), site, "k", []string{"echo", "up"}, 30*time.Second); err != nil || strings.TrimSpace(r.Stdout) != "up" {
		t.Fatalf("first run: %+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(2*time.Second, cancel)
	if _, err := p.Run(ctx, site, "k", []string{"sleep", "45.25"}, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped: %v", err)
	}
	gone(t, "sleep 45.25")
	if r, err := p.Run(context.Background(), site, "k", []string{"echo", "after"}, 30*time.Second); err != nil || strings.TrimSpace(r.Stdout) != "after" {
		t.Fatalf("after a stop: %+v %v", r, err)
	}
	if _, err := p.Run(context.Background(), site, "k", []string{"sleep", "46.75"}, 2*time.Second); err == nil {
		t.Fatal("no timeout")
	}
	gone(t, "sleep 46.75")
}
