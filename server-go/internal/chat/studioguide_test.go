package chat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// mcpGuide is the instructions the MCP server gives on initialize, and the
// tools it lists, to one caller.
func mcpGuide(t *testing.T, m *Module, u *auth.User, headers map[string]string) (string, []string) {
	t.Helper()
	code, out := mcpAs(t, m, u, headers, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	res, _ := out["result"].(map[string]any)
	in, _ := res["instructions"].(string)
	if code != 200 || res["serverInfo"] == nil {
		t.Fatalf("initialize: %d %v", code, out)
	}
	return in, listedTools(t, m, u, headers)
}

// namesOnly checks that text names no built-in tool but those in have.
func namesOnly(t *testing.T, text string, have []string) {
	t.Helper()
	for _, n := range BuiltinNames() {
		if strings.Contains(n, "_") && regexp.MustCompile(`\b`+n+`\b`).MatchString(text) && !slices.Contains(have, n) {
			t.Errorf("names %s, which the caller does not get", n)
		}
	}
}

// The MCP server's instructions say which tool to use when, with the names
// of the tools that caller gets and no others, and stay short.
func TestTheMCPServerSaysWhichToolToUse(t *testing.T) {
	m := bridgeStudio(t)
	c := m.Conv.Create(nil, "bob")
	tok, err := m.issueChatToken(bobProxy, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		u       *auth.User
		headers map[string]string
		has     []string
		hasNot  []string
	}{
		{"a chat's session", bobProxy, map[string]string{"Authorization": "Bearer " + tok},
			[]string{"list_libraries", "write_kb_file", "the libraries the person chose for this chat"}, []string{"main library"}},
		{"a person's own session", bobProxy, nil,
			[]string{"list_libraries", "write_kb_file", "the Studio's main library"}, []string{"this chat", "show_in_viewer", "run_workflow"}},
		{"another MCP client", bobOIDC, nil,
			[]string{"read_original", "the Studio's main library"}, []string{"this chat", "list_libraries", "write_kb_file", "edit_kb_file", "show_in_viewer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, listed := mcpGuide(t, m, tc.u, tc.headers)
			for _, s := range append([]string{"search_kb", "query_corpus", "read_kb_file", "not find, grep or ls", "before the shell", "PDFs and Office files"}, tc.has...) {
				if !strings.Contains(in, s) {
					t.Errorf("does not say %q: %s", s, in)
				}
			}
			for _, s := range append([]string{studioToolPrefix}, tc.hasNot...) {
				if strings.Contains(in, s) {
					t.Errorf("says %q: %s", s, in)
				}
			}
			namesOnly(t, in, listed)
			// Claude Code keeps 2 KB of a server's instructions.
			if len(in) > 2000 {
				t.Errorf("%d bytes", len(in))
			}
		})
	}
}

// A tool the administrator turned off is not named, and with none of the
// tools the guide is about there are no instructions.
func TestTheInstructionsLeaveOutAToolTurnedOff(t *testing.T) {
	ix := t.TempDir()
	os.WriteFile(filepath.Join(ix, "settings.json"), []byte(`{"disabledTools":["query_corpus","read_original"]}`), 0o644)
	m, _ := pwModule(t, &fakePW{}, map[string]string{"INDEX_BASE": ix})
	in, listed := mcpGuide(t, m, nil, nil)
	if slices.Contains(listed, "query_corpus") || !slices.Contains(listed, "search_kb") {
		t.Fatalf("listed %v", listed)
	}
	namesOnly(t, in, listed)
	if !strings.Contains(in, "search_kb") {
		t.Errorf("%s", in)
	}
	if in := m.mcpInstructions(nil, false); in != "" {
		t.Errorf("with no tools: %s", in)
	}
}

// The instructions and the chat prompt are the same guide: each of its
// lines, with pw code's names for the tools, is in the prompt.
func TestThePromptAndTheInstructionsAreOneGuide(t *testing.T) {
	m, _ := pwModule(t, &fakePW{}, nil)
	tools := m.chatTools(httptest.NewRequest(http.MethodGet, "/", nil), true)
	prompt := m.pwcodePrompt(tools)
	lines := strings.Split(m.mcpInstructions(tools, true), "\n")
	if len(lines) < 5 || !strings.HasSuffix(lines[0], guideLead) || !strings.Contains(prompt, guideLead) {
		t.Fatalf("%q", lines)
	}
	names := specNames(tools)
	for _, l := range lines[1:] {
		for _, n := range names {
			l = regexp.MustCompile(`\b`+regexp.QuoteMeta(n)+`\b`).ReplaceAllString(l, studioToolPrefix+n)
		}
		if !strings.Contains(prompt, l) {
			t.Errorf("not in the prompt: %s", l)
		}
	}
}

// The descriptions, which a client that keeps no instructions still has,
// say what each tool does that find and grep do not.
func TestTheDescriptionsSayWhatTheyAreBetterAt(t *testing.T) {
	for name, says := range map[string][]string{
		"search_kb":    {"instead of find, grep", "index", "PDFs, Office files", "meaning", "other hosts"},
		"query_corpus": {"instead of find, du or ls", "index", "other hosts"},
		"read_kb_file": {"instead of cat", "index", "PDFs, Office files", "other hosts"},
	} {
		i := slices.IndexFunc(builtinSpecs, func(s ToolSpec) bool { return s.Name == name })
		for _, s := range says {
			if !strings.Contains(builtinSpecs[i].Description, s) {
				t.Errorf("%s does not say %q", name, s)
			}
		}
	}
}
