package main

import (
	"io"

	"github.com/parallelworks/activate-studio/server-go/internal/semanticmap"
)

func init() {
	registerSubcommand("semantic-map", subcommand{
		Run:     func(a []string, _ io.Reader, out, errw io.Writer) int { return semanticmap.Main(a, out, errw) },
		Summary: "a semantic map of an index or of fetched vectors (indexer/semantic_map.py)",
	})
}
