//go:build windows

package platform

// executable is fs.accessSync(f, X_OK), which on Windows checks only that
// the file exists, as there is no execute bit.
func executable(string) bool { return true }
