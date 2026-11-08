package userlibs

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The pure functions against Node's, on testdata/cases.json: the golden
// file is written from server/dist by scripts/golden/userlibs.mjs.

func outcome(v any, err error) any {
	if err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) {
			return jsonv.NewObject("error", ae.Message, "status", float64(ae.Status))
		}
		return jsonv.NewObject("error", err.Error(), "status", nil)
	}
	return jsonv.NewObject("ok", v)
}

func strs(a []string) []any {
	out := make([]any, len(a))
	for i, s := range a {
		out[i] = s
	}
	return out
}

func destJSON(d sshDestSpec) *jsonv.Object {
	var port any
	if d.Port != 0 {
		port = float64(d.Port)
	}
	return jsonv.NewObject("destination", d.Destination, "port", port, "sshOptions", strs(d.SSHOptions))
}

func testModule(t *testing.T) *Module {
	t.Helper()
	dir := t.TempDir()
	pool := remote.New(remote.Options{IndexBase: dir, Getenv: func(string) string { return "" }})
	t.Cleanup(pool.Close)
	root, _ := filepath.Abs("../../..")
	return New(nil, pool, nil, dir, root, nil)
}

func goGolden(t *testing.T) []byte {
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	cv, err := jsonv.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonv.Obj(cv)
	list := func(k string) []any { return jsonv.Arr(get(c, k)) }
	m := testModule(t)
	out := jsonv.NewObject()

	out.Set("scripts", jsonv.NewObject("build_check", buildCheck, "build_run", buildRun, "build_start", buildStart, "build_status", buildStatusSh, "embed_probe", embedProbe))
	phases := jsonv.NewObject()
	for k, v := range phaseLabel {
		phases.Set(k, v)
	}
	out.Set("phases", phases)
	var kvs, statuses, made, gaps, dests, specs, pws, advice, dirs, manifests, public, embeds []any
	for _, x := range list("keyValues") {
		o := jsonv.NewObject()
		for k, v := range keyValues(x.(string)) {
			o.Set(k, v)
		}
		kvs = append(kvs, o)
	}
	for _, x := range list("statuses") {
		statuses = append(statuses, parseStatus(x.(string)).json())
	}
	for _, x := range list("manifests") {
		made = append(made, vectorsMade(jsonv.Obj(x)))
		gaps = append(gaps, strs(buildGaps(jsonv.Obj(x))))
	}
	for _, x := range list("dests") {
		o := jsonv.Obj(x)
		d, err := sshDest(field(o, "destination"), field(o, "port"), field(o, "sshOptions"))
		if err != nil {
			dests = append(dests, outcome(nil, err))
		} else {
			dests = append(dests, outcome(destJSON(d), nil))
		}
	}
	for _, x := range list("sshSpecs") {
		s, err := sshSpecOf(jsonv.Obj(x))
		if err != nil {
			specs = append(specs, outcome(nil, err))
			continue
		}
		o := destJSON(s.sshDestSpec)
		o.Set("indexRoot", s.IndexRoot).Set("gufiBin", optional(s.GufiBin)).Set("sourceRoot", optional(s.SourceRoot))
		specs = append(specs, outcome(o, nil))
	}
	for _, x := range list("pwSpecs") {
		s, err := pwSpecOf(jsonv.Obj(x))
		if err != nil {
			pws = append(pws, outcome(nil, err))
			continue
		}
		pws = append(pws, outcome(jsonv.NewObject("cluster", s.Cluster, "indexRoot", s.IndexRoot, "gufiBin", optional(s.GufiBin), "sourceRoot", optional(s.SourceRoot)), nil))
	}
	for _, x := range list("advice") {
		o := jsonv.Obj(x)
		port, _ := get(o, "port").(float64)
		advice = append(advice, sshAdvice(jsonv.String(get(o, "message")), jsonv.String(get(o, "destination")), int(port)))
	}
	for _, x := range list("indexDirs") {
		a := jsonv.Arr(x)
		dirs = append(dirs, defaultIndexDir(a[0].(string), a[1].(string)))
	}
	for _, x := range list("logs") {
		if mf := manifestFrom(x.(string)); mf != nil {
			manifests = append(manifests, mf)
		} else {
			manifests = append(manifests, nil)
		}
	}
	for _, x := range list("defs") {
		b, _ := jsonv.Marshal(x)
		var d libraries.UserDef
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		job, _ := get(jsonv.Obj(x), "job").(float64)
		public = append(public, m.public(&d, int(job)))
	}
	files := func(fs []embedFile) any {
		if fs == nil {
			return nil
		}
		out := []any{}
		for _, f := range fs {
			out = append(out, f.json())
		}
		return out
	}
	for _, x := range list("platforms") {
		p := x.(string)
		embeds = append(embeds, jsonv.NewObject("files", files(m.embedFiles(p)), "version", nullable(m.embedVersion(p)), "megabytes", m.embedMegabytes(p)))
	}
	out.Set("keyValues", kvs).Set("statuses", statuses).Set("vectorsMade", made).Set("gaps", gaps).Set("dests", dests).Set("sshSpecs", specs)
	out.Set("pwSpecs", pws).Set("advice", advice).Set("indexDirs", dirs).Set("manifestFrom", manifests).Set("public", public).Set("embed", embeds)
	out.Set("readers", jsonv.NewObject("files", files(m.readerFiles()), "version", m.readersVersion()))
	b, err := jsonv.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
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
	want, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	g, w := normal(t, goGolden(t)).(map[string]any), normal(t, want).(map[string]any)
	for k := range w {
		if !reflect.DeepEqual(g[k], w[k]) {
			gl, wl := g[k], w[k]
			if ga, ok := gl.([]any); ok {
				if wa, ok := wl.([]any); ok && len(ga) == len(wa) {
					for i := range wa {
						if !reflect.DeepEqual(ga[i], wa[i]) {
							gj, _ := json.MarshalIndent(ga[i], "", " ")
							wj, _ := json.MarshalIndent(wa[i], "", " ")
							t.Errorf("%s[%d] differs:\ngo   %s\nnode %s", k, i, gj, wj)
						}
					}
					continue
				}
			}
			gj, _ := json.MarshalIndent(gl, "", " ")
			wj, _ := json.MarshalIndent(wl, "", " ")
			t.Errorf("%s differs:\ngo   %s\nnode %s", k, gj, wj)
		}
	}
}

// TestGoldenIsCurrent regenerates the golden file, and the scripts, from
// the built Node server when STUDIO_GOLDEN_NODE names a node binary.
func TestGoldenIsCurrent(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset")
	}
	out, err := exec.Command(node, "../../scripts/golden/userlibs.mjs", "testdata/cases.json", "../../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/golden.json")
	if !reflect.DeepEqual(normal(t, out), normal(t, want)) {
		t.Fatal("Node's userLibraries.ts, remoteBuild.ts or remoteEmbed.ts changed: regenerate testdata/golden.json")
	}
	dir := t.TempDir()
	if err := exec.Command(node, "../../scripts/golden/userlibs.mjs", "--scripts", dir, "../../..").Run(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build_check", "build_run", "build_start", "build_status", "embed_probe"} {
		got, _ := os.ReadFile(filepath.Join(dir, name+".sh"))
		if string(got) != script(name) {
			t.Errorf("scripts/%s.sh is not remoteBuild.ts's: run scripts/golden/userlibs.mjs --scripts internal/userlibs/scripts", name)
		}
	}
}
