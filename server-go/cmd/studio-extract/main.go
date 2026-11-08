// Command studio-extract fills the Studio's extract cache in one process:
// the stand-in for preextract.js plus enrich.py's extraction step. See
// internal/extract/cli for its arguments. The Go server will run the same
// pass as `studio-server extract` once indexing moves to Go.
package main

import (
	"os"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
