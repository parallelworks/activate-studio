package main

import (
	"io"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/cli"
)

// `studio-server extract ...`: the document extraction pass
// (internal/extract/cli), the stand-in for preextract.js and enrich.py's
// extraction step. The index job runs it as a child process, so a reader
// failing badly on one file cannot take the server down.
func init() {
	registerSubcommand("extract", subcommand{
		Run: func(args []string, _ io.Reader, stdout, stderr io.Writer) int {
			return cli.Main(args, stdout, stderr)
		},
		Summary: "fill the extract cache from the knowledge base's documents",
	})
}
