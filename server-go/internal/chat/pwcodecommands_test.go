package chat

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// remoteAliases is a palette as pw code gives it, with another name for
// remote control than any the Studio has fixed.
func remoteAliases() map[string]any {
	return map[string]any{"commands": []any{
		map[string]any{"name": "help"}, map[string]any{"name": "newest", "aliases": []any{"latest"}},
		map[string]any{"name": "remote-control", "aliases": []any{"rc", "beam"}},
		map[string]any{"name": "branch", "aliases": []any{"fork"}},
	}, "prompts": []any{}}
}

// refusedNames are ways to write a command the Studio refuses; the first
// ones ran in the session, which drops spaces and a slash before a name.
var refusedNames = []string{
	"rc", " rc", "rc ", "/rc", " /rc ", "rc\n", " rc", " /rc", "\u0085rc", "remote-control", " remote-control", "/remote-control",
	"/rename", " fork", "/archive ", "\tresume", "RC", "Rc", "//rc", "rc status", "rc\tstatus", "Remote-Control", "RENAME x", "Fork", "ARCHIVE", "Resume",
}

func TestACommandIsRefusedByAnyOfItsNames(t *testing.T) {
	for _, relayed := range []bool{true, false} {
		f := &fakePW{status: map[string]any{"version": "v7.1.0"}}
		var base, s string
		if relayed {
			_, base, _, _ = relayModule(t, &fakePW{}, f)
			relayedChat(t, base)
			s = base + "/api/code/machines/m2/sessions/s1"
		} else {
			_, base, _ = continueModule(t, f, "d1")
			send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m1","session":"s1","confirmed":true}`)
			s = base + "/api/code/machines/studio/sessions/s1"
		}
		var ran []string
		canList := true
		f.mu.Lock()
		f.routes = map[string]http.HandlerFunc{
			"POST /v1/sessions/s1/commands": func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				ran = append(ran, string(b))
				gives(map[string]any{"output": "ok"})(w, r)
			},
			"GET /v1/sessions/s1/palette": func(w http.ResponseWriter, r *http.Request) {
				if !canList {
					http.Error(w, "gone", 502)
					return
				}
				gives(remoteAliases())(w, r)
			},
		}
		f.mu.Unlock()
		refused := func(route, body string, want int) {
			t.Helper()
			if code, out, _ := send(t, http.MethodPost, s+route, body); code != want {
				t.Errorf("relayed %v, %s %s: %d %v, want %d", relayed, route, body, code, out, want)
			}
		}
		for _, n := range refusedNames {
			refused("/commands", mustJSON(jsonv.NewObject("name", n, "args", "on")), 403)
		}
		// pw code's own word on its aliases, before any list was read.
		refused("/commands", `{"name":"beam"}`, 403)
		refused("/commands", `{"name":"branch"}`, 403)
		// One the session does not list is not known, nor any while its
		// list cannot be read.
		refused("/commands", `{"name":"unlisted"}`, 403)
		canList = false
		refused("/commands", `{"name":"newest"}`, 403)
		canList = true
		// A field's name in another case is the same field to pw code.
		for _, b := range []string{`{"Name":"rc"}`, `{"name":"help","Name":"rc"}`, `{"name":"help","NAME":"remote-control"}`, `{"name":"help","Args":"x"}`, `{}`, `{"name":" / "}`} {
			if code, _, _ := send(t, http.MethodPost, s+"/commands", b); code != 400 && code != 403 {
				t.Errorf("%s: %d", b, code)
			}
		}
		refused("/approvals/ap1", `{"allowed":true,"Mode":"bypass-permissions"}`, 400)
		refused("/subagents/a1/commands", `{"name":"rc"}`, 404)
		if len(ran) != 0 || len(f.answers) != 0 {
			t.Errorf("relayed %v: reached the session: %q %v", relayed, ran, f.answers)
		}
		// A mode is held to the rules however the command is written.
		if relayed {
			for _, n := range []string{" permissions", "/permissions", "permissions\n"} {
				refused("/commands", mustJSON(jsonv.NewObject("name", n, "args", "bypass-permissions")), 403)
			}
		}
		// What is checked is what is sent.
		for _, n := range []string{"cost", " /cost ", "newest", "latest"} {
			refused("/commands", mustJSON(jsonv.NewObject("name", n)), 200)
		}
		if got := strings.Join(ran, " "); got != `{"name":"cost"} {"name":"cost"} {"name":"newest"} {"name":"latest"}` {
			t.Errorf("ran %s", got)
		}
		// Its list leaves out every name.
		_, out, _ := send(t, http.MethodGet, s+"/palette", "")
		if got := mustJSON(out["notListed"]); !strings.Contains(got, `"beam"`) || !strings.Contains(got, `"branch"`) || !strings.Contains(got, `"rc"`) || strings.Contains(got, `"newest"`) {
			t.Errorf("not listed: %s", got)
		}
	}
}

// A line sent as a message, a steer or to a subagent is refused the same
// way, on either path to a session.
func TestALineIsRefusedByAnyOfACommandsNames(t *testing.T) {
	f := &fakePW{script: helloTurn, status: map[string]any{"version": "v7.1.0"}}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	post(t, base, first(c.ID, "hello", true))
	s := base + "/api/code/machines/studio/sessions/s1"
	f.mu.Lock()
	f.routes = map[string]http.HandlerFunc{"GET /v1/sessions/s1/palette": gives(remoteAliases())}
	sent := len(f.messages)
	f.mu.Unlock()
	send(t, http.MethodGet, s+"/palette", "")
	lines := []string{"/rc", "/rc on", "  /rc on", "\n/RC on", " /rc on", "/Remote-Control", "/rename x", "/FORK", " /archive", "/resume s9", "/beam", "/Beam on", "/branch x"}
	for _, l := range lines {
		for _, route := range []string{"/messages", "/direction", "/subagents/a1/messages", "/subagents/a1/direction"} {
			if code, out, _ := send(t, http.MethodPost, s+route, mustJSON(jsonv.NewObject("text", l))); code != 403 || !strings.Contains(out["detail"].(string), "is not sent from here") {
				t.Errorf("%s %q: %d %v", route, l, code, out)
			}
		}
		if out := post(t, base, first(c.ID, l, false)); !strings.Contains(out, "is not sent from here") {
			t.Errorf("stream %q: %s", l, out)
		}
	}
	if code, _, _ := send(t, http.MethodPost, s+"/messages", `{"Text":"/rc on"}`); code != 400 {
		t.Errorf("another case: %d", code)
	}
	if len(f.messages) != sent {
		t.Errorf("reached the session: %q", f.messages[sent:])
	}
	// Other text is a message.
	for _, l := range []string{"/archive/2024 has what?", "resume the run", "what does /rc do?", "// rename this", "/review rc"} {
		if code, out, _ := send(t, http.MethodPost, s+"/messages", mustJSON(jsonv.NewObject("text", l))); code != 200 && code != 204 {
			t.Errorf("%q: %d %v", l, code, out)
		}
	}
}
