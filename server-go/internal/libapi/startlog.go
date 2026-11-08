package libapi

import (
	"context"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
)

// logLibraryStates is main.ts at start: every library probed, and its
// state line written to the server log and the index log. With Node
// behind the Go server Node's own start writes them.
func logLibraryStates(a *app.App, s *Service) {
	if !a.NodeFree() {
		return
	}
	a.OnStart("libraries: state lines", func(ctx context.Context) {
		log := index.Of(a).Log
		for _, l := range s.Reg.ProbeAll(ctx, s.kbLabel()) {
			line := s.Reg.StateLine(l)
			a.Log.Info(line)
			log.Write(line)
		}
	})
}
