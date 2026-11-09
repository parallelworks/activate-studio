package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The last tools ported from Node: read_kb_file and read_original over the
// index's text, the extraction cache and the originals, search_kb's grep
// fallback where there is no GUFI, and search_kb with Settings' search
// engine set to gufi_vt. Each runs on both servers and is compared as the
// other golden scenarios are, and each must run in Go.

// readLibraries writes, beside the main library, docs (indexed, with its
// files), plain (its files, no index) and indexonly (an index, no files).
func readLibraries(t *testing.T, bin, kb string, bases []string) {
	parent := filepath.Dir(kb)
	docs := filepath.Join(parent, "docs")
	copyTree(t, "../gufi/testdata/tree/reports", docs)
	os.WriteFile(filepath.Join(docs, "budget notes.md"), []byte("Budget notes for the docs library.\n"), 0o644)
	docsIndex := indexTree(t, bin, docs, filepath.Join(parent, "docs-ix"))
	plain := filepath.Join(parent, "plain")
	os.MkdirAll(plain, 0o755)
	os.WriteFile(filepath.Join(plain, "file.txt"), []byte("A file of a library with no index.\n"), 0o644)
	only := filepath.Join(parent, "only")
	os.MkdirAll(only, 0o755)
	os.WriteFile(filepath.Join(only, "kept.md"), []byte("Kept in the index only.\n"), 0o644)
	onlyIndex := indexTree(t, bin, only, filepath.Join(parent, "only-ix"))
	for _, base := range bases {
		indexTree(t, bin, kb, filepath.Join(base, "gufi"))
		defs, _ := json.Marshal([]any{
			map[string]any{"id": "docs", "label": "Docs", "indexRoot": docsIndex, "sourceRoot": docs},
			map[string]any{"id": "plain", "label": "Plain", "indexRoot": filepath.Join(parent, "plain-ix", "plain"), "sourceRoot": plain},
			map[string]any{"id": "indexonly", "label": "Index only", "indexRoot": onlyIndex},
		})
		os.WriteFile(filepath.Join(base, "libraries.json"), defs, 0o644)
		os.WriteFile(filepath.Join(base, "indexing.json"), []byte(`{"kb":{"depth":"text"},"docs":{"depth":"text"}}`), 0o644)
		// Extracted text the library keeps for two files the index has no words for.
		os.MkdirAll(filepath.Join(base, "extract", "media"), 0o755)
		os.WriteFile(filepath.Join(base, "extract", "media", "scan.png.txt"), []byte("OCR text of the scan.\n"), 0o644)
		os.WriteFile(filepath.Join(base, "extract", "media", "memo.docx.txt"), []byte("The memo's extracted text.\n"), 0o644)
	}
	// Files added after the index was built: it has no words for them.
	os.MkdirAll(filepath.Join(kb, "media"), 0o755)
	for name, body := range map[string]string{"scan.png": "png", "memo.docx": "docx", "spec.pdf": "%PDF-1.4", "part.stl": "solid x", "late.txt": "Written after the index was built.\n"} {
		os.WriteFile(filepath.Join(kb, "media", name), []byte(body), 0o644)
	}
}

// ranInGoCheck wraps every Go executor to note which ran here, and checks
// at the end that each tool the scenarios call did.
func ranInGoCheck(t *testing.T, scs []scenario) {
	var mu sync.Mutex
	ran := map[string]bool{}
	saved := map[string]func(*Module, *http.Request, map[string]any, ToolCtx) (*Outcome, error){}
	for name, fn := range goExecutors {
		saved[name] = fn
		goExecutors[name] = func(m *Module, r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
			mu.Lock()
			ran[name] = true
			mu.Unlock()
			return fn(m, r, args, ctx)
		}
	}
	t.Cleanup(func() {
		for name, fn := range saved {
			goExecutors[name] = fn
		}
		for _, sc := range scs {
			for _, rp := range sc.replies {
				for _, f := range rp.frames {
					if name, ok := jsonPathString(f, "choices", 0, "delta", "tool_calls", 0, "function", "name"); ok && !ran[name] {
						t.Errorf("%s did not run in Go", name)
					}
				}
			}
		}
	})
}

func TestGoldenReadToolsMatchNode(t *testing.T) {
	if os.Getenv("STUDIO_GOLDEN_NODE") == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	bin := corpusGufiBin(t)
	g := startGoldenWith(t, func(kb string, bases []string) {
		copyTree(t, "../gufi/testdata/tree", kb)
		readLibraries(t, bin, kb, bases)
	})
	long := strings.Repeat("The file says what the reply sums up. ", 5)
	scs := []scenario{
		{name: "indexed text", text: "read the budget", replies: []reply{call("read_kb_file", `{"path":"reports/2026/budget-q1.md"}`),
			call("read_kb_file", `{"path":"/reports/2026/budget-q1.md","offset":"12"}`), call("read_kb_file", `{"path":"budget notes.md","library":"docs"}`),
			call("read_kb_file", `{"path":"kept.md","library":"indexonly"}`), text(long)}},
		{name: "several files", text: "read them all", replies: []reply{
			call("read_kb_file", `{"paths":["README.txt","notes/café.txt","missing.md","../x","",null,"media/scan.png"]}`), call("read_original", `{"paths":["README.txt","code/big.bin","gone.txt"]}`), text(long)}},
		{name: "files the index has no words for", text: "read the media", replies: []reply{
			call("read_kb_file", `{"path":"media/scan.png"}`), call("read_kb_file", `{"path":"media/memo.docx"}`), call("read_kb_file", `{"path":"media/spec.pdf"}`),
			call("read_kb_file", `{"path":"media/part.stl"}`), call("read_kb_file", `{"path":"media/late.txt"}`), call("read_kb_file", `{"path":"code/big.bin"}`),
			call("read_kb_file", `{"path":"nowhere/gone.txt"}`), text(long)}},
		{name: "originals", text: "open the originals", replies: []reply{
			call("read_original", `{"path":"README.txt"}`), call("read_original", `{"path":"media/scan.png"}`), call("read_original", `{"path":"media/spec.pdf"}`),
			call("read_original", `{"path":"code/big.bin"}`), call("read_original", `{"path":"gone.txt"}`), call("read_original", `{"path":"file.txt","library":"plain"}`),
			call("read_original", `{"path":"kept.md","library":"indexonly"}`), text(long)}},
		{name: "reads that cannot run", text: "read wrongly", replies: []reply{
			call("read_kb_file", `{}`), call("read_kb_file", `{"path":"../etc/passwd"}`), call("read_kb_file", `{"path":"a.md","library":"all"}`),
			call("read_kb_file", `{"path":"a.md","library":"nope"}`), call("read_kb_file", `{"path":"file.txt","library":"plain"}`), call("read_original", `{"path":"."}`), text(long)}},
	}
	ranInGoCheck(t, scs)
	for i, sc := range scs {
		t.Run(sc.name, func(t *testing.T) { runScenario(t, g, 200+i, sc) })
	}
}

func TestGoldenGrepFallbackMatchesNode(t *testing.T) {
	if os.Getenv("STUDIO_GOLDEN_NODE") == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	noGufi := t.TempDir()
	g := startGoldenEnv(t, func(kb string, _ []string) {
		copyTree(t, "../gufi/testdata/tree", kb)
	}, map[string]string{"GUFI_BIN": noGufi}, nil)
	long := strings.Repeat("Grep found what the reply sums up. ", 5)
	scs := []scenario{
		{name: "grep fallback", text: "find the budget", replies: []reply{call("search_kb", `{"query":"budget"}`), call("search_kb", `{"query":"budget","limit":1}`),
			call("search_kb", `{"text":"forecast"}`), call("search_kb", `{"name":"onboarding"}`), call("search_kb", `{"query":"zebraquagga"}`), text(long)}},
		{name: "reading without an index", text: "read it", replies: []reply{call("read_kb_file", `{"path":"README.txt"}`), call("read_original", `{"path":"notes/onboarding.md"}`), text(long)}},
	}
	ranInGoCheck(t, scs)
	for i, sc := range scs {
		t.Run(sc.name, func(t *testing.T) { runScenario(t, g, 300+i, sc) })
	}
}

func TestGoldenGufiVtSearchMatchesNode(t *testing.T) {
	if os.Getenv("STUDIO_GOLDEN_NODE") == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	bin := corpusGufiBin(t)
	g := startGoldenEnv(t, func(kb string, bases []string) {
		copyTree(t, "../gufi/testdata/tree", kb)
		readLibraries(t, bin, kb, bases)
	}, map[string]string{"STUDIO_SEARCH_ENGINE": "gufi_vt"}, nil)
	long := strings.Repeat("Every library answered. ", 5)
	// The commands traced differ where this host's GUFI has gufi_vt: Node
	// runs one statement, Go its own (search.Engine.SearchScope); the hits
	// the model reads are compared.
	scs := []scenario{
		{name: "search every library with gufi_vt", text: "budget everywhere", requestsOnly: true, replies: []reply{
			call("search_kb", `{"query":"budget","library":"all"}`), call("search_kb", `{"query":"budget","library":"docs"}`), text(long)}},
	}
	ranInGoCheck(t, scs)
	for i, sc := range scs {
		t.Run(sc.name, func(t *testing.T) { runScenario(t, g, 400+i, sc) })
	}
}

// A pw CLI whose ssh runs the session's command here, under the remote
// host's HOME, as the platform's ssh would on the cluster.
const fakePwSSH = `#!/bin/sh
if [ "$1" = ssh ]; then
  shift
  [ "$1" = --platform-host ] && shift 2
  shift
  HOME="$FAKE_REMOTE_HOME" exec /bin/sh -c "$1"
fi
echo "pw: not here: $*" >&2
exit 1
`

func TestGoldenRemoteLibraryMatchesNode(t *testing.T) {
	if os.Getenv("STUDIO_GOLDEN_NODE") == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	bin := corpusGufiBin(t)
	host := t.TempDir()
	src := filepath.Join(host, "proj")
	copyTree(t, "../gufi/testdata/tree/notes", filepath.Join(src, "notes"))
	os.WriteFile(filepath.Join(src, "readme.md"), []byte("The project on vega.\n"), 0o644)
	os.WriteFile(filepath.Join(src, "notes", ".draft"), []byte("hidden\n"), 0o644)
	index := indexTree(t, bin, src, filepath.Join(host, "idx"))
	cli := filepath.Join(host, "pw")
	os.WriteFile(cli, []byte(fakePwSSH), 0o755)
	g := startGoldenEnv(t, func(kb string, bases []string) {
		def, _ := json.Marshal([]any{map[string]any{"id": "u-vegaproj", "label": "Vega project", "site": "vega", "resource": "vega", "sourceDir": src, "sourceRoot": src,
			"indexRoot": index, "gufiBin": bin, "state": "ready", "run": nil, "error": nil, "builtAt": "2026-11-01T10:00:00.000Z", "directories": 2, "files": 4, "scheduler": nil, "fullText": true}})
		for _, base := range bases {
			own := filepath.Join(base, "users", ownerHashForTest("single"))
			os.MkdirAll(own, 0o755)
			os.WriteFile(filepath.Join(own, "libraries.json"), def, 0o644)
			os.WriteFile(filepath.Join(own, "owner"), []byte("single"), 0o644)
		}
	}, map[string]string{"PW_CLI": cli, "FAKE_REMOTE_HOME": host}, nil)
	long := strings.Repeat("The library on vega answered. ", 5)
	// The GUFI commands run over the session; the traces name the same
	// commands and differ in their timing and session lines, so the
	// scenario compares what the model reads.
	scs := []scenario{
		{name: "a library on another system", text: "what is on vega?", requestsOnly: true, replies: []reply{
			call("list_libraries", `{}`), call("list_kb_dir", `{"library":"u-vegaproj"}`),
			call("read_kb_file", `{"library":"u-vegaproj","path":"readme.md"}`), call("read_original", `{"library":"u-vegaproj","path":"notes/onboarding.md"}`),
			call("read_original", `{"library":"u-vegaproj","path":"gone.md"}`), call("search_kb", `{"query":"onboard","library":"u-vegaproj"}`), text(long)}},
	}
	ranInGoCheck(t, scs)
	for i, sc := range scs {
		t.Run(sc.name, func(t *testing.T) { runScenario(t, g, 500+i, sc) })
	}
	// Hidden names are left out, as a listing here leaves them out; Node's
	// tool lists them (its tree route does not), so this is Go's alone.
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	out, ok := g.module.executeInGo(r, "list_kb_dir", `{"library":"u-vegaproj","path":"notes"}`, ToolCtx{})
	if !ok || strings.Contains(out.Result, ".draft") || !strings.Contains(out.Result, "notes/onboarding.md") {
		t.Errorf("remote listing with a hidden name: %+v", out)
	}
}

func ownerHashForTest(owner string) string {
	s := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(s[:])[:16]
}
