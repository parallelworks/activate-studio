// Command index-extract reads the text of files for an index, with the
// same readers the Studio's own indexing uses, and writes it in GUFI's
// common external database schema keyed by inode and mtime. It is its own
// small program, apart from the server, so it can be copied to a node next
// to the storage, run as a job of a scheduler array, or used on its own.
//
//	index-extract files --list FILE|- --out ARTIFACT.db [--root DIR] [--shard K/N] ...
//	index-extract spread --spread DIR --fsid NAME ARTIFACT.db...
//	index-extract shards --spread DIR --fsid NAME --index DIR
//	index-extract schema
//
// See internal/extractfiles and docs/EXTRACTION.md.
package main

import (
	"os"

	"github.com/parallelworks/activate-studio/server-go/internal/extractfiles"
)

func main() { os.Exit(extractfiles.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
