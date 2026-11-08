package workflows

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// The cases of server/test/workflowReadme.test.mjs.

var testReadme = strings.Join([]string{
	"# Sweep",
	"![diagram](thumbnails/flow.png)",
	"See [the bounds format](docs/bounds.md#format), [the site](https://example.org), and [inputs](#inputs).",
	"```",
	"![not rewritten](thumbnails/flow.png)",
	"```",
	"Inline `![kept](a.png)` stays.",
	`<img src="../shared/logo.png" width="80">`,
}, "\n")

var testPNG = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

func readmeFixture(t *testing.T) (*fixture, *hosts) {
	t.Helper()
	net := fakeNet(t)
	net.set("platform.test", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/workflows/gitlab-flow":
			writeJSON(w, mustJSON(t, `{"name":"gitlab-flow","type":"remote","remote":{"repo":"https://gitlab.example.org/team/flows.git","branch":"main","readme":"sweep/README.md","yaml":"sweep/workflow.yaml"}}`))
		case "/api/workflows/local-flow":
			writeJSON(w, mustJSON(t, `{"name":"local-flow","type":"local"}`))
		case "/api/repositories/readme":
			writeJSON(w, jsonv.NewObject("markdown", testReadme+"\n<!-- "+q.Get("repo")+" "+q.Get("ref")+" "+q.Get("path")+" -->"))
		case "/api/repositories/thumbnail":
			w.Header().Set("Content-Type", "image/png")
			w.Write(testPNG)
		default:
			http.Error(w, "no", 404)
		}
	})
	f := newFixture(t, map[string]string{"PW_API_KEY": "viewer-key"})
	f.answer = func(args []string, key string) (string, error) {
		if args[0] == "marketplace" && args[1] == "get" {
			return `{"slug":"` + args[2] + `","versionTags":{"latest":"v2"},"versionOrder":["v1","v2"],"versions":{"v1":{"repo":"https://github.com/acme/flows.git","branch":"old","readme":"x/README.md"},"v2":{"repo":"https://github.com/acme/flows.git","branch":"canary","readme":"workflows/naca/README.md"}}}`, nil
		}
		return "", errors.New("unexpected " + strings.Join(args, " "))
	}
	return f, net
}

func TestRepoKeyTakesThePlatformForm(t *testing.T) {
	for _, c := range []struct {
		in, key    string
		github, ok bool
	}{
		{"https://github.com/acme/flows.git", "acme/flows", true, true},
		{"acme/flows", "acme/flows", true, true},
		{"https://gitlab.example.org/team/sub/flows.git", "gitlab.example.org/team/sub/flows", false, true},
		{"https://github.com/acme", "", false, false},
		{"not a repo", "", false, false},
	} {
		k, g, ok := RepoKey(c.in)
		if ok != c.ok || ok && (k != c.key || g != c.github) {
			t.Errorf("%s: %q %v %v", c.in, k, g, ok)
		}
	}
}

func TestGitHubEntryReadsTheReadmeInItsDirectory(t *testing.T) {
	src := func(path, ref string) *ReadmeSource {
		e := &Entry{Kind: KindGitHub, Entry: "x", Owner: "o", Repo: "r", Path: path}
		if ref != "" {
			e.Ref = &ref
		}
		return GitHubReadmeSource(e)
	}
	for in, want := range map[string]string{"workflows/naca/yamls/site.yaml": "workflows/naca/README.md", "workflows/naca/workflow.yaml": "workflows/naca/README.md", "workflows/naca": "workflows/naca/README.md", "": "README.md"} {
		if got := src(in, "").Path; got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if src("a", "v1").Ref != "v1" {
		t.Error("ref")
	}
}

func TestRewriteReadme(t *testing.T) {
	out := RewriteReadme(testReadme, &ReadmeSource{Repo: "gitlab.example.org/team/flows", Ref: "main", Path: "sweep/README.md"}, "gitlab-flow")
	for _, re := range []string{
		`!\[diagram\]\(/api/workflows/item/asset\?w=gitlab-flow&path=sweep%2Fthumbnails%2Fflow\.png\)`,
		`\[the bounds format\]\(https://gitlab\.example\.org/team/flows/-/blob/main/sweep/docs/bounds\.md\)`,
		`\[the site\]\(https://example\.org\)`,
		`\[inputs\]\(#inputs\)`,
		"```\n!\\[not rewritten\\]\\(thumbnails/flow\\.png\\)\n```",
		"`!\\[kept\\]\\(a\\.png\\)`",
		`<img src="/api/workflows/item/asset\?w=gitlab-flow&path=shared%2Flogo\.png"`,
	} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("no %s in\n%s", re, out)
		}
	}
	if ResolveInRepo("README.md", "../outside.png") != "" {
		t.Error("nothing above the repository root")
	}
}

func TestAccountWorkflowReadmeWithTheViewerKey(t *testing.T) {
	f, net := readmeFixture(t)
	a := f.do("GET", "/api/workflows/item/readme?w=gitlab-flow", nil)
	md := jsStr(jsonv.Path(a.json, "markdown"))
	if !strings.HasPrefix(md, "# Sweep") || !strings.Contains(md, "gitlab.example.org/team/flows main sweep/README.md") {
		t.Fatal(string(a.body))
	}
	eq(t, "web", jsonv.Path(a.json, "web"), `"https://gitlab.example.org/team/flows/-/blob/main/sweep/README.md"`)
	for _, s := range net.reset() {
		if s.Auth != "Bearer viewer-key" {
			t.Error(s)
		}
	}
}

func TestMarketplaceReadmeAndPlatformOnlyHasNone(t *testing.T) {
	f, _ := readmeFixture(t)
	m := f.do("GET", "/api/workflows/item/readme?w=marketplace/naca", nil)
	if !strings.Contains(jsStr(jsonv.Path(m.json, "markdown")), "acme/flows canary workflows/naca/README.md") {
		t.Fatal(string(m.body))
	}
	eq(t, "local", f.do("GET", "/api/workflows/item/readme?w=local-flow", nil).json, `{"markdown":null,"web":null}`)
}

func TestFailedReadmeLookupIsNotRemembered(t *testing.T) {
	f, net := readmeFixture(t)
	good := net.h["platform.test"]
	net.set("platform.test", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) })
	eq(t, "down", f.do("GET", "/api/workflows/item/readme?w=gitlab-flow", nil).json, `{"markdown":null,"web":null}`)
	net.set("platform.test", good)
	if md := jsStr(jsonv.Path(f.do("GET", "/api/workflows/item/readme?w=gitlab-flow", nil).json, "markdown")); !strings.HasPrefix(md, "# Sweep") {
		t.Fatal("asked again once the platform answers:", md)
	}
}

func TestAssetServesRepositoryImagesOnly(t *testing.T) {
	f, _ := readmeFixture(t)
	ok := f.do("GET", "/api/workflows/item/asset?w=gitlab-flow&path=sweep%2Fthumbnails%2Fflow.png", nil)
	if ok.status != 200 || ok.header.Get("Content-Type") != "image/png" || !strings.Contains(ok.header.Get("Content-Security-Policy"), "sandbox") || string(ok.body) != string(testPNG) {
		t.Fatal(ok.status, ok.header)
	}
	for _, p := range []string{"..%2Fsecret.png", "sweep%2Fworkflow.yaml"} {
		if a := f.do("GET", "/api/workflows/item/asset?w=gitlab-flow&path="+p, nil); a.status != 400 {
			t.Error(p, a.status)
		}
	}
}
