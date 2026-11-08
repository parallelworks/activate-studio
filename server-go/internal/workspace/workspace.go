// Package workspace is workspace.ts. The platform runs a user's workflows
// inside a per-user workspace that scales down when idle. A submission
// made while it is down fails with "User workspace not found or is not
// running" (and, for a little while after a start request, "Could not
// execute workflow"), and the platform does not start it on demand for a
// CLI launch. Every launch path goes through With: on that failure, ask
// for a start, then retry the launch until it is accepted or the budget
// runs out. The launch itself is the ground truth for readiness.
package workspace

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"time"
)

// Down matches the workspace-down condition in an error's text.
var Down = regexp.MustCompile(`(?i)user workspace not found or is not running|could not execute workflow`)

// IsDown is isWorkspaceDown.
func IsDown(message string) bool { return Down.MatchString(message) }

// Runner is workspace.ts's WorkspaceRunner.
type Runner struct {
	// CLI runs the pw CLI with these arguments; an error carries its text.
	CLI func(ctx context.Context, args []string, timeout time.Duration) (string, error)
	// Sleep waits; nil is a timer that ends with ctx.
	Sleep func(ctx context.Context, d time.Duration) error
	// Budget is the total time to keep retrying after a start request
	// (default 240 s); RetryEvery the wait between launches (default 15 s).
	Budget     time.Duration
	RetryEvery time.Duration
	Log        func(string)
}

// Result is what With returns besides the launch's value.
type Result struct {
	Started bool
	Waited  time.Duration
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// With runs launch, and if it fails because the workspace is down, starts
// the workspace and keeps retrying the launch until it succeeds or the
// budget is spent. Any other error comes back as it is.
func With[T any](ctx context.Context, r Runner, launch func(ctx context.Context) (T, error)) (T, Result, error) {
	sleep := r.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	budget, every := r.Budget, r.RetryEvery
	if budget == 0 {
		budget = 240 * time.Second
	}
	if every == 0 {
		every = 15 * time.Second
	}
	log := r.Log
	if log == nil {
		log = func(string) {}
	}
	v, err := launch(ctx)
	if err == nil {
		return v, Result{}, nil
	}
	if !IsDown(err.Error()) {
		return v, Result{}, err
	}
	log("user workspace is not running; requesting a start and retrying the launch")
	if r.CLI != nil {
		if _, err := r.CLI(ctx, []string{"workspace", "start"}, 120*time.Second); err != nil {
			log("workspace start: " + clip(err.Error(), 200))
		}
	}
	t0 := time.Now()
	var last error
	for time.Since(t0) < budget {
		if err := sleep(ctx, every); err != nil {
			return v, Result{}, err
		}
		v, err = launch(ctx)
		if err == nil {
			return v, Result{Started: true, Waited: time.Since(t0)}, nil
		}
		last = err
		if !IsDown(err.Error()) {
			return v, Result{}, err
		}
	}
	lastText := "null"
	if last != nil {
		lastText = last.Error()
	}
	return v, Result{}, fmt.Errorf("The platform user workspace is still not running after a start request and %d seconds of retries; the last error was: %s. Start it from the platform (pw workspace start) and try again.", int(math.Round(budget.Seconds())), clip(lastText, 200))
}
