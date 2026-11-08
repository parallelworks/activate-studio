package main

import (
	"io"
	"os"

	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// `studio-server gufi-vt-remote <token> "<gufi_query command line>"`: gufi_vt's
// remote command for a library on another system (gufiVtRemote.ts). It
// splits the line without a shell and posts it to the server over
// loopback, which runs it on that host through the user's session.
func init() {
	registerSubcommand(search.VtSubcommand, subcommand{
		Run: func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
			return search.VtRemoteMain(args, os.Getenv, stdout, stderr)
		},
		Summary: "gufi_vt's remote command: hand a location's gufi_query to the server",
	})
}
