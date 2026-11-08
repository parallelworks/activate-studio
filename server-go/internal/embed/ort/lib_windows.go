//go:build windows

package ort

import (
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// openLibrary loads the DLL and returns OrtGetApiBase. A DLL beside it
// (onnxruntime_providers_shared.dll) is found from the DLL's own folder.
func openLibrary(path string) (uintptr, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	// LOAD_WITH_ALTERED_SEARCH_PATH: dependencies from the DLL's folder.
	h, _, callErr := syscall.SyscallN(procLoadLibraryExW.Addr(), uintptr(unsafe.Pointer(p)), 0, 0x00000008)
	if h == 0 {
		return 0, callErr
	}
	return syscall.GetProcAddress(syscall.Handle(h), "OrtGetApiBase")
}

var procLoadLibraryExW = syscall.NewLazyDLL("kernel32.dll").NewProc("LoadLibraryExW")

// pathString is a model path as ORTCHAR_T, which is wchar_t on Windows:
// UTF-16, NUL-terminated, as bytes.
func pathString(s string) []byte {
	u := append(utf16.Encode([]rune(s)), 0)
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return b
}
