// Command semantic-map makes the semantic map of an index, with the same
// arguments, output files and summary line as indexer/semantic_map.py, so
// the server can run either (STUDIO_SEMANTIC_MAP_BIN; docs/GO-PORT.md).
//
//	semantic-map --index <tree> --out <dir> [--sample 3000] [--fit 20000]
//	semantic-map --vectors <vectors.jsonl> --model <model> [--of M] --out <dir>
//
// Unlike the Python script it never exits 3 (numpy missing): it has no
// dependency to miss.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/parallelworks/activate-studio/server-go/internal/semanticmap"
)

func main() {
	fs := flag.NewFlagSet("semantic-map", flag.ContinueOnError)
	index := fs.String("index", "", "the GUFI index tree")
	vectors := fs.String("vectors", "", "file means fetched from an index on another system, one JSON line each")
	model := fs.String("model", "minilm384", "with --vectors: the model they come from")
	of := fs.Int("of", 0, "with --vectors: the files with vectors there, of which these are a sample")
	out := fs.String("out", "", "the map folder to write (replaced whole)")
	sample := fs.Int("sample", 3000, "files in the overview")
	fit := fs.Int("fit", 20000, "files the projection is fitted on")
	seed := fs.Uint64("seed", 0, "seed for the random choices")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if (*index == "") == (*vectors == "") || *out == "" {
		fmt.Fprintln(os.Stderr, "semantic-map: give one of --index or --vectors, and --out")
		os.Exit(2)
	}
	opt := semanticmap.Options{Sample: max(100, *sample), Fit: max(1000, *fit), Seed: *seed}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "of" {
			opt.Of = of
		}
	})
	var src semanticmap.Source
	if *vectors != "" {
		src = semanticmap.VectorsSource(*vectors, *model)
	} else {
		src = semanticmap.IndexSource(*index)
	}
	sum, err := semanticmap.Build(src, *out, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "semantic-map:", err)
		os.Exit(1)
	}
	b, _ := json.Marshal(sum)
	fmt.Println(string(b))
}
