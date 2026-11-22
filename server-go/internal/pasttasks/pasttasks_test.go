package pasttasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

func seed(t *testing.T) *Archive {
	dir := t.TempDir()
	put := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	put("2026-11-01-bad-ef56/state.json", `{"id":"2026-11-01-bad-ef56","objective":"Failed work","model":"m","state":"completed","createdAt":"2026-10-31T00:00:00.000Z","agents":[{"name":"w1","state":"failed","note":"no data"}]}`)
	put("2026-11-01-old-ab12/state.json", `{"id":"2026-11-01-old-ab12","objective":"Old work","model":"m","state":"completed","createdAt":"2026-11-01T00:00:00.000Z","usage":{"total":10},"execution":"local","runtime":"runner","agents":[{"name":"w1","state":"completed","resultPath":"tasks/2026-11-01-old-ab12/w1.md"}]}`)
	put("2026-11-02-live-cd34/state.json", `{"id":"2026-11-02-live-cd34","objective":"Was running","model":"m","state":"working","createdAt":"2026-11-02T00:00:00.000Z","agents":[{"name":"w1","state":"working","note":"tick","approvals":[{"id":"a","text":"rm"}]},{"name":"w2","state":"completed"}]}`)
	put("2026-11-02-live-cd34/board.jsonl", `{"seq":1,"at":"x","from":"orchestrator","topic":"task","body":"start"}`+"\n"+`{"seq":2,"at":"y","from":"w1","topic":"status","body":"half"}`+"\n")
	put("2026-11-02-live-cd34/work/w1/live.log", strings.Repeat("a", 20000)+"END")
	put("not-a-task/state.json", `{"id":"something-else"}`)
	put("broken/state.json", `{`)
	return &Archive{Dir: dir}
}

func TestListAndTask(t *testing.T) {
	a := seed(t)
	list := a.List()
	if len(list) != 3 {
		t.Fatalf("list %v", list)
	}
	first := jsonv.Obj(list[0])
	if prop(first, "id") != "2026-11-02-live-cd34" || prop(first, "state") != "canceled" || prop(first, "running") != 0 || prop(first, "agents") != 2 ||
		prop(first, "done") != 1 || prop(first, "stopped") != 1 || prop(first, "failed") != 0 {
		t.Fatalf("newest first, stopped: %v", first)
	}
	// A task whose only agent failed is not counted done.
	if bad := jsonv.Obj(list[2]); prop(bad, "id") != "2026-11-01-bad-ef56" || prop(bad, "done") != 0 || prop(bad, "failed") != 1 {
		t.Fatalf("failed task: %v", bad)
	}
	task := a.Task("2026-11-02-live-cd34")
	w1 := jsonv.Obj(jsonv.Arr(prop(task, "agents"))[0])
	if prop(w1, "state") != "canceled" || prop(w1, "note") != Stopped || prop(w1, "approvals") != nil {
		t.Fatalf("agent %v", w1)
	}
	if b := a.Board("2026-11-02-live-cd34", 1); len(b) != 1 || prop(jsonv.Obj(b[0]), "body") != "half" {
		t.Fatalf("board %v", b)
	}
	if a.Task("../etc") != nil || a.Task("not-a-task") != nil || a.Task("broken") != nil {
		t.Fatal("a bad id was read")
	}
}

func TestOutput(t *testing.T) {
	a := seed(t)
	tail, ok := a.Output("2026-11-02-live-cd34", "w1")
	if !ok || len(tail) != 16*1024 || !strings.HasSuffix(tail, "END") {
		t.Fatalf("tail %d %v", len(tail), ok)
	}
	if tail, ok := a.Output("2026-11-02-live-cd34", "w2"); !ok || tail != "" {
		t.Fatal("an agent without output")
	}
	if _, ok := a.Output("2026-11-02-live-cd34", "../w1"); ok {
		t.Fatal("a bad agent name was read")
	}
	if _, ok := a.Output("2026-11-02-live-cd34", "w9"); ok {
		t.Fatal("an unknown agent was read")
	}
}
