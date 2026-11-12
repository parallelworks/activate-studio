package updates

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// A hosted Studio is updated by its deployment and never checks. A
// deployment that runs the Linux payload under a run loop of its own (the
// ACTIVATE workflow, deploy/workflow.yaml, for a payload from a GitHub
// release) sets STUDIO_HOSTED_UPDATES=1, and the Studio then updates itself
// as the per-user Linux app does: the same check, signed download, swap
// with the previous copy kept beside it, and exit 76, after which the run
// loop starts the new payload, or puts the previous one back when the new
// one keeps stopping. It has no window, so nothing asks to reopen.
//
// Only the people STUDIO_UPDATE_OWNERS names (usernames, separated by
// commas or spaces; the workflow sets the person who launched it) see and
// install updates. To anyone else the Studio is as any hosted one. A
// deployment without sign-in is one person's, reached only by them.

// deployment: a hosted Studio whose deployment lets it update itself.
func (u *Updater) deployment() bool {
	return u.hosted() && u.env("STUDIO_HOSTED_UPDATES") == "1"
}

// owners are the usernames STUDIO_UPDATE_OWNERS names.
func (u *Updater) owners() []string {
	return strings.FieldsFunc(u.env("STUDIO_UPDATE_OWNERS"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
}

// MayUpdate is whether the person behind a request may see and install
// updates: anyone, except on a deployment that updates itself, where only
// its owners may when people sign in.
func (u *Updater) MayUpdate(user *auth.User) bool {
	if !u.deployment() || !u.o.AuthEnabled {
		return true
	}
	if user == nil {
		return false
	}
	for _, o := range u.owners() {
		o = strings.TrimPrefix(o, "user:")
		if strings.EqualFold(o, user.Username) || strings.EqualFold(o, strings.TrimPrefix(user.ID, "user:")) {
			return true
		}
	}
	return false
}

// windowFiles are the Linux payload's window and installer, which a
// deployment removes (deploy/workflow.yaml).
var windowFiles = []string{"bin", "share", "install.sh", "uninstall.sh"}

// withoutWindow removes them from a new payload.
func withoutWindow(dir string) error {
	for _, f := range windowFiles {
		if err := rmTree(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
