package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

func testApp(t *testing.T, env map[string]string, node *Node) *App {
	t.Helper()
	base := map[string]string{"INDEX_BASE": t.TempDir(), "KB_ROOT": t.TempDir()}
	for k, v := range env {
		base[k] = v
	}
	cfg, err := config.Load(func(k string) string { return base[k] })
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, slog.New(slog.DiscardHandler), nil, node)
}

// lifecycle.ts: RESTART_EXIT_CODE 75, RELAUNCH_EXIT_CODE 76; a restart
// only where the launcher can (STUDIO_RESTARTABLE=1) and a stopper is set,
// about 300 ms after the request.
func TestRestartContract(t *testing.T) {
	if RestartExitCode != 75 || RelaunchExitCode != 76 {
		t.Fatal("exit codes must match lifecycle.ts and the launchers")
	}
	src, err := os.ReadFile("../../../server/src/lifecycle.ts")
	if err == nil && (!strings.Contains(string(src), "RESTART_EXIT_CODE = 75") || !strings.Contains(string(src), "RELAUNCH_EXIT_CODE = 76")) {
		t.Fatal("lifecycle.ts's exit codes moved")
	}
	a := testApp(t, nil, nil)
	t.Setenv("STUDIO_RESTARTABLE", "")
	got := make(chan int, 1)
	a.SetStopper(func(c int) { got <- c })
	if a.RequestRestart(RestartExitCode) {
		t.Fatal("restarted without STUDIO_RESTARTABLE")
	}
	t.Setenv("STUDIO_RESTARTABLE", "1")
	start := time.Now()
	if !a.RequestRestart(RelaunchExitCode) {
		t.Fatal("no restart")
	}
	select {
	case c := <-got:
		if c != 76 || time.Since(start) < 250*time.Millisecond {
			t.Fatalf("code %d after %v", c, time.Since(start))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopper not called")
	}
	b := testApp(t, nil, nil)
	if b.RequestRestart(75) {
		t.Fatal("restarted without a stopper")
	}
}

// The launcher's studio.env as saveLauncherSetting writes it: KEY='value'
// lines in the file's order, single quotes escaped for a POSIX shell, 0600.
func TestLauncherSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "conf", "studio.env")
	t.Setenv("STUDIO_CONFIG_FILE", file)
	if err := SaveLauncherSetting("PORT", "4080"); err != nil {
		t.Fatal(err)
	}
	if err := SaveLauncherSetting("KB_ROOT", "/home/a b/it's"); err != nil {
		t.Fatal(err)
	}
	SaveLauncherSetting("PORT", "4100")
	b, _ := os.ReadFile(file)
	if string(b) != "PORT='4100'\nKB_ROOT='/home/a b/it'\\''s'\n" {
		t.Fatalf("%q", b)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Fatal("mode")
	}
	if s := ReadLauncherSettings(); s["KB_ROOT"] != "/home/a b/it's" || s["PORT"] != "4100" {
		t.Fatal(s)
	}
	for _, bad := range [][2]string{{"lower", "x"}, {"A", "two\nlines"}} {
		if SaveLauncherSetting(bad[0], bad[1]) == nil {
			t.Error("accepted", bad)
		}
	}
	// Node reads what Go wrote.
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		return
	}
	out, err := exec.Command(node, "--input-type=module", "-e", `const L = await import(process.argv[1]); console.log(JSON.stringify(L.readLauncherSettings()))`, mustAbs("../../../server/dist/lifecycle.js")).Output()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	json.Unmarshal(out, &m)
	if m["KB_ROOT"] != "/home/a b/it's" {
		t.Fatalf("Node read %s", out)
	}
}

func mustAbs(p string) string { a, _ := filepath.Abs(p); return a }

// instanceLock.ts: a free folder is taken; a lock of a process that is
// gone, or that is no Studio, is cleared; a live Studio keeps it, is named,
// and index work is refused with 409.
func TestIndexLock(t *testing.T) {
	dir := t.TempDir()
	l := NewIndexLock(dir, 4080)
	ctx := context.Background()
	if !l.Acquire(ctx, func(string) {}, time.Second) || !l.Held() || !l.IndexWorkAllowed() {
		t.Fatal("free folder not taken")
	}
	var h Holder
	b, _ := os.ReadFile(filepath.Join(dir, ".studio-instance"))
	if json.Unmarshal(b, &h) != nil || h.PID != os.Getpid() || h.Port == nil || *h.Port != 4080 {
		t.Fatalf("lock %s", b)
	}
	l.Release()
	if _, err := os.Stat(filepath.Join(dir, ".studio-instance")); err == nil {
		t.Fatal("not released")
	}

	// A process that is no Studio (sleep) holds a stale lock: cleared.
	sleep := exec.Command("sleep", "30")
	sleep.Start()
	defer sleep.Process.Kill()
	os.WriteFile(filepath.Join(dir, ".studio-instance"), []byte(`{"pid":`+strconv.Itoa(sleep.Process.Pid)+`,"port":null}`), 0o644)
	if !NewIndexLock(dir, 0).Acquire(ctx, func(string) {}, time.Second) {
		t.Fatal("stale lock kept")
	}

	// A live studio-server holds it.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	other := exec.Command(bash, "-c", "exec -a studio-server sleep 30")
	other.Start()
	defer other.Process.Kill()
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, ".studio-instance"), []byte(`{"pid":`+strconv.Itoa(other.Process.Pid)+`,"port":4090,"startedAt":"x"}`), 0o644)
	var logged string
	l2 := NewIndexLock(dir, 0)
	if l2.Acquire(ctx, func(m string) { logged = m }, 400*time.Millisecond) {
		t.Fatal("took a live Studio's folder")
	}
	if !strings.Contains(logged, "another Studio (process "+strconv.Itoa(other.Process.Pid)+", port 4090)") || l2.IndexWorkAllowed() {
		t.Fatal(logged)
	}
	err = l2.RequireIndexWork()
	var kb *apierr.Error
	if e, ok := err.(*apierr.Error); ok {
		kb = e
	}
	if kb == nil || kb.Status != 409 || !strings.HasPrefix(kb.Message, "Another Studio (process ") {
		t.Fatalf("%v", err)
	}
}

func TestHooksAndIndexWork(t *testing.T) {
	a := testApp(t, nil, nil) // Node behind: no index work here
	var ran, indexed atomic.Int32
	a.OnStart("s", func(context.Context) { ran.Add(1) })
	a.OnIndexWork("i", func(context.Context) { indexed.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	a.Start(ctx)
	cancel()
	a.Wait(time.Second)
	if ran.Load() != 1 || indexed.Load() != 0 {
		t.Fatalf("start %d index %d", ran.Load(), indexed.Load())
	}
	b := testApp(t, map[string]string{"STUDIO_NODE": "off"}, nil)
	b.OnIndexWork("i", func(context.Context) { indexed.Add(1) })
	b.Go("panics", func(context.Context) { panic("x") })
	b.Start(context.Background())
	b.Wait(time.Second)
	if indexed.Load() != 1 || !b.Lock.Held() {
		t.Fatal("Node-free mode does the index work")
	}
	done := b.Stream()
	busy := false
	b.AddBusy(func() bool { return busy })
	if !b.Busy() || b.StreamsInFlight() != 1 {
		t.Fatal("a stream is busy")
	}
	done()
	done()
	if b.Busy() || b.StreamsInFlight() != 0 {
		t.Fatal("done twice counts once")
	}
	b.Drain("test", time.Second)
	if b.Lock.Held() {
		t.Fatal("lock kept after drain")
	}
}

func TestProviderDefaults(t *testing.T) {
	// Node-free: the plainest true answers.
	a := testApp(t, map[string]string{"STUDIO_NODE": "off"}, nil)
	libs, _ := a.Providers.Libraries(nil, "Files", "")
	var lib map[string]any
	json.Unmarshal(libs[0], &lib)
	if len(libs) != 1 || lib["id"] != "kb" || lib["label"] != "Files" || lib["primary"] != true {
		t.Fatalf("%s", libs[0])
	}
	if a.Providers.PlatformAvailable(nil, "") {
		t.Fatal("platform")
	}
	if _, err := a.Providers.StartBuild(nil); err == nil {
		t.Fatal("build without an owner")
	}
	if st, _ := a.Providers.SetupIndexState(nil, false, true); string(st.Job) != "null" || st.Files != nil {
		t.Fatal("setup state")
	}

	// Node behind: Node's answer, marked internal, identity passed on.
	var seen []string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get(InternalHeader)+" "+r.Header.Get("x-pw-user-token"))
		io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/api/config":
			w.Write([]byte(`{"libraries":[{"id":"kb"},{"id":"lib2"}],"platform":true}`))
		case "/api/settings":
			w.Write([]byte(`{"deploymentCredential":true}`))
		case "/api/setup/status":
			w.Write([]byte(`{"primary":{"files":3,"earlierIndex":{"builtAt":null}},"job":null}`))
		default:
			w.Write([]byte(`{"id":1}`))
		}
	}))
	defer node.Close()
	u, _ := url.Parse(node.URL)
	b := testApp(t, nil, NewNode(u, nil, "x-pw-user-token"))
	r := httptest.NewRequest("GET", "/api/config", nil)
	r.Header.Set("x-pw-user-token", "tok")
	libs, err := b.Providers.Libraries(r, "", "")
	if err != nil || len(libs) != 2 {
		t.Fatal(err, libs)
	}
	if !b.Providers.PlatformAvailable(r, "") || !b.Providers.GatewayConfigured(r) {
		t.Fatal("platform or credential")
	}
	st, err := b.Providers.SetupIndexState(r, true, true)
	if err != nil || *st.Files != 3 || string(st.EarlierIndex) != `{"builtAt":null}` {
		t.Fatal(err, st)
	}
	if job, _ := b.Providers.StartBuild(r); string(job) != `{"id":1}` {
		t.Fatal(string(job))
	}
	b.Providers.ApplyLabels(context.Background(), r, []string{"papers"}, []string{"paper"}, nil)
	if !strings.Contains(strings.Join(seen, "\n"), "GET /api/config 1 tok") || !strings.Contains(strings.Join(seen, "\n"), "POST /api/kb/tags 1 tok") || !strings.Contains(strings.Join(seen, "\n"), "GET /api/setup/status?files=1 1 tok") {
		t.Fatal(seen)
	}
}

func TestSeed(t *testing.T) {
	a := testApp(t, map[string]string{"STUDIO_NODE": "off"}, nil)
	t.Setenv("KB_STARTER_DIRS", "papers, bad/../name ,")
	var labels []string
	a.Providers.ApplyLabels = func(_ context.Context, _ *http.Request, p, add, _ []string) error {
		labels = append(labels, p[0]+"="+add[0])
		return nil
	}
	a.SeedKnowledgeBase(context.Background(), func(string) {})
	readme, err := os.ReadFile(filepath.Join(a.Cfg.KBRoot, "README.md"))
	if err != nil || !strings.Contains(string(readme), "- papers\n- bad..name\n") {
		t.Fatal(string(readme))
	}
	if strings.Join(labels, ",") != "papers=paper" {
		t.Fatal(labels)
	}
	os.WriteFile(filepath.Join(a.Cfg.KBRoot, "README.md"), []byte("mine"), 0o644)
	a.SeedKnowledgeBase(context.Background(), func(string) {})
	if b, _ := os.ReadFile(filepath.Join(a.Cfg.KBRoot, "README.md")); string(b) != "mine" {
		t.Fatal("seeded a folder with files in it")
	}
}
