package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

// localPool runs every session on this host in bash, as the Node tests'
// setRemoteSpawn seam does; a site named "nobash*" has a login that runs
// only what it is given through sh with no bash.
func localPool(t *testing.T, env map[string]string) *Pool {
	t.Helper()
	get := func(k string) string { return env[k] }
	p := New(Options{Getenv: get, IndexBase: t.TempDir(), Spawn: func(site, key, command string) *exec.Cmd {
		if strings.Contains(site, "nobash") {
			// The forced command: only gufi_query passes; the loop is refused.
			return exec.Command("sh", "-c", `case "$0" in gufi_query*) echo "gufi_query 0.8" ;; *) echo "only gufi_query is allowed" >&2; exit 1 ;; esac`, command)
		}
		return exec.Command("bash", "-c", command)
	}})
	t.Cleanup(p.Shutdown)
	return p
}

func TestRunCarriesArgumentsUnquoted(t *testing.T) {
	p := localPool(t, nil)
	args := []string{"printf", "%s|", "a b", "it's", "$HOME", "", "x;y", "naïve\n"}
	r, err := p.Run(context.Background(), "cluster", "k", args, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// The loop decodes each argument through $(...), which drops its
	// trailing newlines, as Node's loop does.
	if want := "a b|it's|$HOME||x;y|naïve|"; r.Stdout != want || r.Code != 0 {
		t.Fatalf("got %q code %d, want %q", r.Stdout, r.Code, want)
	}
	r, err = p.Run(context.Background(), "cluster", "k", []string{"sh", "-c", "echo oops >&2; exit 3"}, 10*time.Second)
	if err != nil || r.Code != 3 || strings.TrimSpace(r.Stderr) != "oops" {
		t.Fatalf("got %+v %v", r, err)
	}
}

func TestPoolRunsSideBySide(t *testing.T) {
	p := localPool(t, nil)
	var wg sync.WaitGroup
	t0 := time.Now()
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Run(context.Background(), "c", "k", []string{"sleep", "0.5"}, 10*time.Second); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if el := time.Since(t0); el > 1400*time.Millisecond {
		t.Fatalf("three runs on three sessions took %v", el)
	}
	p.mu.Lock()
	n := len(p.channels[channelID("c", "k")])
	p.mu.Unlock()
	if n != 3 {
		t.Fatalf("%d sessions, want 3", n)
	}
}

func TestTimeoutDropsTheSession(t *testing.T) {
	p := localPool(t, nil)
	_, err := p.Run(context.Background(), "c", "k", []string{"sleep", "5"}, 300*time.Millisecond)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Status != 504 || !strings.Contains(ae.Message, "did not finish in 0 s") {
		t.Fatalf("got %v", err)
	}
	r, err := p.Run(context.Background(), "c", "k", []string{"echo", "again"}, 10*time.Second)
	if err != nil || r.Stdout != "again\n" {
		t.Fatalf("after a timeout: %+v %v", r, err)
	}
}

func TestRefusedLoopMarksTheHostDirect(t *testing.T) {
	p := localPool(t, nil)
	site := "ssh://me@nobash"
	r, err := p.Run(context.Background(), site, "", []string{"gufi_query", "--version"}, 10*time.Second)
	if err != nil || !strings.Contains(r.Stdout, "gufi_query 0.8") {
		t.Fatalf("query tool on a direct host: %+v %v", r, err)
	}
	if !p.IsDirect(site) {
		t.Fatal("not marked direct")
	}
	_, err = p.Run(context.Background(), site, "", []string{"cat", "/etc/hostname"}, 10*time.Second)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Status != 409 || ae.Message != NoShell(site) {
		t.Fatalf("got %v", err)
	}
	if n := p.CPUs(context.Background(), site, ""); n != 4 {
		t.Fatalf("cpus on a direct host %d", n)
	}
}

func TestThreadsAndCPUs(t *testing.T) {
	p := localPool(t, map[string]string{})
	n := p.CPUs(context.Background(), "c", "k")
	if n < 1 {
		t.Fatal(n)
	}
	if th := p.Threads(context.Background(), "c", "k"); th != min(n, 16) {
		t.Fatalf("threads %d cpus %d", th, n)
	}
	q := localPool(t, map[string]string{"STUDIO_REMOTE_THREADS": "300"})
	if th := q.Threads(context.Background(), "c", "k"); th != 256 {
		t.Fatal(th)
	}
}

func TestRunOnceStdinAndSSHFailure(t *testing.T) {
	p := localPool(t, nil)
	r, err := p.RunOnce(context.Background(), "c", "k", []string{"cat"}, 10*time.Second, strings.NewReader("in\n"))
	if err != nil || r.Stdout != "in\n" {
		t.Fatalf("%+v %v", r, err)
	}
	q := New(Options{Getenv: func(string) string { return "" }, Spawn: func(site, key, command string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo 'ssh: Could not resolve hostname x' >&2; exit 255")
	}})
	defer q.Shutdown()
	_, err = q.RunOnce(context.Background(), "ssh://me@x", "", []string{"true"}, 10*time.Second, nil)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.Status != 502 || ae.Message != "me@x: ssh: Could not resolve hostname x" {
		t.Fatalf("got %v", err)
	}
}

func TestExecutorThroughRunner(t *testing.T) {
	p := localPool(t, nil)
	rs := &Runners{Pool: p}
	dir := t.TempDir()
	tool := dir + "/gufi_query"
	os.WriteFile(tool, []byte("#!/bin/sh\nprintf '%s ' \"$@\"\n"), 0o755)
	res, err := rs.For("c", "k", dir).Run(context.Background(), gufi.Command{Tool: gufi.ToolQuery, Args: []string{"-n", "40", "-E", "SELECT 'a b';"}})
	if err != nil || string(res.Stdout) != "-n 40 -E SELECT 'a b'; " {
		t.Fatalf("%q %v", res.Stdout, err)
	}
}

func TestSSHSiteRoundTrip(t *testing.T) {
	site, err := SSHSite("me@box", 2222, []string{"-J", "me@jump", "-o", "ConnectTimeout=5"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseSSHSite(site)
	if err != nil || h.Destination != "me@box" || h.Port != 2222 || strings.Join(h.Options, " ") != "-J me@jump -o ConnectTimeout=5" {
		t.Fatalf("%s %+v %v", site, h, err)
	}
	if SiteName(site) != "me@box:2222" {
		t.Fatal(SiteName(site))
	}
	if _, err := SSHSite("me@box", 22, []string{"-p", "23"}); err == nil {
		t.Fatal("port given twice")
	}
	for _, bad := range []string{"-o ProxyCommand=nc", "-X", "-o BatchMode=no", "-p 0", `-i "x"`} {
		if _, err := ParseSSHOptions(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}
