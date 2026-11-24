package conversations

import (
	"encoding/json"
	"regexp"
	"strings"
)

// UnmarshalJSON reads a link as stored, with a model note an earlier build
// wrote put the short way (ShortModelNote).
func (l *PWCodeLink) UnmarshalJSON(b []byte) error {
	type plain PWCodeLink
	if err := json.Unmarshal(b, (*plain)(l)); err != nil {
		return err
	}
	if note, from, ok := ShortModelNote(l.ModelNote); ok {
		l.ModelNote = note
		if l.ModelFrom == "" {
			l.ModelFrom = from
		}
	}
	return nil
}

// The long notes earlier builds stored on a chat whose session the Studio
// moved off pw code's own model, with that model's and the new one's full
// ids: "pw code's own model, X, is not enabled for your AI connection, so
// this chat runs on Y, the first model it offers. Settings, External
// access, pw code model names another.", the same with "failed when you
// last used it, in the last ten minutes", one where the move failed ("and
// the Studio could not move this chat to Y: why."), and "pw code's model
// X is not enabled for the AI connection, so the session runs on Y".
const (
	oldWhy   = `(is not enabled for (?:your|the) AI connection|failed when (?:you )?last used(?: it)?, in the last ten minutes)`
	oldOther = `(?: Settings, External access, pw code model names another\.)?$`
)

var (
	oldMoved = regexp.MustCompile(`^pw code's (?:own )?model,? ([^\s,]+),? ` + oldWhy + `, so (?:this chat|the session) runs on ([^\s,]+)(?:, [^.]*\.|\.)?` + oldOther)
	oldStuck = regexp.MustCompile(`^pw code's (?:own )?model,? ([^\s,]+),? ` + oldWhy + `, and the Studio could not move this chat to (\S+?): (.*?)\.` + oldOther)
)

// ShortModelNote is an old long model note in the words the Studio uses
// now ("Runs on y because x is not enabled for you. Choose another under
// Settings, External access, pw code model."), the models by their short
// names, and the full id of the model the session was moved off. ok is
// false for a note that is not in an old form.
func ShortModelNote(note string) (short, from string, ok bool) {
	why := func(old string) string {
		if strings.HasPrefix(old, "failed") {
			return "failed for you in the last ten minutes"
		}
		return "is not enabled for you"
	}
	const other = " Choose another under Settings, External access, pw code model."
	if m := oldMoved.FindStringSubmatch(note); m != nil {
		to := strings.TrimSuffix(m[3], ".")
		return "Runs on " + shortModelName(to) + " because " + shortModelName(m[1]) + " " + why(m[2]) + "." + other, m[1], true
	}
	if m := oldStuck.FindStringSubmatch(note); m != nil {
		return shortModelName(m[1]) + " " + why(m[2]) + ", and pw code did not take " + shortModelName(m[3]) + " in its place: " + m[4] + "." + other, m[1], true
	}
	return note, "", false
}

// shortModelName is a model as the person picks it, as the chat names it
// (chat's shortModel): "sonnet-x" for acct:anthropic/sonnet-x.
func shortModelName(id string) string {
	if i := strings.LastIndexAny(id, ":/"); i >= 0 && i < len(id)-1 {
		return id[i+1:]
	}
	return id
}
