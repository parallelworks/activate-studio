//go:build darwin || linux || freebsd || netbsd

package ort

import "github.com/ebitengine/purego"

// openLibrary loads the library and returns OrtGetApiBase.
func openLibrary(path string) (uintptr, error) {
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return 0, err
	}
	return purego.Dlsym(h, "OrtGetApiBase")
}

// pathString is a model path as ORTCHAR_T: UTF-8, NUL-terminated.
func pathString(s string) []byte { return cString(s) }
