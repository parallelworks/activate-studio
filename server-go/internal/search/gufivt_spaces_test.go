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
	"sort"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The cases of server/test/gufiVtSpaces.test.mjs: gufi_vt where every path
// has a space in it, as in the macOS app: the server binary inside
// "ACTIVATE Studio.app", the index under "Application Support", GUFI under
// the app's Resources (links to /opt/gufi/bin).
func TestGufiVtWithTheAppsPaths(t *testing.T) {
	if VtPath("/Applications/ACTIVATE Studio.app/node") != "'/Applications/ACTIVATE Studio.app/node'" || VtPath("/idx/it's here") != `"/idx/it's here"` ||
		VtPath(`/idx/say "hi"/x`) != `'/idx/say "hi"/x'` || VtPath(`/idx/odd "x" it's`) != "" || VtPath("/idx/ends'") != "" {
		t.Fatal("VtPath")
	}
	if !VtRemoteIndex("/c/cluster idx/it's proj") || VtRemoteIndex("/c/two  spaces") || VtRemoteIndex("/c/x 'quoted") || !VtRemoteIndex("/c/it's x") || VtRemoteIndex("/c/tab\there") {
		t.Fatal("VtRemoteIndex")
	}
	idx := "/c/cluster idx/it's proj"
	args, _ := SplitCommandLine(`gufi_query --print-tlv -x --threads 4 -E "SELECT name FROM vrpentries;" ` + idx + " ")
	if got := CheckRemoteArgs(args, []string{idx}); !reflect.DeepEqual(got, []string{"--print-tlv", "-x", "--threads", "4", "-E", "SELECT name FROM vrpentries;", idx}) {
		t.Fatalf("%q", got)
	}
	if CheckRemoteArgs(args, []string{"/c/cluster idx"}) != nil || CheckRemoteArgs(append(args, "/etc"), []string{idx}) != nil {
		t.Fatal("another index passes")
	}
	if _, err := os.Stat(filepath.Join(realGufi, "gufi_sqlite3")); err != nil {
		t.Skip("GUFI not installed")
	}
	base := t.TempDir()
	res := filepath.Join(base, "Applications", "ACTIVATE Studio.app", "Contents", "Resources")
	data := filepath.Join(base, "Library", "Application Support", "ACTIVATE Studio")
	bin := filepath.Join(res, "gufi", "bin")
	os.MkdirAll(bin, 0o755)
	ents, _ := os.ReadDir(realGufi)
	for _, e := range ents {
		os.Symlink(filepath.Join(realGufi, e.Name()), filepath.Join(bin, e.Name()))
	}
	if lib := filepath.Join(filepath.Dir(realGufi), "lib"); exists(lib) {
		os.Symlink(lib, filepath.Join(res, "gufi", "lib"))
	}
	exe, _ := os.Executable()
	app := filepath.Join(res, "bin", "studio-server")
	os.MkdirAll(filepath.Dir(app), 0o755)
	os.Symlink(exe, app)
	SetVtCommandForTests([]string{app})
	t.Cleanup(func() { SetVtCommandForTests(nil) })
	kb, ix := filepath.Join(base, "Documents", "ACTIVATE Studio"), filepath.Join(data, "index")
	write(t, filepath.Join(kb, "a b", "big.bin"), 3000)
	write(t, filepath.Join(kb, "note.txt"), 10)
	write(t, filepath.Join(base, "src", "local it's", "mid.dat"), 2000)
	write(t, filepath.Join(base, "src", "it's proj", "huge.bin"), 5000)
	write(t, filepath.Join(base, "src", "it's proj", "runs", "log.txt"), 2500)
	index := func(src, dest string) {
		os.MkdirAll(dest, 0o755)
		if out, err := exec.Command(filepath.Join(bin, "gufi_dir2index"), "-x", src, dest).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
	}
	index(kb, filepath.Join(ix, "gufi"))
	index(filepath.Join(base, "src", "local it's"), filepath.Join(base, "other idx"))
	index(filepath.Join(base, "src", "it's proj"), filepath.Join(base, "cluster idx"))
	b, _ := json.Marshal([]map[string]any{{"id": "other", "label": "Other", "indexRoot": filepath.Join(base, "other idx", "local it's")}})
	os.WriteFile(filepath.Join(ix, "libraries.json"), b, 0o644)
	h := sha256.Sum256([]byte("single"))
	ud := filepath.Join(ix, "users", hex.EncodeToString(h[:])[:16])
	os.MkdirAll(ud, 0o755)
	os.WriteFile(filepath.Join(ud, "owner"), []byte("single"), 0o644)
	ub, _ := json.Marshal([]map[string]any{{"id": "u-proj", "label": "u-proj", "site": "cluster-a", "resource": "pw://jdoe/cluster-a", "sourceDir": "~/proj", "sourceRoot": nil,
		"indexRoot": filepath.Join(base, "cluster idx", "it's proj"), "gufiBin": bin, "state": "ready", "run": nil, "error": nil, "builtAt": nil, "directories": 1, "files": 1, "scheduler": nil}})
	os.WriteFile(filepath.Join(ud, "libraries.json"), ub, 0o644)
	getenv := func(k string) string {
		switch k {
		case "PW_API_KEY":
			return "deployment-key"
		case "PW_GATEWAY_URL":
			return "https://platform.test/api/openai/v1"
		}
		return ""
	}
	pool := remote.New(remote.Options{Getenv: getenv, IndexBase: ix, Spawn: func(site, key, command string) *exec.Cmd { return exec.Command("bash", "-c", command) }})
	t.Cleanup(pool.Close)
	gufiIndex := filepath.Join(ix, "gufi", filepath.Base(kb))
	local := gufi.NewRunner(gufi.NewLocal(bin), nil, gufi.RunnerOptions{})
	reg := libraries.New(libraries.Options{KBRoot: kb, IndexBase: ix, GufiIndex: gufiIndex, GufiBin: bin, Getenv: getenv, SSHSite: remote.SSHSite, Runner: local})
	e := &Engine{Reg: reg, Keys: libraries.DeploymentKeys{Getenv: getenv}, GufiBin: bin, GufiIndex: gufiIndex, KBRoot: kb, IndexBase: ix,
		Pool: pool, Remote: &remote.Runners{Pool: pool}, Getenv: getenv, Local: local}
	srv := httptest.NewServer(http.HandlerFunc(e.ServeGufiVt))
	t.Cleanup(srv.Close)
	e.VtURL = func() string { return srv.URL + "/internal/gufi-vt" }
	ResetGufiVtForTests()
	t.Cleanup(ResetGufiVtForTests)
	if !e.GufiVtAvailable(context.Background()) {
		t.Fatal("with the app's paths, gufi_vt is not available")
	}
	single := libraries.ViewerOf("")
	vt, err := e.QueryScope(context.Background(), []byte(`{"canned":"largest","n":10,"library":"all"}`), single, "kb")
	if err != nil || vt.Engine != "gufi_vt" || vt.Failed == nil || len(*vt.Failed) != 0 {
		t.Fatalf("%+v %v", vt, err)
	}
	var top [][]string
	for _, r := range vt.Rows {
		top = append(top, []string{r[1], r[2]})
	}
	if !reflect.DeepEqual(top, [][]string{{"huge.bin", "5000"}, {"a b/big.bin", "3000"}, {"runs/log.txt", "2500"}, {"mid.dat", "2000"}, {"note.txt", "10"}}) {
		t.Fatalf("rows %v", top)
	}
	libs := map[string]bool{}
	for _, id := range vt.RowLibraries {
		libs[id] = true
	}
	var ids []string
	for id := range libs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "kb,other,u-proj" {
		t.Fatalf("libraries %v", ids)
	}
	vtOff(t, func() {
		m, _ := e.QueryScope(context.Background(), []byte(`{"canned":"largest","n":10,"library":"all"}`), single, "kb")
		if m.Engine != "merge" || !reflect.DeepEqual(m.Rows, vt.Rows) {
			t.Fatalf("merge %v", m.Rows)
		}
	})
	some, err := e.QueryScope(context.Background(), []byte(`{"canned":"largest","n":10,"library":["other","u-proj"]}`), single, "kb")
	if err != nil || some.Engine != "gufi_vt" {
		t.Fatalf("%+v %v", some, err)
	}
	var want [][]string
	for i, r := range vt.Rows {
		if id := vt.RowLibraries[i]; id == "other" || id == "u-proj" {
			want = append(want, r)
		}
	}
	if !reflect.DeepEqual(some.Rows, want) {
		t.Fatalf("some %v want %v", some.Rows, want)
	}
}
