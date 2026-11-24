package kb

import "golang.org/x/sys/windows"

// FreeBytes is what the caller may still use at p.
func FreeBytes(p string) (int64, bool) {
	var avail, total, free uint64
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil || windows.GetDiskFreeSpaceEx(ptr, &avail, &total, &free) != nil {
		return 0, false
	}
	return int64(avail), true
}
