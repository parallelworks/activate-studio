package agentsmd

import (
	"path/filepath"
	"strings"
)

// An agent the Studio starts in a library folder (a delegated session, a
// standing agent) runs in pw code's accept-edits mode: it reads and edits
// in its folder without asking, while every shell command, and any read
// outside the folder, waits for a person. pw code's allow rules match a
// shell command's text, so they cannot say "a command that only reads,
// inside this folder": a rule such as Bash(ls *) matches "ls > file"
// too. So the Studio pre-approves what it can say exactly, and tells the
// agent to read with its file tools:
//
//   - ReadFile, GlobSearch and GrepSearch anywhere in the library, the
//     path rules pw code checks against the resolved path;
//   - a few whole commands that only list or print where the agent works
//     (each must be a whole stage of the command: "pwd; ls -la" runs,
//     "ls -la notes" or "ls > x" asks).
//
// pw code takes the rules only from a local caller (the daemon refuses
// them from a remote origin), so they go only to this host's daemon.

// folderCommands are the whole commands pre-approved: they list or print
// the working folder and change nothing.
var folderCommands = []string{"pwd", "ls", "ls -l", "ls -a", "ls -la", "ls -al", "ls -lah", "ls -alh", "ls -R", "ls -lR", "tree", "date"}

// readTools are pw code's file reading tools, which check a path outside
// the workspace against path rules.
var readTools = []string{"ReadFile", "GlobSearch", "GrepSearch"}

// ReadRules are the allowedTools for a session working in a folder of the
// library whose top is library on this host; with no library, only the
// folder commands.
func ReadRules(library string) []string {
	out := make([]string, 0, len(folderCommands)+2*len(readTools))
	for _, c := range folderCommands {
		out = append(out, "Bash("+c+")")
	}
	library = strings.TrimRight(filepath.Clean(library), string(filepath.Separator))
	if library == "" || library == "." || !filepath.IsAbs(library) {
		return out
	}
	tops := []string{library}
	// pw code matches the resolved path, so a library reached through a
	// link is named both ways.
	if real, err := filepath.EvalSymlinks(library); err == nil && real != library {
		tops = append(tops, real)
	}
	for _, top := range tops {
		for _, t := range readTools {
			out = append(out, t+"("+filepath.ToSlash(top)+"/**)")
		}
	}
	return out
}

// WorkingNote tells an agent in a library folder how to work without
// waiting for a person; it goes into what the Studio writes for it.
const WorkingNote = "Read, list and search files with your file tools (ReadFile, GlobSearch, GrepSearch): they run without asking, here and anywhere in the library. Write and edit files in this folder with your edit tools (WriteFile, EditFile), which also run without asking; append to a file by editing it. A shell command waits until a person approves it, except pwd, date and a plain ls or tree of this folder, so use the shell only when a file tool cannot do the work."
