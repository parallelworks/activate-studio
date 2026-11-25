//go:build windows

package pwbucket

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockDir locks a folder that is still there, by a file in it that no other
// may open. Closing it unlocks the folder.
func lockDir(dir string) (*os.File, error) {
	name := filepath.Join(dir, ".lock")
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

// removeLocked removes a folder that lockDir locked. What is in it goes while
// it is locked; the open lock file itself may keep the folder until it closes.
func removeLocked(dir string, l *os.File) error {
	os.RemoveAll(dir)
	l.Close()
	return os.RemoveAll(dir)
}

// ownDir is whether p is a folder, not a link to one. The folder for
// temporary files it is in is the account's own.
func ownDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir()
}
