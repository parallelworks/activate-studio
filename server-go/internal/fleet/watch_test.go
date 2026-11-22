package fleet

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An agent that watches a folder holding its own (the results reviewer
// watches agents/) is not woken again by what its round wrote, there or
// beside the results it read; a file someone writes after the round
// wakes it.
func TestAWatcherIsNotWokenByItsOwnRound(t *testing.T) {
	f, d, root := stand(t)
	os.MkdirAll(filepath.Join(root, "agents", "runs"), 0o755)
	os.WriteFile(filepath.Join(root, "agents", "runs", "result.csv"), []byte("1"), 0o644)
	watch := "agents"
	a, err := f.Create(Input{Name: "Results reviewer", Goal: "Summarize new results.", Every: "manual", WatchPath: watch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ticks := func() float64 { return f.Get(a.ID).Ticks }
	if !f.Tick(a.ID, "first", true) {
		t.Fatal("no wake-up")
	}
	waitFor(t, "the session", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.agents[a.ID].Session != "" && !f.busy[a.ID] })
	// Its round writes in its own folder and beside the results.
	later := time.Now().Add(2 * time.Second)
	for _, p := range []string{filepath.Join(root, a.Folder, "notes.md"), filepath.Join(root, "agents", "runs", "summary.md")} {
		os.WriteFile(p, []byte("x"), 0o644)
		os.Chtimes(p, later, later)
	}
	d.finish(f.Get(a.ID).Session, "Summarized one run.", 10)
	f.follow(context.Background())
	if s := f.state(a.ID); s != "idle" {
		t.Fatalf("after the round: %s", s)
	}
	f.Pass(context.Background())
	if ticks() != 1 {
		t.Fatalf("its own round woke it again: %v wake-ups", ticks())
	}
	// Its own folder never wakes it, even after the round.
	evenLater := later.Add(2 * time.Second)
	own := filepath.Join(root, a.Folder, "more.md")
	os.WriteFile(own, []byte("x"), 0o644)
	os.Chtimes(own, evenLater, evenLater)
	f.Pass(context.Background())
	if ticks() != 1 {
		t.Fatalf("its own folder woke it: %v wake-ups", ticks())
	}
	// Someone else's new result does.
	other := filepath.Join(root, "agents", "runs", "result2.csv")
	os.WriteFile(other, []byte("2"), 0o644)
	os.Chtimes(other, evenLater.Add(time.Second), evenLater.Add(time.Second))
	f.Pass(context.Background())
	if ticks() != 2 {
		t.Fatalf("a new result did not wake it: %v wake-ups", ticks())
	}
	waitFor(t, "the second wake-up", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.busy[a.ID] })
}
