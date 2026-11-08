package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The cases of server/test/libraryScope.test.mjs, which stands its two
// clusters in with setRemoteSpawn (bash -c here, HOME the stand-in's), so
// they run here with the same stand-in Spawn.

const fakeQuery = `#!/bin/sh
args="$*"
for last; do :; done
R=$(printf '\036')
lib=$(basename "$last")
row() { printf '%s' "$1"; shift; for c in "$@"; do printf '%s%s' "$R" "$c"; done; printf '\n'; }
case "$args" in
  *"--max-level 0"*) row "$lib-file.txt" f 5 1700000000 ;;
  *"ORDER BY size DESC"*)
    if [ "$lib" = a ]; then row "$last/big.bin" 100 "2026-01-01 00:00:00"; row "$last/mid.bin" 50 "2026-01-02 00:00:00"
    else row "$last/large.dat" 80 "2026-01-03 00:00:00"; row "$last/small.dat" 10 "2026-01-04 00:00:00"; fi ;;
  *"GROUP BY ext"*)
    if [ "$lib" = a ]; then row pdf 2 300; row txt 1 10; else row pdf 1 100; fi ;;
  *"SELECT 'n', "*)
    for a in "$@"; do case "$a" in "$HOME"/idx/*) b=$(basename "$a"); row n 2 "$a/notes/$b-report.md" "$b-report.md" 7 1700000000 "" ;; esac; done ;;
esac
`

func scopeEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	ix, kb, home := filepath.Join(dir, "ix"), filepath.Join(dir, "kb"), filepath.Join(dir, "home")
	bin := filepath.Join(home, "gufi", "bin")
	for _, d := range []string{ix, kb, bin, filepath.Join(home, "idx", "a"), filepath.Join(home, "idx", "b")} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(bin, "gufi_query"), []byte(fakeQuery), 0o755)
	h := sha256.Sum256([]byte("single"))
	hash := hex.EncodeToString(h[:])[:16]
	ud := filepath.Join(ix, "users", hash)
	os.MkdirAll(ud, 0o755)
	os.WriteFile(filepath.Join(ud, "owner"), []byte("single"), 0o644)
	def := func(id, label, n string) map[string]any {
		return map[string]any{"id": id, "label": label, "site": "cluster-" + n, "resource": "pw://u/cluster-" + n, "sourceDir": "~",
			"sourceRoot": filepath.Join(home, "src", n), "indexRoot": filepath.Join(home, "idx", n), "gufiBin": bin, "state": "ready",
			"run": nil, "error": nil, "builtAt": nil, "directories": 1, "files": 1, "scheduler": nil}
	}
	b, _ := json.Marshal([]any{def("u-a", "Library A", "a"), def("u-b", "Library B", "b")})
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
		c := exec.Command("bash", "-c", command)
		c.Env = append(os.Environ(), "HOME="+home)
		return c
	}})
	t.Cleanup(pool.Close)
	reg := libraries.New(libraries.Options{KBRoot: kb, IndexBase: ix, GufiIndex: filepath.Join(ix, "gufi", "kb"),
		GufiBin: filepath.Join(dir, "no-local-gufi"), Getenv: getenv, SSHSite: remote.SSHSite})
	return &Engine{Reg: reg, Keys: libraries.DeploymentKeys{Getenv: getenv}, GufiBin: filepath.Join(dir, "no-local-gufi"),
		GufiIndex: filepath.Join(ix, "gufi", "kb"), KBRoot: kb, IndexBase: ix, Pool: pool, Remote: &remote.Runners{Pool: pool}, Getenv: getenv}
}

func ids(ls []libraries.Library) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.ID)
	}
	return out
}

func TestScopeAcrossUserLibraries(t *testing.T) {
	e := scopeEngine(t)
	single := libraries.ViewerOf("")
	all, _, err := e.Reg.Scope([]string{"all"}, single, "kb")
	if err != nil || !reflect.DeepEqual(ids(all), []string{"kb", "u-a", "u-b"}) {
		t.Fatalf("all: %v %v", ids(all), err)
	}
	one, _, _ := e.Reg.Scope([]string{"u-b"}, single, "kb")
	if !reflect.DeepEqual(ids(one), []string{"u-b"}) {
		t.Fatalf("one: %v", ids(one))
	}
	ctx := context.Background()

	r, err := e.QueryScope(ctx, []byte(`{"canned":"largest","n":3,"library":"all"}`), single, "kb")
	if err != nil {
		t.Fatal(err)
	}
	var top [][]string
	for _, x := range r.Rows {
		top = append(top, x[:3])
	}
	if !reflect.DeepEqual(r.Columns, []string{"library", "path", "bytes", "modified"}) ||
		!reflect.DeepEqual(top, [][]string{{"Library A", "big.bin", "100"}, {"Library B", "large.dat", "80"}, {"Library A", "mid.bin", "50"}}) ||
		!reflect.DeepEqual(r.RowLibraries, []string{"u-a", "u-b", "u-a"}) {
		t.Fatalf("ranked: %+v", r)
	}
	if r.Failed == nil || len(*r.Failed) != 1 || (*r.Failed)[0].Library != "kb" {
		t.Fatalf("the main folder has no index and says so: %+v", r.Failed)
	}

	g, err := e.QueryScope(ctx, []byte(`{"canned":"by_extension","n":10,"library":"all"}`), single, "kb")
	if err != nil {
		t.Fatal(err)
	}
	var sums [][]string
	for _, x := range g.Rows {
		sums = append(sums, x[1:])
	}
	if !reflect.DeepEqual(sums, [][]string{{"pdf", "3", "400"}, {"txt", "1", "10"}}) {
		t.Fatalf("grouped: %v", sums)
	}

	b, err := e.QueryScope(ctx, []byte(`{"canned":"largest","n":5,"library":"u-b"}`), single, "kb")
	if err != nil || !reflect.DeepEqual(b.Columns, []string{"path", "bytes", "modified"}) || b.Rows[0][0] != "large.dat" || b.Rows[1][0] != "small.dat" {
		t.Fatalf("one library: %+v %v", b, err)
	}

	for _, scope := range [][]string{{"u-a", "u-b"}, {"u-a,u-b"}} {
		libs, _, err := e.Reg.Scope(scope, single, "kb")
		if err != nil {
			t.Fatal(err)
		}
		hits := e.SearchScope(ctx, libs, "report", 10, ScopeOpts{})
		var got [][2]string
		for _, h := range hits {
			got = append(got, [2]string{h.Library.ID, h.Path})
		}
		sort.Slice(got, func(i, j int) bool { return got[i][0] < got[j][0] })
		if !reflect.DeepEqual(got, [][2]string{{"u-a", "notes/a-report.md"}, {"u-b", "notes/b-report.md"}}) {
			t.Fatalf("%v: %v", scope, got)
		}
	}
	refined := e.SearchScope(ctx, all[1:], "", 10, ScopeOpts{Refine: RefineOf("report", "", "all")})
	if len(refined) != 2 {
		t.Fatalf("refined: %+v", refined)
	}
	if _, _, err := e.Reg.Scope([]string{"u-a,u-nope"}, single, "kb"); err == nil {
		t.Fatal("an unknown id is a 404")
	}
}
