package workflows

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// The composer cases of server/test/workflowSchema.test.mjs: a fake pw
// CLI serves two small workflows, each with a form layout; the gateway does
// not answer, so the schema is the index cache's copy.

func composePlatform(t *testing.T) *platform.Platform {
	t.Helper()
	ix := t.TempDir()
	b, err := os.ReadFile("testdata/workflow.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ix, "workflow.schema.json"), b, 0o644)
	cli := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(cli, []byte(`#!/bin/sh
for a; do if [ "$prev" = get ]; then name=$a; fi; prev=$a; done
printf '{"name":"%s","yaml":{"on":{"execute":{"inputs":{"$meta":{"layout":{"type":"stack","children":[{"type":"field","field":"resource"}]}},"resource":{"type":"compute-clusters","label":"Resource"}}}},"jobs":{"main":{"steps":[{"run":"true"}]}}}}' "$name"
`), 0o755)
	env := map[string]string{"PW_GATEWAY_URL": "http://127.0.0.1:1/api/openai/v1", "PW_CLI": cli}
	return platform.New(platform.Options{IndexBase: ix, Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
}

func TestComposedWorkflowPassesTheSchema(t *testing.T) {
	fakeNet(t)
	p := composePlatform(t)
	ctx := context.Background()
	c, err := Compose(ctx, p, []ComposeStep{{Uses: "cloudinit"}, {Uses: "Hello.World"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if v := NewSchema(p).Validate(ctx, c.YAML); v.OK == nil || !*v.OK {
		t.Fatal("composer output must be executable:", v.Errors)
	}
	jobs := jsonv.Obj(jsonv.Path(c.YAML, "jobs"))
	key := regexp.MustCompile(`^[a-z0-9_-]{1,255}$`)
	uses := regexp.MustCompile(`^(workflow|marketplace|github|parallelworks)/`)
	for _, k := range jobs.Keys() {
		j, _ := jobs.Get(k)
		if !key.MatchString(k) || !uses.MatchString(jsStr(jsonv.Path(jsonv.Arr(jsonv.Path(j, "steps"))[0], "uses"))) {
			t.Error(k, js(j))
		}
		if strings.Contains(js(j), "$meta") {
			t.Error("a source form's layout is left behind:", js(j))
		}
	}
	inputs := jsonv.Obj(jsonv.Path(c.YAML, "on", "execute", "inputs"))
	if !inputs.Has("resource") || inputs.Has("$meta") {
		t.Fatal(js(inputs))
	}
}

func TestComposeKeepsMarketplaceAndGitHubReferences(t *testing.T) {
	fakeNet(t)
	p := composePlatform(t)
	ctx := context.Background()
	c, err := Compose(ctx, p, []ComposeStep{{Uses: "marketplace/cloudinit/v1.0.1"}, {Uses: "github/org/repo/wf"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	jobs := jsonv.Obj(jsonv.Path(c.YAML, "jobs"))
	for _, k := range jobs.Keys() {
		j, _ := jobs.Get(k)
		got = append(got, jsStr(jsonv.Path(jsonv.Arr(jsonv.Path(j, "steps"))[0], "uses")))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "github/org/repo/wf,marketplace/cloudinit/v1.0.1" {
		t.Fatal(got)
	}
	if v := NewSchema(p).Validate(ctx, c.YAML); v.OK == nil || !*v.OK {
		t.Fatal(v.Errors)
	}
	if _, err := Compose(ctx, p, nil, true); err == nil {
		t.Fatal("no steps is an error")
	}
}

func TestFormLayoutPassesTheSchema(t *testing.T) {
	p := composePlatform(t)
	doc := mustJSON(t, `{"on":{"execute":{"inputs":{"$meta":{"layout":{"type":"section","label":"Run","children":[{"type":"grid","columns":[2,1],"gap":"md","children":[{"type":"field","field":"study","span":{"base":2,"md":1}},{"type":"field","field":"cases"}]}]}},"cases":{"type":"number","label":"Cases","default":8},"study":{"type":"group","label":"Study","items":{"$meta":{"layout":{"type":"grid","columns":{"base":1,"md":2},"children":[{"type":"field","field":"low"},{"type":"field","field":"high"}]}},"low":{"type":"number","label":"Low"},"high":{"type":"number","label":"High"}}}}}},"jobs":{"main":{"steps":[{"run":"true"}]}}}`)
	if v := NewSchema(p).Validate(context.Background(), doc); v.OK == nil || !*v.OK {
		t.Fatal(v.Errors)
	}
}
