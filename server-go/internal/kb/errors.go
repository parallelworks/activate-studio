package kb

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// libuv's messages for the errors a file operation meets, so a reason
// shown for a skipped item reads as Node's did.
var uvMessages = map[syscall.Errno]string{
	syscall.ENOENT: "no such file or directory", syscall.EACCES: "permission denied",
	syscall.EEXIST: "file already exists", syscall.ENOTDIR: "not a directory",
	syscall.EISDIR: "illegal operation on a directory", syscall.EXDEV: "cross-device link not permitted",
	syscall.ENOTEMPTY: "directory not empty", syscall.EPERM: "operation not permitted",
	syscall.EROFS: "read-only file system", syscall.EBUSY: "resource busy or locked",
	syscall.EINVAL: "invalid argument", syscall.ENAMETOOLONG: "name too long",
	syscall.ELOOP: "too many symbolic links encountered", syscall.ENOSPC: "no space left on device",
	syscall.EIO: "i/o error", syscall.EMFILE: "too many open files",
}

var uvCodes = map[syscall.Errno]string{
	syscall.ENOENT: "ENOENT", syscall.EACCES: "EACCES", syscall.EEXIST: "EEXIST", syscall.ENOTDIR: "ENOTDIR",
	syscall.EISDIR: "EISDIR", syscall.EXDEV: "EXDEV", syscall.ENOTEMPTY: "ENOTEMPTY", syscall.EPERM: "EPERM",
	syscall.EROFS: "EROFS", syscall.EBUSY: "EBUSY", syscall.EINVAL: "EINVAL", syscall.ENAMETOOLONG: "ENAMETOOLONG",
	syscall.ELOOP: "ELOOP", syscall.ENOSPC: "ENOSPC", syscall.EIO: "EIO", syscall.EMFILE: "EMFILE",
}

// nodeSyscall is the syscall name Node's message gives for a Go op.
var nodeSyscall = map[string]string{
	"stat": "stat", "lstat": "lstat", "open": "open", "rename": "rename", "remove": "rm",
	"unlinkat": "unlink", "unlink": "unlink", "mkdir": "mkdir", "readlink": "readlink",
	"symlink": "symlink", "readdirent": "scandir", "openat": "open", "rmdir": "rmdir",
}

// errMessage is String(err.message ?? err) as Node would write it: a
// KbError's message, and a file error as libuv words it
// ("ENOENT: no such file or directory, stat '/a/b'").
func errMessage(err error) string {
	var kb *apierr.Error
	if errors.As(err, &kb) {
		return kb.Message
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if msg, ok := uvMessages[errno]; ok {
			var pe *fs.PathError
			var le *os.LinkError
			switch {
			case errors.As(err, &le):
				return fmt.Sprintf("%s: %s, %s '%s' -> '%s'", uvCodes[errno], msg, opName(le.Op), le.Old, le.New)
			case errors.As(err, &pe):
				return fmt.Sprintf("%s: %s, %s '%s'", uvCodes[errno], msg, opName(pe.Op), pe.Path)
			}
			return fmt.Sprintf("%s: %s", uvCodes[errno], msg)
		}
	}
	return err.Error()
}

func opName(op string) string {
	if n, ok := nodeSyscall[op]; ok {
		return n
	}
	return op
}
