// Command studio-write is what the Studio sends to another system to write
// in a library there (internal/writehelper).
package main

import (
	"os"

	"github.com/parallelworks/activate-studio/server-go/internal/writehelper"
)

func main() { os.Exit(writehelper.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
