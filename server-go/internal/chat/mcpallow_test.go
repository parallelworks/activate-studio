package chat

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// What is allowed on a machine is chatReadTools by name, less those that
// can do more than read.
func TestMachineRulesOnlyRead(t *testing.T) {
	got := machineRules([]string{"search_kb", "delegate", "apply_labels", "hpc_status", "pw_help", "agent_sites", "query_corpus", "search_kb"})
	if want := []string{"mcp__studio__query_corpus", "mcp__studio__search_kb"}; !slices.Equal(got, want) {
		t.Errorf("rules %v, want %v", got, want)
	}
	for _, r := range machineRulesAll() {
		n := strings.TrimPrefix(r, studioToolPrefix)
		if !chatReadTools[n] || machineAsks[n] || chatNoAsk[n] || strings.ContainsAny(r, "*( ") {
			t.Errorf("%s is allowed", r)
		}
	}
	for n := range machineAsks {
		if !chatReadTools[n] {
			t.Errorf("%s is not one of chatReadTools any more", n)
		}
	}
}

// rulesOn runs bridgeRules on a settings file under a scratch home, with
// each awk here in turn.
func rulesOn(t *testing.T, awk, home, act string, rules ...string) (int, string) {
	t.Helper()
	bin := t.TempDir()
	if p, err := exec.LookPath(awk); err != nil {
		t.Skip(awk + " is not installed")
	} else if awk == "busybox" {
		os.WriteFile(filepath.Join(bin, "awk"), []byte("#!/bin/sh\nexec "+p+" awk \"$@\"\n"), 0o755)
	} else {
		os.Symlink(p, filepath.Join(bin, "awk"))
	}
	cmd := exec.Command("sh", "-c", bridgeRules, "studio-rules", act, strings.Join(rules, " "), allowAwk)
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.Run()
	return parseBridgeRules(remote.Result{Code: cmd.ProcessState.ExitCode(), Stdout: out.String(), Stderr: errOut.String()})
}

func TestRulesInASettingsFile(t *testing.T) {
	a, b := "mcp__studio__list_kb_dir", "mcp__studio__search_kb"
	for _, awk := range []string{"awk", "gawk", "mawk", "busybox"} {
		t.Run(awk, func(t *testing.T) {
			for _, c := range []struct{ name, in, why string }{
				{"empty object", `{}`, ""},
				{"no permissions", "{\n  \"model\": \"m\",\n  \"mcpServers\": {\"x\": {\"env\": {\"permissions\": \"p\", \"allow\": [1]}}}\n}\n", ""},
				{"empty permissions", `{"permissions": {}}`, ""},
				{"only deny", `{"permissions": {"deny": ["Bash(rm *)"]}, "a": 1}`, ""},
				{"empty allow", `{"permissions": {"allow": [ ]}}`, ""},
				{"one already", "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(git status *)\",\n      \"" + b + "\"\n    ],\n    \"deny\": []\n  }\n}", ""},
				{"odd text", `{"q": "a \"} ] , \\", "u": "éé ☃", "n": [-1.5e+3, null, true, [], {}], "permissions": {"future": {"allow": ["x"]}, "allow": ["W(\"a,]\")", "\\"]}}`, ""},
				{"not JSON", `{"a": }`, "not valid JSON"},
				{"cut short", `{"permissions": {"allow": ["a"`, "not valid JSON"},
				{"trailing", `{} {}`, "not valid JSON"},
				{"trailing comma", `{"a": 1,}`, "not valid JSON"},
				{"comment", "{\n// x\n\"a\": 1}", "not valid JSON"},
				{"an array", `[]`, "not a JSON object"},
				{"allow not a list", `{"permissions": {"allow": "x"}}`, "in a form"},
				{"permissions null", `{"permissions": null}`, "in a form"},
				{"permissions twice", `{"permissions": {}, "permissions": {}}`, "in a form"},
				{"another case", `{"Permissions": {"allow": []}}`, "in a form"},
				{"an escaped key", `{"permission\u0073": {"allow": []}}`, "in a form"},
			} {
				t.Run(c.name, func(t *testing.T) {
					home := t.TempDir()
					f := filepath.Join(home, ".config", "pw", "code.json")
					os.MkdirAll(filepath.Dir(f), 0o755)
					os.WriteFile(f, []byte(c.in), 0o600)
					n, why := rulesOn(t, awk, home, "add", a, b)
					got, _ := os.ReadFile(f)
					if c.why != "" {
						if !strings.Contains(why, c.why) || string(got) != c.in {
							t.Fatalf("why %q; file %s", why, got)
						}
						return
					}
					var before, after map[string]any
					json.Unmarshal([]byte(c.in), &before)
					if err := json.Unmarshal(got, &after); err != nil || why != "" {
						t.Fatalf("%v %s: %s", err, why, got)
					}
					allow := func(m map[string]any) (out []string) {
						p, _ := m["permissions"].(map[string]any)
						l, _ := p["allow"].([]any)
						for _, x := range l {
							out = append(out, x.(string))
						}
						return
					}
					want := allow(before)
					for _, r := range []string{a, b} {
						if !slices.Contains(want, r) {
							want = append(want, r)
						}
					}
					if !slices.Equal(allow(after), want) || n != len(want)-len(allow(before)) {
						t.Errorf("allow %q (%d changed), want %q", allow(after), n, want)
					}
					rest := func(m map[string]any) map[string]any {
						if p, ok := m["permissions"].(map[string]any); ok {
							delete(p, "allow")
							if len(p) == 0 {
								delete(m, "permissions")
							}
						}
						return m
					}
					keep := allow(before)
					if !reflect.DeepEqual(rest(before), rest(after)) {
						t.Errorf("the rest changed: %s", got)
					}
					if n, why := rulesOn(t, awk, home, "add", a, b); n != 0 || why != "" {
						t.Errorf("again: %d %q", n, why)
					}
					if again, _ := os.ReadFile(f); string(again) != string(got) {
						t.Errorf("written again: %s", again)
					}
					if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
						t.Errorf("mode %v", fi.Mode())
					}
					// Taken back: no rule of the Studio's is left, every other is.
					if _, why := rulesOn(t, awk, home, "remove", a, b); why != "" {
						t.Fatal(why)
					}
					got, _ = os.ReadFile(f)
					after = nil
					if err := json.Unmarshal(got, &after); err != nil {
						t.Fatalf("%v: %s", err, got)
					}
					keep = slices.DeleteFunc(keep, func(s string) bool { return s == a || s == b })
					if !slices.Equal(allow(after), keep) {
						t.Errorf("after remove %q, want %q", allow(after), keep)
					}
					if left, _ := filepath.Glob(f + ".??????"); len(left) != 0 {
						t.Errorf("left behind: %v", left)
					}
				})
			}
		})
	}
}

// A file that is not there is not made (pw would stop reading older
// settings for it), one that is a link stays one, and a settings file pw
// is writing is left alone.
func TestRulesLeaveOtherFilesAlone(t *testing.T) {
	home := t.TempDir()
	f := filepath.Join(home, ".config", "pw", "code.json")
	if n, why := rulesOn(t, "awk", home, "add", "mcp__studio__search_kb"); n != 0 || why == "" {
		t.Errorf("no file: %d %q", n, why)
	}
	if n, why := rulesOn(t, "awk", home, "remove", "mcp__studio__search_kb"); n != 0 || why != "" {
		t.Errorf("no file, remove: %d %q", n, why)
	}
	if _, err := os.Stat(f); err == nil {
		t.Fatal("made one")
	}
	os.MkdirAll(filepath.Dir(f), 0o755)
	real := filepath.Join(home, "dotfiles.json")
	os.WriteFile(real, []byte("{}"), 0o640)
	os.Symlink(real, f)
	if n, why := rulesOn(t, "awk", home, "add", "mcp__studio__search_kb"); n != 1 || why != "" {
		t.Fatalf("link: %d %q", n, why)
	}
	if fi, _ := os.Lstat(f); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced")
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), "mcp__studio__search_kb") {
		t.Errorf("not written: %s", b)
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", fi.Mode())
	}
}

func removeAs(t *testing.T, m *Module, u *auth.User, body string) (int, map[string]any) {
	t.Helper()
	r := setupReq(u)
	r.Body = io.NopCloser(strings.NewReader(body))
	w := httptest.NewRecorder()
	err := m.bridgeRemove(w, r)
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Status, map[string]any{"error": ae.Message}
	} else if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// Setting up allows the tools that read, of those the person is listed, and
// nothing else; a machine set up before gets them on its next Check, and
// not before.
func TestSetUpAllowsTheToolsThatRead(t *testing.T) {
	m, f, _ := setupStudio(t)
	code, out := setupAs(t, m, bobProxy, `{"machineId":"m-bob"}`)
	if code != 200 || out["worked"] != true || out["allowWhy"] != "" {
		t.Fatalf("%d %v", code, out)
	}
	if !slices.Contains(f.rules, "mcp__studio__search_kb") || !slices.Contains(f.rules, "mcp__studio__query_corpus") || int(out["allowed"].(float64)) != len(f.rules) {
		t.Errorf("rules %v, out %v", f.rules, out["allowed"])
	}
	listed := specNames(m.personTools(setupReq(bobProxy), m.personCtx(setupReq(bobProxy))))
	for _, x := range f.rules {
		n := strings.TrimPrefix(x, studioToolPrefix)
		if !chatReadTools[n] || machineAsks[n] || !slices.Contains(listed, n) {
			t.Errorf("%s is allowed", x)
		}
	}
	// Set up by an older Studio: no rules until Check.
	f.rules = []string{"Bash(git status *)"}
	setupAs(t, m, bobProxy, `{"machineId":"m-bob"}`)
	if len(f.rules) != 1 {
		t.Errorf("opened again without Check: %v", f.rules)
	}
	if _, out := setupAs(t, m, bobProxy, `{"machineId":"m-bob","check":true}`); out["changed"] != false || len(f.rules) < 5 {
		t.Errorf("after Check %v: %v", out, f.rules)
	}
	// Settings that cannot take them do not undo the setup.
	f.rules, f.rulesWhy = nil, "its settings file is not valid JSON"
	if code, out := setupAs(t, m, bobProxy, `{"machineId":"m-bob","check":true}`); code != 200 || out["worked"] != true || out["allowWhy"] != f.rulesWhy {
		t.Errorf("%d %v", code, out)
	}
}

// Remove takes the rules and the entry back, leaves the person's own
// rules, and the machine is not set up again by itself.
func TestRemoveTakesItBack(t *testing.T) {
	m, f, id := setupStudio(t)
	f.rules = []string{"Bash(git status *)", "mcp__other__x"}
	setupAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`)
	code, out := removeAs(t, m, bobProxy, `{"conversationId":"`+id+`"}`)
	if code != 200 || out["removed"] != true || f.entry != nil || !slices.Equal(f.rules, []string{"Bash(git status *)", "mcp__other__x"}) {
		t.Fatalf("%d %v entry %v rules %v", code, out, f.entry, f.rules)
	}
	if o := m.bridgeOffer(setupReq(bobProxy), bridgeMach{ID: "m-bob", Kind: "cloud", Name: "bobs-cluster", Owner: "bob", Status: "connected"}); o.State != "declined" {
		t.Errorf("offered as %s", o.State)
	}
	if _, out := removeAs(t, m, bobProxy, `{"machineId":"m-bob"}`); out["removed"] != false {
		t.Errorf("again: %v", out)
	}
}

// Remove is the owner's, from the Studio's own pages, and leaves another
// Studio's entry, and one whose rules it could not take back.
func TestRemoveLeavesWhatIsNotItsOwn(t *testing.T) {
	m, f, _ := setupStudio(t)
	setupAs(t, m, bobProxy, `{"machineId":"m-bob"}`)
	if code, _ := removeAs(t, m, bobProxy, `{"machineId":"m-ada"}`); code/100 != 4 {
		t.Errorf("another person's machine: %d", code)
	}
	f.rulesWhy = "its settings file is not valid JSON"
	if code, out := removeAs(t, m, bobProxy, `{"machineId":"m-bob"}`); code != 502 || f.entry == nil {
		t.Errorf("%d %v", code, out)
	}
	f.rulesWhy = ""
	f.entry.Config.Args[3] = "https://platform.example/me/session/cy/other/api/mcp"
	had := len(f.rules)
	if code, _ := removeAs(t, m, bobProxy, `{"machineId":"m-bob"}`); code != 409 || f.entry == nil || len(f.rules) != had {
		t.Errorf("another Studio's: %d, %d rules", code, len(f.rules))
	}
	r := setupReq(bobProxy)
	r.Header.Set("Origin", "https://elsewhere.example")
	r.Body = io.NopCloser(strings.NewReader(`{"machineId":"m-bob"}`))
	if err := m.bridgeRemove(httptest.NewRecorder(), r); err == nil {
		t.Error("another site may remove it")
	}
}
