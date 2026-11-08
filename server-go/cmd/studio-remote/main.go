// Command studio-remote is what the Studio sends to another system for a
// library's build there (internal/userlibs), so the build needs no Python:
//
//	studio-remote version             its platform, to show it runs there
//	studio-remote text [enrich.py's arguments]
//	studio-remote embed [embed.py's arguments]   (ONNX Runtime from STUDIO_ORT_LIB)
//	studio-remote verify DIR NAME=SHA256 ...     files the Studio sent, by digest
//
// It is built without cgo for each platform the Studio builds on, and holds
// only the text pass and the embedder.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	embedcli "github.com/parallelworks/activate-studio/server-go/internal/embed/cli"
	"github.com/parallelworks/activate-studio/server-go/internal/remotepass"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: studio-remote version | text ... | embed ... | verify DIR NAME=SHA256 ...")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "studio-remote %s-%s\n", runtime.GOOS, runtime.GOARCH)
		return 0
	case "text":
		return remotepass.TextMain(args[1:], stdout, stderr)
	case "embed":
		return embedcli.Main(args[1:], stdout, stderr)
	case "verify":
		return verify(args[1:], stderr)
	}
	fmt.Fprintf(stderr, "studio-remote: no command %q\n", args[0])
	return 2
}

// verify checks each NAME in DIR against its SHA-256.
func verify(args []string, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: studio-remote verify DIR NAME=SHA256 ...")
		return 2
	}
	dir := args[0]
	for _, a := range args[1:] {
		name, want, ok := strings.Cut(a, "=")
		if !ok || strings.ContainsRune(name, '/') {
			fmt.Fprintf(stderr, "verify: %q is not NAME=SHA256\n", a)
			return 2
		}
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			fmt.Fprintf(stderr, "%s is missing: %v\n", name, err)
			return 1
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			fmt.Fprintf(stderr, "%s could not be read: %v\n", name, err)
			return 1
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			fmt.Fprintf(stderr, "%s has SHA-256 %s, not %s\n", name, got, want)
			return 1
		}
	}
	return 0
}
