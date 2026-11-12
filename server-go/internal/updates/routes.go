package updates

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/bridge"
	"github.com/parallelworks/activate-studio/server-go/internal/version"
)

// Register makes the updater of a server, registers its routes on mux,
// and runs its background check through a.Go, as main.ts's startUpdates
// and updateRoutes do. It returns the updater, for tests.
func Register(mux *http.ServeMux, a *app.App) *Updater {
	root := a.Cfg.ProjectRoot
	u := New(Options{
		ProjectRoot: root,
		IndexBase:   a.Cfg.IndexBase,
		Settings:    a.Settings,
		AuthEnabled: a.Cfg.AuthEnabled(),
		Log:         func(m string) { a.Log.Info(m) },
		Restart:     a.RequestRestart,
		Busy:        a.Busy,
		Version: func() (string, *string) {
			i := version.Of(root)
			return i.Version, i.BuiltAt
		},
	})
	// While Node still answers chats and indexes, an automatic update also
	// waits on Node's own busy check (main.ts), which it reports with the
	// index status when it runs behind Go (STUDIO_BEHIND_GO); streams
	// through the proxy count in a.Busy. Node is attached after Register,
	// so its handler is looked up at each check; without Node it answers
	// 501 and nothing is busy there.
	a.AddBusy(func() bool { return nodeBusy(a.Node()) })
	// The Node this server spawns leaves the update check to Go.
	nodeEnvOnce.Do(func() {
		bridge.RegisterNodeEnv("updates", func(string) map[string]string {
			return map[string]string{"STUDIO_BEHIND_GO": "1"}
		})
	})
	a.Go("updates", func(ctx context.Context) {
		u.Start()
		<-ctx.Done()
		u.Stop()
	})
	Routes(u, mux, a.Log)
	return u
}

var nodeEnvOnce sync.Once

// nodeBusy asks Node, through h, whether it is busy.
func nodeBusy(h http.Handler) bool {
	req := httptest.NewRequest(http.MethodGet, "/api/index/status", nil)
	req.RemoteAddr = "127.0.0.1:0"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return false
	}
	var st struct {
		Running bool `json:"running"`
		Busy    bool `json:"busy"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		return false
	}
	return st.Running || st.Busy
}

// Routes registers the six routes on mux.
func Routes(u *Updater, mux *http.ServeMux, log *slog.Logger) {
	h := func(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
		return apierr.Handler(log, fn)
	}
	// A hosted Studio is updated by its deployment; one whose deployment
	// lets it update itself (deployment.go) is updated by the people the
	// deployment names, and to anyone else it is as any hosted Studio.
	refuseHosted := func(r *http.Request) error {
		if u.Mode() == "hosted" || !u.MayUpdate(auth.UserOf(r)) {
			return apierr.New(403, "This Studio is updated by its deployment, not from here.")
		}
		return nil
	}
	requireApp := func(r *http.Request) error {
		if err := refuseHosted(r); err != nil {
			return err
		}
		if mode := u.Mode(); mode != "app" {
			u.mu.Lock()
			r := u.refusal(mode, nil, false)
			u.mu.Unlock()
			msg := "Only an installed app updates itself."
			if r != nil {
				msg = *r
			}
			return apierr.New(409, "%s", msg)
		}
		return nil
	}

	mux.HandleFunc("GET /api/updates", h(func(w http.ResponseWriter, r *http.Request) error {
		if err := refuseHosted(r); err != nil {
			return err
		}
		// The window opening after the Mac slept past a check starts it
		// here, without waiting for the next poll.
		u.mu.Lock()
		if u.scheduled && u.checkDue(time.Now()) {
			u.startCheck()
		}
		u.mu.Unlock()
		apierr.JSON(w, 200, u.Status())
		return nil
	}))

	// Every mode, hosted included: What's new reads it.
	mux.HandleFunc("GET /api/changelog", h(func(w http.ResponseWriter, r *http.Request) error {
		apierr.JSON(w, 200, u.Changelog(r.URL.Query().Get("after")))
		return nil
	}))

	mux.HandleFunc("POST /api/updates/check", h(func(w http.ResponseWriter, r *http.Request) error {
		if err := refuseHosted(r); err != nil {
			return err
		}
		u.mu.Lock()
		busy := u.inProgress()
		u.mu.Unlock()
		if !busy {
			u.Check()
		}
		apierr.JSON(w, 200, u.Status())
		return nil
	}))

	mux.HandleFunc("POST /api/updates/install", h(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireApp(r); err != nil {
			return err
		}
		body := readBody(r)
		if v, ok := body["version"]; ok && v != nil {
			// A version the person picked: newer or older, on the channel.
			vs, isStr := v.(string)
			if _, valid := ParseVersion(vs); !isStr || !valid {
				return apierr.New(400, "version must be a release version")
			}
			u.mu.Lock()
			defer u.mu.Unlock()
			if u.op != nil || u.inProgress() {
				return apierr.New(409, "An update is %s; wait for it to finish.", u.st.State)
			}
			s := u.status()
			if s.Reason != nil {
				return apierr.New(409, "%s", *s.Reason)
			}
			rel := u.pickable(vs, s.Policy.Channel)
			if rel == nil {
				return apierr.New(409, "%s is not a release this Studio can install on the %s channel.", vs, channelName(s.Policy.Channel))
			}
			if sameVersion(rel.Tag, s.Current.Version) {
				return apierr.New(409, "%s is the version running.", rel.Tag)
			}
			restore, _ := body["restoreState"].(bool)
			u.startInstall(*rel, installOpts{allowOlder: true, restoreState: restore})
			apierr.JSON(w, 200, u.status())
			return nil
		}
		u.mu.Lock()
		if c := u.op; u.st.State == "checking" && c != nil {
			u.mu.Unlock()
			<-c
			u.mu.Lock()
		}
		if !u.inProgress() && u.loadSaved().CheckedAt == nil {
			u.mu.Unlock()
			u.Check()
			u.mu.Lock()
		}
		defer u.mu.Unlock()
		// An automatic download under way installs as soon as it is verified.
		if u.op != nil || u.inProgress() {
			if u.st.State == "downloading" || u.st.State == "verifying" {
				u.wanted = true
			}
			apierr.JSON(w, 200, u.status())
			return nil
		}
		s := u.status()
		rel := u.latestRelease(s.Policy.Channel, u.needsBuild())
		if rel == nil || !s.Available {
			return apierr.New(409, "There is no version newer than %s.", s.Current.Version)
		}
		if !s.CanInstall {
			msg := "The update cannot be installed."
			if s.Reason != nil {
				msg = *s.Reason
			}
			return apierr.New(409, "%s", msg)
		}
		u.startInstall(*rel, installOpts{})
		apierr.JSON(w, 200, u.status())
		return nil
	}))

	// What installing a version would do, from its signed manifest: older
	// or not, and whether it reads the state or needs a snapshot put back.
	mux.HandleFunc("POST /api/updates/inspect", h(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireApp(r); err != nil {
			return err
		}
		vs, _ := readBody(r)["version"].(string)
		if _, ok := ParseVersion(vs); !ok {
			return apierr.New(400, "version must be a release version")
		}
		u.mu.Lock()
		s := u.status()
		rel := u.pickable(vs, s.Policy.Channel)
		u.mu.Unlock()
		if rel == nil {
			return apierr.New(409, "%s is not a release this Studio can install on the %s channel.", vs, channelName(s.Policy.Channel))
		}
		man, err := u.verifiedManifest(*rel, true)
		if err != nil {
			if isRefused(err) {
				return apierr.New(409, "%s", err.Error())
			}
			return apierr.New(502, "The release could not be read: %s.", plain(err))
		}
		readable, snap := u.stateNeeds(man.StateFormat)
		out := VersionCheck{Version: man.Version, Older: CompareVersions(man.Version, s.Current.Version) < 0, Channel: man.Channel,
			StateFormat: man.StateFormat, CurrentFormat: u.state.StateFormat(), Readable: readable, RequiresRelaunch: man.RequiresRelaunch}
		if snap != nil {
			b := brief(*snap)
			out.Snapshot = &b
		}
		apierr.JSON(w, 200, out)
		return nil
	}))

	// Roll back: the version the last update replaced, when it is a Go
	// release, downloaded and installed as a picked version is (retire.go),
	// the state snapshot put back when it cannot read the state as it is.
	mux.HandleFunc("POST /api/updates/rollback", h(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireApp(r); err != nil {
			return err
		}
		u.mu.Lock()
		if c := u.op; c != nil && u.st.State == "checking" {
			u.mu.Unlock()
			<-c
			u.mu.Lock()
		}
		defer u.mu.Unlock()
		if u.op != nil || u.inProgress() {
			return apierr.New(409, "An update is %s; wait for it to finish.", u.st.State)
		}
		prev := u.previousVersion()
		if prev == nil {
			if v := u.previousTag(); v != "" {
				return apierr.New(409, "%s runs the Node server, which this version does not go back to.", v)
			}
			return apierr.New(409, "There is no previous version to go back to.")
		}
		rel := u.rollbackRelease(prev.Version)
		if rel == nil && u.loadSaved().CheckedAt == nil {
			u.mu.Unlock()
			u.Check()
			u.mu.Lock()
			rel = u.rollbackRelease(prev.Version)
		}
		if rel == nil {
			if u.st.Offline {
				return apierr.New(409, "Rolling back downloads %s from the release server again, and this computer could not reach it. This version is still installed; try again once it is online.", prev.Version)
			}
			return apierr.New(409, "Rolling back downloads %s again, and the release server has no signed build of it on the %s channel.", prev.Version, channelName(u.policy().Channel))
		}
		u.startInstall(*rel, installOpts{allowOlder: true, restoreState: true, rollback: true})
		apierr.JSON(w, 200, u.status())
		return nil
	}))
}

// VersionCheck is what installing a given version would do.
type VersionCheck struct {
	Version          string `json:"version"`
	Older            bool   `json:"older"`
	Channel          string `json:"channel"`
	StateFormat      int    `json:"stateFormat"`
	CurrentFormat    int    `json:"currentFormat"`
	Readable         bool   `json:"readable"`
	Snapshot         *Brief `json:"snapshot"`
	RequiresRelaunch bool   `json:"requiresRelaunch"`
}

func channelName(c string) string {
	if c == "preview" {
		return "Preview"
	}
	return "Stable"
}

func readBody(r *http.Request) map[string]any {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}
