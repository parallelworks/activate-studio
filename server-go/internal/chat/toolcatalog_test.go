package chat

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

func TestPlatformToolHashesMatchTheCatalog(t *testing.T) {
	for name, want := range nodeHashes {
		if got := toolHash(name); got != want {
			t.Errorf("toolHash(%s) = %s, Node's is %s", name, got, want)
		}
	}
	if len(nodeHashes) != len(platformByName) {
		t.Errorf("%d hashes for %d platform tools", len(nodeHashes), len(platformByName))
	}
}

func TestSplitToolArgs(t *testing.T) {
	got := splitToolArgs(`--flag "two words" 'it''s' "" x`)
	want := []string{"--flag", "two words", "its", "", "x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
}

func compactJSON(t *testing.T, b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return buf.String()
}

func toJSON(t *testing.T, v any) string {
	b, err := jsonobj.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return compactJSON(t, b)
}

// TestToolCatalogMatchesNode checks the embedded catalog against Node's,
// and the catalog's uses against tools.ts, platformTools.ts and
// context.ts: the routes, the slash commands, the groups, the tools sent,
// prepared arguments, approval cards, projected results, system prompts.
func TestToolCatalogMatchesNode(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	dir := t.TempDir()
	kb, ix := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	for _, d := range []string{filepath.Join(kb, "notes"), filepath.Join(ix, "extensions", "skills"), filepath.Join(ix, "extensions", "agents"), filepath.Join(ix, "extensions", "tools"), filepath.Join(kb, ".agents")} {
		os.MkdirAll(d, 0o755)
	}
	write := func(p, s string) { os.WriteFile(p, []byte(s), 0o644) }
	write(filepath.Join(kb, conventionsFile), "Write plainly.\n")
	write(filepath.Join(ix, "extensions", "skills", "brief-writer.md"), "---\ndescription: Writes a two page brief from the sources.\n---\nSteps.\n")
	write(filepath.Join(ix, "extensions", "agents", "reviewer.md"), "---\ndescription: Reviews drafts.\n---\nReview hard.\n")
	write(filepath.Join(kb, ".agents", "steward.md"), "---\ndescription: Keeps the library tidy.\n---\nTidy.\n")
	write(filepath.Join(ix, "extensions", "tools", "greet.json"), `{"name":"greet","description":"Says hello","command":"echo hello"}`)
	write(filepath.Join(ix, "settings.json"), `{"customTools":[{"name":"uptime","description":"","command":"uptime"},{"name":"greet","description":"Says hi","command":"echo hi"},{"name":"search_kb","command":"x"}],"disabledTools":["pw_help"],"kbMission":"Support the design team."}`)

	env := map[string]string{
		"INDEX_BASE": ix, "KB_ROOT": kb, "PW_GATEWAY_URL": "http://127.0.0.1:9/v1", "PW_API_KEY": "test-key",
		"PW_CREDENTIALS_DIR": filepath.Join(dir, "nopw"), "PW_CLI": "/bin/false", "HOME": dir, "AI_ENABLED": "1",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	for _, k := range []string{"AUTH_JWKS_URL", "OPENAI_BASE_URL", "OPENAI_API_KEY", "PW_CONTEXT"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	cases := map[string]any{
		"slash":     []string{"/help", "/tools", "/nope", "/nope with text", "/greet hello", "/search_kb pumps", "/brief-writer do it", "/Brief_Writer", "/reviewer", "plain text", "/uptime"},
		"texts":     []string{"run the workflow on the cluster", "please stop cluster c1 now", "label these files", "delegate to agents in parallel", "what is in the report"},
		"used":      []string{"get_labels", "list_clusters"},
		"groupSets": [][]string{{}, {"workflows"}, {"workflows", "changes", "agents", "editing"}},
		"prepare": []map[string]any{
			{"tool": "stop_cluster", "args": `{"clusterName":"c1"}`, "user": "alice"},
			{"tool": "stop_cluster", "args": `{}`, "user": "alice"},
			{"tool": "stop_cluster", "args": `{"clusterName":"c1"}`, "user": nil},
			{"tool": "list_storage", "args": map[string]any{"category": "buckets", "type": []any{"aws-bucket", 3}, "provisioned": "true", "scope": "  mine "}, "user": nil},
			{"tool": "list_storage", "args": map[string]any{"category": "bad"}, "user": nil},
			{"tool": "list_storage", "args": map[string]any{"provisioned": "yes"}, "user": nil},
			{"tool": "cancel_workflow_run", "args": "not json", "user": nil},
			{"tool": "cancel_workflow_run", "args": "", "user": nil},
			{"tool": "start_cluster", "args": `{"user":"bob","clusterName":"c"}`, "user": nil},
			{"tool": "destroy_cluster", "args": map[string]any{"clusterName": 5}, "user": "u"},
			{"tool": "nope", "args": "{}", "user": nil},
		},
		"project": []map[string]any{
			{"tool": "list_storage", "value": []any{map[string]any{"name": "a", "token": "x", "csp": "aws", "region": nil}}},
			{"tool": "list_storage", "value": map[string]any{"$schema": "s", "count": 2, "items": []any{map[string]any{"name": "b", "secret": 1}}, "meta": map[string]any{"x": 1}}},
			{"tool": "cancel_workflow_run", "value": map[string]any{"$schema": "s", "ok": true}},
		},
	}
	in, _ := json.Marshal(cases)
	cmd := exec.Command(node, "../../scripts/golden/chat.mjs", "../../..", "tools")
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": ix, "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	a.Providers.PlatformAvailable = func(*http.Request, string) bool { return false }
	mux := http.NewServeMux()
	Register(mux, a)
	m := Default
	srv := httptest.NewServer(mux)
	defer srv.Close()
	get := func(path string) string {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return compactJSON(t, b)
	}
	check := func(what, got string) {
		t.Helper()
		if w := compactJSON(t, want[what]); got != w {
			t.Errorf("%s differs\n go:   %s\n node: %s", what, got, w)
		}
	}
	check("tools", get("/api/chat/tools"))
	check("options", get("/api/chat/tool-options"))

	var active []string
	for _, s := range activeToolSpecs(m.eff(), m.Ext) {
		active = append(active, s.Name)
	}
	check("active", toJSON(t, active))
	labels := jsonobj.New()
	for _, s := range m.activeToolSpecsWithRemote() {
		labels.SetValue(s.Name, m.toolOptionLabel(s.Name))
	}
	check("labels", toJSON(t, labels))

	var slash []any
	for _, s := range cases["slash"].([]string) {
		text, listing := m.expandSlashCommand(s)
		switch {
		case listing != nil:
			slash = append(slash, reqbody.O("listing", *listing))
		case text != "":
			slash = append(slash, text)
		default:
			slash = append(slash, nil)
		}
	}
	check("slash", toJSON(t, slash))

	var groups [][]string
	for _, s := range cases["texts"].([]string) {
		g := groupOrderList(initialToolGroups(s, cases["used"].([]string)))
		sortStrings(g)
		groups = append(groups, g)
	}
	check("groups", toJSON(t, groups))

	var selected [][]json.RawMessage
	all := m.activeToolSpecsWithRemote()
	for _, gs := range cases["groupSets"].([][]string) {
		set := map[string]bool{}
		for _, g := range gs {
			set[g] = true
		}
		var out []json.RawMessage
		for _, s := range selectToolSpecs(all, set) {
			out = append(out, s.JSON())
		}
		selected = append(selected, out)
	}
	check("selected", toJSON(t, selected))

	var prepared, approvals []any
	for _, c := range cases["prepare"].([]map[string]any) {
		user, _ := c["user"].(string)
		var argIn any = c["args"]
		if _, ok := argIn.(string); !ok {
			b, _ := json.Marshal(argIn)
			argIn = json.RawMessage(b)
		}
		args, canonical, problem := prepareArgs(c["tool"].(string), argIn, user)
		if problem != "" {
			prepared = append(prepared, reqbody.O("problem", problem))
			approvals = append(approvals, nil)
			continue
		}
		prepared = append(prepared, reqbody.O("args", args, "canonical", canonical))
		approvals = append(approvals, approvalPart(c["tool"].(string), args, "call_1", "2026-01-01T00:00:00.000Z"))
	}
	check("prepared", toJSON(t, prepared))
	check("approvals", toJSON(t, approvals))

	var projected []json.RawMessage
	for _, c := range cases["project"].([]map[string]any) {
		b, _ := json.Marshal(c["value"])
		projected = append(projected, projectResult(platformByName[c["tool"].(string)], b))
	}
	check("projected", toJSON(t, projected))

	var prompts []string
	for _, pc := range [][2]bool{{true, true}, {false, true}, {false, false}} {
		r := httptest.NewRequest("GET", "/", nil)
		prompts = append(prompts, m.systemPrompt(r.Context(), pc[0], pc[1]))
	}
	check("prompts", toJSON(t, prompts))
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
