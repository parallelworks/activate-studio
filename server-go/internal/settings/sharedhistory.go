package settings

import (
	"encoding/json"
	"slices"
	"strings"
)

// SharedHistoryChosenKey marks a saved chatSharedHistory that an
// administrator chose. Up to v1.203.39 saving Settings wrote every setting of
// the form into the file, so there a true may be the old default and not a
// choice.
const SharedHistoryChosenKey = "chatSharedHistoryChosen"

// sharedHistory is whether people see each other's chats. Where nobody
// signs in there is one person, and the setting stands as it is. With
// sign-in it is off unless it was turned on: by the setting, or by
// CHAT_SHARED_HISTORY=1.
func (s *Store) sharedHistory(m map[string]json.RawMessage) bool {
	env := s.env("CHAT_SHARED_HISTORY")
	saved := field[bool](m, "chatSharedHistory")
	if !s.authEnabled {
		return or(saved, env != "0")
	}
	if saved != nil && (!*saved || sharedHistoryChosen(m, env)) {
		return *saved
	}
	return slices.Contains([]string{"1", "true", "yes", "on"}, strings.ToLower(strings.TrimSpace(env)))
}

// sharedHistoryChosen is whether a saved true was a choice: it is marked as
// one, or it was saved before there was a mark and the old default does not
// account for it. Under CHAT_SHARED_HISTORY=0 the form showed off, and a
// file with it and without the form's other settings was not the form's.
func sharedHistoryChosen(m map[string]json.RawMessage, env string) bool {
	if c := field[bool](m, SharedHistoryChosenKey); c != nil {
		return *c
	}
	if env == "0" {
		return true
	}
	for _, k := range []string{"ragAdvertiseRagModel", "ragEndpointAutoStart", "requirePersonalKey"} {
		if _, ok := m[k]; !ok {
			return true
		}
	}
	return false
}
