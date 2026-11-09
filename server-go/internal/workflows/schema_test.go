package workflows

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// schemaPlatform is a platform whose gateway does not answer, so the
// validator reads the schema from the index cache, as the Node test does.
func schemaPlatform(t *testing.T, withSchema bool) *platform.Platform {
	t.Helper()
	ix := t.TempDir()
	if withSchema {
		b, err := os.ReadFile("testdata/workflow.schema.json")
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(ix, "workflow.schema.json"), b, 0o644)
	}
	env := map[string]string{"PW_GATEWAY_URL": "http://127.0.0.1:1/api/openai/v1"}
	return platform.New(platform.Options{IndexBase: ix, Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
}

// Each error set matches ajv's, up to order (schema.go).
func TestSchemaErrorsAsAjv(t *testing.T) {
	s := NewSchema(schemaPlatform(t, true))
	b, _ := os.ReadFile("testdata/schema-cases.json")
	cases, _ := jsonv.Decode(b)
	g, _ := os.ReadFile("testdata/schema-golden.json")
	golden, _ := jsonv.Decode(g)
	for i, doc := range jsonv.Arr(cases) {
		want := jsonv.Arr(golden)[i]
		v := s.Validate(t.Context(), doc)
		if v.OK == nil || *v.OK != (jsonv.Path(want, "ok") == true) {
			t.Errorf("case %d: ok %v, want %v (%v)", i, v.OK, jsonv.Path(want, "ok"), v.Errors)
			continue
		}
		var we []string
		for _, e := range jsonv.Arr(jsonv.Path(want, "errors")) {
			we = append(we, jsonv.Str(e))
		}
		got := append([]string(nil), v.Errors...)
		sort.Strings(got)
		sort.Strings(we)
		if strings.Join(got, "\n") != strings.Join(we, "\n") {
			t.Errorf("case %d\n go:  %s\n ajv: %s", i, strings.Join(v.Errors, " | "), strings.Join(we, " | "))
		}
	}
}

func TestSchemaGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../scripts/golden/wfschema.mjs", "testdata", "../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/schema-golden.json")
	a, _ := jsonv.Decode(out)
	b, _ := jsonv.Decode(want)
	if string(jsonv.MustMarshal(a)) != string(jsonv.MustMarshal(b)) {
		t.Fatal("ajv's output changed: regenerate testdata/schema-golden.json")
	}
}

func TestSchemaKnownGoodAndMissing(t *testing.T) {
	s := NewSchema(schemaPlatform(t, true))
	b, _ := os.ReadFile("../../../deploy/workflow.yaml")
	doc, err := jsonv.ParseYAML(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Validate(t.Context(), doc); v.OK == nil || !*v.OK {
		t.Fatal(v.Errors)
	}
	none := NewSchema(schemaPlatform(t, false)).Validate(t.Context(), jsonv.NewObject("jobs", jsonv.NewObject()))
	if none.OK != nil || !strings.Contains(none.Errors[0], "not validated") {
		t.Fatal(none)
	}
	if got := string(jsonv.MustMarshal(none.JSON())); !strings.HasPrefix(got, `{"ok":null,"errors":["The platform`) || !strings.HasSuffix(got, `"source":"http://127.0.0.1:1/workflow.schema.json"}`) {
		t.Fatal(got)
	}
}
