package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// parityModule is a pw code module whose deployment has its own tools: a
// custom tool, a skill, an attached MCP server, and a custom tool turned
// off.
func parityModule(t *testing.T, env map[string]string) *Module {
	t.Helper()
	ix := t.TempDir()
	crm := httptest.NewServer(http.HandlerFunc(fakeMCPServer))
	t.Cleanup(crm.Close)
	os.MkdirAll(filepath.Join(ix, "extensions", "skills"), 0o755)
	os.WriteFile(filepath.Join(ix, "extensions", "skills", "brief-writer.md"), []byte("---\ndescription: Writes a short brief.\n---\nWrite two paragraphs.\n"), 0o644)
	settings, _ := json.Marshal(map[string]any{
		"customTools":   []any{map[string]any{"name": "greet", "description": "Says hi", "command": "echo hi"}, map[string]any{"name": "wipe", "description": "Off", "command": "true"}},
		"mcpServers":    []any{map[string]any{"name": "crm", "url": crm.URL + "/mcp"}},
		"disabledTools": []any{"wipe"},
	})
	os.WriteFile(filepath.Join(ix, "settings.json"), settings, 0o644)
	e := map[string]string{"INDEX_BASE": ix}
	for k, v := range env {
		e[k] = v
	}
	m, _ := pwModule(t, &fakePW{}, e)
	return m
}

// offeredByTheAssistant is what the Studio assistant's own loop offers r's
// viewer, by name.
func offeredByTheAssistant(t *testing.T, m *Module, r *http.Request) []string {
	t.Helper()
	sp, err := goTools{m}.Specs(r, nil, false, true, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range sp.Available {
		var x struct{ Function struct{ Name string } }
		json.Unmarshal(s, &x)
		out = append(out, x.Function.Name)
	}
	return out
}

// Every tool the Studio assistant offers is offered to a pw code session
// over MCP, except those left out on purpose (calls held for an answer in
// the Studio's chat, and the agent tools, since pw code is the agent):
// the deployment's own tools, use_skill and the attached servers' tools
// included. A tool turned off is off on both.
func TestAPWCodeSessionHasTheAssistantsTools(t *testing.T) {
	m := parityModule(t, nil)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	mine := specNames(m.chatTools(r, true))
	assistant := offeredByTheAssistant(t, m, r)
	for _, n := range []string{"greet", "use_skill", "crm__find_deals", "search_kb", "query_corpus", "read_kb_file", "list_libraries"} {
		if !slices.Contains(assistant, n) {
			t.Fatalf("the assistant is not offered %s: %v", n, assistant)
		}
	}
	for _, n := range assistant {
		if !slices.Contains(mine, n) && !chatLeftOut(n) {
			t.Errorf("the assistant has %s and a pw code session does not", n)
		}
	}
	if slices.Contains(mine, "wipe") || slices.Contains(assistant, "wipe") {
		t.Error("a tool turned off is offered")
	}
	for _, n := range mine {
		if !slices.Contains(assistant, n) {
			t.Errorf("a pw code session has %s, which the assistant is not offered", n)
		}
	}
	// use_skill only reads; a deployment's command and another server's
	// tool ask first, as pw code's permission mode decides.
	var listed map[string]bool = map[string]bool{}
	for _, s := range m.chatTools(r, true) {
		var x struct {
			Annotations struct{ ReadOnlyHint bool } `json:"annotations"`
		}
		json.Unmarshal(chatToolJSON(s), &x)
		listed[s.Name] = x.Annotations.ReadOnlyHint
	}
	if !listed["use_skill"] || listed["greet"] || listed["crm__find_deals"] {
		t.Errorf("read only: %v", listed)
	}
}
