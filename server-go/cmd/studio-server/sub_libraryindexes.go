package main

// `studio-server library-index-dirs LIBRARIES_JSON` prints the index folder
// of each library built here (build.indexDir, an absolute path), one per
// line: what the macOS launcher's Uninstall plan reads from libraries.json
// (deploy/mac/launcher.sh, plan), which the app's Python read before the Go
// version carried none. A file that cannot be read or parsed prints nothing.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func init() {
	registerSubcommand("library-index-dirs", subcommand{Summary: "the index folders of the libraries built here, from libraries.json (the macOS Uninstall plan)", Run: runLibraryIndexDirs})
}

func runLibraryIndexDirs(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: studio-server library-index-dirs LIBRARIES_JSON")
		return 2
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		return 0
	}
	var defs []any
	if json.Unmarshal(b, &defs) != nil {
		return 0
	}
	for _, d := range defs {
		m, _ := d.(map[string]any)
		build, _ := m["build"].(map[string]any)
		if dir, ok := build["indexDir"].(string); ok && strings.HasPrefix(dir, "/") {
			fmt.Fprintln(stdout, dir)
		}
	}
	return 0
}

// `studio-server json-string FILE KEY` prints the string at KEY in the JSON
// object in FILE, or nothing: the launcher's read of a built library's
// .studio-index.json ("index"), in place of the app's Python.
func init() {
	registerSubcommand("json-string", subcommand{Summary: "the string at a key of a JSON object in a file (the macOS launcher)", Run: runJSONString})
}

func runJSONString(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: studio-server json-string FILE KEY")
		return 2
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		return 0
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return 0
	}
	if v, ok := m[args[1]].(string); ok {
		fmt.Fprintln(stdout, v)
	}
	return 0
}
