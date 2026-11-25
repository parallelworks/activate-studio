package transfers

import (
	"context"
	"regexp"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/writehelper"
)

// WriteTool is the write helper (cmd/studio-write) on a library's system,
// which every write in the library runs through. Another account that can
// write in the library's folder cannot send one elsewhere: the helper
// reaches all it changes from the folder it opened, through no link.
type WriteTool = writehelper.Tool

// ToolGone is the launcher's status when the helper is not there any more,
// and codeGuard the shell helper's when another account could redirect a
// write.
const (
	ToolGone  = 40
	codeGuard = 13
)

// again is whether a command's exit has the helper, or the folder it is
// for, looked at again before it is refused.
func again(code int) bool { return code == ToolGone || code == writehelper.ExitMoved }

// shellCmds are the commands that only read, which the shell helper
// answers everywhere. Any other goes to the write helper of a library: one
// it does not know is refused, and never run by path in the shell instead.
var shellCmds = map[string]bool{"list": true, "list2": true, "walk": true, "stat": true, "free": true, "sha": true, "guard": true}

// options are the helper's, for the location and its pin.
func (r *RemoteFS) options(w WriteTool) []string {
	var out []string
	switch {
	case r.Private:
		out = append(out, "--priv=1")
	case r.Own:
		out = append(out, "--priv=2")
	}
	if w.Pin != "" {
		out = append(out, "--pin="+w.Pin)
	}
	return out
}

// launch is the script that runs the helper with a command's arguments.
func (r *RemoteFS) launch(w WriteTool) string {
	var b strings.Builder
	b.WriteString("h=" + shQuote(w.Path) + "; [ -x \"$h\" ] || exit 40; exec \"$h\"")
	for _, o := range r.options(w) {
		b.WriteString(" " + shQuote(o))
	}
	b.WriteString(" \"$@\"")
	return b.String()
}

// scriptFor is what runs a command at the location, and the write helper
// it runs where it is that. In a person's library with no helper, the
// shell helper runs, and refuses a write another account could redirect.
func (r *RemoteFS) scriptFor(ctx context.Context, cmd string) (string, *WriteTool) {
	if r.Writer == nil || shellCmds[cmd] {
		return r.script(cmd == "guard"), nil
	}
	w := r.Writer(ctx)
	if w.Path == "" {
		return pinLine(w.Pin) + r.script(true), nil
	}
	return r.launch(w), &w
}

// pinLine sets PIN for the shell helper's guard.
func pinLine(pin string) string {
	if !pinRe.MatchString(pin) {
		return ""
	}
	return "PIN=" + pin + "\n"
}

var pinRe = regexp.MustCompile(`^[0-9]+:[0-9]+$`)

// lost tells that the helper was not where it was found before.
func lost(w *WriteTool) {
	if w != nil && w.Missing != nil {
		w.Missing()
	}
}

// refused is what a guarded write's refusal says where no helper could
// have made it.
func (r *RemoteFS) refused(ctx context.Context, msg string) string {
	if r.Writer == nil {
		return msg
	}
	if w := r.Writer(ctx); w.Path == "" && w.Why != "" {
		return msg + " " + w.Why
	}
	return msg
}

// Identity is the folder the location is now, as a pin.
func (r *RemoteFS) Identity(ctx context.Context) (string, error) {
	res, err := r.run(ctx, nil, "id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(lastLine(res.Stdout)), nil
}

// Loose is why another account could redirect what a program writes in the
// location by its path, "" where none can.
func (r *RemoteFS) Loose(ctx context.Context) string {
	if r.Writer == nil {
		return ""
	}
	res, err := r.run(ctx, nil, "guard")
	switch {
	case err == nil:
		return ""
	case res.Code == codeGuard:
		said, _, _ := strings.Cut(lastLine(remote.QuietStderr(res.Stderr)), ", so ")
		return said
	}
	return messageOf(err)
}

// programIn is the words that run a program on the location's host with a
// staging folder held open as writehelper.InDir; ok false where the write helper
// does not run, or the location is no one's library.
func (r *RemoteFS) programIn(ctx context.Context, stage string) ([]string, bool) {
	if r.Writer == nil {
		return nil, false
	}
	w := r.Writer(ctx)
	if w.Path == "" {
		return nil, false
	}
	return append(append([]string{w.Path}, r.options(w)...), "-", "in", r.Dir, stage), true
}

// statusOfCode is the status a helper command's exit answers.
func statusOfCode(code int) int {
	switch code {
	case 4:
		return 404
	case 5, 6, 7, 11:
		return 400
	case 10:
		return 409
	case codeGuard, writehelper.ExitMoved:
		return 403
	}
	return 502
}

// errToolGone is a write whose input cannot be sent twice, where the
// helper was not there: it is sent again at the next one.
func errToolGone(name string) error {
	return apierr.New(503, "%s: the Studio's write helper was not in your ~/.activate-studio there. Try again, and it is sent again.", name)
}
