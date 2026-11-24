package search

import (
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// A file of a library on another system with no text in its index says
// why when the index, not the file, is the reason: the library is
// indexing, or the file changed after its last pass.
func TestAFileWithoutIndexedTextSaysWhy(t *testing.T) {
	file := func(mtime int64) *kb.Obj { return kb.O("path", "a.xlsx", "mtime", mtime, "content", nil) }
	built := "2026-11-23T10:00:00Z" // 1795428000
	state := func(n *kb.Obj) string {
		if n == nil {
			return ""
		}
		v, _ := n.Get("state")
		return v.(string)
	}
	ready := &libraries.Target{Def: libraries.UserDef{State: "ready"}, BuiltAt: built}
	if got := state(indexNote(ready, "remote://x", file(1795427000))); got != "" {
		t.Errorf("indexed before the pass and without text: %q", got)
	}
	n := indexNote(ready, "remote://x", file(1795429000))
	if at, _ := n.Get("indexedAt"); state(n) != "newer" || at != built {
		t.Errorf("changed after the pass: %v", n)
	}
	for _, st := range []string{"building", "updating"} {
		if got := state(indexNote(&libraries.Target{Def: libraries.UserDef{State: st}, BuiltAt: built}, "remote://x", file(1795427000))); got != "indexing" {
			t.Errorf("%s: %q", st, got)
		}
	}
	IndexQueued = func(root string) bool { return root == "remote://x" }
	t.Cleanup(func() { IndexQueued = nil })
	if got := state(indexNote(ready, "remote://x", file(1795429000))); got != "indexing" {
		t.Errorf("a build waiting to start: %q", got)
	}
	if got := state(indexNote(&libraries.Target{Def: libraries.UserDef{State: "ready"}}, "remote://y", file(1795429000))); got != "" {
		t.Errorf("never built, or its time unknown: %q", got)
	}
}
