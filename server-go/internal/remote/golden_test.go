package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

type goldenCases struct {
	Options []string `json:"options"`
	Sites   []struct {
		Destination string   `json:"destination"`
		Port        *int     `json:"port"`
		Options     []string `json:"options"`
	} `json:"sites"`
	Parsed []string `json:"parsed"`
	Words  []string `json:"words"`
	Stderr []string `json:"stderr"`
}

func outcome(v any, err error) map[string]any {
	if err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) {
			return map[string]any{"error": ae.Message, "status": ae.Status}
		}
		return map[string]any{"error": err.Error(), "status": nil}
	}
	return map[string]any{"ok": v}
}

func goGolden(t *testing.T) []byte {
	raw, _ := os.ReadFile("testdata/cases.json")
	var c goldenCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	var opts, sites, parsed, names, shells, tags, jumps, words, quiet []any
	for _, o := range c.Options {
		v, err := ParseSSHOptions(o)
		opts = append(opts, outcome(v, err))
	}
	for _, s := range c.Sites {
		port := 0
		if s.Port != nil {
			port = *s.Port
		}
		v, err := SSHSite(s.Destination, port, s.Options)
		sites = append(sites, outcome(v, err))
		tags = append(tags, OptionsTag(s.Options))
		if j := JumpOf(s.Options); j != "" {
			jumps = append(jumps, j)
		} else {
			jumps = append(jumps, nil)
		}
	}
	for _, s := range c.Parsed {
		h, err := ParseSSHSite(s)
		var v any
		if h != nil {
			var port any
			if h.Port != 0 {
				port = h.Port
			}
			v = map[string]any{"destination": h.Destination, "port": port, "options": h.Options}
		}
		parsed = append(parsed, outcome(v, err))
		names = append(names, SiteName(s))
		if _, err := ParseSSHSite(s); err != nil {
			shells = append(shells, outcome(nil, err))
		} else {
			shells = append(shells, outcome(ShellFor(s), nil))
		}
	}
	for _, w := range c.Words {
		words = append(words, ShellWord(w))
	}
	for _, s := range c.Stderr {
		quiet = append(quiet, QuietStderr(s))
	}
	out["options"], out["sites"], out["parsed"], out["names"], out["shells"] = opts, sites, parsed, names, shells
	out["tags"], out["jumps"], out["words"], out["quiet"] = tags, jumps, words, quiet
	out["direct"] = DirectCommand(c.Words)
	out["bootstrap"] = map[string]any{"ssh": BootstrapCommand("ssh://me@box"), "pw": BootstrapCommand("cluster")}
	b, _ := json.Marshal(out)
	return b
}

func normal(t *testing.T, b []byte) any {
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMatchesNodeGolden(t *testing.T) {
	want, _ := os.ReadFile("testdata/golden.json")
	got := goGolden(t)
	g, w := normal(t, got).(map[string]any), normal(t, want).(map[string]any)
	for k := range w {
		if !reflect.DeepEqual(g[k], w[k]) {
			gj, _ := json.MarshalIndent(g[k], "", " ")
			wj, _ := json.MarshalIndent(w[k], "", " ")
			t.Errorf("%s differs:\ngo   %s\nnode %s", k, gj, wj)
		}
	}
}

// TestGoldenIsCurrent regenerates the golden file from the built Node
// server when STUDIO_GOLDEN_NODE names a node binary.
func TestGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../scripts/golden/remote.mjs", "testdata/cases.json", "../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/golden.json")
	if !reflect.DeepEqual(normal(t, out), normal(t, want)) {
		t.Fatal("Node's remoteExec.ts changed: regenerate testdata/golden.json")
	}
}
