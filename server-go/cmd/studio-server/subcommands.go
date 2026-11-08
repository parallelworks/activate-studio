package main

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// A subcommand runs the binary as a helper process in place of the server:
// `studio-server <name> args...`. Work that loads native code or can crash
// (extraction, the ONNX embedder) runs this way as a child of the server, so a
// failure there cannot take the server down. Each subcommand registers itself
// from its own file (sub_<name>.go) in an init function, so adding one never
// touches main.go.
type subcommand struct {
	// Run gets the arguments after the name and returns the exit code.
	Run     func(args []string, stdin io.Reader, stdout, stderr io.Writer) int
	Summary string
}

var subcommands = map[string]subcommand{}

func registerSubcommand(name string, c subcommand) {
	if _, dup := subcommands[name]; dup {
		panic("studio-server: subcommand registered twice: " + name)
	}
	subcommands[name] = c
}

// dispatchSubcommand runs os.Args[1] as a subcommand when one is registered
// under that name, and reports whether it did.
func dispatchSubcommand() (int, bool) {
	if len(os.Args) < 2 {
		return 0, false
	}
	if os.Args[1] == "help-subcommands" {
		names := make([]string, 0, len(subcommands))
		for n := range subcommands {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(os.Stdout, "%s\t%s\n", n, subcommands[n].Summary)
		}
		return 0, true
	}
	c, ok := subcommands[os.Args[1]]
	if !ok {
		return 0, false
	}
	return c.Run(os.Args[2:], os.Stdin, os.Stdout, os.Stderr), true
}
