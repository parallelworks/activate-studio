package main

import (
	"io"

	embedcli "github.com/parallelworks/activate-studio/server-go/internal/embed/cli"
)

func init() {
	registerSubcommand("embed", subcommand{
		Run:     func(a []string, _ io.Reader, out, errw io.Writer) int { return embedcli.Main(a, out, errw) },
		Summary: "the index's meaning phase (indexer/embed.py): passages, embeddings, vec0 tables",
	})
	registerSubcommand("embed-query", subcommand{
		Run:     embedcli.QueryMain,
		Summary: "query vectors over JSON lines (indexer/embed_query.py)",
	})
}
