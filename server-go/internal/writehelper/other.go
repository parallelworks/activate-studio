//go:build !linux

package writehelper

import (
	"fmt"
	"io"
)

// Main says the helper is built for Linux.
func Main(_ []string, _ io.Reader, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "studio-write runs on Linux only")
	return exitUsage
}
