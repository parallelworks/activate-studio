package index

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// Register serves the index's routes (indexRoutes.ts) and the history routes (history.ts) where Go builds the index
// (Node-free mode, or STUDIO_GO_INDEX=1), sets the index providers, and
// runs the background sync where this process holds the index lock.
// Elsewhere it registers nothing, and the routes stay Node's.
func Register(mux *http.ServeMux, a *app.App) {
	if !a.GoIndexes() {
		return
	}
	ix := Of(a)
	if !a.Cfg.NodeOff {
		// Node runs beside (STUDIO_GO_INDEX=1) and numbers its own index jobs
		// (a library's build) from 1: Go's start above them, and a job id
		// below that is Node's.
		ix.mu.Lock()
		ix.seq = max(ix.seq, NodeJobIDs)
		ix.mu.Unlock()
	}
	ix.Hooks.RequireWritable = ix.defaultRequireWritable
	ix.History = newHistory(ix)
	ix.register(mux)
	a.Providers.StartBuild = func(r *http.Request) (json.RawMessage, error) {
		j, err := ix.StartBuild()
		if err != nil {
			return nil, err
		}
		return marshal(j)
	}
	a.Providers.IndexDir = func(r *http.Request, rel string) error {
		_, err := ix.IndexDir(r.Context(), rel)
		return err
	}
	a.Providers.SetupIndexState = func(r *http.Request, indexed, files bool) (app.SetupIndex, error) {
		out := app.SetupIndex{Job: json.RawMessage("null"), EarlierIndex: json.RawMessage("null")}
		if j := ix.CurrentBuild(); j != nil {
			out.Job, _ = marshal(j)
		}
		if !indexed {
			if e := ix.EarlierIndexOf(ix.P.KBRoot); e != nil {
				out.EarlierIndex, _ = marshal(e)
			}
		}
		return out, nil
	}
	a.Providers.IndexDirMS = func(ctx context.Context, rel string) (int64, error) { return ix.IndexDir(ctx, rel) }
	a.Providers.IndexRootDB = func(ctx context.Context) (int64, error) { return ix.IndexRootDb(ctx) }
	// afterChange: the folder list and the assistant's view of the folder
	// are stale (gufi.ts, chat/context.ts); their owners answer Changed.
	ix.Hooks.InvalidateContext = func() { a.Providers.Changed(context.Background(), "context") }
	ix.OnChange(func(string) { a.Providers.Changed(context.Background(), "dbList", "context") })
	a.AddBusy(ix.Running)
	a.OnIndexWork("index sync", func(ctx context.Context) {
		ix.Start(ctx, func(ctx context.Context, log func(string)) {
			if snap, err := ix.History.CaptureIfStale(ctx); err == nil && snap != nil {
				log(fmt.Sprintf("history: captured snapshot %s (%d files)", snap.ID, snap.Files))
			}
		})
	})
	a.OnStop("index jobs", func() int { return ix.CancelAll() })
}

// NodeJobIDs is where Go's index job ids start while Node runs beside it.
const NodeJobIDs = 1 << 21

func writeOK(w http.ResponseWriter, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(b)
	return err
}

// readBody is the request's JSON object, or an empty one.
func readBody(r *http.Request) map[string]any {
	m := map[string]any{}
	if r.Body == nil {
		return m
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	// Kept, for a request handed on to Node.
	r.Body = io.NopCloser(bytes.NewReader(b))
	if len(b) > 0 {
		json.Unmarshal(b, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// jsNum is JavaScript's Number(v); NaN for what is not a number.
func jsNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case nil:
		return 0
	case string:
		t := strings.TrimSpace(x)
		if t == "" {
			return 0
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}

// jsString is String(v) for the values a JSON body holds.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		b, _ := json.Marshal(x)
		return string(b)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (ix *Indexer) defaultRequireWritable(r *http.Request, library string) error {
	want := strings.TrimSpace(library)
	if want == "" {
		want = "kb"
	}
	if strings.ToLower(want) == "all" {
		return apierr.New(400, "This action works in one library at a time, and the items are in more than one (All libraries is selected).")
	}
	if ix.App == nil || ix.App.Providers.Libraries == nil {
		if want == "kb" {
			return nil
		}
		return apierr.New(404, "no library named %s", want)
	}
	label := ""
	if ix.App.Settings != nil {
		label = ix.App.Settings.Effective().KbLabel
	}
	viewer := ""
	if u := auth.UserOf(r); u != nil {
		viewer = u.ID
	}
	libs, err := ix.App.Providers.Libraries(r, label, viewer)
	if err != nil {
		return err
	}
	for _, raw := range libs {
		var l struct {
			ID       string `json:"id"`
			Label    string `json:"label"`
			Writable bool   `json:"writable"`
		}
		if json.Unmarshal(raw, &l) != nil || l.ID != want {
			continue
		}
		if !l.Writable {
			return apierr.New(403, "%s is read only", l.Label)
		}
		return nil
	}
	return apierr.New(404, "no library named %s", want)
}

// primary is the library a request names (?library= or its body's).
func primaryOf(r *http.Request, body map[string]any) string {
	q := r.URL.Query()
	if q.Has("library") {
		return q.Get("library")
	}
	if v, ok := body["library"]; ok && v != nil {
		return jsString(v)
	}
	return ""
}

func (ix *Indexer) authEnabled() bool { return ix.App != nil && ix.App.Cfg.AuthEnabled() }

func (ix *Indexer) requireSingleUser(what string) error {
	if ix.authEnabled() {
		return apierr.New(403, "%s is for a single-user install; on this Studio the deployment manages the index", what)
	}
	return nil
}

func (ix *Indexer) readerOptions() ReaderOptions {
	s := ix.Settings.Get("kb")
	vision := false
	if ix.App != nil && ix.App.Settings != nil {
		eff := ix.App.Settings.Effective()
		vision = eff.AiEnabled && s.OCR && s.ImageDescriptions && eff.VisionModel != ""
	}
	return ReaderOptions{Documents: s.Documents, OCR: s.OCR, LegacyOffice: s.LegacyOffice, PDFReader: s.PDFReader, Vision: vision}
}

func (ix *Indexer) report() Report { return ExtractorReportFor(ix.readerOptions(), ix.P.ProjectRoot) }

func expandHome(p string) string {
	home, _ := os.UserHomeDir()
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(home, p)
}

func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		if a, err := filepath.Abs(r); err == nil {
			return a
		}
	}
	a, _ := filepath.Abs(p)
	return a
}

func (ix *Indexer) register(mux *http.ServeMux) {
	var log = ix.App.Log
	wrap := func(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc { return apierr.Handler(log, h) }
	// with the request's body read once, and the library it names checked.
	writable := func(h func(w http.ResponseWriter, r *http.Request, body map[string]any) error) http.HandlerFunc {
		return wrap(func(w http.ResponseWriter, r *http.Request) error {
			body := readBody(r)
			if err := ix.Hooks.RequireWritable(r, primaryOf(r, body)); err != nil {
				return err
			}
			return h(w, r, body)
		})
	}
	notFound := func(w http.ResponseWriter) error {
		apierr.JSON(w, 404, map[string]string{"error": "no such job"})
		return nil
	}
	jobID := func(r *http.Request) int {
		f := jsNum(r.PathValue("id"))
		if f != math.Trunc(f) || math.IsNaN(f) {
			return -1
		}
		return int(f)
	}

	mux.HandleFunc("GET /api/index/status", wrap(func(w http.ResponseWriter, r *http.Request) error {
		st := ix.Status()
		o := st["\x00ordered"].(ordered)
		info := ix.Log.Info()
		logObj := ordered{}
		for _, k := range []string{"path", "bytes", "updatedAt"} {
			if v, ok := info[k]; ok {
				logObj = append(logObj, [2]any{k, v})
			}
		}
		for k, v := range info {
			if k != "path" && k != "bytes" && k != "updatedAt" {
				logObj = append(logObj, [2]any{k, v})
			}
		}
		logObj = append(logObj, [2]any{"reveal", runtime.GOOS == "darwin" && !ix.authEnabled()})
		o = append(o, [2]any{"log", logObj.Map()})
		return writeOK(w, o.Map())
	}))

	mux.HandleFunc("GET /api/index/extractors", wrap(func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Query().Get("fresh") == "1" {
			ForgetExtractors()
		}
		return writeOK(w, ix.report())
	}))

	mux.HandleFunc("POST /api/index/dir", writable(func(w http.ResponseWriter, r *http.Request, body map[string]any) error {
		rel := ""
		if v, ok := body["path"]; ok && v != nil {
			rel = jsString(v)
		}
		if rel == "" {
			return writeOK(w, map[string]string{"error": "path required"})
		}
		ms, err := ix.IndexDir(r.Context(), rel)
		if err != nil {
			return err
		}
		return writeOK(w, ordered{{"indexed", rel}, {"ms", ms}}.Map())
	}))

	mux.HandleFunc("POST /api/index/job", writable(func(w http.ResponseWriter, r *http.Request, body map[string]any) error {
		rel := ""
		if v, ok := body["path"]; ok && v != nil {
			rel = jsString(v)
		}
		j, err := ix.Enqueue(strings.Trim(rel, "/"))
		if err != nil {
			return err
		}
		return writeOK(w, j)
	}))

	// A job of Node's: with Node beside, library builds are Node's (the
	// libraries track serves them in Go only Node-free), so their job ids
	// are answered there.
	nodeJob := func(w http.ResponseWriter, r *http.Request) bool {
		id := jobID(r)
		if ix.App.NodeFree() || id < 0 || id >= NodeJobIDs {
			return false
		}
		ix.App.Node().ServeHTTP(w, app.HandOff(r, "an index job of Node's"))
		return true
	}
	getJob := wrap(func(w http.ResponseWriter, r *http.Request) error {
		j, ok := ix.Job(jobID(r))
		if !ok {
			if nodeJob(w, r) {
				return nil
			}
			return notFound(w)
		}
		return writeOK(w, j)
	})
	mux.HandleFunc("GET /api/index/job/{id}", getJob)

	mux.HandleFunc("POST /api/index/job/{id}/cancel", writable(func(w http.ResponseWriter, r *http.Request, _ map[string]any) error {
		j, ok := ix.Cancel(jobID(r))
		if !ok {
			if nodeJob(w, r) {
				return nil
			}
			return notFound(w)
		}
		return writeOK(w, j)
	}))

	mux.HandleFunc("POST /api/index/cancel", writable(func(w http.ResponseWriter, r *http.Request, _ map[string]any) error {
		return writeOK(w, map[string]int{"stopped": ix.CancelAll()})
	}))

	mux.HandleFunc("POST /api/index/build", writable(func(w http.ResponseWriter, r *http.Request, _ map[string]any) error {
		j, err := ix.StartBuild()
		if err != nil {
			return err
		}
		return writeOK(w, j)
	}))

	mux.HandleFunc("POST /api/reindex", writable(func(w http.ResponseWriter, r *http.Request, _ map[string]any) error {
		j, err := ix.StartBuild()
		if err != nil {
			return err
		}
		return writeOK(w, ordered{{"started", true}, {"job", j}}.Map())
	}))

	mux.HandleFunc("DELETE /api/index", wrap(func(w http.ResponseWriter, r *http.Request) error {
		if err := ix.requireSingleUser("Deleting the index"); err != nil {
			return err
		}
		body := readBody(r)
		if err := ix.Hooks.RequireWritable(r, primaryOf(r, body)); err != nil {
			return err
		}
		if r.URL.Query().Get("confirm") != "1" {
			return apierr.New(400, "add ?confirm=1: this deletes the index (the files stay)")
		}
		j, err := ix.StartDelete()
		if err != nil {
			return err
		}
		return writeOK(w, j)
	}))

	mux.HandleFunc("POST /api/index/sweep", writable(func(w http.ResponseWriter, r *http.Request, _ map[string]any) error {
		changed := ix.Sweep(r.Context(), func(m string) { log.Info(m) })
		return writeOK(w, map[string][]string{"changed": changed})
	}))

	mux.HandleFunc("GET /api/index/log", wrap(func(w http.ResponseWriter, r *http.Request) error {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if q.Get("full") == "1" {
			b, _ := os.ReadFile(ix.Log.File)
			w.Write(b)
			return nil
		}
		n := 400
		if f := jsNum(q.Get("lines")); !math.IsNaN(f) && f != 0 {
			n = int(f)
		}
		n = min(max(n, 1), 5000)
		w.Write([]byte(ix.Log.Tail(n)))
		return nil
	}))

	mux.HandleFunc("POST /api/index/log/reveal", wrap(func(w http.ResponseWriter, r *http.Request) error {
		if err := ix.requireSingleUser("Opening the log"); err != nil {
			return err
		}
		if runtime.GOOS != "darwin" {
			return apierr.New(409, "open %s on the machine the Studio runs on", ix.Log.File)
		}
		if !exists(ix.Log.File) {
			return apierr.New(404, "nothing has been logged yet")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "open", "-R", ix.Log.File).Run(); err != nil {
			return err
		}
		return writeOK(w, map[string]string{"opened": ix.Log.File})
	}))

	mux.HandleFunc("GET /api/index/settings", wrap(func(w http.ResponseWriter, r *http.Request) error {
		launcher := app.LauncherConfigFile() != ""
		saved := ""
		if launcher {
			saved = app.ReadLauncherSettings()["STUDIO_INDEX_DIR"]
		}
		aiOn, vm := false, ""
		if ix.App.Settings != nil {
			eff := ix.App.Settings.Effective()
			aiOn, vm = eff.AiEnabled, eff.VisionModel
		}
		return writeOK(w, ordered{
			{"settings", ix.Settings.Get("kb")},
			{"defaults", Defaults()},
			{"single", !ix.authEnabled()},
			{"cpus", CPUCount()},
			{"threads", ix.Settings.Threads("kb")},
			{"readers", ix.report()},
			{"ai", ordered{{"enabled", aiOn}, {"visionModel", vm}}.Map()},
			{"location", ordered{
				{"current", ix.P.IndexDir}, {"default", ix.P.IndexBase}, {"folder", ix.P.KBRoot},
				{"canChange", !ix.authEnabled() && launcher}, {"saved", saved},
			}.Map()},
		}.Map())
	}))

	mux.HandleFunc("PUT /api/index/settings", wrap(func(w http.ResponseWriter, r *http.Request) error {
		body := readBody(r)
		s, err := ix.Settings.Save("kb", body)
		if err != nil {
			return err
		}
		ForgetExtractors()
		for _, k := range []string{"depth", "documents", "ocr", "meaning", "embeddings", "meaningSpeed"} {
			if _, ok := body[k]; ok {
				ix.RequestSweep(2 * time.Second)
				break
			}
		}
		if _, ok := body["meaningSpeed"]; ok {
			ix.Hooks.RequestAllLibrarySyncs()
		}
		return writeOK(w, ordered{{"settings", s}, {"threads", ix.Settings.Threads("kb")}, {"readers", ix.report()}}.Map())
	}))

	mux.HandleFunc("POST /api/index/location", wrap(func(w http.ResponseWriter, r *http.Request) error {
		if err := ix.requireSingleUser("Moving the index"); err != nil {
			return err
		}
		if app.LauncherConfigFile() == "" {
			return apierr.New(409, "Set STUDIO_INDEX_DIR in the Studio's environment and restart it.")
		}
		body := readBody(r)
		raw := ""
		if v, ok := body["path"]; ok && v != nil {
			raw = strings.TrimSpace(jsString(v))
		}
		target := ix.P.IndexBase
		if raw != "" {
			target = expandHome(raw)
		}
		if realPath(target) == realPath(ix.P.IndexDir) {
			return writeOK(w, ordered{{"moved", false}, {"restarting", false}, {"note", "The index is already kept there."}}.Map())
		}
		if st, err := os.Stat(target); err == nil && !st.IsDir() {
			return apierr.New(400, "%s is not a folder", target)
		}
		if err := os.MkdirAll(target, 0o755); err != nil || !writableDir(target) {
			return apierr.New(400, "%s is not writable", target)
		}
		if within(realPath(ix.P.KBRoot), realPath(target)) {
			return apierr.New(400, "%s is inside the folder being indexed; choose a place outside it", target)
		}
		if within(realPath(target), realPath(ix.P.KBRoot)) {
			return apierr.New(400, "%s contains the folder being indexed; choose a place outside it", target)
		}
		if ix.Running() {
			return apierr.New(409, "Indexing is running; stop it or wait for it to finish, then move the index.")
		}
		from := ix.P.IndexDir
		job, err := ix.StartOtherJob("move", "Moving the index to "+target, func(logf func(string)) error {
			for _, name := range []string{"gufi", "extract", "embed-cache.db", "folders", "indexed-root", "semantic-map"} {
				src := filepath.Join(from, name)
				if !exists(src) {
					continue
				}
				dst := filepath.Join(target, name)
				os.RemoveAll(dst)
				if err := os.Rename(src, dst); err != nil {
					if !isCrossDevice(err) {
						return err
					}
					if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
						return err
					}
					os.RemoveAll(src)
				}
				logf(fmt.Sprintf("moved %s to %s", src, dst))
			}
			v := ""
			if raw != "" && realPath(target) != realPath(ix.P.IndexBase) {
				v = target
			}
			return app.SaveLauncherSetting("STUDIO_INDEX_DIR", v)
		})
		if err != nil {
			return err
		}
		go func() {
			until := time.Now().Add(6 * time.Hour)
			for time.Now().Before(until) {
				j, ok := ix.Job(job.ID)
				if !ok || (j.State != "queued" && j.State != "running") {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			if j, ok := ix.Job(job.ID); ok && j.State == "done" {
				ix.App.RequestRestart(0)
			}
		}()
		if app.CanRestart() {
			return writeOK(w, ordered{{"moved", true}, {"restarting", true}, {"job", job}}.Map())
		}
		return writeOK(w, ordered{{"moved", true}, {"restarting", false}, {"job", job}, {"note", "Restart the Studio to use the index there."}}.Map())
	}))

	ix.History.register(mux, wrap)
}

func writableDir(p string) bool {
	f, err := os.CreateTemp(p, ".studio-write-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
