package maps_test

// libraryMaps.test.mjs's cases for a library on another system, against
// the Go routes: the host runs gufi_query and nothing else (a stand-in
// Spawn, as the Node test's setRemoteSpawn), the vectors are fetched with
// it alone, and the map is made here in the owner's folder.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/maps"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/userlibs"
)

func gufiBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("GUFI_BIN")
	if bin == "" {
		bin = "/opt/gufi/bin"
	}
	for _, tool := range []string{"gufi_query", "gufi_dir2index", "gufi_sqlite3"} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Skip("GUFI not installed")
		}
	}
	cmd := exec.Command(filepath.Join(bin, "gufi_sqlite3"))
	cmd.Stdin = strings.NewReader("SELECT vec_version();")
	if out, err := cmd.Output(); err != nil || len(bytes.TrimSpace(out)) == 0 {
		t.Skip("needs GUFI's sqlite3 with sqlite-vec")
	}
	return bin
}

type farFile struct {
	p, name, text string
	inode         int
	passages      [][]float32
}

type world struct {
	t        *testing.T
	gufi     string
	dir      string
	home     string
	log      string
	failMark string
	mux      *http.ServeMux
	m        *maps.Module
	files    []*farFile
	mu       sync.Mutex
}

func unit384(axis, toward int, lean float64) []float32 {
	v := make([]float64, 384)
	v[axis] = 1 - lean
	v[toward] += lean
	n := 0.0
	for _, x := range v {
		n += x * x
	}
	n = math.Sqrt(n)
	out := make([]float32, 384)
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out
}

func hexOf(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return hex.EncodeToString(b)
}

func newWorld(t *testing.T) *world {
	t.Helper()
	gufi := gufiBin(t)
	dir := t.TempDir()
	w := &world{t: t, gufi: gufi, dir: dir, home: filepath.Join(dir, "home"), log: filepath.Join(dir, "commands.log"), failMark: filepath.Join(dir, "fail-vectors")}
	for _, k := range []string{"AUTH_JWKS_URL", "OIDC_ISSUER", "STUDIO_CONFIG_FILE", "STUDIO_INDEX_DIR", "STUDIO_DATA_DIR", "PW_API_KEY", "STUDIO_REMOTE_MAP_FILES", "STUDIO_REMOTE_SPAWN"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	run := filepath.Join(dir, "run")
	os.MkdirAll(run, 0o700)
	for k, v := range map[string]string{"HOME": filepath.Join(dir, "localhome"), "KB_ROOT": filepath.Join(dir, "kb"), "INDEX_BASE": filepath.Join(dir, "ix"),
		"STUDIO_NODE": "off", "GUFI_BIN": gufi, "XDG_RUNTIME_DIR": run, "STUDIO_PROJECT_ROOT": dir} {
		t.Setenv(k, v)
	}
	os.MkdirAll(filepath.Join(dir, "kb"), 0o755)
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	t.Cleanup(func() { a.Drain("test", 0) })
	// The login on me@query-only runs gufi_query and nothing else, and logs
	// each command; with the mark file there, the connection drops on a
	// walk that fetches vectors, as ssh's does (exit 255).
	script := `printf '%s\n' "$CMD" >> ` + w.log + `
if [ -f ` + w.failMark + ` ]; then case "$CMD" in *vec_add*) echo "Connection closed by 127.0.0.1 port 22" >&2; exit 255 ;; esac; fi
case "$CMD" in
  gufi_query|"gufi_query "*) eval "$CMD" ;;
  *) echo "This account may run only gufi_query." >&2; exit 1 ;;
esac`
	pool, _ := remote.Shared(a, func() remote.Options {
		return remote.Options{IndexBase: cfg.IndexBase, Getenv: os.Getenv,
			SSHLocal: func([]string, time.Duration) remote.LocalResult { return remote.LocalResult{Code: 255} },
			Spawn: func(site, key, command string) *exec.Cmd {
				body := "exit 255"
				if s, err := remote.ParseSSHSite(site); err == nil && s != nil && s.Destination == "me@query-only" {
					body = script
				}
				cmd := exec.Command("/bin/sh", "-c", body)
				cmd.Env = []string{"HOME=" + w.home, "PATH=" + gufi + ":/usr/bin:/bin", "CMD=" + command}
				return cmd
			}}
	})
	t.Cleanup(pool.Close)
	w.mux = http.NewServeMux()
	userlibs.Register(w.mux, a)
	maps.Register(w.mux, a)
	w.m = maps.Of(a)
	w.m.DelayScale = 0
	return w
}

func (w *world) call(method, url string, body any) (int, map[string]any) {
	w.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, url, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, r)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		w.t.Fatalf("%s %s: %d %q", method, url, rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func (w *world) get(url string) map[string]any { _, b := w.call("GET", url, nil); return b }

func (w *world) commands() []string {
	b, err := os.ReadFile(w.log)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (w *world) clearLog() { os.Remove(w.log) }

func (w *world) settled(id string) map[string]any {
	w.t.Helper()
	end := time.Now().Add(60 * time.Second)
	for {
		o := w.get("/api/libraries/" + id + "/semantic-map")
		if o["building"] != true {
			return o
		}
		if time.Now().After(end) {
			w.t.Fatalf("%s: still building: %v", id, o)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (w *world) sqlite(db string, sql []string) {
	w.t.Helper()
	cmd := exec.Command(filepath.Join(w.gufi, "gufi_sqlite3"), db)
	cmd.Stdin = strings.NewReader(strings.Join(sql, "\n"))
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("%v %s", err, out)
	}
}

func (w *world) index(src, dst string) {
	w.t.Helper()
	os.MkdirAll(dst, 0o755)
	if out, err := exec.Command(filepath.Join(w.gufi, "gufi_dir2index"), "-x", src, dst).CombinedOutput(); err != nil {
		w.t.Fatalf("%v %s", err, out)
	}
}

func write(t *testing.T, f, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(f), 0o755)
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (w *world) farFile(folder string, i, axis int, text string) *farFile {
	f := &farFile{p: fmt.Sprintf("%s/%c%d.md", folder, folder[0], i), name: fmt.Sprintf("%c%d.md", folder[0], i), inode: 5000 + len(w.files), text: text,
		passages: [][]float32{unit384(axis, axis+1, 0.05*float64(i%5)), unit384(axis, axis+2, 0.05*float64(i%3))}}
	w.files = append(w.files, f)
	return f
}

func (w *world) farFolder(far, folder, model string, files []*farFile) {
	sql := []string{
		"CREATE TABLE IF NOT EXISTS gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT);",
		"CREATE VIRTUAL TABLE IF NOT EXISTS gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);",
		"CREATE TABLE IF NOT EXISTS gvecmodel (model TEXT);",
		"DELETE FROM gvecmodel;",
		"INSERT INTO gvecmodel VALUES ('" + model + "');",
	}
	for _, f := range files {
		for seq, v := range f.passages {
			sql = append(sql,
				fmt.Sprintf("INSERT INTO gchunks (tinode, fname, seq, ctext, h) VALUES ('%d', '%s', %d, '%s', 'h%d%d');", f.inode, f.name, seq, f.text, f.inode, seq),
				"INSERT INTO gvec (cid, fp384) VALUES ((SELECT max(cid) FROM gchunks), X'"+hexOf(v)+"');")
		}
	}
	w.sqlite(filepath.Join(far, folder, "db.db"), sql)
}

func fetched(t *testing.T, home string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "vectors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func eq(t *testing.T, got, want any, what string) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if !bytes.Equal(g, w) {
		t.Errorf("%s: got %s, want %s", what, g, w)
	}
}

func TestRemoteMap(t *testing.T) {
	w := newWorld(t)
	// An index with vectors on the remote host: folders about two topics,
	// with inodes of their own (a sample is taken by inode), and a folder
	// whose vectors come from another model, which the map leaves out.
	for _, folder := range []string{"turbines", "kitchen", "other"} {
		write(t, filepath.Join(w.home, "vsrc", folder, "keep.txt"), "x")
	}
	w.index(filepath.Join(w.home, "vsrc"), filepath.Join(w.home, "vidx"))
	far := filepath.Join(w.home, "vidx", "vsrc")
	var turb, kit, other []*farFile
	for i := 0; i < 30; i++ {
		turb = append(turb, w.farFile("turbines", i, 10, "turbine blade rotor wind turbine blade"))
	}
	for i := 0; i < 30; i++ {
		kit = append(kit, w.farFile("kitchen", i, 200, "sourdough starter flour oven sourdough bread"))
	}
	for i := 0; i < 4; i++ {
		other = append(other, w.farFile("other", i, 10, "turbine elsewhere"))
	}
	w.farFolder(far, "turbines", "minilm-l6-mean-int8", turb)
	w.farFolder(far, "kitchen", "minilm-l6-mean-int8", kit)
	w.farFolder(far, "other", "another-model", other)
	var mapped []*farFile
	for _, f := range w.files {
		if !strings.HasPrefix(f.p, "other/") {
			mapped = append(mapped, f)
		}
	}
	paths := func(fs []*farFile) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.p)
		}
		sort.Strings(out)
		return out
	}

	status, added := w.call("POST", "/api/user-libraries/ssh", map[string]any{"destination": "me@query-only", "label": "Far", "indexRoot": far})
	if status != 200 {
		t.Fatalf("added: %d %v", status, added)
	}
	farID := added["id"].(string)
	users := &libraries.Users{IndexBase: filepath.Join(w.dir, "ix")}
	farHome, err := users.RemoteMapHome(libraries.RemoteRootOf("single", farID))
	if err != nil {
		t.Fatal(err)
	}
	var fetchedRows []map[string]any

	t.Run("a sample fetched with gufi_query alone, the map made here in its owner's folder", func(t *testing.T) {
		w.clearLog()
		first := w.get("/api/libraries/" + farID + "/semantic-map")
		lib, _ := first["library"].(map[string]any)
		eq(t, []any{first["available"], first["building"], lib["kind"], lib["host"]}, []any{false, true, "remote", "me@query-only"}, "first look")
		o := w.settled(farID)
		eq(t, []any{o["available"], o["files"], o["of"], o["model"]}, []any{true, 60, 60, "minilm-l6-mean-int8"}, "map")
		rem, _ := o["remote"].(map[string]any)
		eq(t, rem["host"], "me@query-only", "host")
		// The models its vectors there were made with: the map is of the one most files use.
		eq(t, rem["models"], []any{map[string]any{"model": "minilm-l6-mean-int8", "files": 60}, map[string]any{"model": "another-model", "files": 4}}, "models")
		if rem["fetchedAt"] == nil || rem["bytes"].(float64) <= 60*768 {
			t.Errorf("remote: %v", rem)
		}
		if cl, _ := o["clusters"].([]any); len(cl) < 2 {
			t.Errorf("clusters: %v", o["clusters"])
		}
		// One walk to count, one to fetch: gufi_query alone.
		sent := w.commands()
		if len(sent) != 2 || !strings.HasPrefix(sent[0], "gufi_query ") || !strings.HasPrefix(sent[1], "gufi_query ") ||
			!strings.Contains(sent[0], "gvec_rowids") || !strings.Contains(sent[1], "vec_add") {
			t.Fatalf("sent:\n%s", strings.Join(sent, "\n"))
		}
		// Kept in the owner's folder, not the shared one the library ids name.
		if !strings.HasPrefix(farHome, filepath.Join(w.dir, "ix", "users")) {
			t.Errorf("home %s", farHome)
		}
		if _, err := os.Stat(filepath.Join(farHome, "semantic-map", "overview.json")); err != nil {
			t.Error(err)
		}
		// Each file once, below the library's top, as the mean of its
		// passages; the other model's files are left out.
		fetchedRows = fetched(t, farHome)
		var got []string
		hexRow := regexp.MustCompile(`^[0-9A-F]{768}$`)
		for _, r := range fetchedRows {
			p := r["p"].(string)
			got = append(got, p)
			want := "sourdough"
			if strings.HasPrefix(p, "turbines") {
				want = "turbine"
			}
			if !hexRow.MatchString(r["v"].(string)) || !strings.HasPrefix(r["t"].(string), want) {
				t.Errorf("row %v", r)
			}
		}
		sort.Strings(got)
		eq(t, got, paths(mapped), "fetched")
		for _, r := range fetchedRows[:5] {
			var f *farFile
			for _, x := range w.files {
				if x.p == r["p"] {
					f = x
				}
			}
			back := maps.Dequantize(r["v"].(string), r["r"].(float64))
			var dot, nm, ng float64
			for i := range back {
				m := (float64(f.passages[0][i]) + float64(f.passages[1][i])) / 2
				dot += m * float64(back[i])
				nm += m * m
				ng += float64(back[i]) * float64(back[i])
			}
			if dot/math.Sqrt(nm)/math.Sqrt(ng) <= 0.995 || math.Abs(math.Sqrt(ng)-math.Sqrt(nm)) >= 1e-3 {
				t.Errorf("%s: the mean comes back off (%v, %v %v)", f.p, dot, nm, ng)
			}
		}
		// Its clusters at the library's address, and in the picker's list.
		if pts, _ := w.get("/api/libraries/" + farID + "/semantic-map/cluster/0?limit=100")["points"].([]any); len(pts) == 0 {
			t.Error("no points in cluster 0")
		}
		var listed map[string]any
		for _, m := range w.get("/api/semantic-maps")["maps"].([]any) {
			if m.(map[string]any)["id"] == farID {
				listed = m.(map[string]any)
			}
		}
		eq(t, []any{listed["available"], listed["files"]}, []any{true, 60}, "listed")
	})
	if t.Failed() {
		return
	}

	t.Run("a file's nearest files there come from a search there, kept until the map is made again", func(t *testing.T) {
		w.clearLog()
		status, near := w.call("GET", "/api/libraries/"+farID+"/semantic-map/neighbors?file=turbines/t0.md&k=4", nil)
		if status != 200 {
			t.Fatalf("%d %v", status, near)
		}
		ns, _ := near["neighbors"].([]any)
		if len(ns) != 4 {
			t.Fatalf("neighbors %v", near)
		}
		for _, n := range ns {
			n := n.(map[string]any)
			p := n["p"].(string)
			if !strings.HasPrefix(p, "turbines/") || p == "turbines/t0.md" || n["sim"].(float64) <= 0.9 {
				t.Errorf("neighbor %v", n)
			}
			if _, ok := n["x"].(float64); !ok {
				t.Errorf("%s has no place on the map", p)
			}
		}
		sent := w.commands()
		if len(sent) != 1 || !regexp.MustCompile(`^gufi_query .*MATCH`).MatchString(sent[0]) {
			t.Fatalf("sent %q", sent)
		}
		w.get("/api/libraries/" + farID + "/semantic-map/neighbors?file=turbines/t0.md&k=4")
		eq(t, len(w.commands()), 1, "answered from what was kept")
		none := w.get("/api/libraries/" + farID + "/semantic-map/neighbors?file=no/such.md")
		eq(t, []any{none["neighbors"], none["reason"]}, []any{[]any{}, "none"}, "none")
	})

	t.Run("past the cap, the sample is the files whose inodes hash lowest: the same files every time", func(t *testing.T) {
		was := w.m.RemoteFiles
		w.m.RemoteFiles = func() int { return 5 }
		defer func() { w.m.RemoteFiles = was }()
		byHash := append([]*farFile{}, mapped...)
		sort.SliceStable(byHash, func(i, j int) bool { return maps.FileHash(int64(byHash[i].inode)) < maps.FileHash(int64(byHash[j].inode)) })
		want := paths(byHash[:5])
		for round := 0; round < 2; round++ {
			w.clearLog()
			status, r := w.call("POST", "/api/libraries/"+farID+"/semantic-map/refresh", nil)
			if status != 200 || r["building"] != true {
				t.Fatalf("refresh: %d %v", status, r)
			}
			o := w.settled(farID)
			eq(t, []any{o["files"], o["of"]}, []any{5, 60}, "capped")
			var got []string
			for _, x := range fetched(t, farHome) {
				got = append(got, x["p"].(string))
			}
			sort.Strings(got)
			eq(t, got, want, "lowest hashes")
			sent := w.commands()
			if len(sent) != 2 || !regexp.MustCompile(`% 2147483648 < \d+`).MatchString(sent[1]) {
				t.Errorf("sent %q", sent)
			}
		}
	})

	t.Run("looked at, a map is checked at most hourly: fetched again only when its vectors changed there", func(t *testing.T) {
		look := func() {
			w.m.RemoteCheck = 0
			w.get("/api/libraries/" + farID + "/semantic-map")
			w.m.RemoteCheck = time.Hour
		}
		fetches := func() []bool {
			var out []bool
			for _, c := range w.commands() {
				out = append(out, strings.Contains(c, "vec_add"))
			}
			return out
		}
		// Unchanged there: one count, nothing fetched (the capped map of
		// the case before is made whole again by a refresh first).
		w.call("POST", "/api/libraries/"+farID+"/semantic-map/refresh", nil)
		w.settled(farID)
		w.clearLog()
		look()
		w.settled(farID)
		eq(t, fetches(), []bool{false}, "unchanged")
		// A file more there: the count differs, so the files are fetched again.
		w.farFolder(far, "kitchen", "minilm-l6-mean-int8", []*farFile{w.farFile("kitchen", 30, 200, "sourdough rye")})
		w.clearLog()
		look()
		o := w.settled(farID)
		eq(t, []any{o["files"], o["of"]}, []any{61, 61}, "one more")
		eq(t, fetches(), []bool{false, true}, "changed")
		// Not due: viewing it sends nothing.
		w.clearLog()
		w.get("/api/libraries/" + farID + "/semantic-map")
		time.Sleep(100 * time.Millisecond)
		eq(t, len(w.commands()), 0, "not due")
	})

	t.Run("vectors that could not be fetched: the map stays, with the error; with none yet, a failed state to retry", func(t *testing.T) {
		write(t, w.failMark, "")
		defer os.Remove(w.failMark)
		w.call("POST", "/api/libraries/"+farID+"/semantic-map/refresh", nil)
		o := w.settled(farID)
		eq(t, o["available"], true, "the map stays")
		if s, _ := o["error"].(string); !regexp.MustCompile(`me@query-only: Connection closed`).MatchString(s) {
			t.Errorf("error %q", o["error"])
		}
		// Another library, of a copy of that index, with no map yet.
		cp := filepath.Join(w.home, "vidx2", "vsrc")
		os.MkdirAll(filepath.Dir(cp), 0o755)
		if out, err := exec.Command("cp", "-a", far, filepath.Dir(cp)).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		status, again := w.call("POST", "/api/user-libraries/ssh", map[string]any{"destination": "me@query-only", "label": "Far again", "indexRoot": cp})
		if status != 200 {
			t.Fatalf("%d %v", status, again)
		}
		againID := again["id"].(string)
		w.get("/api/libraries/" + againID + "/semantic-map")
		failed := w.settled(againID)
		eq(t, []any{failed["available"], failed["reason"]}, []any{false, "failed"}, "failed")
		if s, _ := failed["error"].(string); !strings.Contains(s, "Connection closed") {
			t.Errorf("error %q", failed["error"])
		}
		os.Remove(w.failMark)
		_, retry := w.call("POST", "/api/libraries/"+againID+"/semantic-map/refresh", nil)
		eq(t, []any{retry["building"], retry["error"]}, []any{true, nil}, "retry")
		eq(t, w.settled(againID)["available"], true, "made on retry")
		// Removed, its folder goes with it.
		home2, _ := users.RemoteMapHome(libraries.RemoteRootOf("single", againID))
		if _, err := os.Stat(home2); err != nil {
			t.Fatal(err)
		}
		if status, _ := w.call("DELETE", "/api/user-libraries/"+againID, nil); status != 200 {
			t.Fatalf("delete %d", status)
		}
		if _, err := os.Stat(home2); !os.IsNotExist(err) {
			t.Errorf("%s stays: %v", home2, err)
		}
	})

	t.Run("a library there with no vectors says so, after one count there", func(t *testing.T) {
		write(t, filepath.Join(w.home, "plain", "a0", "x.txt"), "abc")
		w.index(filepath.Join(w.home, "plain"), filepath.Join(w.home, "pidx"))
		_, added := w.call("POST", "/api/user-libraries/ssh", map[string]any{"destination": "me@query-only", "label": "Plain", "indexRoot": filepath.Join(w.home, "pidx", "plain")})
		id, _ := added["id"].(string)
		if id == "" {
			t.Fatalf("added %v", added)
		}
		w.clearLog()
		w.get("/api/libraries/" + id + "/semantic-map")
		o := w.settled(id)
		lib, _ := o["library"].(map[string]any)
		eq(t, []any{o["available"], o["reason"], lib["kind"]}, []any{false, "remote-none", "remote"}, "none")
		sent := w.commands()
		if len(sent) != 1 || !strings.HasPrefix(sent[0], "gufi_query ") {
			t.Errorf("sent %q", sent)
		}
		near := w.get("/api/libraries/" + id + "/semantic-map/neighbors?file=a0/x.txt")
		eq(t, []any{near["neighbors"], near["reason"]}, []any{[]any{}, "none"}, "no neighbors")
	})

	t.Run("a library there with too few files with vectors for a map says how many", func(t *testing.T) {
		write(t, filepath.Join(w.home, "fsrc", "x", "keep.txt"), "x")
		w.index(filepath.Join(w.home, "fsrc"), filepath.Join(w.home, "fidx"))
		few := filepath.Join(w.home, "fidx", "fsrc")
		w.sqlite(filepath.Join(few, "x", "db.db"), []string{
			"CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT);",
			"CREATE VIRTUAL TABLE gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);",
			"CREATE TABLE gvecmodel (model TEXT);", "INSERT INTO gvecmodel VALUES ('minilm384');",
			"INSERT INTO gchunks VALUES (1, '7001', 'one.md', 0, 'one', 'h1'); INSERT INTO gvec (cid, fp384) VALUES (1, X'" + hexOf(unit384(3, 4, 0.1)) + "');",
			"INSERT INTO gchunks VALUES (2, '7002', 'two.md', 0, 'two', 'h2'); INSERT INTO gvec (cid, fp384) VALUES (2, X'" + hexOf(unit384(5, 6, 0.1)) + "');",
		})
		status, added := w.call("POST", "/api/user-libraries/ssh", map[string]any{"destination": "me@query-only", "label": "Few", "indexRoot": few})
		if status != 200 {
			t.Fatalf("%d %v", status, added)
		}
		id := added["id"].(string)
		w.get("/api/libraries/" + id + "/semantic-map")
		o := w.settled(id)
		rem, _ := o["remote"].(map[string]any)
		eq(t, []any{o["available"], o["reason"], o["of"], rem["models"]}, []any{false, "remote-none", 2, []any{map[string]any{"model": "minilm384", "files": 2}}}, "few")
	})
}

// semanticMap.test.mjs's schedule cases and the routes over a map that is
// there, with the builder replaced as the Node test replaces it: each build
// is counted and writes a map of two clusters.
func TestLocalMapRoutesAndSchedule(t *testing.T) {
	w := newWorld(t)
	write(t, filepath.Join(w.dir, "kb", "notes", "a.md"), "# a\n")
	w.index(filepath.Join(w.dir, "kb"), filepath.Join(w.dir, "ix", "gufi"))
	var mu sync.Mutex
	builds := 0
	out := w.m.SemanticMapDir
	w.m.Build = func(tg maps.Target) error {
		mu.Lock()
		builds++
		mu.Unlock()
		if tg.Out != out {
			t.Errorf("out %s, want %s", tg.Out, out)
		}
		os.MkdirAll(tg.Out, 0o755)
		var pts []string
		for i := 0; i < 7; i++ {
			pts = append(pts, fmt.Sprintf(`{"p":"notes/%d.md","x":0.1,"y":0.2,"z":0.3}`, i))
		}
		write(t, filepath.Join(tg.Out, "overview.json"), `{"version":3,"available":true,"files":8,"shown":8,"clusters":[{"c":0,"label":"notes","count":7},{"c":1,"label":"other","count":1}],"points":[],"links":[]}`)
		write(t, filepath.Join(tg.Out, "cluster-0.json"), `{"c":0,"label":"notes","count":7,"points":[`+strings.Join(pts, ",")+`]}`)
		write(t, filepath.Join(tg.Out, "cluster-1.json"), `{"c":1,"label":"other","count":1,"points":[{"p":"x.md","x":0,"y":0,"z":0}]}`)
		return nil
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return builds }
	w.m.DelayScale = 1

	// Rebuilds asked for together run once.
	w.m.Schedule("kb", 30*time.Millisecond)
	w.m.Schedule("kb", 10*time.Millisecond)
	w.m.Schedule("kb", 50*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	eq(t, count(), 1, "one build for three asks")

	// The routes read the map and never make one while it is there.
	a := w.get("/api/kb/semantic-map")
	eq(t, []any{a["available"], a["building"]}, []any{true, false}, "there")
	w.get("/api/kb/semantic-map")
	time.Sleep(30 * time.Millisecond)
	eq(t, count(), 1, "reading does not rebuild")
	page := w.get("/api/kb/semantic-map/cluster/0?offset=0&limit=5")
	eq(t, []any{page["count"], len(page["points"].([]any))}, []any{7, 5}, "first page")
	rest := w.get("/api/kb/semantic-map/cluster/0?offset=5&limit=5000")
	eq(t, len(rest["points"].([]any)), 2, "the rest")
	if s, _ := w.call("GET", "/api/kb/semantic-map/cluster/99", nil); s != 404 {
		t.Errorf("cluster 99: %d", s)
	}
	if s, b := w.call("GET", "/api/kb/semantic-map/cluster/x", nil); s != 400 || b["error"] != "cluster: a number from 0" {
		t.Errorf("cluster x: %d %v", s, b)
	}
	if s, b := w.call("GET", "/api/kb/semantic-map/neighbors", nil); s != 400 || b["error"] != "file: a path in the library" {
		t.Errorf("neighbors without a file: %d %v", s, b)
	}

	// A missing map is made once, in the background, however many reads.
	os.RemoveAll(out)
	w.m.ResetForTests()
	before := count()
	r := w.get("/api/kb/semantic-map")
	eq(t, []any{r["available"], r["reason"], r["building"]}, []any{false, "none", true}, "missing")
	w.get("/api/kb/semantic-map")
	time.Sleep(50 * time.Millisecond)
	eq(t, count()-before, 1, "one build for a missing map")

	// Asked for by hand: a build now. With the map there, building is
	// whether one runs, as in Node, so the answer comes before it starts.
	time.Sleep(30 * time.Millisecond)
	before = count()
	if s, b := w.call("POST", "/api/kb/semantic-map/refresh", nil); s != 200 || b["available"] != true {
		t.Errorf("refresh: %d %v", s, b)
	}
	time.Sleep(50 * time.Millisecond)
	eq(t, count()-before, 1, "refresh builds")
}
