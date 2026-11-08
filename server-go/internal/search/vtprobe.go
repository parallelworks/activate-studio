package search

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

var vtOnce sync.Once
var vtAvail bool

// GufiVtAvailable is gufiVt.ts gufiVtAvailable: this host's GUFI has
// gufi_vt with remote locations. Probed once.
func (e *Engine) GufiVtAvailable(ctx context.Context) bool {
	vtOnce.Do(func() {
		nothing := ""
		for _, p := range []string{"/usr/bin/true", "/bin/true"} {
			if _, err := os.Stat(p); err == nil {
				nothing = p
				break
			}
		}
		if nothing == "" || e.Local == nil {
			return
		}
		probe := `CREATE VIRTUAL TABLE temp.p USING gufi_vt(index='/', E="SELECT name FROM vrpentries;", remote_cmd='` + nothing + `'); SELECT count(*) FROM temp.p;`
		res, err := e.Local.Run(context.WithoutCancel(ctx), gufi.Command{Tool: gufi.ToolSQLite3, Args: []string{":memory:", probe}, Timeout: 15 * time.Second})
		vtAvail = err == nil && res.ExitCode == 0 && strings.TrimSpace(string(res.Stdout)) == "0"
	})
	return vtAvail
}

// mergeReason is why rows across libraries were merged here.
func (e *Engine) mergeReason(ctx context.Context, _ gufi.Plan) string {
	if !e.GufiVtAvailable(ctx) {
		return "this computer's GUFI has no gufi_vt with remote locations"
	}
	// TODO(stage c): gufi_vt across libraries (gufiVt.ts runAcross).
	return "gufi_vt across libraries is not in the Go server yet"
}
