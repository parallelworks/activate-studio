//go:build windows

package transfers

import "golang.org/x/sys/windows"

// freeBytes is what the caller may still use at p.
func freeBytes(p string) (int64, bool) {
	var avail, total, free uint64
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil || windows.GetDiskFreeSpaceEx(ptr, &avail, &total, &free) != nil {
		return 0, false
	}
	return int64(avail), true
}
