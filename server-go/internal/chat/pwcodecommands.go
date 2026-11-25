package chat

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// sessionCommands are the slash commands that manage the session or how its
// machine takes remote control, with their aliases: the Code page's, or the
// machine's own. What pw code names as an alias of one is refused too
// (refusedAmong).
var sessionCommands = map[string]bool{"rename": true, "fork": true, "archive": true, "resume": true, "remote-control": true, "rc": true}

// sentCommands are pw code's own that the Studio sends. Another is sent
// where the session lists it, so that an alias pw code does not give, new
// in a release, is refused until it is known.
var sentCommands = map[string]bool{
	"add-dir": true, "agents": true, "allocation": true, "approve": true, "auth": true, "autocompact": true, "claude-code": true,
	"clear": true, "compact": true, "cost": true, "diff": true, "effort": true, "goal": true, "help": true, "hooks": true, "mcp": true,
	"model": true, "permissions": true, "plan": true, "recap": true, "rewind": true, "settings": true, "skills": true, "status": true,
	"subtask": true, "tasks": true, "usage": true, "version": true,
}

// plainCommand is a command's name as pw code has it.
var plainCommand = regexp.MustCompile(`^[A-Za-z0-9_:-]+$`)

// commandWord is the command a line or a command's name starts with,
// however it is written: pw code drops spaces, and a slash, before it.
func commandWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(f[0], "/"))
}

// refusedAmong is sessionCommands with every other name a list of commands
// gives them.
func refusedAmong(commands []any) map[string]bool {
	out := maps.Clone(sessionCommands)
	for _, c := range commands {
		names := []string{strings.ToLower(commandName(c))}
		for _, a := range jsonv.Arr(jsonv.Path(c, "aliases")) {
			names = append(names, commandWord(jsonv.String(a)))
		}
		if slices.ContainsFunc(names, func(n string) bool { return sessionCommands[n] }) {
			for _, n := range names {
				out[n] = true
			}
		}
	}
	return out
}

func notSentFromHere(name string) error {
	return pwErr(403, "/"+name+" is not sent from here: use the session's menu in the Code page's list, or manage it where it runs.")
}

// refusedLine refuses a message pw code could read as one of the session's
// own commands. Other text is a message, with a slash or not.
func (m *Module) refusedLine(text string) error {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return nil
	}
	if n := commandWord(text); refusedAmong(m.pwCommands().every())[n] {
		return notSentFromHere(n)
	}
	return nil
}

// sentCommand is the name of a command the Studio sends a session: never
// one that manages it, by any of its names, and one it knows or the
// session lists.
func (m *Module) sentCommand(ctx context.Context, t *pwTarget, id string, body any) (string, error) {
	name := strings.TrimPrefix(strings.TrimSpace(jsonv.String(jsonv.Path(body, "name"))), "/")
	if name == "" {
		return "", pwErr(400, "Name the command.")
	}
	if w := commandWord(name); refusedAmong(m.pwCommands().every())[w] {
		return "", notSentFromHere(w)
	}
	if sentCommands[name] {
		return name, nil
	}
	if plainCommand.MatchString(name) {
		route := "/v1/sessions/" + url.PathEscape(id) + "/palette"
		v, err := t.client.Call(ctx, http.MethodGet, route, nil, 0)
		if err != nil && isNotLive(err) && !t.relay {
			if _, err = pwcode.AttachSession(ctx, t.client, id); err == nil {
				v, err = t.client.Call(ctx, http.MethodGet, route, nil, 0)
			}
		}
		if err != nil {
			v = nil
		}
		listed := jsonv.Arr(jsonv.Path(v, "commands"))
		if refusedAmong(listed)[strings.ToLower(name)] {
			return "", notSentFromHere(name)
		}
		for _, c := range listed {
			if commandName(c) == name || slices.ContainsFunc(jsonv.Arr(jsonv.Path(c, "aliases")), func(a any) bool { return jsonv.String(a) == name }) {
				return name, nil
			}
		}
	}
	return "", pwErr(403, "The Studio does not know /"+name+" as a command of this session, so it was not sent. Type / to see the session's commands.")
}

// checkedFields are the fields of a request the Studio decides by.
var checkedFields = []string{"name", "args", "text", "mode", "permissionMode", "model", "effort", "allocation"}

// misnamedField is a field a session would take for a checked one: pw code
// reads a field's name in any case.
func misnamedField(body any) string {
	if o := jsonv.Obj(body); o != nil {
		for _, k := range o.Keys() {
			for _, f := range checkedFields {
				if k != f && strings.EqualFold(k, f) {
					return k
				}
			}
		}
	}
	return ""
}
