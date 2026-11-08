package kb

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

// fileInfo is GetFileInformationByHandle for a path (a folder too).
func fileInfo(abs string) (windows.ByHandleFileInformation, bool) {
	var fi windows.ByHandleFileInformation
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return fi, false
	}
	h, err := windows.CreateFile(p, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return fi, false
	}
	defer windows.CloseHandle(h)
	if windows.GetFileInformationByHandle(h, &fi) != nil {
		return fi, false
	}
	return fi, true
}

func fileIndex(fi windows.ByHandleFileInformation) uint64 {
	return uint64(fi.FileIndexHigh)<<32 | uint64(fi.FileIndexLow)
}

// fileStamp changes when a file is replaced or written: size,
// modification time and file index, "none" when it is not there.
func fileStamp(f string) string {
	fi, ok := fileInfo(f)
	if !ok {
		return "none"
	}
	size := int64(fi.FileSizeHigh)<<32 | int64(fi.FileSizeLow)
	return fmt.Sprintf("%d:%d:%d", size, fi.LastWriteTime.Nanoseconds(), fileIndex(fi))
}

// identity is a path's volume serial and file index, creation time in
// nanoseconds and kind: what libuv reports as dev, ino and birthtime on
// Windows, so the overlay's keys match Node's there.
func identity(abs string) (key, btime string, dir bool, ok bool) {
	fi, ok := fileInfo(abs)
	if !ok {
		return "", "", false, false
	}
	key = strconv.FormatUint(uint64(fi.VolumeSerialNumber), 10) + "." + strconv.FormatUint(fileIndex(fi), 10)
	return key, strconv.FormatInt(fi.CreationTime.Nanoseconds(), 10), fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0, true
}

// umask: Windows has none, so a new file keeps the mode it is given.
func umask() os.FileMode { return 0 }
