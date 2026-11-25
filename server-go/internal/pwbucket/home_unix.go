//go:build !windows

package pwbucket

import (
	"errors"
	"os"
	"syscall"
)

// lockDir locks a folder that is still there. Closing the file unlocks it;
// pw runs do not inherit it. With an error the folder is gone or another has
// it (no file), or its file system has no locks (a file, not locked).
func lockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil && !errors.Is(err, syscall.EWOULDBLOCK) {
		return f, err
	}
	if err == nil {
		held, herr := f.Stat()
		now, nerr := os.Lstat(dir)
		if herr == nil && nerr == nil && os.SameFile(held, now) {
			return f, nil
		}
		err = os.ErrNotExist
	}
	f.Close()
	return nil, err
}

// removeLocked removes a folder that lockDir locked, and the lock after it.
func removeLocked(dir string, l *os.File) error {
	defer l.Close()
	return os.RemoveAll(dir)
}

// ownDir is whether p is a folder of this account's, not a link to one.
func ownDir(p string) bool {
	st, err := os.Lstat(p)
	if err != nil || !st.IsDir() {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) == os.Geteuid()
}
