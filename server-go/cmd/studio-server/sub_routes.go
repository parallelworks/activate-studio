package main

import (
	"encoding/json"
	"io"
	"log/slog"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/server"
)

// routes: which of the Node server's routes Go serves, as JSON
// ({ported, unported}), for the test harness's strict mode and the
// coordinator's count. Nothing is started.
func init() {
	registerSubcommand("routes", subcommand{
		Summary: "print the Node routes Go serves and the ones it does not, as JSON",
		Run: func(_ []string, _ io.Reader, stdout, stderr io.Writer) int {
			cfg, err := config.Load(nil)
			if err != nil {
				io.WriteString(stderr, err.Error()+"\n")
				return 2
			}
			log := slog.New(slog.DiscardHandler)
			// The front the server runs with: where it verifies identity
			// itself (with Node off, for one), it answers /api/whoami and
			// /api/auth/*. Nothing it would start (the hand-off's key set)
			// is started for a listing.
			var front *auth.Front
			if cfg.FrontAuth {
				front = auth.NewFront(cfg, log, nil, nil)
			}
			s := server.New(cfg, log, nil, front)
			json.NewEncoder(stdout).Encode(map[string]any{"ported": s.Ported(), "unported": s.Unported()})
			return 0
		},
	})
}
