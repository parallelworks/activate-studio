package gufi

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLabelConditions(t *testing.T) {
	p := &Planner{IndexRoot: "/ix/kb", Labels: LabelMaps{
		Dirs:  []LabelEntry{{"", []string{"Finance"}}, {"a_b/100%", []string{"Finance", "x"}}, {"other", []string{"hr"}}},
		Files: []LabelEntry{{"f/one.pdf", []string{"FINANCE"}}, {"f/two.pdf", []string{"financial"}}},
	}}
	plan, err := p.Builder(BuilderPayload{Source: "files", Filters: []byte(`[{"field":"label","op":"eq","value":"finance"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	e := plan.Stages.E
	for _, want := range []string{`LIKE '%,finance,%'`, `LIKE '/ix/kb/a\_b/100\%/%' ESCAPE '\'`, `IN ('/ix/kb/f/one.pdf')`} {
		if !strings.Contains(e, want) {
			t.Errorf("missing %s in %s", want, e)
		}
	}
	if strings.Contains(e, "other") || strings.Contains(e, "two.pdf") {
		t.Error(e)
	}
	plan, _ = p.Builder(BuilderPayload{Source: "files", Filters: []byte(`[{"field":"label","op":"contains","value":"financ"}]`)})
	if !strings.Contains(plan.Stages.E, "'/ix/kb/f/one.pdf', '/ix/kb/f/two.pdf'") {
		t.Error(plan.Stages.E)
	}
	// A label on the library's top folder covers all of it.
	p.Labels.Files = append(p.Labels.Files, LabelEntry{"", []string{"finance"}})
	plan, _ = p.Builder(BuilderPayload{Source: "files", Filters: []byte(`[{"field":"label","op":"eq","value":"finance"}]`)})
	if !strings.Contains(plan.Stages.E, "WHERE ((1=1)) ORDER") {
		t.Error(plan.Stages.E)
	}
}

func TestJSNumberAndTruth(t *testing.T) {
	for in, want := range map[any]float64{"": 0, " 12 ": 12, "0x10": 16, "0b11": 3, "1e3": 1000, ".5": 0.5, "-Infinity": math.Inf(-1), true: 1, nil: 0} {
		if got := jsNumber(in); got != want {
			t.Errorf("Number(%v) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []any{"12px", "1_000", "-0x10", "inf", "NaN", map[string]any{}} {
		if got := jsNumber(in); !math.IsNaN(got) {
			t.Errorf("Number(%v) = %v, want NaN", in, got)
		}
	}
	if jsNumber([]any{"7"}) != 7 || truthy("") || truthy(0.0) || !truthy([]any{}) || !truthy("0") {
		t.Error("truth")
	}
	if num(1e21) != "1e+21" && num(1e21) != "1000000000000000000000" {
		t.Error(num(1e21))
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"-n": "-n", "/a/b.c": "/a/b.c", "a b": "'a b'", "it's": `'it'\''s'`, Delim: `$'\x1e'`, "": "''"} {
		if got := ShellQuote(in); got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
}

func TestEnvFor(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	os.MkdirAll(filepath.Join(root, "lib"), 0o755)
	base := []string{"PATH=/usr/bin", "LD_LIBRARY_PATH=/opt/x", "HOME=/h"}
	env := EnvFor(bin, base, map[string]string{"X": "1"}, EnvOptions{GOOS: "linux"})
	for _, want := range []string{"GUFI_BIN=" + bin, "PATH=" + bin + ":/usr/bin", "LD_LIBRARY_PATH=" + filepath.Join(root, "lib") + ":/opt/x", "HOME=/h", "X=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s in %v", want, env)
		}
	}
	mac := EnvFor(bin, base, nil, EnvOptions{GOOS: "darwin"})
	if !slices.Contains(mac, "DYLD_LIBRARY_PATH="+filepath.Join(root, "lib")) || !slices.Contains(mac, "LD_LIBRARY_PATH=/opt/x") {
		t.Error(mac)
	}
	plain := EnvFor(bin, base, nil, EnvOptions{NoLibraries: true})
	if !slices.Equal(plain, []string{"GUFI_BIN=" + bin, "HOME=/h", "LD_LIBRARY_PATH=/opt/x", "PATH=/usr/bin"}) {
		t.Error(plain)
	}
	// No lib directory, or a system one: the loader's path is left alone.
	for _, b := range []string{filepath.Join(t.TempDir(), "bin"), "/usr/bin"} {
		if e := EnvFor(b, base, nil, EnvOptions{GOOS: "linux"}); !slices.Contains(e, "LD_LIBRARY_PATH=/opt/x") {
			t.Error(b, e)
		}
	}
}

func TestRowsShaping(t *testing.T) {
	p := &Planner{IndexRoot: "/ix/kb"}
	agg := Plan{Columns: []string{"path", "bytes"}}
	res := p.Rows(agg, "/ix/kb/a\x1e1\nshort\n\n/ix/kbx\x1e2\n")
	if len(res.Rows) != 2 || res.Rows[0][0] != "a" || res.Rows[1][0] != "/ix/kbx" {
		t.Fatal(res.Rows)
	}
	raw := p.Rows(Plan{Raw: true}, "a\nb\x1ec\x1ed\n")
	if !slices.Equal(raw.Columns, []string{"col1", "col2", "col3"}) || len(raw.Rows) != 2 {
		t.Fatal(raw)
	}
	if empty := p.Rows(Plan{Raw: true}, ""); !slices.Equal(empty.Columns, []string{"col1"}) || empty.Rows == nil {
		t.Fatal(empty)
	}
	big := strings.Repeat("x\x1e1\n", MaxRows+1)
	if r := p.Rows(agg, big); len(r.Rows) != MaxRows || !r.Truncated {
		t.Fatal(len(r.Rows), r.Truncated)
	}
}
