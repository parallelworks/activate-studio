package gufi

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// LibDir is gufiLibDir: GUFI's lib directory beside its bin directory.
func LibDir(bin string) string { return filepath.Join(filepath.Dir(bin), "lib") }

// LibraryPathVar is the variable the platform's dynamic loader searches
// first.
func LibraryPathVar(goos string) string {
	if goos == "darwin" {
		return "DYLD_LIBRARY_PATH"
	}
	return "LD_LIBRARY_PATH"
}

// EnvOptions are gufiEnvFor's options.
type EnvOptions struct {
	// NoLibraries is libraries: false, for the indexer scripts: GUFI_BIN
	// alone, no PATH or loader path.
	NoLibraries bool
	// GOOS is the platform; "" is this one.
	GOOS string
}

// EnvFor is config.ts's gufiEnvFor: base and extra, GUFI_BIN, bin first
// on PATH, and GUFI's lib directory first on the loader's path unless it
// is a system directory or absent. base is os.Environ's form; the result
// is too, sorted.
func EnvFor(bin string, base []string, extra map[string]string, o EnvOptions) []string {
	env := map[string]string{}
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	orig := func(k string) string { return env[k] }
	join := func(parts ...string) string {
		var keep []string
		for _, p := range parts {
			if p != "" {
				keep = append(keep, p)
			}
		}
		return strings.Join(keep, string(os.PathListSeparator))
	}
	basePath := orig("PATH")
	goos := o.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	v := LibraryPathVar(goos)
	baseLib := orig(v)
	for k, x := range extra {
		env[k] = x
	}
	env["GUFI_BIN"] = bin
	if !o.NoLibraries {
		env["PATH"] = join(bin, basePath)
		lib := LibDir(bin)
		if lib != "/lib" && lib != "/usr/lib" {
			if _, err := os.Stat(lib); err == nil {
				env[v] = join(lib, baseLib)
			}
		}
	}
	out := make([]string, 0, len(env))
	for k, x := range env {
		out = append(out, k+"="+x)
	}
	sort.Strings(out)
	return out
}
