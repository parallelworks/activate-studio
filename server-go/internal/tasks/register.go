package tasks

import (
	"context"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// envMs is Number(process.env[k]) || d, in milliseconds.
func envMs(p *platform.Platform, k string, d time.Duration) time.Duration {
	if n, err := strconv.ParseFloat(p.Getenv(k), 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Millisecond))
	}
	return d
}

// Register serves agent sites, tasks, the token board and the fleet, all
// over one pw code daemon access, and starts their background work: the
// bridge sweeper always; reattaching earlier tasks and the fleet's
// scheduler only where this process owns the platform's jobs. It returns
// the fleet, whose NotifyRunEnded a run watcher calls (ConnectRunEnded).
func Register(mux *http.ServeMux, a *app.App, p *platform.Platform) *Fleet {
	daemon := pwcode.New(p)
	daemon.Start(a)
	agentsites.Register(mux, a, p, daemon)

	e := New(Config{
		IndexBase: a.Cfg.IndexBase, KBRoot: a.Cfg.KBRoot, Port: a.Cfg.Port, P: p, Daemon: daemon,
		AgentExecution: func() string { return a.Settings.Effective().AgentExecution },
		Personas: func() []Persona {
			if n := p.Node(); n != nil {
				var out struct{ Personas []Persona }
				if err := n.Background(http.MethodGet, platform.BridgePrefix+"personas", nil, &out); err == nil {
					return out.Personas
				}
			}
			return ReadPersonas(a.Cfg.KBRoot, a.Cfg.IndexBase)
		},
		TaskTools: func() []string {
			if n := p.Node(); n != nil {
				var out struct{ Tools []string }
				if err := n.Background(http.MethodGet, platform.BridgePrefix+"task-tools", nil, &out); err == nil && out.Tools != nil {
					return out.Tools
				}
			}
			// Without Node there is no /api/mcp corpus server: the board only.
			return BoardTools
		},
		IndexDir: func(rel string) {
			if fn := a.Providers.IndexDir; fn != nil {
				fn(nil, rel)
			}
		},
		Go:           a.Go,
		SessionPoll:  envMs(p, "STUDIO_SESSION_POLL_MS", 2*time.Second),
		CampaignPoll: envMs(p, "STUDIO_CAMPAIGN_POLL_MS", 20*time.Second),
	})
	e.Routes(mux, a)

	fleet := NewFleet(e, func(m string) { a.Log.Info(m) })
	fleet.Routes(mux, a, p, func() bool { return a.Settings.Effective().DelegationEnabled })

	if p.OwnsJobs() {
		a.OnStart("tasks: reattach and fleet", func(context.Context) {
			e.Rehydrate()
			fleet.Start(a, envMs(p, "STUDIO_FLEET_TICK_MS", 30*time.Second))
		})
	}
	a.OnStop("tasks: runners", e.StopRunners)
	return fleet
}

// ConnectRunEnded has the fleet hear about runs that end on src.
func ConnectRunEnded(f *Fleet, src RunEndedSource) { src.OnRunEnded(f.NotifyRunEnded) }

// StopRunners ends the one-shot runs beside the server at stop, as Node's
// children end with its process; it returns how many.
func (e *Engine) StopRunners() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, id := range e.order {
		for _, proc := range e.tasks[id].procs {
			if proc.cmd != nil && proc.cmd.Process != nil {
				proc.cmd.Process.Signal(syscall.SIGTERM)
				n++
			}
		}
	}
	return n
}
