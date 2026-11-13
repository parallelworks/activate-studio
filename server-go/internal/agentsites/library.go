package agentsites

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// LibraryMark is the file placeInLibrary leaves in a folder for a moment
// to learn whether a site sees the same folder.
const LibraryMark = ".studio-site-check"

// agentsFolder is the library folder agents work in when none is named.
const agentsFolder = "agents"

// LibraryFolder checks a folder named relative to the library: no
// parent steps, no dot folders, slashes only.
func LibraryFolder(rel string) (string, error) {
	c := strings.Trim(strings.ReplaceAll(trimJS(rel), "\\", "/"), "/")
	if c == "" {
		return "", kb(400, "folder: a folder in the library, relative to its top")
	}
	for _, part := range strings.Split(c, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", kb(400, "folder: a folder in the library, relative to its top, with no .. or dot folders")
		}
	}
	return path.Clean(c), nil
}

// shq quotes a word for a POSIX shell.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// placeInLibrary makes rel in the library at root and reports the path
// the site sees it at, when the site sees this library: always for the
// local site, and for a cluster when a mark written here reads back
// there (the same machine, or the library on storage they share). A
// folder the site does not see is removed again when this made it and it
// is empty, and ok is false.
func (s *Sites) placeInLibrary(ctx context.Context, site, key, root, rel string) (string, bool, error) {
	dir := filepath.Join(root, filepath.FromSlash(rel))
	existed := true
	if _, err := os.Stat(dir); err != nil {
		existed = false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	if site == Local {
		return dir, true, nil
	}
	b := make([]byte, 12)
	rand.Read(b)
	nonce := hex.EncodeToString(b)
	mark := filepath.Join(dir, LibraryMark)
	if err := os.WriteFile(mark, []byte(nonce), 0o644); err != nil {
		return "", false, err
	}
	args := []string{"ssh"}
	if h := s.platformHost(); h != "" {
		args = append(args, "--platform-host", h)
	}
	q := shq(dir)
	args = append(args, site, `if [ "$(cat -- `+q+`/`+LibraryMark+` 2>/dev/null)" = `+shq(nonce)+` ]; then cd -- `+q+` && pwd; fi`)
	out, err := s.pwWithKey(ctx, args, key)
	os.Remove(mark)
	lines := strings.Split(trimJS(out), "\n")
	seen := lines[len(lines)-1]
	if err != nil || !strings.HasPrefix(seen, "/") {
		if !existed {
			os.Remove(dir)
			// The agents folder too, when this left it empty.
			if parent := filepath.Dir(dir); parent != filepath.Clean(root) {
				os.Remove(parent)
			}
		}
		return "", false, nil
	}
	return seen, true, nil
}

// InLibrary is the library-relative folder of a workspace path, when it
// is a folder inside the library at root on this machine.
func InLibrary(root, workspace string) (string, bool) {
	if root == "" || workspace == "" {
		return "", false
	}
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	w, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(r, w)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
