// Package writehelper is studio-write, the program the Studio sends to a
// system to write in a library's folder there, as the person whose library
// it is:
//
//	studio-write [--priv=1|2] [--pin=INODE:UID] MARK COMMAND ROOT [arguments]
//	studio-write version
//
// It answers the write commands of the transfers track's shell helper
// (transfers.RemoteFS), with the same arguments, output and exit statuses.
//
// Another account that can write in the folder can put a link, or another
// folder, in the place of any name in it at any moment. So the helper never
// uses a path twice. It opens the library's folder once, compares it with
// the one the library was added for (--pin), and reaches everything else
// from that descriptor, one folder at a time, following no link (openat2
// with RESOLVE_BENEATH and RESOLVE_NO_SYMLINKS where the kernel has it,
// openat with O_NOFOLLOW where it does not). It creates, renames, removes
// and changes access on descriptors alone. What is swapped in meanwhile is
// refused, and nothing outside the folder is ever written.
//
// It is built for Linux, without cgo.
package writehelper

import "github.com/parallelworks/activate-studio/server-go/internal/staging"

// Exit statuses. 3 to 13 are the shell helper's.
const (
	exitUsage    = 2
	exitNoRoot   = 3
	exitMissing  = 4
	exitNotPlain = 5
	exitOutside  = 6
	exitLink     = 7
	exitCannot   = 8
	exitFailed   = 9
	exitTaken    = 10
	exitRead     = 12
	// ExitMoved: the folder is not the one the library was added for.
	ExitMoved = 16
	// ExitOpen: a staging folder is not closed to other accounts.
	ExitOpen = 17
)

// InDir is the staging folder to a program run with the in command.
const InDir = "/proc/self/fd/9/"

const inFD = 9

// The starts of the names of what the Studio writes before it takes its
// name: a transfer's staging folder, and an upload's file or folder.
const (
	stagePrefix  = staging.Transfer
	uploadPrefix = staging.Upload
)

// Tool is the helper on a library's system.
type Tool struct {
	// Path is the program there, absolute; "" where it cannot run, and Why.
	Path string
	Why  string
	// Pin is the folder the library was added for, as INODE:UID; the helper
	// refuses another in its place. "" where it is not known yet.
	Pin string
	// Missing is told that the program was not there after all.
	Missing func()
}
