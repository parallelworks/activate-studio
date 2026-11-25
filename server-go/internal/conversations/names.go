package conversations

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxName is the longest name kept for a chat or sent to its session.
const maxName = 120

// oneName is a name as a chat's title holds it: one line, cut.
func oneName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxName {
		r := []rune(s)
		s = string(r[:maxName-1]) + "…"
	}
	return s
}

// The chat page titles a new chat with the start of its first message, less
// what Markdown marks it with.
var (
	titleCode  = regexp.MustCompile("(?s)```.*?```|`[^`]+`")
	titleMarks = strings.NewReplacer("#", "", "*", "", "_", "", "~", "", "[", "", "]", "", "(", "", ")", "", "`", "")
)

// commandTitle is a title made of a pw code command, such as "/plan".
var commandTitle = regexp.MustCompile(`^/[A-Za-z0-9_:-]+(\s|\.\.\.$|…$|$)`)

func plainTitle(s string) string { return strings.Join(strings.Fields(titleMarks.Replace(s)), " ") }

// IsDefaultTitle is whether title is one a chat is given without anyone
// naming it: none, the start of its first message (first), a command it
// started with, or the folder of a session started with no message.
func IsDefaultTitle(title, first string) bool {
	if commandTitle.MatchString(strings.TrimSpace(title)) {
		return true
	}
	t := plainTitle(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(title), "..."), "…"))
	if t == "" || t == "Untitled session" || strings.HasPrefix(t, "pw code in ") {
		return true
	}
	return strings.HasPrefix(plainTitle(first), t) || strings.HasPrefix(plainTitle(titleCode.ReplaceAllString(first, "")), t)
}

// FirstMessage is the conversation's first message from a person.
func (c *Conversation) FirstMessage() string {
	for _, m := range c.Messages {
		if m.Role == "user" && m.Content != nil {
			return *m.Content
		}
	}
	return ""
}

// titleFollows is whether the title may take its session's name: it is a
// default one, or the name of the session the chat had before.
func (c *Conversation) titleFollows() bool {
	if c.Title == nil || IsDefaultTitle(*c.Title, c.FirstMessage()) {
		return true
	}
	return c.SessionName != nil && c.SessionName.Rename == "" && c.SessionName.Seen == oneName(*c.Title)
}
