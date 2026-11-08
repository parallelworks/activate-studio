package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The cases of server/test/gufiVt.test.mjs. The unit tests need nothing
// installed; the end-to-end ones need GUFI in /opt/gufi/bin and index two
// synthetic trees, one as the main library and one as a library on a
// cluster whose session is a local bash, and compare gufi_vt's answers
// with the per-library merge's.

func TestMain(m *testing.M) {
	// Any copy of this binary started below the tests is gufi_vt's remote
	// command, never the tests again: the variable is set before anything
	// can start one, and every child inherits it.
	if os.Getenv("SEARCH_TEST_CHILD") != "" {
		os.Exit(VtRemoteMain(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
	}
	os.Setenv("SEARCH_TEST_CHILD", "vt-remote")
	os.Exit(m.Run())
}

func TestSplitCommandLine(t *testing.T) {
	got, err := SplitCommandLine("gufi_query --print-tlv -x --threads 4 -E \"SELECT 'a b', `x`, $HOME, \\n FROM t;\" /idx/p ")
	want := []string{"gufi_query", "--print-tlv", "-x", "--threads", "4", "-E", "SELECT 'a b', `x`, $HOME, \\n FROM t;", "/idx/p"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := SplitCommandLine(`gufi_query -E "unterminated`); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatal("an unterminated quote is an error")
	}
}

func TestCheckRemoteArgs(t *testing.T) {
	ok := []string{"gufi_query", "--print-tlv", "-x", "--threads", "4", "-I", "CREATE TABLE t (a);", "-E", "SELECT 1;", "/idx/p"}
	if got := CheckRemoteArgs(ok, []string{"/idx/p"}); !reflect.DeepEqual(got, ok[1:]) {
		t.Fatalf("%q", got)
	}
	for _, bad := range [][]string{
		ok,
		{"rm", "-rf", "/idx/p"},
		{"gufi_query", "-o", "/tmp/out", "-E", "SELECT 1;", "/idx/p"},
		{"gufi_query", "--threads", "64", "-E", "SELECT 1;", "/idx/p"},
		{"gufi_query", "-E", "/idx/p"},
	} {
		roots := []string{"/idx/p"}
		if reflect.DeepEqual(bad, ok) {
			roots = []string{"/idx/other"}
		}
		if got := CheckRemoteArgs(bad, roots); got != nil {
			t.Fatalf("%q passed: %q", bad, got)
		}
	}
	// An index with a space arrives split, and is joined back.
	if got := CheckRemoteArgs([]string{"gufi_query", "-E", "SELECT 1;", "/my", "idx"}, []string{"/my idx"}); !reflect.DeepEqual(got, []string{"-E", "SELECT 1;", "/my idx"}) {
		t.Fatalf("%q", got)
	}
}

func TestVtScript(t *testing.T) {
	plan := gufi.Plan{Stages: gufi.Stages{E: "SELECT name FROM vrpentries;"}, Columns: []string{"name"}}
	SetVtCommandForTests([]string{"/usr/bin/studio", VtSubcommand})
	defer SetVtCommandForTests(nil)
	locs := []vtLoc{{id: "kb", indexes: []string{"/ix/kb"}, plan: plan}, {id: "u-p", indexes: []string{"/c/idx"}, plan: plan, remote: &vtRemote{"tok", "cluster-a", "/c/bin"}}}
	s, err := vtScript(locs, &VtOrder{Col: 1, Desc: true, Limit: 5}, viaStudio)
	if err != nil {
		t.Fatal(err)
	}
	for _, re := range []string{
		`CREATE VIRTUAL TABLE temp\.loc_0 USING gufi_vt\(index='/ix/kb', threads=4, E="SELECT name FROM vrpentries;"\);`,
		`temp\.loc_1 USING gufi_vt\(index='/c/idx'.*remote_cmd='/usr/bin/studio', remote_arg='gufi-vt-remote', remote_arg='tok'\);`,
		`SELECT 'kb', \* FROM temp\.loc_0 UNION ALL SELECT 'u-p', \* FROM temp\.loc_1 ORDER BY 3 DESC LIMIT 5;$`,
	} {
		if !regexp.MustCompile(re).MatchString(s) {
			t.Fatalf("%s\nnot in\n%s", re, s)
		}
	}
	e := &Engine{Pool: remote.New(remote.Options{Getenv: func(string) string { return "" }})}
	shell, err := vtScript(locs[1:], nil, e.viaShell)
	if err != nil || !strings.Contains(shell, `remote_cmd='pw', remote_arg='ssh', remote_arg='cluster-a', remote_arg='env', remote_arg='PATH=/c/bin:$PATH');`) {
		t.Fatalf("%s %v", shell, err)
	}
	if got := e.viaShell(vtRemote{"t", "ssh://hpc.example", "/g/bin"}); !reflect.DeepEqual(got, []string{"ssh", "hpc.example", "env", "PATH=/g/bin:$PATH"}) {
		t.Fatalf("%q", got)
	}
	if VtEligible(gufi.Plan{Stages: gufi.Stages{E: `SELECT "name" FROM vrpentries;`}}) {
		t.Fatal("a quoted identifier cannot go to gufi_vt")
	}
	if st, ok := VtStage(`SELECT 1 WHERE x MATCH '"a" AND "b"';`); !ok || st != `SELECT 1 WHERE x MATCH (''||char(34)||'a'||char(34)||' AND '||char(34)||'b'||char(34)||'');` {
		t.Fatalf("%q %v", st, ok)
	}
	for p, want := range map[string]string{"/a b": "'/a b'", "/it's": `"/it's"`, `/a'"b`: "", "'/x": "", "/x\n": ""} {
		if got := VtPath(p); got != want {
			t.Fatalf("VtPath(%q) = %q, want %q", p, got, want)
		}
	}
	if VtRemoteIndex("/a  b") || VtRemoteIndex("/a 'b") || !VtRemoteIndex("/a b") {
		t.Fatal("VtRemoteIndex")
	}
}

func TestGufiVtRoute(t *testing.T) {
	e := &Engine{Pool: remote.New(remote.Options{Getenv: func(string) string { return "" }})}
	srv := httptest.NewServer(http.HandlerFunc(e.ServeGufiVt))
	defer srv.Close()
	post := func(secret string, body any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", srv.URL, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Studio-Secret", secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if s := post("nope", map[string]any{"token": "t", "args": []string{}}); s != 403 {
		t.Fatalf("wrong secret: %d", s)
	}
	if s := post(VtSecretForTests(), map[string]any{"token": "missing", "args": []string{"gufi_query", "/i"}}); s != 404 {
		t.Fatalf("unknown token: %d", s)
	}
	AddVtLocationForTests("t1", "cluster-a", "k", "/c/bin", []string{"/i"}, "p")
	if s := post(VtSecretForTests(), map[string]any{"token": "t1", "args": []string{"cat", "/etc/passwd", "/i"}}); s != 400 {
		t.Fatalf("another command: %d", s)
	}
}

const realGufi = "/opt/gufi/bin"

func write(t *testing.T, f string, n int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(f), 0o755)
	if err := os.WriteFile(f, []byte(strings.Repeat("a", n)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// vtEngine is the Node test's fixture: the main library, a library on
// "the cluster", and one there whose index is gone.
func vtEngine(t *testing.T) *Engine {
	t.Helper()
	if _, err := os.Stat(filepath.Join(realGufi, "gufi_sqlite3")); err != nil {
		t.Skip("GUFI not installed")
	}
	dir := t.TempDir()
	kb, ix := filepath.Join(dir, "kb"), filepath.Join(dir, "ix")
	write(t, filepath.Join(kb, "a", "big.bin"), 3000)
	write(t, filepath.Join(kb, "a", "note.txt"), 10)
	write(t, filepath.Join(kb, "mid.dat"), 2000)
	write(t, filepath.Join(dir, "cluster", "proj", "huge.bin"), 5000)
	write(t, filepath.Join(dir, "cluster", "proj", "runs", "log.txt"), 2500)
	write(t, filepath.Join(dir, "cluster", "proj", "tiny.txt"), 5)
	os.MkdirAll(filepath.Join(ix, "gufi"), 0o755)
	os.MkdirAll(filepath.Join(dir, "cluster", "idx"), 0o755)
	for _, x := range [][2]string{{kb, filepath.Join(ix, "gufi")}, {filepath.Join(dir, "cluster", "proj"), filepath.Join(dir, "cluster", "idx")}} {
		if out, err := exec.Command(filepath.Join(realGufi, "gufi_dir2index"), "-x", x[0], x[1]).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	h := sha256.Sum256([]byte("single"))
	ud := filepath.Join(ix, "users", hex.EncodeToString(h[:])[:16])
	os.MkdirAll(ud, 0o755)
	os.WriteFile(filepath.Join(ud, "owner"), []byte("single"), 0o644)
	def := func(id, root string) map[string]any {
		return map[string]any{"id": id, "label": id, "site": "cluster-a", "resource": "pw://jdoe/cluster-a", "sourceDir": "~/proj", "sourceRoot": nil,
			"indexRoot": root, "gufiBin": realGufi, "state": "ready", "run": nil, "error": nil, "builtAt": nil, "directories": 1, "files": 1, "scheduler": nil}
	}
	b, _ := json.Marshal([]any{def("u-proj", filepath.Join(dir, "cluster", "idx", "proj")), def("u-gone", filepath.Join(dir, "cluster", "idx", "gone"))})
	os.WriteFile(filepath.Join(ud, "libraries.json"), b, 0o644)
	getenv := func(k string) string {
		switch k {
		case "PW_API_KEY":
			return "deployment-key"
		case "PW_GATEWAY_URL":
			return "https://platform.test/api/openai/v1"
		}
		return ""
	}
	pool := remote.New(remote.Options{Getenv: getenv, IndexBase: ix, Spawn: func(site, key, command string) *exec.Cmd {
		return exec.Command("bash", "-c", command)
	}})
	t.Cleanup(pool.Close)
	gufiIndex := filepath.Join(ix, "gufi", "kb")
	local := gufi.NewRunner(gufi.NewLocal(realGufi), nil, gufi.RunnerOptions{})
	reg := libraries.New(libraries.Options{KBRoot: kb, IndexBase: ix, GufiIndex: gufiIndex, GufiBin: realGufi, Getenv: getenv, SSHSite: remote.SSHSite, Runner: local})
	// The remote command is this test binary, as the child TestMain runs.
	exe, _ := os.Executable()
	SetVtCommandForTests([]string{exe})
	t.Cleanup(func() { SetVtCommandForTests(nil) })
	e := &Engine{Reg: reg, Keys: libraries.DeploymentKeys{Getenv: getenv}, GufiBin: realGufi, GufiIndex: gufiIndex, KBRoot: kb, IndexBase: ix,
		Pool: pool, Remote: &remote.Runners{Pool: pool}, Getenv: getenv, Local: local}
	srv := httptest.NewServer(http.HandlerFunc(e.ServeGufiVt))
	t.Cleanup(srv.Close)
	e.VtURL = func() string { return srv.URL + "/internal/gufi-vt" }
	return e
}

func vtOff(t *testing.T, fn func()) {
	off := false
	ForceGufiVtForTests(&off)
	defer ForceGufiVtForTests(nil)
	fn()
}

func TestQueryAcrossAsOneGufiVtStatement(t *testing.T) {
	e := vtEngine(t)
	single := libraries.ViewerOf("")
	ctx, done := gufitrace.Start(context.Background(), "query")
	body := []byte(`{"canned":"largest","n":3,"library":"all"}`)
	vt, err := e.QueryScope(ctx, body, single, "kb")
	cmds := done()
	if err != nil {
		t.Fatal(err)
	}
	if vt.Engine != "gufi_vt" {
		t.Fatalf("engine %q: %s", vt.Engine, vt.MergeReason)
	}
	var top [][]string
	for _, r := range vt.Rows {
		top = append(top, []string{r[1], r[2]})
	}
	if !reflect.DeepEqual(top, [][]string{{"huge.bin", "5000"}, {"a/big.bin", "3000"}, {"runs/log.txt", "2500"}}) || !reflect.DeepEqual(vt.RowLibraries, []string{"u-proj", "kb", "u-proj"}) {
		t.Fatalf("rows %v %v", top, vt.RowLibraries)
	}
	if vt.Failed == nil || len(*vt.Failed) != 1 || (*vt.Failed)[0].Library != "u-gone" {
		t.Fatalf("failed %+v", vt.Failed)
	}
	var vtCmd, there *gufitrace.Command
	for i := range cmds {
		c := &cmds[i]
		if c.Engine == "gufi_vt" {
			vtCmd = c
		}
		if c.Site == "cluster-a" && strings.Contains(c.Command, "gufi_query --print-tlv -x --threads 4") {
			there = c
		}
		if strings.Contains(c.Command, "-S 'SELECT 1;'") {
			t.Fatalf("a check is shown: %s", c.Command)
		}
	}
	if vtCmd == nil || !regexp.MustCompile(`^\S+gufi_sqlite3 -d x :memory: <<'SQL'\nCREATE VIRTUAL TABLE`).MatchString(vtCmd.Command) ||
		!strings.Contains(vtCmd.Command, "remote_cmd='pw', remote_arg='ssh', remote_arg='cluster-a'") || vtCmd.Exit == nil || *vtCmd.Exit != 0 {
		t.Fatalf("statement %+v", vtCmd)
	}
	if there == nil || there.Exit == nil || *there.Exit != 0 || there.Stderr == nil {
		t.Fatalf("location %+v in %+v", there, cmds)
	}
	vtOff(t, func() {
		merged, err := e.QueryScope(context.Background(), body, single, "kb")
		if err != nil || merged.Engine != "merge" || !reflect.DeepEqual(merged.Rows, vt.Rows) {
			t.Fatalf("merge %+v %v", merged, err)
		}
	})

	g := []byte(`{"canned":"by_extension","n":10,"library":"all"}`)
	gv, err := e.QueryScope(context.Background(), g, single, "kb")
	if err != nil || gv.Engine != "gufi_vt" {
		t.Fatalf("%+v %v", gv, err)
	}
	var txt []string
	for _, r := range gv.Rows {
		if r[1] == "txt" {
			txt = r[1:]
		}
	}
	if !reflect.DeepEqual(txt, []string{"txt", "3", "2515"}) {
		f := []QueryFailure{}
		if gv.Failed != nil {
			f = *gv.Failed
		}
		t.Fatalf("txt %v in %v; failed %+v", txt, gv.Rows, f)
	}
	vtOff(t, func() {
		gm, _ := e.QueryScope(context.Background(), g, single, "kb")
		if !reflect.DeepEqual(gm.Rows, gv.Rows) {
			t.Fatalf("grouped: %v vs %v", gm.Rows, gv.Rows)
		}
	})

	bq, err := e.QueryScope(context.Background(), []byte(`{"library":"all","builder":{"source":"files","filters":[{"field":"size","op":"gt","value":"2400"}],"groupBy":"none","sort":"size","dir":"asc","limit":10}}`), single, "kb")
	if err != nil || bq.Engine != "gufi_vt" {
		t.Fatalf("%+v %v", bq, err)
	}
	var sizes []string
	for _, r := range bq.Rows {
		sizes = append(sizes, r[2])
	}
	if !reflect.DeepEqual(sizes, []string{"2500", "3000", "5000"}) {
		t.Fatalf("sizes %v", sizes)
	}
	raw, err := e.QueryScope(context.Background(), []byte(`{"library":"all","sql":"SELECT name FROM vrpentries WHERE size > 2400"}`), single, "kb")
	if err != nil || raw.Engine != "gufi_vt" {
		t.Fatalf("%+v %v", raw, err)
	}
	var names []string
	for _, r := range raw.Rows {
		names = append(names, r[1])
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"big.bin", "huge.bin", "log.txt"}) {
		t.Fatalf("raw %v", names)
	}
}

func TestSearchAcrossWithTheGufiVtSetting(t *testing.T) {
	e := vtEngine(t)
	single := libraries.ViewerOf("")
	libs, _, err := e.Reg.Scope([]string{"all"}, single, "kb")
	if err != nil {
		t.Fatal(err)
	}
	engine := "parallel"
	e.SearchEngine = func() string { return engine }
	ran := func(q string) ([]ScopedHit, bool) {
		ctx, done := gufitrace.Start(context.Background(), "search")
		hits := e.SearchScope(ctx, libs, q, 20, ScopeOpts{Omitted: &libraries.Omissions{}})
		for _, c := range done() {
			if c.Engine == "gufi_vt" {
				return hits, true
			}
		}
		return hits, false
	}
	if hits, vt := ran("tiny"); vt || len(hits) == 0 {
		t.Fatalf("by default each library runs on its own: vt %v, %d hits", vt, len(hits))
	}
	engine = "gufi_vt"
	pick := func(hs []ScopedHit) []string {
		var out []string
		for _, h := range hs {
			out = append(out, h.Library.ID+":"+h.Path)
		}
		sort.Strings(out)
		return out
	}
	for _, q := range []string{"tiny", "note", "log"} {
		vt, used := ran(q)
		if !used {
			t.Fatalf("%s: not run as gufi_vt", q)
		}
		vtOff(t, func() {
			merged, _ := ran(q)
			if !reflect.DeepEqual(pick(vt), pick(merged)) || len(vt) == 0 {
				t.Fatalf("%s: %v vs %v", q, pick(vt), pick(merged))
			}
		})
	}
}

func TestFtsExprBareForGufiVt(t *testing.T) {
	if s, ok := gufi.FtsExprBare("pressure pulse"); !ok || s != "(pressure AND pulse)" {
		t.Fatalf("%q %v", s, ok)
	}
	if s, ok := gufi.FtsExprBare("mod*"); !ok || s != "mod*" {
		t.Fatalf("%q", s)
	}
	if _, ok := gufi.FtsExprBare("det-train"); ok {
		t.Fatal("a hyphenated term is not bare")
	}
	s, _ := gufi.FtsExprBare("pressure pulse")
	if strings.Contains(gufi.FtsSQL(s, 10), `"`) {
		t.Fatal("no double quotes for gufi_vt")
	}
}
