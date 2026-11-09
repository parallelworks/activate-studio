package settings

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type goldenCase struct {
	Name  string            `json:"name"`
	Auth  bool              `json:"auth"`
	Env   map[string]string `json:"env"`
	Saved json.RawMessage   `json:"saved"`
}

// TestEffectiveMatchesNode compares Effective with effectiveSettings() of
// the Node server on the same cases (testdata/effective.golden.json,
// written by scripts/golden/settings.mjs).
func TestEffectiveMatchesNode(t *testing.T) {
	var cases []goldenCase
	readJSON(t, "testdata/cases.json", &cases)
	var golden []struct {
		Name      string          `json:"name"`
		Effective json.RawMessage `json:"effective"`
	}
	readJSON(t, "testdata/effective.golden.json", &golden)
	if len(golden) != len(cases) {
		t.Fatalf("%d golden values for %d cases: regenerate with scripts/golden/settings.mjs", len(golden), len(cases))
	}
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			dir := t.TempDir()
			if string(c.Saved) != "null" {
				os.WriteFile(filepath.Join(dir, "settings.json"), c.Saved, 0o644)
			}
			s := Open(dir, "/somewhere/kb", c.Auth, func(k string) (string, bool) { v, ok := c.Env[k]; return v, ok })
			got, _ := json.Marshal(s.Effective())
			if !jsonEqual(t, got, golden[i].Effective) {
				t.Errorf("Go:   %s\nNode: %s", got, golden[i].Effective)
			}
		})
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		t.Fatal("not JSON")
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

func readJSON(t *testing.T, file string, v any) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// TestGoldenIsCurrent regenerates the golden file from the built Node
// server when STUDIO_GOLDEN_NODE names a node binary, and fails when it
// has drifted from the committed one.
func TestGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../scripts/golden/settings.mjs", "testdata/cases.json", "../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/effective.golden.json")
	if !jsonEqual(t, out, want) {
		t.Fatal("Node's effectiveSettings changed: regenerate testdata/effective.golden.json")
	}
}

func TestRereadOnChangeAndKeepLastGoodRead(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "settings.json")
	s := Open(dir, "/kb", false, func(string) (string, bool) { return "", false })
	if s.Effective().AppName != "Studio" {
		t.Fatal("default name")
	}
	os.WriteFile(file, []byte(`{"appName":"One"}`), 0o644)
	if s.Effective().AppName != "One" {
		t.Fatal("not reread after a write")
	}
	tmp := file + ".x"
	os.WriteFile(tmp, []byte(`{"appName":"Two"}`), 0o644)
	os.Rename(tmp, file)
	if s.Effective().AppName != "Two" {
		t.Fatal("not reread after a rename of the same size")
	}
	os.WriteFile(file, []byte(`{"appName": "Thr`), 0o644)
	if s.Effective().AppName != "Two" {
		t.Fatal("a half-written file replaced the last good read")
	}
	os.Remove(file)
	if s.Effective().AppName != "Studio" {
		t.Fatal("a removed file still counts")
	}
}

func TestUpdateKeepsOtherKeysAndRenames(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "settings.json")
	os.WriteFile(file, []byte(`{"appName":"Kept","futureKey":{"a":1}}`), 0o644)
	s := Open(dir, "/kb", false, func(string) (string, bool) { return "", false })
	if err := s.Update(map[string]any{"kbLabel": "Docs", "appName": nil}); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	b, _ := os.ReadFile(file)
	json.Unmarshal(b, &m)
	if m["kbLabel"] != "Docs" || m["appName"] != nil || m["futureKey"] == nil {
		t.Fatalf("saved %s", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("left %d files", len(ents))
	}
	if s.Effective().KbLabel != "Docs" {
		t.Fatal("own write not seen")
	}
}
