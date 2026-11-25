package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// palette is a daemon's palette: pw code's own commands (where a session
// gives it), then skills.
func palette(own []string, skills ...string) map[string]any {
	cmds := []any{}
	for _, n := range own {
		cmds = append(cmds, map[string]any{"name": n, "usage": "/" + n})
	}
	for _, n := range skills {
		cmds = append(cmds, map[string]any{"name": n, "usage": "/" + n})
	}
	return map[string]any{"commands": cmds, "mcpPrompts": []any{map[string]any{"name": "mcp__harbor__tide", "mcp": true}}, "prompts": skills}
}

func gives(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
}

func slashListed(t *testing.T, base, machine, q string) (string, map[string]any) {
	t.Helper()
	code, out, _ := send(t, http.MethodGet, base+"/api/chat/pwcode/start/machines/"+machine+"/palette?"+q, "")
	if code != 200 {
		t.Fatalf("%s: %d %v", machine, code, out)
	}
	var names []string
	for _, c := range out["commands"].([]any) {
		names = append(names, c.(map[string]any)["name"].(string))
	}
	return strings.Join(names, " "), out
}

const seeded = "clear compact goal help model plan"

// Before a session, "/" has what pw code has in the folder the chat starts
// in, and pw code's own commands as a live session there lists them, less
// those the Studio does not send and the terminal's.
func TestANewChatListsWhatPWCodeHas(t *testing.T) {
	f := &fakePW{status: map[string]any{"version": "v7.1.0"}}
	var asked []url.Values
	f.routes = map[string]http.HandlerFunc{
		"GET /v1/palette": func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, r.URL.Query())
			gives(palette(nil, "loop", "moor"))(w, r)
		},
		"GET /v1/sessions": gives([]any{
			map[string]any{"id": "saved", "status": "idle"},
			map[string]any{"id": "there", "status": "live_elsewhere", "liveHost": "x"},
			map[string]any{"id": "account", "status": "idle", "liveHost": "x", "model": "claude-code/sonnet"},
			map[string]any{"id": "live", "status": "idle", "liveHost": "x", "model": "m1"},
		}),
		"GET /v1/sessions/live/palette": gives(palette([]string{"add-dir", "agents", "approve", "archive", "auth", "fork", "help", "loop", "newest", "plan", "remote-control", "rename", "resume", "rewind", "settings"}, "loop", "moor", "private")),
	}
	for _, id := range []string{"saved", "there", "account"} {
		f.routes["GET /v1/sessions/"+id+"/palette"] = func(w http.ResponseWriter, r *http.Request) { t.Errorf("asked %s", r.URL.Path) }
	}
	m, base := pwModule(t, f, nil)

	// What is known without asking.
	if got, _ := slashListed(t, base, "studio", "own=1"); got != seeded || len(asked) != 0 {
		t.Errorf("at first: %s", got)
	}
	got, out := slashListed(t, base, "studio", "workspace=/home/me/solver&model=m9")
	if got != "add-dir help newest plan loop moor" {
		t.Errorf("listed %s", got)
	}
	if mustJSON(out["prompts"]) != `["loop","moor"]` || !strings.Contains(mustJSON(out["mcpPrompts"]), "mcp__harbor__tide") {
		t.Errorf("answer %v", out)
	}
	if asked[0].Get("workspace") != "/home/me/solver" || asked[0].Get("model") != "m9" {
		t.Errorf("asked %v", asked)
	}
	// The default folder is the one the chat would start in.
	if slashListed(t, base, "studio", ""); asked[1].Get("workspace") != m.A.Cfg.KBRoot || asked[1].Has("model") {
		t.Errorf("asked %v", asked[1])
	}
	// Kept, so now known at once, and the release's session asked once.
	if got, _ := slashListed(t, base, "studio", "own=1"); got != "add-dir help newest plan" {
		t.Errorf("kept: %s", got)
	}
	if n := slices.Index(f.asked, "GET /v1/sessions/live/palette"); n < 0 || slices.Contains(f.asked[n+1:], "GET /v1/sessions/live/palette") {
		t.Errorf("asked %v", f.asked)
	}
	// Kept over a restart, with nothing of the person's own.
	b, err := os.ReadFile(filepath.Join(m.A.Cfg.IndexBase, "pwcode-commands.json"))
	if err != nil || strings.Contains(string(b), "private") || strings.Contains(string(b), "moor") {
		t.Errorf("kept %s (%v)", b, err)
	}
	m.pw.mu.Lock()
	m.pw.commands = nil
	m.pw.mu.Unlock()
	if got, _ := slashListed(t, base, "studio", "own=1"); got != "add-dir help newest plan" {
		t.Errorf("after a restart: %s", got)
	}

	// A folder not made yet has nothing of its own.
	f.mu.Lock()
	f.routes["GET /v1/palette"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("workspace") != "/" {
			w.WriteHeader(422)
			gives(map[string]any{"title": "Workspace invalid", "status": 422, "detail": "does not exist"})(w, r)
			return
		}
		gives(palette(nil, "everywhere"))(w, r)
	}
	f.mu.Unlock()
	if got, _ := slashListed(t, base, "studio", "persona=reviewer"); got != "add-dir help newest plan everywhere" {
		t.Errorf("no folder yet: %s", got)
	}

	// pw code that does not say what a folder has: its own commands only.
	f.mu.Lock()
	f.routes["GET /v1/palette"] = func(w http.ResponseWriter, r *http.Request) { http.Error(w, "gone", 502) }
	f.mu.Unlock()
	if got, out := slashListed(t, base, "studio", ""); got != "add-dir help newest plan" || mustJSON(out["prompts"]) != "[]" {
		t.Errorf("not read: %s", got)
	}
}

// A pw code upgrade is asked again what it has: until a session there
// says, the release before stands in.
func TestAnUpgradedPWCodeIsAskedAgain(t *testing.T) {
	f := &fakePW{status: map[string]any{"version": "v7.1.0"}}
	rows := []any{map[string]any{"id": "live", "status": "idle", "liveHost": "x"}}
	f.routes = map[string]http.HandlerFunc{
		"GET /v1/palette":               gives(palette(nil)),
		"GET /v1/sessions":              func(w http.ResponseWriter, r *http.Request) { gives(rows)(w, r) },
		"GET /v1/sessions/live/palette": gives(palette([]string{"help", "old"})),
	}
	m, base := pwModule(t, f, nil)
	if got, _ := slashListed(t, base, "studio", ""); got != "help old" {
		t.Fatalf("listed %s", got)
	}
	f.mu.Lock()
	f.status["version"], rows = "v7.2.0", nil
	f.routes["GET /v1/sessions/live/palette"] = gives(palette([]string{"help", "new"}))
	f.mu.Unlock()
	m.pw.mu.Lock()
	m.pw.here = nil
	m.pw.mu.Unlock()
	if got, _ := slashListed(t, base, "studio", ""); got != "help old" {
		t.Errorf("no session of it: %s", got)
	}
	f.mu.Lock()
	rows = []any{map[string]any{"id": "live", "status": "running"}}
	f.mu.Unlock()
	if got, _ := slashListed(t, base, "studio", ""); got != "help new" {
		t.Errorf("a session of it: %s", got)
	}
	// Not for the release before, whose machine has its own.
	if l, exact := m.pwCommands().of("7.1.0"); !exact || commandName(l[1]) != "old" {
		t.Errorf("7.1.0: %v", l)
	}
}

// What a chat's session lists is learned: as the session is made, and as
// its view reads it, which is told what "/" leaves out.
func TestAChatsSessionSaysWhatPWCodeHas(t *testing.T) {
	f := &fakePW{script: helloTurn, status: map[string]any{"version": "7.3.0"}}
	has := palette([]string{"help", "first", "rename"}, "moor")
	f.routes = map[string]http.HandlerFunc{"GET /v1/sessions/s1/palette": func(w http.ResponseWriter, r *http.Request) { gives(has)(w, r) }}
	m, base := pwModule(t, f, nil)
	c := m.Conv.Create(nil, "")
	post(t, base, first(c.ID, "hello", true))
	for i := 0; ; i++ {
		if _, exact := m.pwCommands().of("v7.3.0"); exact {
			break
		}
		if i == 100 {
			t.Fatal("not learned from the session made")
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	has = palette([]string{"help", "second", "rename"}, "moor")
	f.mu.Unlock()
	code, out, _ := send(t, http.MethodGet, base+"/api/code/machines/studio/sessions/s1/palette", "")
	if code != 200 || len(out["commands"].([]any)) != 4 || mustJSON(out["notListed"]) != `["agents","approve","archive","auth","fork","remote-control","rename","resume","rewind","settings"]` {
		t.Errorf("%d %v", code, out)
	}
	if got, _ := slashListed(t, base, "studio", "own=1"); got != "help second" {
		t.Errorf("learned %s", got)
	}
	// One on Claude Code lists Claude Code's.
	f.mu.Lock()
	has = palette([]string{"theirs"})
	f.mu.Unlock()
	l := *m.Conv.Get(c.ID).PWCode
	l.Model = "claude-code/sonnet"
	m.Conv.SetPWCode(c.ID, &l)
	send(t, http.MethodGet, base+"/api/code/machines/studio/sessions/s1/palette", "")
	if got, _ := slashListed(t, base, "studio", "own=1"); got != "help second" {
		t.Errorf("learned %s", got)
	}
}

// Another of the person's machines is asked through ACTIVATE as them, by
// its own release; one that cannot be read has pw code's own only.
func TestANewChatOnAnotherMachineListsWhatItHas(t *testing.T) {
	local := &fakePW{status: map[string]any{"version": "v7.1.0"}}
	remote := &fakePW{}
	local.routes = map[string]http.HandlerFunc{"GET /v1/palette": gives(palette(nil, "here-only"))}
	var asked []url.Values
	remote.routes = map[string]http.HandlerFunc{
		"GET /v1/palette": func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, r.URL.Query())
			gives(palette(nil, "there-only"))(w, r)
		},
		"GET /v1/sessions/s1/palette": gives(palette([]string{"help", "theirs"})),
	}
	m, base, plat, _ := relayModule(t, local, remote)
	canStart(plat, map[string]any{"pwVersion": "v7.2.0", "claudeInstalled": true, "claudeCodeAllowed": true})
	// Its session is saved: asking would take it up.
	if got, _ := slashListed(t, base, "m2", "workspace=/home/me/solver&persona=reviewer"); got != "claude-code "+seeded+" there-only" {
		t.Errorf("no live session: %s", got)
	}
	if len(asked) != 1 || asked[0].Get("workspace") != "/home/me/solver" {
		t.Errorf("asked %v", asked)
	}
	plat.mu.Lock()
	for _, s := range plat.sessions {
		if s["machineId"] == "m2" {
			s["liveHost"] = "other-box"
		}
	}
	plat.mu.Unlock()
	if got, _ := slashListed(t, base, "m2", "workspace=/home/me/solver"); got != "claude-code help theirs there-only" {
		t.Errorf("a live session: %s", got)
	}
	if _, exact := m.pwCommands().of("7.2.0"); !exact {
		t.Error("not kept for its release")
	}
	if _, exact := m.pwCommands().of("7.1.0"); exact {
		t.Error("kept for another's release")
	}
	if got, _ := slashListed(t, base, "studio", ""); got != "help theirs here-only" {
		t.Errorf("here: %s", got)
	}
	plat.mu.Lock()
	relayed := strings.Join(plat.relayed, "\n")
	plat.relay = nil
	plat.mu.Unlock()
	if !strings.Contains(relayed, "GET /api/code/machines/m2/palette Bearer k") {
		t.Errorf("relayed: %s", relayed)
	}
	if got, _ := slashListed(t, base, "m2", "workspace=/home/me/solver"); got != "claude-code help theirs" {
		t.Errorf("out of reach: %s", got)
	}
	if code, _, _ := send(t, http.MethodGet, base+"/api/chat/pwcode/start/machines/..%2Fx/palette", ""); code != 404 {
		t.Errorf("not a machine: %d", code)
	}
}

func TestOnlyTheLastReleasesAreKept(t *testing.T) {
	s := &pwCommandStore{file: filepath.Join(t.TempDir(), "c.json")}
	for i := range keptReleases + 3 {
		v, _ := jsonv.Decode([]byte(fmt.Sprintf(`{"commands":[{"name":"c%d"}]}`, i)))
		s.learn(fmt.Sprintf("v1.%d", i), v)
		time.Sleep(time.Millisecond)
	}
	if len(s.all) != keptReleases || s.all["1.10"] == nil || s.all["1.0"] != nil {
		t.Errorf("kept %d", len(s.all))
	}
	// Nothing for a session that lists nothing, or no release.
	before, _ := os.ReadFile(s.file)
	s.learn("v2", nil)
	v, _ := jsonv.Decode([]byte(`{"commands":[{"name":"x"}]}`))
	s.learn("", v)
	if after, _ := os.ReadFile(s.file); string(after) != string(before) {
		t.Error("kept nothing's commands")
	}
}
