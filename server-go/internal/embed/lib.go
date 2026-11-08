package embed

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// LibraryNames are the ONNX Runtime library's file names on this system,
// the versioned one first.
func LibraryNames() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"libonnxruntime.dylib"}
	case "windows":
		return []string{"onnxruntime.dll"}
	}
	return []string{"libonnxruntime.so.1", "libonnxruntime.so"}
}

var (
	libOnce sync.Once
	libPath string
	libErr  error
)

// FindLibrary is the ONNX Runtime library to load, looked for once:
//
//  1. STUDIO_ORT_LIB, a file;
//  2. beside the executable, and in lib/ beside it or one folder up (the
//     packaged apps put it in the payload's lib/);
//  3. in the onnxruntime Python package the Studio installed before the
//     Go port (indexer/.deps, the macOS app's Python, or the Linux
//     payload's), which holds the same library, so an installation that
//     updates keeps search by meaning without a download.
//
// The library is never looked for on the system's search path: a copy of
// another version there could serve another API.
func FindLibrary() (string, error) {
	libOnce.Do(func() { libPath, libErr = findLibrary() })
	return libPath, libErr
}

func findLibrary() (string, error) {
	if p := os.Getenv("STUDIO_ORT_LIB"); p != "" {
		if isFile(p) {
			return p, nil
		}
		return "", errors.New("STUDIO_ORT_LIB names no file: " + p)
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		d := filepath.Dir(exe)
		dirs = append(dirs, d, filepath.Join(d, "lib"), filepath.Join(d, "..", "lib"))
	}
	for _, d := range dirs {
		for _, n := range LibraryNames() {
			if p := filepath.Join(d, n); isFile(p) {
				return p, nil
			}
		}
	}
	for _, d := range pythonPackageDirs() {
		if p := wheelLibrary(filepath.Join(d, "onnxruntime", "capi")); p != "" {
			return p, nil
		}
	}
	return "", errors.New("onnxruntime is not installed: no " + LibraryNames()[0] + " beside the server, and STUDIO_ORT_LIB is not set")
}

// pythonPackageDirs are the site-packages the Studio's Python steps used.
func pythonPackageDirs() []string {
	var out []string
	if root := projectRoot(); root != "" {
		out = append(out, filepath.Join(root, "indexer", ".deps"))
	}
	for _, env := range []string{"STUDIO_PAYLOAD_ROOT", "STUDIO_APP_BUNDLE"} {
		if v := os.Getenv(env); v != "" {
			m, _ := filepath.Glob(filepath.Join(v, "*", "lib", "python3*", "site-packages"))
			out = append(out, m...)
			m, _ = filepath.Glob(filepath.Join(v, "Contents", "Resources", "*", "lib", "python3*", "site-packages"))
			out = append(out, m...)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		m, _ := filepath.Glob(filepath.Join(home, ".local", "lib", "activate-studio", "python", "lib", "python3*", "site-packages"))
		out = append(out, m...)
	}
	return out
}

// wheelLibrary is the library in a wheel's capi folder:
// libonnxruntime.so.1.31.0, libonnxruntime.1.31.0.dylib or onnxruntime.dll.
func wheelLibrary(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var found []string
	for _, e := range entries {
		n := e.Name()
		switch runtime.GOOS {
		case "darwin":
			if strings.HasPrefix(n, "libonnxruntime.") && strings.HasSuffix(n, ".dylib") {
				found = append(found, n)
			}
		case "windows":
			if strings.EqualFold(n, "onnxruntime.dll") {
				found = append(found, n)
			}
		default:
			if strings.HasPrefix(n, "libonnxruntime.so") {
				found = append(found, n)
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	return filepath.Join(dir, found[len(found)-1])
}

// projectRoot is STUDIO_PROJECT_ROOT, else the folder above the
// executable or the working directory that holds indexer/.
func projectRoot() string {
	if r := os.Getenv("STUDIO_PROJECT_ROOT"); r != "" {
		return r
	}
	var starts []string
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, s := range starts {
		for d := s; ; d = filepath.Dir(d) {
			if st, err := os.Stat(filepath.Join(d, "indexer")); err == nil && st.IsDir() {
				return d
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return ""
}
