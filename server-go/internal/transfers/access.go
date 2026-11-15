package transfers

import (
	"context"
	"path"
	"sort"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// Access is whose permissions reach a library's files.
type Access int

const (
	// AccessNone: Studio cannot reach the files (an index only).
	AccessNone Access = iota
	// AccessServer: the account the Studio runs as reaches them: a folder
	// on this computer, a library over ssh with that account's keys, or a
	// read-through host.
	AccessServer
	// AccessViewer: the viewer's own platform key reaches them, so the
	// viewer's own permissions on that host apply.
	AccessViewer
)

// ServerAccountReadOnly is why a library reached with the server
// account's own access is read only with sign-in on.
const ServerAccountReadOnly = "read only for transfers: its files are reached with the account the Studio runs as, whose permissions are not yours"

// LibraryWritable is whether a transfer may copy into a library and move
// out of it, and if not, why (for the location's Detail).
//
// The registry's own Writable (indexWritable) says Studio may edit the
// library through its file operations and index; only the main library
// has it. Transfers do not need that: they write the files, and the index
// catches up when it is next refreshed. So writing follows who reaches the
// files:
//
//   - Sign-in off: there is one user, and the account the Studio runs as
//     is theirs. Every library whose files Studio reaches is writable: a
//     folder on this computer, a library on another host through the
//     user's key, or a read-through host.
//   - Sign-in on: a library reached with the server account's own access
//     (a folder on this computer; ssh and read-through libraries are not
//     offered at all, see readThroughOff) keeps the registry's rule, since
//     that account's permissions are not the viewer's. A library on a host
//     reached with the viewer's own platform key is writable, because the
//     viewer's own permissions apply there.
//
// In every case the file system decides in the end: a plan checks that the
// folders it writes in can be written (checkWritable) before it is offered.
func LibraryWritable(indexWritable bool, by Access, authOn bool) (bool, string) {
	switch {
	case by == AccessNone:
		return false, ""
	case indexWritable, by == AccessViewer, !authOn:
		return true, ""
	}
	return false, ServerAccountReadOnly
}

// probesMax caps the folders a move's source is checked in, two calls each
// on a host; folders past it are found out when the originals are removed,
// after the copy is in place, so nothing is lost.
const probesMax = 8

// checkWritable checks that the account can write in each folder of l by
// making and removing a staging-named folder there, as the Settings
// connection test does; it works the same on this computer, over a shell
// on a host, and over SFTP. what says what the write is for.
func (s *Service) checkWritable(ctx context.Context, l *loc, dirs []string, what string) error {
	for _, d := range dirs {
		probe := join(d, stagingPrefix+"probe-"+newID())
		if err := l.fs.Mkdir(ctx, probe); err != nil {
			s.d.Log.Info("transfers: write check failed", "location", l.ID, "folder", d, "error", messageOf(err))
			return apierr.New(400, "you cannot write in %s on %s (%s), so %s", folderName(d, l.fs), l.fs.Where(), l.Label, what)
		}
		if err := l.fs.RemoveStaging(ctx, probe); err != nil {
			s.d.Log.Warn("transfers: a write check's folder was not removed", "location", l.ID, "folder", probe, "error", messageOf(err))
		}
	}
	return nil
}

// folderName is a folder of a location as a person knows it: its path on
// the host.
func folderName(rel string, fs FS) string {
	return path.Join(fs.Root(), rel)
}

// moveDirs are the folders a move removes from: the folder holding each
// item named, and each folder named (its files are removed from it). Only
// the top of the move is checked; folders below are found out as the
// originals are removed.
func moveDirs(items, srcDirs []string) []string {
	isDir := map[string]bool{}
	for _, d := range srcDirs {
		isDir[d] = true
	}
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, it := range items {
		add(parentOf(it))
		if isDir[it] {
			add(it)
		}
	}
	sort.Strings(out)
	if len(out) > probesMax {
		out = out[:probesMax]
	}
	return out
}
