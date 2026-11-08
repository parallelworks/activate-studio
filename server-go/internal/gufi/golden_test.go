package gufi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// The golden tests hold the Go port to the Node server's output for the
// cases in testdata/cases.json; scripts/golden/gufi.mjs wrote
// testdata/golden.json from server/dist.

type stageCase struct {
	Q       string        `json:"q"`
	Lim     WalkLimits    `json:"lim"`
	Roots   []string      `json:"roots"`
	Vectors []QueryVector `json:"vectors"`
	Refine  *RefineSpec   `json:"refine"`
}

type fuseCase struct {
	Caps  map[string]int `json:"caps"`
	Limit int            `json:"limit"`
	Rows  []WalkRow      `json:"rows"`
}

type cases struct {
	Now         int64             `json:"now"`
	Queries     []string          `json:"queries"`
	NameFilters []string          `json:"nameFilters"`
	Stages      []stageCase       `json:"stages"`
	Passages    []string          `json:"passages"`
	Fuse        []fuseCase        `json:"fuse"`
	Sizes       []string          `json:"sizes"`
	Whens       []string          `json:"whens"`
	Plans       []json.RawMessage `json:"plans"`
}

type golden struct {
	Queries     []json.RawMessage `json:"queries"`
	NameFilters []json.RawMessage `json:"nameFilters"`
	Stages      []json.RawMessage `json:"stages"`
	Passages    []json.RawMessage `json:"passages"`
	Fuse        []json.RawMessage `json:"fuse"`
	Sizes       []json.RawMessage `json:"sizes"`
	Whens       []json.RawMessage `json:"whens"`
	Plans       []json.RawMessage `json:"plans"`
}

func load[T any](t *testing.T, file string) T {
	t.Helper()
	var v T
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// same compares got, as JSON, with Node's want.
func same(t *testing.T, name string, got any, want json.RawMessage) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var g, w any
	json.Unmarshal(b, &g)
	json.Unmarshal(want, &w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s\n go:   %s\n node: %s", name, b, compact(want))
	}
}

func compact(b json.RawMessage) string {
	var v any
	json.Unmarshal(b, &v)
	out, _ := json.Marshal(v)
	return string(out)
}

func orNull(s string, ok bool) any {
	if !ok {
		return nil
	}
	return s
}

// outcome is {value} or {error, status} as the script records them.
func outcome(v any, err error) any {
	if err != nil {
		var e *apierr.Error
		if errors.As(err, &e) {
			return map[string]any{"error": e.Message, "status": e.Status}
		}
		return map[string]any{"error": err.Error(), "status": nil}
	}
	return map[string]any{"value": v}
}

func TestGoldenSearchParsing(t *testing.T) {
	c := load[cases](t, "testdata/cases.json")
	g := load[golden](t, "testdata/golden.json")
	for i, q := range c.Queries {
		p := ParseSearchQuery(q)
		bare, ok := FtsExprBare(q)
		names := NamesSQL(q, 20)
		same(t, fmt.Sprintf("query %q", q), map[string]any{
			"q": q, "parsed": p, "fts": FtsExprFor(p), "bare": orNull(bare, ok), "semantic": WantsSemantic(q),
			"names": orNull(names, names != ""), "rank": NameRank(p), "where": NameWhere(p),
		}, g.Queries[i])
	}
	for i, f := range c.NameFilters {
		same(t, fmt.Sprintf("name filter %q", f), map[string]any{"f": f, "groups": ParseNameFilter(f)}, g.NameFilters[i])
	}
}

func TestGoldenOnePassStages(t *testing.T) {
	c := load[cases](t, "testdata/cases.json")
	g := load[golden](t, "testdata/golden.json")
	for i, s := range c.Stages {
		same(t, fmt.Sprintf("stages %d (%q)", i, s.Q), OnePassStages(s.Q, s.Lim, s.Roots, s.Vectors, s.Refine), g.Stages[i])
	}
}

func TestGoldenPassagesAndFusion(t *testing.T) {
	c := load[cases](t, "testdata/cases.json")
	g := load[golden](t, "testdata/golden.json")
	for i, p := range c.Passages {
		same(t, fmt.Sprintf("passage %d", i), map[string]any{"snippet": PassageSnippet(p), "clean": WithoutProvenance(p)}, g.Passages[i])
	}
	for i, f := range c.Fuse {
		same(t, fmt.Sprintf("fuse %d", i), FuseRows(f.Rows, f.Caps, f.Limit), g.Fuse[i])
	}
}

func TestGoldenPlans(t *testing.T) {
	c := load[cases](t, "testdata/cases.json")
	g := load[golden](t, "testdata/golden.json")
	for i, s := range c.Sizes {
		n, err := ParseSize(s)
		same(t, fmt.Sprintf("size %q", s), outcome(n, err), g.Sizes[i])
	}
	for i, s := range c.Whens {
		start, end, err := ParseWhen(s, c.Now)
		same(t, fmt.Sprintf("when %q", s), outcome(map[string]float64{"start": start, "end": end}, err), g.Whens[i])
	}
	p := &Planner{IndexRoot: "/golden/ix/gufi/kb", Now: func() int64 { return c.Now }}
	for i, body := range c.Plans {
		plan, err := p.PlanFor(body)
		var v any
		if err == nil {
			var cmd string
			cmd, err = p.Command("/golden/gufi/bin", plan)
			v = map[string]any{"stages": plan.List(), "commands": []map[string]string{{"engine": "gufi_query", "command": cmd}}}
		}
		same(t, fmt.Sprintf("plan %s", body), outcome(v, err), g.Plans[i])
	}
}

// TestGoldenIsCurrent regenerates the golden file from the Node server
// when STUDIO_GOLDEN_NODE names a node binary, and fails when it has
// drifted from the committed one.
func TestGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../scripts/golden/gufi.mjs", "testdata/cases.json", "../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/golden.json")
	var a, b any
	json.Unmarshal(out, &a)
	json.Unmarshal(want, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Node's output changed: regenerate testdata/golden.json")
	}
}
