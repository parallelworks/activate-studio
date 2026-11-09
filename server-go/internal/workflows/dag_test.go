package workflows

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// GET /api/workflows, GET /api/workflows/{name} and GET /api/kb/dag of
// routes.ts. No Node test drives them; the answers are checked against
// what routes.ts computes for the same input.

const dagYAML = `on:
  execute:
    inputs:
      n:
        type: number
        label: Cases
        default: 2
jobs:
  prep:
    steps:
      - name: Fetch
        run: echo fetch
      - run: echo two
  solve:
    needs: [prep]
    steps:
      - run: echo solve
  report:
    needs:
      - solve
    steps:
      - uses: marketplace/notify
`

func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWorkflowListFromTheAPIAndTheCLI(t *testing.T) {
	net := fakeNet(t)
	net.set("platform.test", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workflows" {
			writeJSON(w, mustJSON(t, `[{"name":"a","type":"remote"},{"name":""},{"name":"b"}]`))
			return
		}
		http.Error(w, "no", 404)
	})
	f := newFixture(t, map[string]string{"PW_API_KEY": "k"})
	eq(t, "api", f.do("GET", "/api/workflows", nil).json, `{"workflows":[{"name":"a","type":"remote"},{"name":"b","type":""}]}`)
	if s := net.reset(); s[len(s)-1].Auth != "Bearer k" {
		t.Error(s)
	}

	// No key: the CLI's table, its header line dropped.
	cli := fakeCLI(t, `printf 'NAME TYPE\nalpha remote\n  beta local  \nnot a row here\n'`)
	g := newFixture(t, map[string]string{"PW_CLI": cli})
	eq(t, "cli", g.do("GET", "/api/workflows", nil).json, `{"workflows":[{"name":"alpha","type":"remote"},{"name":"beta","type":"local"}]}`)

	bad := newFixture(t, map[string]string{"PW_CLI": fakeCLI(t, `echo 'not signed in' >&2; exit 1`)})
	a := bad.do("GET", "/api/workflows", nil)
	if a.status != 502 || !strings.HasPrefix(jsStr(jsonv.Path(a.json, "error")), "Cannot list workflows: no platform credential available (this deployment has none, and you have not added your own under Settings, Model access), and the pw CLI on this host is not usable (not signed in") {
		t.Fatal(a.status, string(a.body))
	}
}

func TestWorkflowItemIsTheDagPayload(t *testing.T) {
	doc, _ := jsonv.ParseYAML(dagYAML)
	net := fakeNet(t)
	net.set("platform.test", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workflows/sweep" {
			writeJSON(w, jsonv.NewObject("name", "sweep", "displayName", "Sweep", "yaml", doc))
			return
		}
		http.Error(w, "no", 404)
	})
	f := newFixture(t, map[string]string{"PW_API_KEY": "k"})
	// Characters outside a name are dropped, as Node does.
	a := f.do("GET", "/api/workflows/sw%20eep", nil)
	if a.status != 200 {
		t.Fatal(a.status, string(a.body))
	}
	o := jsonv.Obj(a.json)
	eq(t, "keys", o.Keys(), `["name","displayName","nodes","edges","yaml","form","yamlText"]`)
	eq(t, "nodes", jsonv.Path(a.json, "nodes"), `[{"id":"prep","steps":["Fetch","echo two"],"dependsOn":[]},{"id":"solve","steps":["echo solve"],"dependsOn":["prep"]},{"id":"report","steps":["marketplace/notify"],"dependsOn":["solve","prep"]}]`)
	eq(t, "edges", jsonv.Path(a.json, "edges"), `[{"from":"prep","to":"solve"},{"from":"solve","to":"report"}]`)
	if jsonv.Obj(jsonv.Path(a.json, "form")) == nil || !strings.HasPrefix(jsStr(jsonv.Path(a.json, "yamlText")), "'on':\n  execute:") {
		t.Fatal(string(a.body))
	}
	// The routes beside it are not shadowed by the name pattern.
	if a := f.do("GET", "/api/workflows/runs", nil); jsonv.Path(a.json, "runs") == nil {
		t.Fatal("runs route:", a.status, string(a.body))
	}
}

func TestKbDagDrawsALibraryYAML(t *testing.T) {
	fakeNet(t) // no schema reachable: the verdict is "not validated" and no errors ride along
	f := newFixture(t, nil)
	kb := f.p.KBRoot()
	os.MkdirAll(filepath.Join(kb, "flows"), 0o755)
	os.WriteFile(filepath.Join(kb, "flows", "sweep.yaml"), []byte(dagYAML), 0o644)
	os.WriteFile(filepath.Join(kb, "flows", "nojobs.yaml"), []byte("on: {}\n"), 0o644)
	os.WriteFile(filepath.Join(kb, "flows", "bad.yaml"), []byte("a: [\n"), 0o644)

	a := f.do("GET", "/api/kb/dag?path=flows/sweep.yaml", nil)
	if a.status != 200 {
		t.Fatal(a.status, string(a.body))
	}
	eq(t, "keys", jsonv.Obj(a.json).Keys(), `["name","nodes","edges","yaml","form","yamlText","schemaErrors"]`)
	eq(t, "name", jsonv.Path(a.json, "name"), `"sweep.yaml"`)
	eq(t, "schemaErrors", jsonv.Path(a.json, "schemaErrors"), `[]`)

	for path, want := range map[string]int{"flows/nojobs.yaml": 422, "flows/bad.yaml": 422, "flows/absent.yaml": 422, "flows": 422, "../outside.yaml": 400} {
		if a := f.do("GET", "/api/kb/dag?path="+path, nil); a.status != want {
			t.Errorf("%s: %d %s", path, a.status, a.body)
		}
	}
	a = f.do("GET", "/api/kb/dag?path=flows/absent.yaml", nil)
	eq(t, "absent", jsonv.Path(a.json, "error"), `"Not parseable as workflow YAML: ENOENT: no such file or directory, open '`+filepath.Join(kb, "flows", "absent.yaml")+`'"`)
	eq(t, "no jobs", jsonv.Path(f.do("GET", "/api/kb/dag?path=flows/nojobs.yaml", nil).json, "error"), `"This YAML has no jobs section, so there is no DAG to draw."`)
}

func TestKbDagCarriesTheSchemaErrors(t *testing.T) {
	schema, err := os.ReadFile("testdata/workflow.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	fakeNet(t).set("platform.test", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workflow.schema.json" {
			w.Header().Set("Content-Type", "application/json")
			w.Write(schema)
			return
		}
		http.Error(w, "no", 404)
	})
	f := newFixture(t, nil)
	os.WriteFile(filepath.Join(f.p.KBRoot(), "bad.yaml"), []byte("jobs:\n  main:\n    stepz: []\n"), 0o644)
	a := f.do("GET", "/api/kb/dag?path=bad.yaml", nil)
	if a.status != 200 || len(jsonv.Arr(jsonv.Path(a.json, "schemaErrors"))) == 0 {
		t.Fatal(a.status, string(a.body))
	}
}

func TestDagPayloadCycleIsAnError(t *testing.T) {
	doc, _ := jsonv.ParseYAML("jobs:\n  a:\n    needs: [b]\n  b:\n    needs: [a]\n")
	if _, err := DagPayload("x", jsonv.Undefined, doc); err == nil {
		t.Fatal("a cycle in needs is an error (Node overflows its stack)")
	}
}
