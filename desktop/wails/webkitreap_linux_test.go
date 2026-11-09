//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadStatTakesCommToTheLastParen(t *testing.T) {
	f := filepath.Join(t.TempDir(), "stat")
	os.WriteFile(f, []byte("4242 (WebKit (Web) Process) S 17 4242 4242 0 -1\n"), 0o644)
	pid, comm, state, ppid, ok := readStat(f)
	if !ok || pid != 4242 || comm != "WebKit (Web) Process" || state != "S" || ppid != 17 {
		t.Fatalf("%d %q %q %d %v", pid, comm, state, ppid, ok)
	}
}

// A child named WebKit... that does not end by itself is stopped within the
// bound; other children are left alone.
func TestReapWebKitStopsLeftoverHelpers(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "WebKitWebProcess")
	if err := os.Symlink("/bin/sleep", helper); err != nil {
		t.Skip(err)
	}
	wk := exec.Command(helper, "30")
	other := exec.Command("/bin/sleep", "30")
	if err := wk.Start(); err != nil {
		t.Fatal(err)
	}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Process.Kill()
	go wk.Wait()
	time.Sleep(100 * time.Millisecond)
	if n := webKitChildren(os.Getpid()); len(n) != 1 || n[0] != wk.Process.Pid {
		t.Fatalf("children named WebKit: %v", n)
	}
	start := time.Now()
	reapWebKit(300 * time.Millisecond)
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("took %v", el)
	}
	if n := webKitChildren(os.Getpid()); len(n) != 0 {
		t.Fatalf("left: %v", n)
	}
	if err := syscall.Kill(other.Process.Pid, 0); err != nil {
		t.Fatal("another child was stopped:", err)
	}
}
