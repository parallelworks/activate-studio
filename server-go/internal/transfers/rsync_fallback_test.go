package transfers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRsyncThatCannotConnectFallsBack: rsync whose remote shell closes at
// once (here a stand-in for pw ssh that refuses the session, as the plan's
// own checks over the pool did not) fails before any byte moves. The
// transfer goes on with studio-copy, says so in its events with what the
// remote shell said, and lands every file.
func TestRsyncThatCannotConnectFallsBack(t *testing.T) {
	if !runnable("/usr/bin/rsync") {
		t.Skip("no rsync")
	}
	r := newRig(t)
	bad := filepath.Join(r.dir, "refusing-shell")
	os.WriteFile(bad, []byte("#!/bin/sh\necho 'A new version of the PW CLI is available' >&2\necho 'pw ssh: session refused: Unauthorized' >&2\nexit 255\n"), 0o755)
	r.s.d.Shell = func(site, key string) ([]string, []string, error) { return []string{bad}, nil, nil }
	write(t, filepath.Join(r.kb, "papers", "p1.md"), "one")
	write(t, filepath.Join(r.kb, "papers", "deep", "p2.md"), "two")

	q := req(ActionMove, "library:kb", []string{"papers"}, "saved:b", "")
	p := r.mustPlan(single, q)
	if p.Mover != MoverRsync {
		// An rsync this system ships that Studio does not use for a send.
		t.Skipf("the plan chose %s: %+v", p.Mover, p.Movers)
	}
	tr, err := r.s.Start(context.Background(), single, p.ID, ByUser, "")
	if err != nil {
		t.Fatal(err)
	}
	tr = r.wait(single, tr.ID)
	if tr.State != StateSucceeded || tr.Mover != MoverCopy {
		t.Fatalf("transfer %+v", tr)
	}
	if read(t, filepath.Join(r.hostB, "papers", "deep", "p2.md")) != "two" {
		t.Fatal("not landed")
	}
	gone(t, filepath.Join(r.kb, "papers"))
	noStaging(t, r.hostB)
	var said string
	for _, e := range tr.Events {
		if strings.Contains(e.Text, "failed before any data moved") {
			said = e.Text
		}
	}
	if !strings.Contains(said, "continuing with studio-copy") || !strings.Contains(said, "Unauthorized") || strings.Contains(said, "new version") {
		t.Fatalf("events %v", tr.Events)
	}
}

// TestRsyncFailureSaysWhatTheRemoteShellSaid: the message keeps rsync's
// own line and adds the remote shell's last lines, without the CLI's
// update notice.
func TestRsyncFailureSaysWhatTheRemoteShellSaid(t *testing.T) {
	err := rsyncFailedWith("rsync: connection unexpectedly closed (0 bytes received so far) [sender]\nrsync error: error in rsync protocol data stream (code 12)\n",
		"\nA new version of the PW CLI is available (current: v1, latest: v2).\nRun 'pw upgrade' to update.\nError: no such cluster\n", 12)
	if got := err.Error(); got != "rsync: connection unexpectedly closed (0 bytes received so far) [sender]; the remote shell said: Error: no such cluster" {
		t.Fatalf("%q", got)
	}
	if got := rsyncFailedWith("rsync: oops\n", "", 1).Error(); got != "rsync: oops" {
		t.Fatalf("%q", got)
	}
}

// TestRshRunsThroughSh: the remote shell script is run by sh, so a
// temporary folder mounted noexec does not stop it, and a folder with a
// space stays one word for rsync.
func TestRshRunsThroughSh(t *testing.T) {
	if got := rshQuote("/tmp/a b/rsh"); got != "'/tmp/a b/rsh'" {
		t.Fatalf("%q", got)
	}
	if got := rshQuote("/tmp/x/rsh"); got != "/tmp/x/rsh" {
		t.Fatalf("%q", got)
	}
	c := rsyncCmd{kind: "push", program: "rsync", rsh: "sh " + rshQuote("/tmp/x/rsh"), from: "/a/", to: "/b/"}
	argv := strings.Join(c.argv(), " ")
	if !strings.Contains(argv, "--rsh=sh /tmp/x/rsh") {
		t.Fatalf("%s", argv)
	}
}
