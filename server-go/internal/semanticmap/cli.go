package semanticmap

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
)

// Main is the semantic-map command (cmd/semantic-map, and
// `studio-server semantic-map`): the arguments, output files and summary
// line of indexer/semantic_map.py. It never exits 3 (numpy missing).
//
//	semantic-map --index <tree> --out <dir> [--sample 3000] [--fit 20000]
//	semantic-map --vectors <vectors.jsonl> --model <model> [--of M] --out <dir>
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("semantic-map", flag.ContinueOnError)
	fs.SetOutput(stderr)
	index := fs.String("index", "", "the GUFI index tree")
	vectors := fs.String("vectors", "", "file means fetched from an index on another system, one JSON line each")
	model := fs.String("model", "minilm384", "with --vectors: the model they come from")
	of := fs.Int("of", 0, "with --vectors: the files with vectors there, of which these are a sample")
	out := fs.String("out", "", "the map folder to write (replaced whole)")
	sample := fs.Int("sample", 3000, "files in the overview")
	fit := fs.Int("fit", 20000, "files the projection is fitted on")
	seed := fs.Uint64("seed", 0, "seed for the random choices")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if (*index == "") == (*vectors == "") || *out == "" {
		fmt.Fprintln(stderr, "semantic-map: give one of --index or --vectors, and --out")
		return 2
	}
	opt := Options{Sample: max(MinSample, *sample), Fit: max(MinFit, *fit), Seed: *seed}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "of" {
			opt.Of = of
		}
	})
	var src Source
	if *vectors != "" {
		src = VectorsSource(*vectors, *model)
	} else {
		src = IndexSource(*index)
	}
	sum, err := Build(src, *out, opt)
	if err != nil {
		fmt.Fprintln(stderr, "semantic-map:", err)
		return 1
	}
	b, _ := json.Marshal(sum)
	fmt.Fprintln(stdout, string(b))
	return 0
}
