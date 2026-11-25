// Package ownfile tells the AGENTS.md the Studio keeps in a folder for pw
// code from one a person wrote. The Studio writes its section there when a
// chat of its starts work in the folder (internal/agentsmd), so a file
// that holds that section alone, as the Studio wrote it, is not library
// content: nothing lists, counts or indexes it. The section's begin marker
// carries a digest of its text, so a file with a word of a person's
// anywhere in it, inside the markers or out, is theirs and stays listed.
package ownfile

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// Name is the file's name.
	Name = "AGENTS.md"
	// Section is the name of the Studio's section in it.
	Section = "studio_tools"
	// Max is the largest file read to tell.
	Max = 64 << 10

	beginHead = "<!-- ACTIVATE Studio instructions " + Section + ": begin."
	endMark   = "<!-- ACTIVATE Studio instructions " + Section + ": end -->"
)

var only = regexp.MustCompile(`(?s)^` + regexp.QuoteMeta(beginHead) + `[^>]*\[([0-9a-f]{16})\] -->\n\n(.*)\n\n` + regexp.QuoteMeta(endMark) + `$`)

func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}

// Begin is the marker the section starts with, for its text.
func Begin(body string) string {
	return beginHead + " ACTIVATE Studio writes this section when a chat of its starts work here, and keeps it up to date. Write your own text outside it. [" + digest(body) + "] -->"
}

// Is reports whether text is the Studio's section and nothing else.
func Is(text []byte) bool {
	if len(text) > Max {
		return false
	}
	s := strings.TrimSpace(strings.ReplaceAll(string(text), "\r\n", "\n"))
	m := only.FindStringSubmatch(s)
	return m != nil && m[1] == digest(m[2])
}

// At reports whether the file at abs is the Studio's: a plain file of that
// name that Is. One that cannot be read is not.
func At(abs string) bool {
	if filepath.Base(abs) != Name {
		return false
	}
	st, err := os.Lstat(abs)
	if err != nil || !st.Mode().IsRegular() || st.Size() > Max {
		return false
	}
	b, err := os.ReadFile(abs)
	return err == nil && Is(b)
}
