package wfparser

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

func attempt(f func() (any, error)) *jsonv.Object {
	v, err := f()
	if err != nil {
		return jsonv.NewObject("error", err.Error())
	}
	return jsonv.NewObject("out", v)
}

// results computes, in Go, what scripts/golden/wfparser.mjs prints.
func results(t *testing.T) *jsonv.Object {
	t.Helper()
	b, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, _ := jsonv.Decode(b)
	var docs []any
	for _, c := range jsonv.Arr(jsonv.Path(cases, "docs")) {
		file := jsonv.Str(jsonv.Path(c, "file"))
		text, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonv.ParseYAML(string(text))
		if err != nil {
			t.Fatal(file, err)
		}
		jobs := jsonv.Obj(jsonv.Path(doc, "jobs"))
		if jobs == nil {
			jobs = jsonv.NewObject()
		}
		var dumpIn any = doc
		if doc == nil {
			dumpIn = jsonv.NewObject()
		}
		labels, deps := jsonv.NewObject(), jsonv.NewObject()
		for _, id := range jobs.Keys() {
			def, _ := jobs.Get(id)
			labels.Set(id, attempt(func() (any, error) {
				out := []any{}
				for i, s := range jsonv.Arr(jsonv.Path(def, "steps")) {
					l, err := StepLabel(s, i)
					if err != nil {
						return nil, err
					}
					out = append(out, l)
				}
				return out, nil
			}))
			deps.Set(id, attempt(func() (any, error) { return AllDeps(id, jobs) }))
		}
		var submit []any
		for _, s := range jsonv.Arr(jsonv.Path(c, "submit")) {
			submit = append(submit, attempt(func() (any, error) {
				return PrepareSubmittableValues(doc, jsonv.Path(s, "values"), jsonv.Str(jsonv.Path(s, "user"))), nil
			}))
		}
		if submit == nil {
			submit = []any{}
		}
		inputs := jsonv.Path(doc, "on", "execute", "inputs")
		docs = append(docs, jsonv.NewObject(
			"file", file,
			"dump", attempt(func() (any, error) { return DumpYAML(dumpIn), nil }),
			"hasInputs", HasUserInputs(doc),
			"form", attempt(func() (any, error) {
				if !jsonv.Truthy(inputs) {
					return nil, nil
				}
				return ConvertToDynamicForm(inputs), nil
			}),
			"labels", labels,
			"deps", deps,
			"submit", submit,
		))
	}
	var values []any
	for _, v := range jsonv.Arr(jsonv.Path(cases, "values")) {
		values = append(values, attempt(func() (any, error) { return DumpYAML(v), nil }))
	}
	return jsonv.NewObject("docs", docs, "values", values)
}

func TestMatchesNode(t *testing.T) {
	want, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	w, _ := jsonv.Decode(want)
	got := results(t)
	wd, gd := jsonv.Arr(jsonv.Path(w, "docs")), jsonv.Arr(jsonv.Path(got, "docs"))
	for i := range wd {
		for _, k := range []string{"dump", "hasInputs", "form", "labels", "deps", "submit"} {
			a, b := string(jsonv.MustMarshal(jsonv.Path(gd[i], k))), string(jsonv.MustMarshal(jsonv.Path(wd[i], k)))
			if a != b {
				if k == "dump" {
					diffLines(t, jsonv.Str(jsonv.Path(gd[i], k, "out")), jsonv.Str(jsonv.Path(wd[i], k, "out")))
				}
				t.Errorf("%s %s:\n got %.600s\nwant %.600s", jsonv.Str(jsonv.Path(wd[i], "file")), k, a, b)
			}
		}
	}
	wv, gv := jsonv.Arr(jsonv.Path(w, "values")), jsonv.Arr(jsonv.Path(got, "values"))
	for i := range wv {
		if a, b := string(jsonv.MustMarshal(gv[i])), string(jsonv.MustMarshal(wv[i])); a != b {
			t.Errorf("value %d:\n got %s\nwant %s", i, a, b)
		}
	}
}

func diffLines(t *testing.T, got, want string) {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			t.Errorf("first differing line %d:\n got %q\nwant %q", i+1, a, b)
			return
		}
	}
}

// TestGoldenIsCurrent regenerates the golden values with Node when
// STUDIO_GOLDEN_NODE names a node binary.
func TestGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../../scripts/golden/wfparser.mjs", "testdata/cases.json", "../../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/golden.json")
	a, _ := jsonv.Decode(out)
	b, _ := jsonv.Decode(want)
	if string(jsonv.MustMarshal(a)) != string(jsonv.MustMarshal(b)) {
		t.Fatal("Node's output changed: regenerate testdata/golden.json")
	}
}
