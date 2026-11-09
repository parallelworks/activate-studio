package userlibs

import (
	"context"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// HostStep is one step of a connection test, as the API shows it.
type HostStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail"`
}

// HostRun runs a command on the host a connection test reached: a session
// that fails reads as exit -1 with its message.
type HostRun func(argv []string, timeout time.Duration) remote.Result

// ConnectSSH is the first step of a connection test to a host over plain
// ssh, with the advice the library tests give (originals.ts's
// probeReadThrough uses it for the host originals are read through): the
// steps so far, the runner for the next ones, and whether the host was
// reached and runs a shell. onlyQuery is the step's detail when the
// login runs only gufi_query.
func (m *Module) ConnectSSH(ctx context.Context, destination string, port int, sshOptions []string) (steps []HostStep, run HostRun, ok bool, onlyQuery string) {
	site, err := remote.SSHSite(destination, port, sshOptions)
	if err != nil {
		return []HostStep{{Name: "ssh", Detail: err.Error()}}, nil, false, onlyGufiQuery
	}
	h := probeHost{Site: site, Name: destination, Reached: m.reachedOver(site, destination, port), Advice: func(msg string) string { return sshAdvice(msg, destination, port) }}
	r := m.runner(ctx, h)
	var ss []step
	c := m.connectStep(h, r, &ss)
	for _, s := range ss {
		steps = append(steps, HostStep{Name: s.Name, OK: s.OK, Warn: s.Warn, Detail: s.Detail})
	}
	return steps, HostRun(r), c != nil, onlyGufiQuery
}
