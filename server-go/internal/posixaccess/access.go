// Package posixaccess decides whether a file's text may sit in its
// folder's index database. GUFI gives each index folder the owner, group
// and mode of its source folder, so whoever can enter a source folder can
// read that folder's db.db and every text row in it. When the Studio
// builds as root, for an index other people connect to, a file whose own
// mode is narrower than its folder's would have its text readable by
// people who cannot open the file. FileLevel turns on the check, and
// Shared is the check.
package posixaccess

import (
	"os"
	"strings"
)

// Env is the setting that overrides the default: "file" keeps the text of
// a file out of its folder's index unless everyone who can enter the
// folder can read the file, "folder" writes every readable file's text.
const Env = "STUDIO_TEXT_ACCESS"

// FileLevel reports whether text follows each file's permissions: the
// setting when given, otherwise when the Studio runs as root, the case of
// an administrator's index that other accounts read.
func FileLevel() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(Env))) {
	case "file":
		return true
	case "folder":
		return false
	}
	return runsAsRoot()
}

// PrivateDir creates dir, when missing, and limits it to its owner. A
// root build keeps its text cache, which mirrors the source's names and
// holds the text of every file it read, out of other accounts' reach.
func PrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
