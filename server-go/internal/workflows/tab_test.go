package workflows

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/runs"
)

// The cases of server/test/workflowsTab.test.mjs.

const tabYAML = `
on:
  execute:
    inputs:
      resource:
        type: compute-clusters
        label: Cluster
      steps:
        type: number
        default: 2
jobs:
  main:
    steps:
      - run: echo hi
`

func tabFixture(t *testing.T) *fixture {
	t.Helper()
	net := fakeNet(t)
	ordered := `{"on":{"execute":{"inputs":{"zeta":{"type":"string","label":"Zeta"},"alpha":{"type":"string","label":"Alpha"},"mid":{"type":"number","label":"Mid"}}}},"jobs":{"main":{"steps":[{"run":"echo hi"}]}}}`
	laidOut := `{"on":{"execute":{"inputs":{"$meta":{"layout":{"type":"grid","columns":{"base":1,"md":2},"children":[{"type":"field","field":"study"},{"type":"field","field":"cases"}]}},"cases":{"type":"number","label":"Cases","default":8},"study":{"type":"group","label":"Study","items":{"$meta":{"layout":{"type":"stack","children":[{"type":"field","field":"high"},{"type":"field","field":"low"}]}},"low":{"type":"number","label":"Low"},"high":{"type":"number","label":"High"}}}}}},"jobs":{"main":{"steps":[{"run":"echo hi"}]}}}`
	net.set("platform.test", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workflows/laid-out":
			writeJSON(w, mustJSON(t, `{"name":"laid-out","displayName":"Laid out","yaml":`+laidOut+`,"configurations":[]}`))
		case "/api/workflows/ordered-job":
			writeJSON(w, mustJSON(t, `{"name":"ordered-job","displayName":"Ordered","yaml":`+ordered+`,"configurations":[]}`))
		default:
			http.Error(w, "not found", 404)
		}
	})
	f := newFixture(t, map[string]string{"PW_API_KEY": "deployment-key"}, "batch-job", "missing-one")
	f.answer = func(args []string, key string) (string, error) {
		verb := args[1]
		name := ""
		if len(args) > 2 {
			name = args[2]
		}
		switch {
		case verb == "ls":
			return `[{"name":"batch-job","displayName":"Batch job","description":"Runs a script","tags":["hpc"],"configurations":[{"name":"small"}]},{"name":"other","displayName":"Another workflow","description":""}]` + "\nA newer CLI is available.", nil
		case verb == "get" && name == "ordered-job":
			return `{"name":"ordered-job","yaml":{"on":{"execute":{"inputs":{"alpha":{},"mid":{},"zeta":{}}}}}}`, nil
		case verb == "get":
			return js(jsonv.NewObject("name", name, "displayName", "Batch job", "yaml", tabYAML, "configurations", mustJSON(t, `[{"name":"small","inputs":{"steps":1}}]`))), nil
		case verb == "run":
			for _, a := range args {
				if a == "--dry-run" {
					return "inputs are valid", nil
				}
			}
			return `{"slug":"batch-job-00001"}`, nil
		}
		return "", errors.New("unexpected " + strings.Join(args, " "))
	}
	return f
}

func TestFirstJSONSkipsTheNotice(t *testing.T) {
	v, err := jsonv.DecodeString(`[{"a":"]"}]`)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := platform.FirstJSON("[{\"a\":\"]\"}]\nupdate available")
	eq(t, "firstJson", got, js(v))
}

func TestCollectionTilesFollowTheCuratedOrder(t *testing.T) {
	f := tabFixture(t)
	a := f.do("GET", "/api/workflows/collection", nil)
	if a.status != 200 {
		t.Fatal(a.status, string(a.body))
	}
	eq(t, "configured", jsonv.Path(a.json, "configured"), "true")
	ws := jsonv.Arr(jsonv.Path(a.json, "workflows"))
	eq(t, "names", []any{jsonv.Path(ws[0], "name"), jsonv.Path(ws[1], "name")}, `["batch-job","missing-one"]`)
	eq(t, "displayName", jsonv.Path(ws[0], "displayName"), `"Batch job"`)
	eq(t, "configurations", jsonv.Path(ws[0], "configurations"), `["small"]`)
	eq(t, "available", jsonv.Path(ws[1], "available"), "false")
	// The whole tile, key for key, as Node writes it.
	eq(t, "tile", ws[0], `{"name":"batch-job","kind":"account","displayName":"Batch job","description":"Runs a script","tags":["hpc"],"icon":null,"configurations":["small"],"installed":true,"available":true}`)
}

func TestCatalogFlagsTheCurated(t *testing.T) {
	f := tabFixture(t)
	a := f.do("GET", "/api/workflows/catalog", nil)
	var got []any
	for _, w := range jsonv.Arr(jsonv.Path(a.json, "workflows")) {
		got = append(got, []any{jsonv.Path(w, "name"), jsonv.Path(w, "curated")})
	}
	eq(t, "workflows", got, `[["other",false],["batch-job",true]]`)
	eq(t, "missing", jsonv.Path(a.json, "missing"), `["missing-one"]`)
	eq(t, "source", jsonv.Path(a.json, "source"), `"deployment"`)
}

func TestFormConvertsInputsAndKeepsOrderAndLayout(t *testing.T) {
	f := tabFixture(t)
	a := f.do("GET", "/api/workflows/item/form?w=batch-job", nil)
	if a.status != 200 || jsonv.Obj(jsonv.Path(a.json, "form")) == nil {
		t.Fatal(a.status, string(a.body))
	}
	eq(t, "configurations", jsonv.Path(a.json, "configurations"), `[{"name":"small","inputs":{"steps":1}}]`)
	eq(t, "source", jsonv.Path(a.json, "source"), `{"kind":"local"}`)

	a = f.do("GET", "/api/workflows/item/form?w=ordered-job", nil)
	order := string(jsonv.MustMarshal(jsonv.Path(a.json, "form")))
	if !(strings.Index(order, "Zeta") < strings.Index(order, "Alpha") && strings.Index(order, "Alpha") < strings.Index(order, "Mid")) {
		t.Fatal("declared order zeta, alpha, mid:", order)
	}

	a = f.do("GET", "/api/workflows/item/form?w=laid-out", nil)
	eq(t, "$meta", jsonv.Path(a.json, "form", "$meta"), `{"layout":{"type":"grid","columns":{"base":1,"md":2},"children":[{"type":"field","field":"study"},{"type":"field","field":"cases"}]}}`)
	eq(t, "group $meta", jsonv.Path(a.json, "form", "study", "options", "$meta"), `{"layout":{"type":"stack","children":[{"type":"field","field":"high"},{"type":"field","field":"low"}]}}`)
}

func TestRunOutsideTheCollectionIsRefused(t *testing.T) {
	f := tabFixture(t)
	if a := f.do("POST", "/api/workflows/item/run?w=other", jsonv.NewObject("inputs", jsonv.NewObject(), "dryRun", true)); a.status != 403 {
		t.Fatal(a.status)
	}
}

func TestRunSubmitsPickerReferencesAndRecordsTheRun(t *testing.T) {
	f := tabFixture(t)
	a := f.do("POST", "/api/workflows/item/run?w=batch-job", mustJSON(t, `{"inputs":{"resource":{"name":"gpu","user":"someone","schedulerType":"slurm","_studioRef":"pw://someone/gpu"},"steps":3}}`))
	eq(t, "answer", a.json, `{"ok":true,"dryRun":false,"slug":"batch-job-00001","message":"Run batch-job-00001 submitted."}`)
	var run []string
	for _, c := range f.cliCalls() {
		if c.Args[1] == "run" {
			run = c.Args
		}
	}
	for i, x := range run {
		if x == "-i" {
			eq(t, "sent", mustJSON(t, run[i+1]), `{"resource":"pw://someone/gpu","steps":3}`)
		}
	}
	if rec := f.r.Runs.Get("batch-job-00001"); rec == nil || rec.Workflow != "batch-job" {
		t.Fatal("recorded", rec)
	}
	// A dry run validates without recording a run.
	a = f.do("POST", "/api/workflows/item/run?w=batch-job", mustJSON(t, `{"inputs":{"steps":1},"dryRun":true}`))
	eq(t, "dry run", a.json, `{"ok":true,"dryRun":true,"message":"inputs are valid"}`)
	if len(f.r.Runs.List(10)) != 1 {
		t.Fatal("a dry run is not recorded")
	}
}

func TestResolveStudioRefsAtAnyDepth(t *testing.T) {
	eq(t, "refs", ResolveStudioRefs(mustJSON(t, `{"a":{"_studioRef":"pw://u/x","uri":"pw://u/x"},"b":[{"_studioRef":"y"},2],"c":{"d":"e"}}`)), `{"a":"pw://u/x","b":["y",2],"c":{"d":"e"}}`)
}

func TestPlatformErrorNamesTheCause(t *testing.T) {
	f := newFixture(t, nil)
	check := func(msg string, status int, want string, prefix bool) {
		t.Helper()
		var kb *apierr.Error
		if !errors.As(f.r.PlatformError(errors.New(msg)), &kb) || kb.Status != status {
			t.Fatalf("%s: %v", msg, kb)
		}
		if prefix && !strings.HasPrefix(kb.Message, want) || !prefix && kb.Message != want {
			t.Fatalf("%q", kb.Message)
		}
	}
	check("2026-10-02T14:33:54Z [ERROR] Authentication has expired. Please authenticate again using 'pw auth'.", 401, "The platform credential has expired.", true)
	check("2026-10-02T14:33:54Z [ERROR] invalid workflow name\nmore", 502, "The platform did not answer: invalid workflow name", false)
	check("2026-10-02T14:33:54Z [ERROR] connection reset by peer\nmore", 503, "Could not reach platform.test. This computer may be offline", true)
	var kb *apierr.Error
	if !errors.As(f.r.PlatformError(ErrMissingCLI), &kb) || kb.Status != 503 {
		t.Fatal("missing CLI")
	}
}

func TestCatalogFallsBackToTheViewersOwnAccount(t *testing.T) {
	f := tabFixture(t)
	f.ownKey("viewer-key")
	f.answer = func(args []string, key string) (string, error) {
		if key == "deployment-key" {
			return "", errors.New("2026-10-02T14:33:54Z [ERROR] Authentication has expired. Please authenticate again using 'pw auth'.")
		}
		if args[1] == "ls" {
			return `[{"name":"mine-only","displayName":"Mine only"}]`, nil
		}
		return "", errors.New("unexpected")
	}
	a := f.do("GET", "/api/workflows/catalog", nil)
	if a.status != 200 {
		t.Fatal(a.status, string(a.body))
	}
	eq(t, "source", jsonv.Path(a.json, "source"), `"viewer"`)
	eq(t, "names", jsonv.Path(jsonv.Arr(jsonv.Path(a.json, "workflows"))[0], "name"), `"mine-only"`)
	// The deployment's failure stands when the viewer has no key of their own.
	g := tabFixture(t)
	g.answer = f.answer
	if a := g.do("GET", "/api/workflows/catalog", nil); a.status != 401 {
		t.Fatal(a.status, string(a.body))
	}
}

func TestRecordSource(t *testing.T) {
	eq(t, "local", RecordSource(mustJSON(t, `{"type":"local"}`)), `{"kind":"local"}`)
	eq(t, "remote", RecordSource(mustJSON(t, `{"type":"remote","remote":{"repo":"https://github.com/org/repo.git","branch":"main","yaml":"workflows/a/workflow.yaml"}}`)),
		`{"kind":"remote","repo":"github.com/org/repo","branch":"main","path":"workflows/a/workflow.yaml"}`)
	eq(t, "null", RecordSource(nil), `{"kind":"local"}`)
}

func TestNoCredentialIs409(t *testing.T) {
	f := newFixture(t, nil, "batch-job")
	fakeNet(t)
	a := f.do("GET", "/api/workflows/collection", nil)
	if a.status != 409 || !strings.Contains(js(a.json), "No platform credential") {
		t.Fatal(a.status, string(a.body))
	}
	// An empty collection answers before the credential is needed.
	g := newFixture(t, nil)
	eq(t, "unconfigured", g.do("GET", "/api/workflows/collection", nil).json, `{"configured":false,"workflows":[]}`)
}

func TestPickersListClustersPartitionsAndBuckets(t *testing.T) {
	fakeNet(t).set("platform.test", func(w http.ResponseWriter, r *http.Request) {})
	f := newFixture(t, map[string]string{"PW_API_KEY": "k"})
	f.answer = func(args []string, key string) (string, error) {
		switch args[0] {
		case "cluster":
			return `[{"name":"vega","user":"me","displayName":"Vega","status":"on","schedulerType":"slurm","type":"existing"},{"name":"solo"}]`, nil
		case "environments":
			return `[{"clusterName":"vega","partitionName":"gpu","partitionState":"up","availNodes":2,"totalNodes":8},{"clusterName":"other","name":"x"},{"clusterName":"vega","name":"n","status":"down"}]`, nil
		case "buckets":
			return `{"buckets":[{"uri":"pw://me/b1","displayName":"B1"},{"name":"b2","user":"me"},{"name":"b3"}]}`, nil
		}
		return "", errors.New("unexpected")
	}
	eq(t, "clusters", f.do("GET", "/api/platform/clusters", nil).json,
		`{"clusters":[{"value":"pw://me/vega","name":"vega","label":"Vega","status":"on","scheduler":"slurm","user":"me","type":"existing"},{"value":"solo","name":"solo","label":"solo","status":"","scheduler":null,"user":null,"type":null}]}`)
	eq(t, "partitions", f.do("GET", "/api/platform/partitions?cluster="+url.QueryEscape("pw://me/vega"), nil).json,
		`{"partitions":[{"value":"gpu","label":"gpu","state":"up","availNodes":2,"totalNodes":8},{"value":"n","label":"n","state":"down","availNodes":null,"totalNodes":null}]}`)
	eq(t, "buckets", f.do("GET", "/api/platform/buckets", nil).json,
		`{"buckets":[{"value":"pw://me/b1","label":"B1"},{"value":"pw://me/b2","label":"b2"},{"value":"b3","label":"b3"}]}`)
	// The platform unreachable fails at once, in plain words.
	g := newFixture(t, map[string]string{"PW_API_KEY": "k", "PW_GATEWAY_URL": "https://down.test/api/openai/v1"})
	g.answer = f.answer
	if a := g.do("GET", "/api/platform/clusters", nil); a.status != 503 || len(g.cliCalls()) != 0 {
		t.Fatal(a.status, string(a.body))
	}
}

func TestRunsListChecksOpenRunsWithTheViewersKey(t *testing.T) {
	f := tabFixture(t)
	f.r.Runs.RecordRun(runsInput("batch-job-1", "batch-job"))
	f.r.Runs.RecordRun(runsInput("elsewhere-1", "not-curated"))
	f.answer = func(args []string, key string) (string, error) {
		if strings.Join(args[:3], " ") == "workflows runs view" {
			return `{"status":"Completed"}`, nil
		}
		return "", errors.New("unexpected")
	}
	a := f.do("GET", "/api/workflows/runs", nil)
	rs := jsonv.Arr(jsonv.Path(a.json, "runs"))
	if len(rs) != 1 || jsonv.Path(rs[0], "slug") != "batch-job-1" || jsonv.Path(rs[0], "state") != "completed" || jsonv.Path(rs[0], "endedAt") == nil {
		t.Fatal(string(a.body))
	}
	if c := f.cliCalls(); len(c) != 1 || c[0].Key != "deployment-key" {
		t.Fatal(c)
	}
}

func TestInstallCopiesTheOwnersRecord(t *testing.T) {
	f := tabFixture(t)
	if a := f.do("POST", "/api/workflows/item/install?w=batch-job", nil); a.status != 409 || !strings.Contains(js(a.json), "own credential") {
		t.Fatal(a.status, string(a.body))
	}
	f.ownKey("viewer-key")
	var wrote string
	inner := f.answer
	f.answer = func(args []string, key string) (string, error) {
		if args[1] == "create" {
			for i, a := range args {
				if a == "--yaml" {
					wrote = args[i+1]
				}
			}
			if key != "viewer-key" {
				return "", errors.New("wrong key " + key)
			}
			return "created", nil
		}
		return inner(args, key)
	}
	a := f.do("POST", "/api/workflows/item/install?w=batch-job", nil)
	eq(t, "install", a.json, `{"ok":true}`)
	var create []string
	for _, c := range f.cliCalls() {
		if c.Args[1] == "create" {
			create = c.Args
		}
	}
	if strings.Join(create[:4], " ") != "workflows create --display-name Batch job" || !strings.HasSuffix(wrote, "batch-job.yaml") || create[len(create)-1] != "batch-job" {
		t.Fatal(create)
	}
	if a := f.do("POST", "/api/workflows/item/install?w=other", nil); a.status != 403 {
		t.Fatal(a.status)
	}
}

func runsInput(slug, workflow string) runs.RecordInput {
	return runs.RecordInput{Slug: slug, Workflow: workflow}
}

func TestNodeRecordsARunThroughTheInternalRoute(t *testing.T) {
	f := newFixture(t, nil)
	body := mustJSON(t, `{"slug":"n-1","workflow":"w","conversationId":"c1","launchedAt":"2026-09-05T00:00:00Z","state":"running"}`)
	if a := f.do("POST", "/internal/platform/runs", body); a.status != 404 {
		t.Fatal("without the secret:", a.status)
	}
	req := httptest.NewRequest("POST", "/internal/platform/runs", strings.NewReader(js(body)))
	req.Header.Set("Content-Type", "application/json")
	bridge.Sign(req)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if r := f.r.Runs.Get("n-1"); r == nil || r.LaunchedAt != "2026-09-05T00:00:00Z" || *r.ConversationID != "c1" {
		t.Fatal(r)
	}
}
