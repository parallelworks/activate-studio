package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libapi"
)

// labelGateway answers every labelling request with one label per file
// it was shown, "from-<library>", and keeps the prompts it was sent.
type labelGateway struct {
	label   string
	mu      sync.Mutex
	prompts []string
}

var pathLine = regexp.MustCompile(`(?m)^PATH: (.*)$`)

func (g *labelGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"test-model"}]}`)
		return
	}
	var body struct {
		Messages []struct{ Content string } `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	user := body.Messages[len(body.Messages)-1].Content
	g.mu.Lock()
	g.prompts = append(g.prompts, user)
	label := g.label
	g.mu.Unlock()
	var rows []map[string]any
	for _, m := range pathLine.FindAllStringSubmatch(user, -1) {
		rows = append(rows, map[string]any{"path": m[1], "labels": []string{label}, "why": "test"})
	}
	b, _ := json.Marshal(rows)
	w.Header().Set("Content-Type", "text/event-stream")
	for _, f := range []any{delta(map[string]any{"content": string(b)}), finish("stop")} {
		fb, _ := json.Marshal(f)
		fmt.Fprintf(w, "data: %s\n\n", fb)
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

// remoteStandIn is a viewer's library on another system as libapi
// resolves it: its folders from the index there, its files read there.
type remoteStandIn struct {
	lib   kbpkg.Library
	tree  map[string][]kbpkg.Entry
	files map[string]string
}

func (s remoteStandIn) Library(r *http.Request, id string) (kbpkg.Library, error) {
	if id == s.lib.ID {
		return s.lib, nil
	}
	return kbpkg.Library{}, kbpkg.Error(404, "no library named %s", id)
}
func (s remoteStandIn) All(*http.Request) ([]kbpkg.Library, error) {
	return []kbpkg.Library{{ID: kbpkg.PrimaryID, Primary: true}, s.lib}, nil
}
func (s remoteStandIn) Tree(_ *http.Request, _ kbpkg.Library, rel string) ([]kbpkg.Entry, error) {
	return s.tree[rel], nil
}
func (s remoteStandIn) File(_ *http.Request, _ kbpkg.Library, rel string) (*kbpkg.Obj, error) {
	if c, ok := s.files[rel]; ok {
		return kbpkg.O("path", rel, "content", c), nil
	}
	return nil, kbpkg.Error(404, "no such file")
}
func (s remoteStandIn) Download(*http.Request, kbpkg.Library, string) (string, []byte, error) {
	return "", nil, kbpkg.Error(404, "no")
}
func (s remoteStandIn) Aggregate(context.Context, string, []string) (string, error) { return "", nil }
func (s remoteStandIn) BuiltAt(string) string                                       { return "" }

func labelTestServer(t *testing.T) (dir string, srv *httptest.Server, gw *labelGateway) {
	dir = t.TempDir()
	kbRoot, ix := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	other := filepath.Join(dir, "other")
	for rel, text := range map[string]string{
		"main.md":           "in the main library",
		"alpha.md":          "alpha text in the other library",
		"sub/beta.md":       "beta text",
		"labelled/gamma.md": "gamma text",
		".hidden/skip.md":   "never read",
	} {
		root := other
		if rel == "main.md" {
			root = kbRoot
		}
		os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte(text), 0o644)
	}
	os.MkdirAll(ix, 0o755)
	// Indexed, as a library added in Settings is once built.
	os.MkdirAll(filepath.Join(dir, "other-index"), 0o755)
	os.WriteFile(filepath.Join(dir, "other-index", "db.db"), nil, 0o644)
	defs, _ := json.Marshal([]map[string]any{
		{"id": "other", "label": "Other", "indexRoot": filepath.Join(dir, "other-index"), "sourceRoot": other},
		{"id": "idxonly", "label": "Index only", "indexRoot": filepath.Join(dir, "idx-only"), "sourceRoot": nil},
	})
	os.WriteFile(filepath.Join(ix, "libraries.json"), defs, 0o644)
	env := map[string]string{"INDEX_BASE": ix, "KB_ROOT": kbRoot, "STUDIO_NODE": "off", "PW_API_KEY": "k", "AI_ENABLED": "1",
		"HOME": dir, "GUFI_BIN": filepath.Join(dir, "no-gufi"), "STUDIO_WEB_DIST": filepath.Join(dir, "web")}
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	prevResolver, prevKnown := kbpkg.LibraryResolver, kbpkg.RemoteStatsKnown
	t.Cleanup(func() { kbpkg.LibraryResolver, kbpkg.RemoteStatsKnown = prevResolver, prevKnown })
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	libapi.Register(mux, a)
	Register(mux, a)
	gw = &labelGateway{label: "from-other"}
	gs := httptest.NewServer(gw)
	t.Cleanup(gs.Close)
	Default.setAccess(&Access{GatewayBase: gs.URL + "/v1", DeploymentKey: "k"})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return dir, srv, gw
}

func proposalPaths(out map[string]any) []string {
	var paths []string
	ps, _ := out["proposals"].([]any)
	for _, p := range ps {
		paths = append(paths, p.(map[string]any)["path"].(string))
	}
	sort.Strings(paths)
	return paths
}

// TestLabelSuggestionsInAnotherLibrary is the Library view's Suggest
// labels on a library other than the main one: it walks that library's
// folder, reads its files, leaves out what the Studio already keeps a
// label for, and the labels applied from it are kept for that library and
// show in its labels, Details and the vocabulary.
func TestLabelSuggestionsInAnotherLibrary(t *testing.T) {
	_, srv, gw := labelTestServer(t)
	code, out, _ := send(t, "POST", srv.URL+"/api/kb/tags", `{"paths":["labelled"],"add":["kept"],"library":"other"}`)
	if code != 200 {
		t.Fatalf("label a folder: %d %v", code, out)
	}

	code, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"","limit":40,"library":"other","model":"m"}`)
	if code != 200 {
		t.Fatalf("suggest: %d %v", code, out)
	}
	if got := proposalPaths(out); !slices.Equal(got, []string{"alpha.md", "sub/beta.md"}) {
		t.Errorf("proposals %v", got)
	}
	if out["considered"] != float64(2) {
		t.Errorf("considered %v", out["considered"])
	}
	// The vocabulary is every library's, the other library's own included.
	if v, _ := json.Marshal(out["vocabulary"]); string(v) != `[{"count":1,"tag":"kept"}]` {
		t.Errorf("vocabulary %s", v)
	}
	if len(gw.prompts) != 1 || !strings.Contains(gw.prompts[0], "TEXT: alpha text in the other library") || strings.Contains(gw.prompts[0], "main.md") {
		t.Errorf("prompt %q", gw.prompts)
	}

	// A folder inside it, as All mode sends it (its library, the path inside).
	code, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"sub","library":"other","model":"m"}`)
	if code != 200 || !slices.Equal(proposalPaths(out), []string{"sub/beta.md"}) {
		t.Errorf("sub folder: %d %v", code, out)
	}
	code, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"other/sub","library":"all","model":"m"}`)
	if code != 200 || !slices.Equal(proposalPaths(out), []string{"sub/beta.md"}) {
		t.Errorf("all, prefixed: %d %v", code, out)
	}
	// No library, or all at the top: the main library, as before.
	code, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"","model":"m"}`)
	if code != 200 || !slices.Equal(proposalPaths(out), []string{"main.md"}) {
		t.Errorf("main: %d %v", code, out)
	}
	code, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"","library":"all","model":"m"}`)
	if code != 200 || !slices.Equal(proposalPaths(out), []string{"main.md"}) {
		t.Errorf("all at the top: %d %v", code, out)
	}
	// A library with no files here says so; one no one has is a 404.
	code, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"","library":"idxonly","model":"m"}`)
	if code != 409 || out["error"] != "Index only has no files on this host, so labels cannot be suggested for it" {
		t.Errorf("index only: %d %v", code, out)
	}
	code, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"","library":"nobody","model":"m"}`)
	if code != 404 {
		t.Errorf("unknown library: %d %v", code, out)
	}

	// Applying a proposal as the Library view does: kept for that library.
	code, out, _ = send(t, "POST", srv.URL+"/api/kb/tags", `{"paths":["alpha.md"],"add":["from-other"],"remove":[],"library":"other"}`)
	if code != 200 || out["stored"] != "studio" {
		t.Fatalf("apply: %d %v", code, out)
	}
	tags := get(t, srv.URL+"/api/kb/tags?path=alpha.md&library=other")
	if v, _ := json.Marshal(tags["own"]); string(v) != `["from-other"]` || tags["stored"] != "studio" {
		t.Errorf("kb/tags %v", tags)
	}
	if main := get(t, srv.URL+"/api/kb/tags?path=alpha.md"); len(main["own"].([]any)) != 0 {
		t.Errorf("leaked to the main library: %v", main)
	}
	vocab := get(t, srv.URL+"/api/tags")
	if v, _ := json.Marshal(vocab["tags"]); !strings.Contains(string(v), `{"count":1,"tag":"from-other"}`) {
		t.Errorf("/api/tags %s", v)
	}
	details := get(t, srv.URL+"/api/kb/details?path=alpha.md&library=other")
	if v, _ := json.Marshal(details["labels"]); !strings.Contains(string(v), "from-other") {
		t.Errorf("details labels %s (%v)", v, details)
	}
	// Labelled now, so a second pass leaves it out.
	_, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"","library":"other","model":"m"}`)
	if got := proposalPaths(out); !slices.Equal(got, []string{"sub/beta.md"}) {
		t.Errorf("after applying: %v", got)
	}
}

// TestLabelSuggestionsOnAnotherSystem reads a viewer's library on another
// system through kb's resolver, as the tree and the file view do, and its
// applied labels count in /api/tags.
func TestLabelSuggestionsOnAnotherSystem(t *testing.T) {
	_, srv, gw := labelTestServer(t)
	remoteLib := kbpkg.Library{ID: "u-proj", Label: "Proj", IndexRoot: "remote:0123456789abcdef:u-proj", Site: "cluster"}
	kbpkg.LibraryResolver = remoteStandIn{
		lib: remoteLib,
		tree: map[string][]kbpkg.Entry{
			"":     {{Name: "readme.md", Path: "readme.md", Type: "file"}, {Name: "data", Path: "data", Type: "dir"}, {Name: ".git", Path: ".git", Type: "dir"}},
			"data": {{Name: "run.md", Path: "data/run.md", Type: "file"}},
			".git": {{Name: "config", Path: ".git/config", Type: "file"}},
		},
		files: map[string]string{"readme.md": "remote readme text", "data/run.md": "remote run text"},
	}
	gw.label = "from-remote"
	code, out, _ := send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"","library":"u-proj","model":"m"}`)
	if code != 200 || !slices.Equal(proposalPaths(out), []string{"data/run.md", "readme.md"}) {
		t.Fatalf("remote: %d %v", code, out)
	}
	if !strings.Contains(gw.prompts[0], "TEXT: remote readme text") || !strings.Contains(gw.prompts[0], "TEXT: remote run text") {
		t.Errorf("prompt %q", gw.prompts[0])
	}
	code, out, _ = send(t, "POST", srv.URL+"/api/kb/tags", `{"paths":["readme.md"],"add":["from-remote"],"library":"u-proj"}`)
	if code != 200 || out["stored"] != "studio" {
		t.Fatalf("apply: %d %v", code, out)
	}
	vocab := get(t, srv.URL+"/api/tags")
	if v, _ := json.Marshal(vocab["tags"]); !strings.Contains(string(v), `{"count":1,"tag":"from-remote"}`) {
		t.Errorf("/api/tags %s", v)
	}
	_, out, _ = send(t, "POST", srv.URL+"/api/kb/label-suggestions", `{"dir":"","library":"u-proj","model":"m"}`)
	if got := proposalPaths(out); !slices.Equal(got, []string{"data/run.md"}) {
		t.Errorf("after applying: %v", got)
	}
}
