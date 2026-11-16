package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// hangOn makes the stand-in gufi_query of library b run `sleep secs`
// instead of answering; the other library answers as before.
func hangOn(t *testing.T, e *Engine, secs string) {
	t.Helper()
	// scopeEngine's stand-in host is home, beside ix.
	bin := filepath.Join(filepath.Dir(e.IndexBase), "home", "gufi", "bin", "gufi_query")
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
	q := strings.Replace(fakeQuery, `lib=$(basename "$last")`, `lib=$(basename "$last")
[ "$lib" = b ] && exec sleep `+secs, 1)
	os.WriteFile(bin, []byte(q), 0o755)
}

// still says whether a process whose command line is exactly cmd runs.
func still(cmd string) bool {
	return exec.Command("pgrep", "-f", "^"+regexp.QuoteMeta(cmd)+"$").Run() == nil
}

func endsSoon(t *testing.T, cmd string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); still(cmd); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%q still runs on the host", cmd)
		}
	}
}

// A library that passes the search time limit is left out with a mark
// that links the setting, and its gufi_query is stopped on its host; the
// other library's hits stay. One library searched alone has the limit too.
func TestSearchTimeLimitStopsTheLibrary(t *testing.T) {
	e := scopeEngine(t)
	hangOn(t, e, "43.5")
	e.TimeLimitSec = func() float64 { return 2 }
	single := libraries.ViewerOf("")
	both, _, _ := e.Reg.Scope([]string{"u-a,u-b"}, single, "kb")
	om := &libraries.Omissions{}
	var slow []string
	t0 := time.Now()
	hits := e.SearchScope(context.Background(), both, "report", 10, ScopeOpts{Omitted: om, Slow: &slow})
	if took := time.Since(t0); took > 6*time.Second {
		t.Fatalf("took %v", took)
	}
	if len(hits) != 1 || hits[0].Library.ID != "u-a" {
		t.Fatalf("hits: %+v", hits)
	}
	items := om.Items()
	if len(items) != 1 || items[0].ID != "u-b" || !items[0].TimeLimit || items[0].Reason != "did not answer within 2 s" || len(slow) != 1 {
		t.Fatalf("left out: %+v %v", items, slow)
	}
	endsSoon(t, "sleep 43.5")

	one, _, _ := e.Reg.Scope([]string{"u-b"}, single, "kb")
	om = &libraries.Omissions{}
	e.SearchScope(context.Background(), one, "report", 10, ScopeOpts{Omitted: om})
	if items := om.Items(); len(items) != 1 || !items[0].TimeLimit {
		t.Fatalf("one library: %+v", items)
	}
	endsSoon(t, "sleep 43.5")
}

// With no limit a search waits; stopping it stops every library's query
// and leaves nothing out on that account.
func TestStoppedSearchStopsItsQueries(t *testing.T) {
	e := scopeEngine(t)
	hangOn(t, e, "44.5")
	e.TimeLimitSec = func() float64 { return 0 }
	if e.TimeLimitMs(ScopeOpts{}) != 0 {
		t.Fatal("0 is no limit")
	}
	single := libraries.ViewerOf("")
	one, _, _ := e.Reg.Scope([]string{"u-b"}, single, "kb")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(2*time.Second, cancel)
	om := &libraries.Omissions{}
	e.SearchScope(ctx, one, "report", 10, ScopeOpts{Omitted: om})
	if ctx.Err() == nil {
		t.Fatal("returned before it was stopped")
	}
	if items := om.Items(); len(items) != 0 {
		t.Fatalf("a stopped search left out: %+v", items)
	}
	endsSoon(t, "sleep 44.5")
}

func TestCommandBudgetFollowsTheLimit(t *testing.T) {
	bg := context.Background()
	if budget(bg, time.Minute) != time.Minute {
		t.Fatal("outside a search")
	}
	if budget(withBudget(bg, 20000), time.Minute) != time.Minute {
		t.Fatal("a short limit keeps the command's own timeout")
	}
	if budget(withBudget(bg, 600000), time.Minute) != 610*time.Second {
		t.Fatal("a long limit")
	}
	if budget(withBudget(bg, 0), time.Minute) != 24*time.Hour {
		t.Fatal("no limit")
	}
}
