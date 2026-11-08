package main

import (
	"encoding/json"
	"io"
	"log/slog"

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
			s := server.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
			json.NewEncoder(stdout).Encode(map[string]any{"ported": s.Ported(), "unported": s.Unported()})
			return 0
		},
	})
}
