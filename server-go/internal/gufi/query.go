package gufi

import (
	"context"
	"errors"
	"time"
)

// Query runs plan on the planner's (local) index through runner: query.ts's
// runPlan and rawRun. A run that failed and printed nothing is an error;
// rows printed before a failure in some directory still count.
func (p *Planner) Query(ctx context.Context, runner *Runner, plan Plan) (QueryResult, error) {
	scope, err := ScopePath(p.IndexRoot, plan.Subtree)
	if err != nil {
		return QueryResult{}, err
	}
	t0 := time.Now()
	res, err := runner.Run(ctx, Command{Tool: ToolQuery, Args: append(plan.Args(), scope)})
	if errors.Is(err, ErrBusy) || (err != nil && len(res.Stdout) == 0) {
		return QueryResult{}, err
	}
	if err == nil && res.ExitCode != 0 && len(res.Stdout) == 0 {
		msg := res.Stderr
		if u := utf16Units(msg); len(u) > 500 {
			msg = utf16String(u[:500])
		}
		if msg == "" {
			msg = "gufi_query exited " + itoa(res.ExitCode)
		}
		return QueryResult{}, errors.New(msg)
	}
	stdout := string(res.Stdout)
	if plan.Raw {
		if err := RawError(stdout, res.Stderr); err != nil {
			return QueryResult{}, err
		}
	}
	out := p.Rows(plan, stdout)
	out.ElapsedMS = time.Since(t0).Milliseconds()
	return out, nil
}
