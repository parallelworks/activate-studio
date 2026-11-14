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

// missing says whether err is a file that is not there, or one whose
// folder is now a file.
func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// notFound is err as a viewing route answers it: a file that is not
// there is a 404 the client may read, as ReadFileContent answers it;
// anything else is err unchanged.
func notFound(err error) error {
	if missing(err) {
		return Error(404, "no such file")
	}
	return err
}

// missingSince is the 404 when the file at abs went away while a helper
// program was reading it: the helper's own message says only that it
// could not open the file. Any other failure is err unchanged.
func missingSince(abs string, err error) error {
	if _, serr := os.Stat(abs); missing(serr) {
		return Error(404, "no such file")
	}
	return notFound(err)
}

// PastLastPage is a page asked for beyond the end of a PDF, as happens
// when a file is rewritten shorter while its pages are on screen.
type PastLastPage struct{ Page, Pages int }

func (e *PastLastPage) Error() string {
	if e.Pages == 1 {
		return fmt.Sprintf("page %d is past the end: this file has 1 page", e.Page)
	}
	return fmt.Sprintf("page %d is past the end: this file has %d pages", e.Page, e.Pages)
}
