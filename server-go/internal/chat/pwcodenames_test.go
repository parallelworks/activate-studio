package chat

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// renames is f's rename route: the names it was given; down refuses them.
type renames struct {
	mu    sync.Mutex
	names []string
	down  bool
}

func (x *renames) on(f *fakePW) *renames {
	if f.routes == nil {
		f.routes = map[string]http.HandlerFunc{}
	}
	f.routes["POST /v1/sessions/s1/rename"] = func(w http.ResponseWriter, r *http.Request) {
		x.mu.Lock()
		defer x.mu.Unlock()
		if x.down {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"detail":"The machine is not answering."}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		var in struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(b, &in)
		x.names = append(x.names, in.Name)
		w.WriteHeader(http.StatusNoContent)
	}
	return x
}

func (x *renames) got() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return strings.Join(x.names, "|")
}

func titleOf(m *Module, id string) string { return deref(m.Conv.Get(id).Title) }

// A chat's title follows its session's name, on another machine as the
// platform lists it and on this one as the daemon does.
func TestAChatsTitleFollowsItsSessionsName(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, base, plat, _ := relayModule(t, local, remote)
	far := relayedChat(t, base)
	if got := titleOf(m, far); got != "Tune the solver" {
		t.Fatalf("title %q", got)
	}
	updated := m.Conv.Get(far).UpdatedAt
	// Renamed in PW Code on the web, or a terminal.
	plat.mu.Lock()
	for _, s := range plat.sessions {
		if s["machineId"] == "m2" {
			s["name"] = "Solver tuning, round 2"
		}
	}
	plat.mu.Unlock()
	code, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/names", "{}")
	if code != 200 || out["changed"] != float64(1) || titleOf(m, far) != "Solver tuning, round 2" {
		t.Errorf("%d %v, title %q", code, out, titleOf(m, far))
	}
	// Nothing changed there: nothing here, and the chat keeps its place.
	if _, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/names", "{}"); out["changed"] != float64(0) {
		t.Errorf("again: %v", out)
	}
	if got := m.Conv.Get(far).UpdatedAt; got != updated {
		t.Errorf("moved in the list: %s, was %s", got, updated)
	}

	title := "Why does the build fail?"
	near := m.Conv.CreateWith(&title, "", []conversations.Message{{ID: "u1", Role: "user", Content: &title}},
		&conversations.PWCodeLink{Site: "local", Session: "s9", CreatedAt: m.Conv.Now()})
	local.routes = map[string]http.HandlerFunc{"GET /v1/sessions": func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"s9","autoName":"Broken build"}]`))
	}}
	send(t, http.MethodGet, base+"/api/code/sessions", "")
	if got := titleOf(m, near.ID); got != "Broken build" {
		t.Errorf("a new chat keeps %q", got)
	}
}

// Renaming a chat renames its session, where it runs.
func TestARenamedChatRenamesItsSession(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, base, plat, _ := relayModule(t, local, remote)
	x := (&renames{}).on(remote)
	far := relayedChat(t, base)
	code, out, _ := send(t, http.MethodPatch, base+"/api/chat/conversations/"+far, `{"title":"Mesh study"}`)
	if code != 200 || out["title"] != "Mesh study" || out["sessionKeeps"] != nil || x.got() != "Mesh study" {
		t.Fatalf("%d %v, session given %q", code, out, x.got())
	}
	plat.mu.Lock()
	sent := ""
	for _, r := range plat.relayed {
		if strings.Contains(r, "/rename") {
			sent = r
		}
	}
	plat.mu.Unlock()
	if sent != "POST /api/code/machines/m2/sessions/s1/rename Bearer k" {
		t.Errorf("sent as %q", sent)
	}
	// The platform's listing still has the old name for a moment: the chat
	// keeps the new one, since nothing changed on the session.
	send(t, http.MethodPost, base+"/api/chat/pwcode/names", "{}")
	if got := titleOf(m, far); got != "Mesh study" {
		t.Errorf("title %q", got)
	}

	// On this machine.
	y := (&renames{}).on(local)
	title := "hello"
	near := m.Conv.CreateWith(&title, "", nil, &conversations.PWCodeLink{Site: "local", Session: "s1", CreatedAt: m.Conv.Now()})
	if code, out, _ := send(t, http.MethodPatch, base+"/api/chat/conversations/"+near.ID, `{"title":"Budget"}`); code != 200 || out["sessionKeeps"] != nil || y.got() != "Budget" {
		t.Errorf("%d %v, session given %q", code, out, y.got())
	}
}

// Where a rename cannot reach the session, the chat keeps its new title,
// the person is told why, and it goes along with their next message. A name
// changed on the session meanwhile wins.
func TestARenameThatCannotReachTheSessionWaits(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{script: helloTurn}
	m, base, plat, _ := relayModule(t, local, remote)
	x := (&renames{down: true}).on(remote)
	far := relayedChat(t, base)
	_, out, _ := send(t, http.MethodPatch, base+"/api/chat/conversations/"+far, `{"title":"Mesh study"}`)
	if why, _ := out["sessionKeeps"].(string); !strings.Contains(why, "renamed with your next message") || !strings.Contains(why, "not answering") {
		t.Errorf("said %v", out)
	}
	send(t, http.MethodPost, base+"/api/chat/pwcode/names", "{}")
	if got := titleOf(m, far); got != "Mesh study" {
		t.Fatalf("title %q", got)
	}
	x.mu.Lock()
	x.down = false
	x.mu.Unlock()
	if code, out, _ := send(t, http.MethodPost, base+"/api/code/machines/m2/sessions/s1/messages", `{"text":"go on"}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitFor(t, func() bool { return x.got() == "Mesh study" && m.Conv.Get(far).SessionName.Rename == "" })
	waitFor(t, func() bool { return !m.following(far) })

	// One still waiting gives way to a name given on the session since.
	m.Conv.TitleGiven(far, "s1", "Mesh study 2", false)
	plat.mu.Lock()
	for _, s := range plat.sessions {
		if s["machineId"] == "m2" {
			s["name"] = "Named in a terminal"
		}
	}
	plat.mu.Unlock()
	send(t, http.MethodPost, base+"/api/chat/pwcode/names", "{}")
	if got, n := titleOf(m, far), m.Conv.Get(far).SessionName; got != "Named in a terminal" || n.Rename != "" {
		t.Errorf("title %q, %+v", got, n)
	}
}

// A chat named before its session was made names the session; the title
// the page gives a new chat itself does not.
func TestAChatsOwnNameGoesToItsNewSession(t *testing.T) {
	for _, c := range []struct{ title, want string }{{"Budget review", "Budget review"}, {"What is the budget?", ""}, {"What is the...", ""}} {
		f := &fakePW{script: helloTurn}
		x := (&renames{}).on(f)
		m, base := pwModule(t, f, nil)
		title := c.title
		chat := m.Conv.Create(&title, "")
		post(t, base, map[string]any{"conversationId": chat.ID, "userMessageId": "u1",
			"messages": []any{map[string]any{"role": "user", "content": "What is the budget?"}}})
		if c.want != "" {
			waitFor(t, func() bool { return x.got() == c.want })
		} else if x.got() != "" {
			t.Errorf("%q renamed the session %q", c.title, x.got())
		}
	}
}

// A session whose first message also carried the Studio's instructions is
// named after the person's own words.
func TestASessionToldTheStudiosInstructionsIsNamedAfterThePerson(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{script: helloTurn}
	m, _, _, _ := relayModule(t, local, remote)
	x := (&renames{}).on(remote)
	v, ok, err := codeStart(t, m, "m2", jsonv.NewObject("workspace", "/home/me/solver", "permissionMode", "read-only", "prompt", "Find the newest spreadsheet."))
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	waitFor(t, func() bool { return x.got() == "Find the newest spreadsheet." })
	c := chatOf(m, jsonv.String(jsonv.Path(v, "id")))
	if deref(c.Title) != "Find the newest spreadsheet." {
		t.Errorf("title %q", deref(c.Title))
	}
}

// Only the person whose chat it is renames its session.
func TestAnotherPersonsRenameStaysHere(t *testing.T) {
	local, remote := &fakePW{}, &fakePW{}
	m, _, _, _ := relayModule(t, local, remote)
	x := (&renames{}).on(local)
	title := "Tune the solver"
	c := m.Conv.CreateWith(&title, "ada", nil, &conversations.PWCodeLink{Site: "local", Session: "s1", CreatedAt: m.Conv.Now()})
	m.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	m.A.Cfg.FrontAuth = true
	m.Convs.AuthEnabled = func() bool { return true }
	m.Convs.SharedHistory = func() bool { return true }
	renamed := m.Conv.Rename(c.ID, sp("Other"))
	if why := m.chatRenamed(asUser("bob"), renamed); !strings.Contains(why, "only the person whose chat this is") {
		t.Errorf("said %q", why)
	}
	if x.got() != "" {
		t.Errorf("bob renamed ada's session %q", x.got())
	}
}
