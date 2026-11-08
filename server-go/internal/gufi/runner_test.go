package gufi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// gate is an Executor whose runs wait until released, recording the
// order they start in.
type gate struct {
	mu      sync.Mutex
	started []string
	release chan struct{}
}

func (g *gate) Exec(ctx context.Context, c Command) (Result, error) {
	g.mu.Lock()
	g.started = append(g.started, strings.Join(c.Args, " "))
	g.mu.Unlock()
	select {
	case <-g.release:
		return Result{Stdout: []byte("ok\n")}, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (g *gate) order() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.started)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunnerAdmitsInArrivalOrder(t *testing.T) {
	g := &gate{release: make(chan struct{})}
	r := NewRunner(g, nil, RunnerOptions{Concurrency: 2, QueueMax: 10})
	var wg sync.WaitGroup
	run := func(id string) {
		defer wg.Done()
		if _, err := r.Run(context.Background(), Command{Tool: ToolQuery, Args: []string{id}}); err != nil {
			t.Error(err)
		}
	}
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		wg.Add(1)
		go run(id)
		// Each arrives after the one before is running or queued.
		waitFor(t, id, func() bool { s := r.Stats(); return s.Running+s.Queued == i+1 })
	}
	if s := r.Stats(); s.Running != 2 || s.Queued != 3 || s.MaxQueued != 3 {
		t.Fatalf("stats %+v", s)
	}
	// One slot frees at a time, so the next run to start is the queue's
	// first.
	for i := range 3 {
		g.release <- struct{}{}
		waitFor(t, "next start", func() bool { return len(g.order()) == 3+i })
	}
	g.release <- struct{}{}
	g.release <- struct{}{}
	wg.Wait()
	if got := g.order(); !slices.Equal(got, []string{"a", "b", "c", "d", "e"}) && !slices.Equal(got, []string{"b", "a", "c", "d", "e"}) {
		t.Fatalf("order %v", got)
	}
	if s := r.Stats(); s.Runs != 5 || s.Running != 0 || s.Queued != 0 || s.MaxWaitMS <= 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestRunnerRefusesPastTheQueueAndLeavesItOnCancel(t *testing.T) {
	g := &gate{release: make(chan struct{})}
	r := NewRunner(g, nil, RunnerOptions{Concurrency: 1, QueueMax: 1})
	go r.Run(context.Background(), Command{Tool: ToolQuery, Args: []string{"running"}})
	waitFor(t, "running", func() bool { return r.Stats().Running == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := r.Run(ctx, Command{Tool: ToolQuery, Args: []string{"queued"}}); errc <- err }()
	waitFor(t, "queued", func() bool { return r.Stats().Queued == 1 })
	if _, err := r.Run(context.Background(), Command{Tool: ToolQuery}); !errors.Is(err, ErrBusy) {
		t.Fatalf("full queue: %v", err)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled while queued: %v", err)
	}
	if s := r.Stats(); s.Queued != 0 || s.Refused != 1 || s.Cancelled != 1 {
		t.Fatalf("stats %+v", s)
	}
	g.release <- struct{}{}
	waitFor(t, "idle", func() bool { return r.Stats().Running == 0 })
	if got := g.order(); !slices.Equal(got, []string{"running"}) {
		t.Fatalf("a cancelled run started: %v", got)
	}
	// The slot is free again.
	go func() { g.release <- struct{}{} }()
	if _, err := r.Run(context.Background(), Command{Tool: ToolQuery, Args: []string{"next"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerCapsThreads(t *testing.T) {
	r := NewRunner(nil, nil, RunnerOptions{Threads: 4})
	if got := r.capThreads([]string{"-n", "16", "-d", "x", "-n", "2"}); !slices.Equal(got, []string{"-n", "4", "-d", "x", "-n", "2"}) {
		t.Fatal(got)
	}
	if r := NewRunner(nil, nil, RunnerOptions{}); r.concurrency != 2 || r.queueMax != 256 || r.timeout != 60*time.Second {
		t.Fatal("defaults")
	}
}

// fakeGufi is a bin directory whose gufi_query is a shell script.
func fakeGufi(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, ToolQuery), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestLocalExecutorKillsOnTimeoutAndCancel(t *testing.T) {
	bin := fakeGufi(t, `echo partial; echo "$GUFI_BIN" >&2; exec sleep 30`)
	l := NewLocal(bin)
	t0 := time.Now()
	res, err := l.Exec(context.Background(), Command{Tool: ToolQuery, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) || time.Since(t0) > 5*time.Second {
		t.Fatalf("timeout: %v after %v", err, time.Since(t0))
	}
	if string(res.Stdout) != "partial\n" || strings.TrimSpace(res.Stderr) != bin || res.Signal == "" {
		t.Fatalf("result %+v", res)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := l.Exec(ctx, Command{Tool: ToolQuery}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := l.Exec(context.Background(), Command{Tool: "rm"}); err == nil {
		t.Fatal("ran a tool that is not GUFI's")
	}
}

func TestLocalExecutorExitAndStderrTail(t *testing.T) {
	bin := fakeGufi(t, `cat; i=0; while [ $i -lt 3000 ]; do echo "Error: line $i" >&2; i=$((i+1)); done; exit 3`)
	res, err := NewLocal(bin).Exec(context.Background(), Command{Tool: ToolQuery, Stdin: []byte("in\n")})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || string(res.Stdout) != "in\n" || !res.StderrCut || len(res.Stderr) != StderrTail || !strings.HasSuffix(res.Stderr, "line 2999\n") {
		t.Fatalf("exit %d out %q cut %v len %d", res.ExitCode, res.Stdout, res.StderrCut, len(res.Stderr))
	}
	l := &LocalExecutor{Bin: fakeGufi(t, `yes row | head -c 100000`), MaxStdout: 1000}
	if _, err := l.Exec(context.Background(), Command{Tool: ToolQuery}); err == nil || !strings.Contains(err.Error(), "output passed") {
		t.Fatalf("max stdout: %v", err)
	}
}

func TestPlannerQueryErrors(t *testing.T) {
	p := &Planner{IndexRoot: t.TempDir()}
	plan, _ := RawPlan("SELECT 1", "")
	bad := NewRunner(NewLocal(fakeGufi(t, `echo "Error: no such table: x: /i/x/db.db: \"SELECT 1;\"" >&2`)), nil, RunnerOptions{})
	if _, err := p.Query(context.Background(), bad, plan); err == nil || err.Error() != `Error: no such table: x: "SELECT 1;"` {
		t.Fatalf("raw error: %v", err)
	}
	failed := NewRunner(NewLocal(fakeGufi(t, `echo boom >&2; exit 1`)), nil, RunnerOptions{})
	agg, _ := (&Planner{}).Canned("largest", CannedParams{N: 5})
	if _, err := p.Query(context.Background(), failed, agg); err == nil || err.Error() != "boom\n" {
		t.Fatalf("failed run: %v", err)
	}
	partial := NewRunner(NewLocal(fakeGufi(t, `printf 'a\0361\0362\n'; echo "Error: in one directory" >&2; exit 1`)), nil, RunnerOptions{})
	res, err := p.Query(context.Background(), partial, agg)
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] != "a" {
		t.Fatalf("partial: %v %v", res, err)
	}
	if _, err := p.Query(context.Background(), partial, Plan{Subtree: "../x"}); err == nil {
		t.Fatal("escaping subtree")
	}
}
