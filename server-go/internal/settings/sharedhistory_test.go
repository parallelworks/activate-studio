package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// form is what saving Settings wrote up to v1.203.39, whichever setting of
// the form was changed.
const form = `"appName":"Studio","ragAdvertiseRagModel":false,"ragAdvertiseAgentModel":true,"ragEndpointAutoStart":false,"requirePersonalKey":false,"chatSharedHistory":true`

func TestPeopleSeeEachOthersChatsOnlyWhereItWasTurnedOn(t *testing.T) {
	for _, c := range []struct {
		name, saved string
		env         string
		auth, want  bool
	}{
		// With sign-in.
		{"nothing set", "", "", true, false},
		{"env on", "", "1", true, true},
		{"env on in words", "", " True ", true, true},
		{"env off", "", "0", true, false},
		{"env false", "", "false", true, false},
		{"other settings saved", `{"appName":"Lab"}`, "", true, false},
		{"v1.203.39: untouched, Settings never saved", `{"setupDone":true}`, "", true, false},
		{"v1.203.39: untouched, another setting saved with the form", "{" + form + "}", "", true, false},
		{"v1.203.39: the form, env on", "{" + form + "}", "1", true, true},
		{"v1.203.39: turned on over an env that says off", "{" + form + "}", "0", true, true},
		{"v1.203.39: set on alone", `{"chatSharedHistory":true}`, "", true, true},
		{"v1.203.39: turned off", `{"chatSharedHistory":false}`, "", true, false},
		{"v1.203.39: turned off, env on", `{"chatSharedHistory":false}`, "1", true, false},
		{"v1.203.39: the form, turned off, env on", strings.Replace("{"+form+"}", "chatSharedHistory\":true", "chatSharedHistory\":false", 1), "1", true, false},
		{"turned on since", "{" + form + `,"chatSharedHistoryChosen":true}`, "", true, true},
		{"not a boolean", `{"chatSharedHistory":"yes"}`, "", true, false},
		// Without sign-in, as before.
		{"one person, nothing set", "", "", false, true},
		{"one person, env off", "", "0", false, false},
		{"one person, the form", "{" + form + "}", "", false, true},
		{"one person, saved off", `{"chatSharedHistory":false}`, "", false, false},
	} {
		dir := t.TempDir()
		if c.saved != "" {
			os.WriteFile(filepath.Join(dir, "settings.json"), []byte(c.saved), 0o644)
		}
		s := Open(dir, "/kb", c.auth, func(k string) (string, bool) { return c.env, k == "CHAT_SHARED_HISTORY" && c.env != "" })
		if got := s.Effective().ChatSharedHistory; got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
