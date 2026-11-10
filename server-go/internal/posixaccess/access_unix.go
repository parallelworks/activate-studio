//go:build !windows

package posixaccess

import (
	"os"
	"syscall"
)

func runsAsRoot() bool { return os.Geteuid() == 0 }

// Shared reports whether every account that can enter the folder dir can
// read file, from their owners, groups and modes alone (access control
// lists are not read, and the index does not carry them). Only the folder
// itself is considered: the folders above it can only narrow who gets in,
// so the answer is never wider than the truth. Where it cannot be sure it
// says no, and the file is indexed by name only.
func Shared(dir, file os.FileInfo) bool {
	d, ok1 := dir.Sys().(*syscall.Stat_t)
	f, ok2 := file.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	dm, fm := dir.Mode().Perm(), file.Mode().Perm()
	all := fm&0o444 == 0o444
	switch {
	case dm&0o001 != 0:
		// Anyone may enter: anyone must be able to read. All three read
		// bits, since the owner and group classes read by their own bits.
		return all
	case dm&0o010 != 0:
		// The folder's group may enter, and its owner when the owner bit
		// allows.
		group := all || (f.Gid == d.Gid && fm&0o440 == 0o440)
		return group && (dm&0o100 == 0 || ownerReads(d, f, fm))
	case dm&0o100 != 0:
		return ownerReads(d, f, fm)
	}
	// Nobody but root enters, and root reads every file.
	return true
}

// ownerReads: the folder's owner can read the file.
func ownerReads(d, f *syscall.Stat_t, fm os.FileMode) bool {
	return d.Uid == 0 || fm&0o444 == 0o444 || (d.Uid == f.Uid && fm&0o400 != 0)
}
