// Command semantic-map makes the semantic map of an index, with the same
// arguments, output files and summary line as indexer/semantic_map.py, so
// the server can run either (STUDIO_SEMANTIC_MAP_BIN; docs/GO-PORT.md). The
// server binary runs the same as `studio-server semantic-map`.
//
//	semantic-map --index <tree> --out <dir> [--sample 3000] [--fit 20000]
//	semantic-map --vectors <vectors.jsonl> --model <model> [--of M] --out <dir>
//
// Unlike the Python script it never exits 3 (numpy missing): it has no
// dependency to miss.
package main

import (
	"os"

	"github.com/parallelworks/activate-studio/server-go/internal/semanticmap"
)

func main() { os.Exit(semanticmap.Main(os.Args[1:], os.Stdout, os.Stderr)) }
