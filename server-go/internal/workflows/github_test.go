package workflows

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// The cases of server/test/workflowEntries.test.mjs,
// githubWorkflows.test.mjs and workflowIcons.test.mjs.

func TestEntriesParseIntoThreeKinds(t *testing.T) {
	eq(t, "account", ParseEntry("activatebatch").JSON(), `{"kind":"account","entry":"activatebatch","name":"activatebatch"}`)
	eq(t, "marketplace", ParseEntry("marketplace/jupyter").JSON(), `{"kind":"marketplace","entry":"marketplace/jupyter","slug":"jupyter"}`)
	eq(t, "github", ParseEntry("https://github.com/example-org/flows/examples/slurm@main").JSON(),
		`{"kind":"github","entry":"github.com/example-org/flows/examples/slurm@main","owner":"example-org","repo":"flows","path":"examples/slurm","ref":"main"}`)
	for _, bad := range []string{"", "github.com/only-owner", "github.com/a/b/../c", "gitlab.com/a/b", "marketplace/", "a b", "x;rm -rf /"} {
		if ParseEntry(bad) != nil {
			t.Error(bad)
		}
	}
}

func TestGitHubEntriesResolveToRawURLs(t *testing.T) {
	t.Setenv("STUDIO_GITHUB_RAW_URL", "")
	dir := ParseEntry("github.com/o/r/sub@v1")
	eq(t, "dir", GitHubYAMLURLs(dir), `["https://raw.githubusercontent.com/o/r/v1/sub/workflow.yaml","https://raw.githubusercontent.com/o/r/v1/sub/workflow.yml"]`)
	if GitHubThumbnail(dir) != "https://raw.githubusercontent.com/o/r/v1/sub/thumbnail.png" {
		t.Error(GitHubThumbnail(dir))
	}
	eq(t, "file", GitHubYAMLURLs(ParseEntry("github.com/o/r/flows/a.yaml")), `["https://raw.githubusercontent.com/o/r/HEAD/flows/a.yaml"]`)
	// The override the tests through the Go front use.
	t.Setenv("STUDIO_GITHUB_RAW_URL", "http://127.0.0.1:5986/raw/")
	eq(t, "override", GitHubYAMLURLs(ParseEntry("github.com/o/r/flows/a.yaml")), `["http://127.0.0.1:5986/raw/o/r/HEAD/flows/a.yaml"]`)
}

const entriesYAML = "on:\n  execute:\n    inputs:\n      message:\n        type: string\n        label: Message\njobs:\n  main:\n    steps:\n      - run: echo hi\n"

func ghTree(w http.ResponseWriter, paths ...string) {
	tree := []any{}
	for _, p := range paths {
		tree = append(tree, jsonv.NewObject("path", p, "type", "blob"))
	}
	writeJSON(w, jsonv.NewObject("tree", tree))
}

func TestMarketplaceAndGitHubEntries(t *testing.T) {
	net := fakeNet(t)
	net.set("api.github.com", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/example-org/batch-flow/git/trees/v2"):
			ghTree(w, "workflow.yaml", "README.md")
		case strings.HasPrefix(r.URL.Path, "/repos/example-org/absent/git/trees/"):
			ghTree(w, "README.md")
		default:
			http.Error(w, "nope", 404)
		}
	})
	net.set("raw.githubusercontent.com", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/example-org/batch-flow/v2/workflow.yaml" {
			w.Write([]byte(entriesYAML))
			return
		}
		http.Error(w, "nope", 404)
	})
	net.set("platform.test", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 404) })
	f := newFixture(t, map[string]string{"PW_API_KEY": "deployment-key"}, "marketplace/jupyter", "github.com/example-org/batch-flow@v2")
	f.answer = func(args []string, key string) (string, error) {
		switch {
		case args[0] == "marketplace" && args[1] == "ls":
			return `[{"slug":"jupyter","name":"Jupyter","description":"Notebook server","type":"workflow"},{"slug":"a-bucket","type":"storage"}]`, nil
		case args[0] == "marketplace" && args[1] == "get":
			return entriesYAML, nil
		case args[0] == "workflows" && args[1] == "run":
			return `{"run":{"slug":"run-00001"}}`, nil
		}
		return "", errors.New("unexpected " + strings.Join(args, " "))
	}
	var tiles []any
	for _, w := range jsonv.Arr(jsonv.Path(f.do("GET", "/api/workflows/collection", nil).json, "workflows")) {
		tiles = append(tiles, []any{jsonv.Path(w, "name"), jsonv.Path(w, "kind"), jsonv.Path(w, "displayName"), jsonv.Path(w, "installed"), jsonv.Path(w, "available")})
	}
	eq(t, "tiles", tiles, `[["marketplace/jupyter","marketplace","Jupyter",true,true],["github.com/example-org/batch-flow@v2","github","batch-flow",true,true]]`)

	q := url.QueryEscape
	for _, e := range []string{"marketplace/jupyter", "github.com/example-org/batch-flow@v2"} {
		a := f.do("GET", "/api/workflows/item/form?w="+q(e), nil)
		if a.status != 200 || jsonv.Obj(jsonv.Path(a.json, "form")).Len() == 0 {
			t.Fatal(e, a.status, string(a.body))
		}
	}
	a := f.do("GET", "/api/workflows/item/form?w="+q("github.com/example-org/absent"), nil)
	if a.status != 404 || !strings.Contains(jsStr(jsonv.Path(a.json, "error")), "No workflow.yaml") {
		t.Fatal(a.status, string(a.body))
	}

	a = f.do("POST", "/api/workflows/item/run?w="+q("github.com/example-org/batch-flow@v2"), mustJSON(t, `{"inputs":{"message":"hi"}}`))
	eq(t, "ok", jsonv.Path(a.json, "ok"), "true")
	lastRun := func() []string {
		var run []string
		for _, c := range f.cliCalls() {
			if c.Args[1] == "run" {
				run = c.Args
			}
		}
		return run
	}
	run := lastRun()
	if run[2] != "github.com/example-org/batch-flow/workflow.yaml@v2" || strings.Contains(strings.Join(run, " "), "--trust") {
		t.Fatal(run)
	}
	a = f.do("POST", "/api/workflows/item/run?w="+q("marketplace/jupyter"), mustJSON(t, `{"inputs":{},"dryRun":true}`))
	if jsonv.Path(a.json, "ok") != true || lastRun()[2] != "marketplace/jupyter" {
		t.Fatal(string(a.body), lastRun())
	}

	if a := f.do("GET", "/api/workflows/item/form?w="+q("gitlab.com/a/b"), nil); a.status != 400 {
		t.Error(a.status)
	}
	if a := f.do("POST", "/api/workflows/item/install?w="+q("marketplace/jupyter"), nil); a.status != 409 {
		t.Error(a.status)
	}
}

func componentsYAML(perms bool) string {
	s := ""
	if perms {
		s = "permissions:\n  - '*'\n"
	}
	return s + "on:\n  execute:\n    inputs:\n      n_cases:\n        type: number\n        label: Cases\n        default: 8\njobs:\n  main:\n    steps:\n      - run: echo hi\n"
}

const (
	doeEntry  = "github.com/example-org/components/workflows/doe/yamls/general.yaml@canary"
	siteEntry = "github.com/example-org/components/workflows/explorer/yamls/site.yaml@canary"
)

func componentsFixture(t *testing.T) (*fixture, *atomic.Int32) {
	t.Helper()
	tree := []string{
		"README.md", "docs/notes.md",
		"workflows/doe/README.md", "workflows/doe/yamls/general.yaml", "workflows/doe/thumbnails/methods.svg", "workflows/doe/app/doe.py",
		"workflows/explorer/README.md", "workflows/explorer/yamls/general.yaml", "workflows/explorer/yamls/site.yaml", "workflows/explorer/thumbnails/explorer.png",
		"workflows/myworkflow.yaml",
		"workflows/plain/README.md", "workflows/plain/workflow.yaml",
	}
	files := map[string]string{
		"workflows/doe/README.md":                    "# DOE: Design of Experiments\n\nSpreads designs over the [bounds](x.md) of the\ndesign variables.\n\n## Inputs\n",
		"workflows/plain/README.md":                  "# Plain\n\n" + strings.Repeat("Runs one step on a cluster and reports what it found. ", 8) + "Then lists:\n\n- a\n",
		"workflows/explorer/README.md":               "# Design Explorer\n\n![shot](thumbnails/explorer.png)\n\nBrowses a study in a *browser*.\n",
		"workflows/doe/yamls/general.yaml":           componentsYAML(true),
		"workflows/explorer/yamls/general.yaml":      componentsYAML(true),
		"workflows/explorer/yamls/site.yaml":         componentsYAML(true),
		"workflows/plain/workflow.yaml":              componentsYAML(false),
		"workflows/explorer/thumbnails/explorer.png": "PNG",
	}
	var treeCalls atomic.Int32
	net := fakeNet(t)
	net.set("api.github.com", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/example-org/components/git/trees/canary"):
			treeCalls.Add(1)
			ghTree(w, tree...)
		case strings.HasPrefix(r.URL.Path, "/repos/example-org/limited/"):
			http.Error(w, "{}", 403)
		default:
			http.Error(w, "nope", 404)
		}
	})
	net.set("raw.githubusercontent.com", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/example-org/components/canary/")
		body, ok := files[p]
		if !ok || p == r.URL.Path {
			http.Error(w, "nope", 404)
			return
		}
		if strings.HasSuffix(p, ".png") {
			w.Header().Set("Content-Type", "image/png")
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}
		w.Write([]byte(body))
	})
	net.set("platform.test", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 404) })
	f := newFixture(t, map[string]string{"PW_API_KEY": "deployment-key"}, doeEntry, siteEntry, "github.com/example-org/components/workflows/plain@canary")
	f.answer = func(args []string, key string) (string, error) {
		if args[0] == "workflows" && args[1] == "run" {
			if strings.Contains(args[2], "/plain/") {
				return "", errors.New("\x1b[90m2026-10-09T12:45:44Z\x1b[0m [\x1b[31mERROR\x1b[0m] \x1b[1mUnable to find workflow " + args[2] + "\x1b[0m")
			}
			if !strings.Contains(strings.Join(args, " "), "--trust") {
				return "", errors.New("github.com/example-org/components wants access to:\n  permission  *\n\nRe-run with --trust to approve them.\n2026-10-09T12:40:34Z [ERROR] run cancelled")
			}
			return `{"run":{"slug":"general-00001"}}`, nil
		}
		return "", errors.New("unexpected " + strings.Join(args, " "))
	}
	return f, &treeCalls
}

func TestBrowseListsOneEntryPerWorkflowFile(t *testing.T) {
	f, treeCalls := componentsFixture(t)
	q := url.QueryEscape
	a := f.do("GET", "/api/workflows/github/browse?repo="+q("github.com/example-org/components/workflows@canary"), nil)
	if a.status != 200 {
		t.Fatal(a.status, string(a.body))
	}
	ws := jsonv.Arr(jsonv.Path(a.json, "workflows"))
	var got []any
	for _, w := range ws {
		got = append(got, []any{jsonv.Path(w, "entry"), jsonv.Path(w, "title")})
	}
	eq(t, "entries", got, `[["`+doeEntry+`","DOE: Design of Experiments"],["github.com/example-org/components/workflows/explorer/yamls/general.yaml@canary","Design Explorer (general)"],["`+siteEntry+`","Design Explorer (site)"],["github.com/example-org/components/workflows/plain/workflow.yaml@canary","Plain"]]`)
	eq(t, "doe description", jsonv.Path(ws[0], "description"), `"Spreads designs over the bounds of the design variables."`)
	eq(t, "explorer description", jsonv.Path(ws[1], "description"), `"Browses a study in a browser."`)
	all := jsonv.Arr(jsonv.Path(f.do("GET", "/api/workflows/github/browse?repo="+q("github.com/example-org/components@canary"), nil).json, "workflows"))
	if len(all) != 4 || strings.Contains(js(all), "myworkflow") {
		t.Fatal(js(all))
	}
	if treeCalls.Load() != 1 {
		t.Error("the tree is read once and cached:", treeCalls.Load())
	}

	eq(t, "one", jsonv.Path(f.do("GET", "/api/workflows/github/resolve?w="+q("github.com/example-org/components/workflows/doe@canary"), nil).json, "entries"), `["`+doeEntry+`"]`)
	if n := len(jsonv.Arr(jsonv.Path(f.do("GET", "/api/workflows/github/resolve?w="+q("github.com/example-org/components/workflows/explorer@canary"), nil).json, "entries"))); n != 2 {
		t.Error(n)
	}
	form := f.do("GET", "/api/workflows/item/form?w="+q("github.com/example-org/components/workflows/explorer@canary"), nil)
	if form.status != 409 || !strings.Contains(jsStr(jsonv.Path(form.json, "error")), "several workflows; name one") {
		t.Fatal(form.status, string(form.body))
	}
	dir := f.do("GET", "/api/workflows/item/form?w="+q("github.com/example-org/components/workflows/doe@canary"), nil)
	if dir.status != 200 || jsonv.Path(dir.json, "runAs") != doeEntry {
		t.Fatal(dir.status, string(dir.body))
	}
}

func TestComponentTilesTakeTheReadmeAndPNG(t *testing.T) {
	f, _ := componentsFixture(t)
	ws := jsonv.Arr(jsonv.Path(f.do("GET", "/api/workflows/collection", nil).json, "workflows"))
	var got []any
	for _, w := range ws {
		got = append(got, []any{jsonv.Path(w, "name"), jsonv.Path(w, "displayName"), jsonv.Path(w, "icon") != nil})
	}
	eq(t, "tiles", got, `[["`+doeEntry+`","DOE: Design of Experiments",false],["`+siteEntry+`","Design Explorer (site)",true],["github.com/example-org/components/workflows/plain@canary","Plain",false]]`)
	d := jsStr(jsonv.Path(ws[2], "description"))
	if !strings.HasSuffix(d, "found.") || utf16Len(d) > 320 {
		t.Error(d)
	}
	icon := f.do("GET", "/api/workflows/item/icon?w="+url.QueryEscape(siteEntry), nil)
	if icon.status != 200 || icon.header.Get("Content-Type") != "image/png" || !strings.Contains(icon.header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal(icon.status, icon.header)
	}
}

func TestRunNeedsTheViewerToApprovePermissions(t *testing.T) {
	f, _ := componentsFixture(t)
	q := url.QueryEscape
	form := f.do("GET", "/api/workflows/item/form?w="+q(doeEntry), nil)
	eq(t, "permissions", jsonv.Path(form.json, "permissions"), `["*"]`)
	eq(t, "displayName", jsonv.Path(form.json, "displayName"), `"DOE: Design of Experiments"`)

	refused := f.do("POST", "/api/workflows/item/run?w="+q(doeEntry), mustJSON(t, `{"inputs":{"n_cases":4},"dryRun":true}`))
	if jsonv.Path(refused.json, "ok") != false || jsonv.Path(refused.json, "needsTrust") != true || !strings.Contains(jsStr(jsonv.Path(refused.json, "message")), "Approve the access") {
		t.Fatal(string(refused.body))
	}
	approved := f.do("POST", "/api/workflows/item/run?w="+q(doeEntry), mustJSON(t, `{"inputs":{"n_cases":4},"dryRun":false,"trust":true}`))
	calls := f.cliCalls()
	last := calls[len(calls)-1].Args
	if jsonv.Path(approved.json, "ok") != true || last[2] != doeEntry || !strings.Contains(strings.Join(last, " "), "--trust") {
		t.Fatal(string(approved.body), last)
	}

	old := f.do("POST", "/api/workflows/item/run?w="+q("github.com/example-org/components/workflows/plain@canary"), mustJSON(t, `{"inputs":{},"dryRun":true}`))
	msg := jsStr(jsonv.Path(old.json, "message"))
	if jsonv.Path(old.json, "ok") != false || strings.Contains(msg, "\x1b") || !regexp.MustCompile(`^Unable to find workflow .*too old to run a GitHub workflow file`).MatchString(msg) {
		t.Fatal(msg)
	}
}

func TestGitHubRateLimitingReadsAsAMessage(t *testing.T) {
	f, _ := componentsFixture(t)
	a := f.do("GET", "/api/workflows/github/browse?repo="+url.QueryEscape("github.com/example-org/limited"), nil)
	if a.status != 503 || !strings.Contains(jsStr(jsonv.Path(a.json, "error")), "rate limiting") {
		t.Fatal(a.status, string(a.body))
	}
	if a := f.do("GET", "/api/workflows/github/browse?repo="+url.QueryEscape("marketplace/jupyter"), nil); a.status != 400 {
		t.Fatal(a.status)
	}
}

// Icons, from workflowIcons.test.mjs; the GitLab host is an
// example.org one.
const (
	iconFallback = "/api/repositories/thumbnail?path=thumbnails%2Fx.png&ref=main&repo=gitlab.example.org%2Fteam%2Fflows"
	iconDerived  = "/api/repositories/thumbnail?path=thumbnails%2Fy.png&ref=main&repo=gitlab.example.org%2Fteam%2Fflows"
)

func iconFixture(t *testing.T) (*fixture, *hosts) {
	t.Helper()
	net := fakeNet(t)
	png := func(w http.ResponseWriter) { w.Header().Set("Content-Type", "image/png"); w.Write(testPNG) }
	net.set("platform.test", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case iconFallback, iconDerived, "/api/blobs/abc":
			png(w)
		default:
			http.Error(w, "nope", 404)
		}
	})
	net.set("raw.githubusercontent.com", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/o/r/main/thumbnail.png" {
			png(w)
			return
		}
		http.Error(w, "nope", 404)
	})
	net.set("broken.example.org", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>sign in</html>"))
	})
	// gitlab.example.org is unset: unreachable, as an untrusted certificate is.
	f := newFixture(t, map[string]string{"PW_API_KEY": "viewer-key"}, "gitlab-wf", "gitlab-old-cli-wf", "github-wf", "blob-wf", "broken-wf")
	f.answer = func(args []string, key string) (string, error) {
		if args[1] == "ls" {
			return `[{"name":"gitlab-wf","type":"remote","imageUrl":"https://gitlab.example.org/team/flows/-/raw/main/thumbnails/x.png","imageFallbackUrl":"` + iconFallback + `"},` +
				`{"name":"gitlab-old-cli-wf","type":"remote","imageUrl":"https://gitlab.example.org/team/flows/-/raw/main/thumbnails/y.png"},` +
				`{"name":"github-wf","type":"remote","imageUrl":"https://raw.githubusercontent.com/o/r/main/thumbnail.png"},` +
				`{"name":"blob-wf","type":"local","imageUrl":"/api/blobs/abc"},` +
				`{"name":"broken-wf","type":"remote","imageUrl":"https://broken.example.org/x.png"}]`, nil
		}
		return "", errors.New("unexpected " + strings.Join(args, " "))
	}
	return f, net
}

func TestIconsThroughThePlatformFallback(t *testing.T) {
	f, net := iconFixture(t)
	icon := func(name string) answer { return f.do("GET", "/api/workflows/item/icon?w="+name, nil) }

	net.reset()
	if a := icon("gitlab-wf"); a.status != 200 || a.header.Get("Content-Type") != "image/png" {
		t.Fatal(a.status)
	}
	seen := net.reset()
	if seen[0].URL != "https://platform.test"+iconFallback || seen[0].Auth != "Bearer viewer-key" || strings.Contains(js(seen), "https://gitlab.example.org/") {
		t.Fatal(seen)
	}

	if a := icon("gitlab-old-cli-wf"); a.status != 200 {
		t.Fatal(a.status)
	}
	if seen = net.reset(); seen[0].URL != "https://platform.test"+iconDerived || seen[0].Auth != "Bearer viewer-key" {
		t.Fatal(seen)
	}

	if icon("github-wf").status != 200 || icon("blob-wf").status != 200 {
		t.Fatal("github or blob icon")
	}
	for _, s := range net.reset() {
		if strings.Contains(s.URL, "githubusercontent") && s.Auth != "" || strings.Contains(s.URL, "/api/blobs/") && s.Auth != "Bearer viewer-key" {
			t.Error(s)
		}
	}
	if a := icon("broken-wf"); a.status != 404 {
		t.Error("a sign-in page is not an icon:", a.status)
	}
}

func TestThumbnailRouteMatchesThePlatforms(t *testing.T) {
	for in, want := range map[string]string{
		"https://gitlab.example.org/team/flows/-/raw/main/thumbnails/x.png":                             iconFallback,
		"https://gitlab.example.org/a/b/c/-/raw/v2/thumbs/my%20icon.png":                                "/api/repositories/thumbnail?path=thumbs%2Fmy+icon.png&ref=v2&repo=gitlab.example.org%2Fa%2Fb%2Fc",
		"https://raw.githubusercontent.com/parallelworks/workflows/canary/workflows/x/thumbnails/x.png": "/api/repositories/thumbnail?path=workflows%2Fx%2Fthumbnails%2Fx.png&ref=canary&repo=parallelworks%2Fworkflows",
		"https://example.org/x.png":                             "",
		"https://raw.githubusercontent.com/o/r/main":            "",
		"http://gitlab.example.org/team/flows/-/raw/main/x.png": "",
		"/api/blobs/abc": "",
		"":               "",
	} {
		if got := ThumbnailRoute(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}

func TestIconCandidateOrder(t *testing.T) {
	P := "https://platform.test"
	eq(t, "gitlab", IconCandidates("https://gitlab.x/a.png", "/api/repositories/thumbnail?x", P), `["`+P+`/api/repositories/thumbnail?x","https://gitlab.x/a.png"]`)
	eq(t, "github", IconCandidates("https://raw.githubusercontent.com/a.png", "/fb", P), `["https://raw.githubusercontent.com/a.png","`+P+`/fb"]`)
	eq(t, "blob", IconCandidates("/api/blobs/z", "", P), `["`+P+`/api/blobs/z"]`)
	eq(t, "http", IconCandidates("http://plain.example/a.png", "", P), `[]`)
	eq(t, "none", IconCandidates("", "", P), `[]`)
}
