//go:build !windows

package main

// The supervisor against a stand-in server: this test binary, started
// through a run script the way the payload's studio-run.sh --once is. The
// stand-in answers /api/version, starts a helper process the way a build or
// an indexer would, and exits with the status written to <data>/trigger.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Linux: the supervisor starts its child through this executable's guard.
	if maybeRunGuard() {
		return
	}
	if os.Getenv("STUDIO_FAKE_SERVER") == "1" {
		fakeServer()
		return
	}
	os.Exit(m.Run())
}

func fakeServer() {
	data := os.Getenv("STUDIO_DATA_DIR")
	port := os.Getenv("STUDIO_RUN_PORT")
	helper := exec.Command("sleep", "300")
	if err := helper.Start(); err == nil {
		_ = os.WriteFile(filepath.Join(data, "helper.pid"), []byte(strconv.Itoa(helper.Process.Pid)), 0o644)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(9)
	}
	go func() {
		_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"version":"fake"}`) }))
	}()
	trigger := filepath.Join(data, "trigger")
	for {
		if b, err := os.ReadFile(trigger); err == nil {
			code, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			_ = os.Remove(trigger)
			os.Exit(code)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type harness struct {
	t      *testing.T
	sup    *Supervisor
	data   string
	events chan ServerEvent
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	data := t.TempDir()
	script := filepath.Join(t.TempDir(), "studio-run.sh")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// STUDIO_DATA_DIR is what studio-run.sh gives the server.
	body := fmt.Sprintf(`#!/bin/bash
if [ "$1" = --rollback ]; then echo "$3" >"$2/rolled-back"; rm -f "$2/updates/pending.json"; exit 0; fi
[ "$1" = --once ] || exit 64
exec env STUDIO_FAKE_SERVER=1 STUDIO_DATA_DIR="$2" %q
`, exe)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, data: data, events: make(chan ServerEvent, 64)}
	h.sup = &Supervisor{RunScript: script, DataDir: data, Pause: 100 * time.Millisecond, Events: func(e ServerEvent) { h.events <- e }}
	go h.sup.Run()
	t.Cleanup(h.sup.Stop)
	return h
}

// next waits for an event of kind.
func (h *harness) next(kind string) ServerEvent {
	h.t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case e := <-h.events:
			if e.Kind == kind {
				return e
			}
		case <-deadline:
			h.t.Fatalf("no %q event", kind)
		}
	}
}

func (h *harness) trigger(code int) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.data, "trigger"), []byte(strconv.Itoa(code)), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// A zombie is gone for our purposes.
		f := strings.Fields(string(b))
		return len(f) > 2 && f[2] != "Z"
	}
	return syscall.Kill(pid, 0) == nil
}

func (h *harness) helperPID() int {
	h.t.Helper()
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(filepath.Join(h.data, "helper.pid")); err == nil {
			p, _ := strconv.Atoi(string(b))
			if p > 0 {
				return p
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatal("the stand-in did not start its helper")
	return 0
}

func TestStartsAndStops(t *testing.T) {
	h := newHarness(t)
	if e := h.next("ready"); e.Reason != "launch" || e.Port < 1024 {
		t.Fatalf("ready event %+v", e)
	}
	helper := h.helperPID()
	h.sup.Stop()
	time.Sleep(300 * time.Millisecond)
	if alive(helper) {
		t.Error("the server's helper process was left running after Stop")
	}
	if !portFree(h.sup.Port()) {
		t.Error("the server's port is still in use after Stop")
	}
}

func TestRestartsAfter75And76(t *testing.T) {
	h := newHarness(t)
	first := h.next("ready")
	for _, c := range []struct {
		code   int
		reason string
	}{{75, "exit-75"}, {76, "exit-76"}} {
		oldHelper := h.helperPID()
		_ = os.Remove(filepath.Join(h.data, "helper.pid"))
		h.trigger(c.code)
		e := h.next("ready")
		if e.Reason != c.reason {
			t.Errorf("after exit %d: reason %q, want %q", c.code, e.Reason, c.reason)
		}
		if e.Port != first.Port {
			t.Errorf("after exit %d: port %d, was %d", c.code, e.Port, first.Port)
		}
		if alive(oldHelper) {
			t.Errorf("after exit %d: the old server's helper is still running", c.code)
		}
	}
}

func TestOtherExitFailsUntilRetry(t *testing.T) {
	h := newHarness(t)
	h.next("ready")
	h.trigger(3)
	f := h.next("failed")
	if !strings.Contains(f.Message, "exit status 3") {
		t.Errorf("failed message %q", f.Message)
	}
	h.sup.Retry()
	if e := h.next("ready"); e.Reason != "retry" {
		t.Errorf("reason %q after Try Again", e.Reason)
	}
}

func TestRelaunchRestarts(t *testing.T) {
	h := newHarness(t)
	h.next("ready")
	h.sup.Restart("relaunch")
	if e := h.next("ready"); e.Reason != "relaunch" {
		t.Errorf("reason %q", e.Reason)
	}
}

func TestStudioEnvPortWins(t *testing.T) {
	data := t.TempDir()
	port := freePort()
	if err := os.WriteFile(filepath.Join(data, "studio.env"), []byte(fmt.Sprintf("PORT='%d'\n", port)), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{DataDir: data}
	if got := s.choosePort(readSettings(filepath.Join(data, "studio.env"))); got != port {
		t.Errorf("port %d, studio.env says %d", got, port)
	}
}

func TestPortIsRemembered(t *testing.T) {
	data := t.TempDir()
	s := &Supervisor{DataDir: data}
	first := s.choosePort(map[string]string{})
	again := (&Supervisor{DataDir: data}).choosePort(map[string]string{})
	if first != again {
		t.Errorf("the port changed between launches: %d then %d", first, again)
	}
}

func TestReadSettingsUnquotes(t *testing.T) {
	f := filepath.Join(t.TempDir(), "studio.env")
	_ = os.WriteFile(f, []byte("A='x'\\''y'\n# c\nB=plain\n"), 0o644)
	m := readSettings(f)
	if m["A"] != "x'y" || m["B"] != "plain" {
		t.Errorf("settings %v", m)
	}
}

func TestRollsBackAfterThreeStopsAfterAnUpdate(t *testing.T) {
	h := newHarness(t)
	h.next("ready")
	if err := os.MkdirAll(filepath.Join(h.data, "updates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.data, "updates", "pending.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two stops: started again quietly, no failure shown.
	for i := 0; i < 2; i++ {
		h.trigger(3)
		if e := h.next("ready"); e.Reason != "after-update" {
			t.Fatalf("stop %d: reason %q", i+1, e.Reason)
		}
	}
	// The third: the run script's rollback runs, and the server starts again.
	h.trigger(3)
	if e := h.next("ready"); e.Reason != "rollback" {
		t.Fatalf("third stop: reason %q", e.Reason)
	}
	if b, err := os.ReadFile(filepath.Join(h.data, "rolled-back")); err != nil || strings.TrimSpace(string(b)) != "3" {
		t.Errorf("the rollback was not asked for with the count: %q, %v", b, err)
	}
}

// A payload cut off between the two moves of an update is put back before the
// server starts: the staged copy when the swap file vouches for a complete
// one, the previous payload otherwise.
func TestRecoversAPayloadCutOffBetweenTheMoves(t *testing.T) {
	recoverSrc, err := os.ReadFile(filepath.Join("..", "..", "deploy", "linux", "studio-recover.sh"))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := fmt.Sprintf(`#!/bin/bash
[ "$1" = --once ] || exit 64
exec env STUDIO_FAKE_SERVER=1 STUDIO_DATA_DIR="$2" %q
`, exe)
	// A payload that studio-recover.sh counts as complete, at version v2.
	payload := func(dir, version string) {
		for f, mode := range map[string]os.FileMode{"studio-run.sh": 0o755, "studio-recover.sh": 0o755, "node/bin/node": 0o755, "app/server/dist/main.js": 0o644, "update-signing.pub": 0o644, "manifest.json": 0o644} {
			body := []byte("x\n")
			switch f {
			case "studio-run.sh":
				body = []byte(run)
			case "studio-recover.sh":
				body = recoverSrc
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), body, mode); err != nil {
				t.Fatal(err)
			}
		}
		info := fmt.Sprintf(`{"version": "%s"}`, version)
		if err := os.WriteFile(filepath.Join(dir, "app", "build-info.json"), []byte(info), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name    string
		swap    bool
		partial bool
		want    string
	}{
		{"the staged copy is complete", true, false, "v2"},
		{"the staged copy is partial", true, true, "v1"},
		{"there is no swap file", false, false, "v1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			lib := t.TempDir()
			data := t.TempDir()
			payload(filepath.Join(lib, ".activate-studio.previous"), "v1")
			payload(filepath.Join(lib, ".activate-studio.new"), "v2")
			if c.partial {
				_ = os.Remove(filepath.Join(lib, ".activate-studio.new", "node", "bin", "node"))
			}
			if c.swap {
				if err := os.MkdirAll(filepath.Join(data, "updates"), 0o755); err != nil {
					t.Fatal(err)
				}
				swap := `{"from": "v1", "to": "v2", "at": "2026-11-04T00:00:00Z", "reopen": "no"}`
				if err := os.WriteFile(filepath.Join(data, "updates", "swap.json"), []byte(swap), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			events := make(chan ServerEvent, 64)
			t.Setenv("STUDIO_RECOVER_WAIT", "0")
			sup := &Supervisor{RunScript: filepath.Join(lib, "activate-studio", "studio-run.sh"), DataDir: data, Pause: 100 * time.Millisecond, Events: func(e ServerEvent) { events <- e }}
			go sup.Run()
			t.Cleanup(sup.Stop)
			deadline := time.After(20 * time.Second)
			for ready := false; !ready; {
				select {
				case e := <-events:
					ready = e.Kind == "ready"
				case <-deadline:
					t.Fatal("the server did not start from the recovered payload")
				}
			}
			b, err := os.ReadFile(filepath.Join(lib, "activate-studio", "app", "build-info.json"))
			if err != nil || !strings.Contains(string(b), c.want) {
				t.Errorf("the payload is %q (%v), want %s", b, err, c.want)
			}
		})
	}
}

func TestNoRollbackWithoutAnUpdate(t *testing.T) {
	h := newHarness(t)
	h.next("ready")
	h.trigger(3)
	h.next("failed")
}
