package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// withAgentsMD gives m the library and the AGENTS.md service.
func withAgentsMD(t *testing.T, m *Module) {
	prevKB, prevSvc := kbpkg.Default, agentsmd.Default
	if kbpkg.Default == nil || kbpkg.Default.A != m.A {
		kbpkg.Register(http.NewServeMux(), m.A)
	}
	agentsmd.Default = agentsmd.New(m.A)
	t.Cleanup(func() { kbpkg.Default, agentsmd.Default = prevKB, prevSvc })
}

// A new chat's session works in the main library, whose AGENTS.md then says
// to use the index first, with the person's own text kept; a later chat
// changes nothing more.
func TestANewChatWritesTheStudiosSection(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	withAgentsMD(t, m)
	os.MkdirAll(m.A.Cfg.KBRoot, 0o755)
	file := filepath.Join(m.A.Cfg.KBRoot, "AGENTS.md")
	own := "# House rules\n\nSay metres, not feet.\n"
	os.WriteFile(file, []byte(own), 0o640)
	os.Chmod(file, 0o640)

	startMessage(t, base, m.Conv.Create(nil, "").ID, "u1", nil)
	b, _ := os.ReadFile(file)
	got := string(b)
	for _, want := range []string{"mcp__studio__search_kb", "mcp__studio__query_corpus", "mcp__studio__read_kb_file", "knowledge base", "my files", "this folder", "The working folder is one of those libraries", "GrepSearch"} {
		if !strings.Contains(got, want) {
			t.Errorf("AGENTS.md does not say %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, own) || len(got)-len(own) > 2600 {
		t.Errorf("%d bytes added:\n%s", len(got)-len(own), got)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st.Mode())
	}
	// It is the Studio's, not one of the instructions a person applies.
	if st, err := agentsmd.Read(agentsmd.Local{Root: m.A.Cfg.KBRoot, Label: "kb"}); err != nil || !st.Exists || len(st.Instructions) != 0 {
		t.Errorf("listed: %+v %v", st, err)
	}
	startMessage(t, base, m.Conv.Create(nil, "").ID, "u1", nil)
	if b, _ := os.ReadFile(file); string(b) != got {
		t.Errorf("a second chat changed it:\n%s", b)
	}
	// An older section is replaced where it is, and the text around it stays.
	os.WriteFile(file, []byte(strings.Replace(got, "knowledge base", "kb of old", 1)+"\nAfter.\n"), 0o640)
	startMessage(t, base, m.Conv.Create(nil, "").ID, "u1", nil)
	if b, _ := os.ReadFile(file); string(b) != got+"\nAfter.\n" {
		t.Errorf("not brought up to date:\n%s", b)
	}
}

// A folder the person chose is theirs: nothing is written in it, or in the
// library for it, and the prompt says it alone.
func TestAFolderTheyChoseGetsNoSection(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	withAgentsMD(t, m)
	folder := t.TempDir()
	startMessage(t, base, m.Conv.Create(nil, "").ID, "u1", map[string]any{"pwcodeFolder": folder})
	if e, _ := os.ReadDir(folder); len(e) != 0 {
		t.Errorf("wrote %v", e)
	}
	if _, err := os.Stat(filepath.Join(m.A.Cfg.KBRoot, "AGENTS.md")); err == nil {
		t.Error("wrote in the library")
	}
	p := startAgents(f, 0)
	if !strings.Contains(p, "is not one of them: look in it with your own file tools") || strings.Contains(p, "our docs, this folder") {
		t.Errorf("prompt %s", p)
	}
	// One inside the library is the library's to search, and still unwritten.
	in := filepath.Join(m.A.Cfg.KBRoot, "reports")
	os.MkdirAll(in, 0o755)
	startMessage(t, base, m.Conv.Create(nil, "").ID, "u1", map[string]any{"pwcodeFolder": in})
	if p := startAgents(f, 1); !strings.Contains(p, "our docs, this folder, or a library") || !strings.Contains(p, "one of those libraries, or inside one") {
		t.Errorf("prompt %s", p)
	}
	if e, _ := os.ReadDir(in); len(e) != 0 {
		t.Errorf("wrote %v", e)
	}
}

// A library that cannot be written starts the session all the same.
func TestAnAgentsMDThatCannotBeWrittenStopsNothing(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, nil)
	withAgentsMD(t, m)
	elsewhere := filepath.Join(t.TempDir(), "mine.md")
	os.WriteFile(elsewhere, []byte("mine\n"), 0o644)
	os.MkdirAll(m.A.Cfg.KBRoot, 0o755)
	if err := os.Symlink(elsewhere, filepath.Join(m.A.Cfg.KBRoot, "AGENTS.md")); err != nil {
		t.Skip(err)
	}
	startMessage(t, base, m.Conv.Create(nil, "").ID, "u1", nil)
	if b, _ := os.ReadFile(elsewhere); string(b) != "mine\n" || len(f.created) != 1 {
		t.Errorf("%q, %d sessions", b, len(f.created))
	}
}

func (s *spaceFake) Library(_ *http.Request, id string) (kbpkg.Library, error) {
	for _, l := range s.libs {
		if l.ID == id {
			return l, nil
		}
	}
	return kbpkg.Library{}, apierr.New(404, "no library %s", id)
}

// Where people sign in: My library and agents/<persona> in it get the
// section as the person's own files; the main library never does.
func TestMyLibraryGetsTheSection(t *testing.T) {
	b := newWhereBed(t)
	m := b.m
	withAgentsMD(t, m)
	prev := m.A.Providers.SpaceFolder
	m.A.Providers.SpaceFolder = func(owner string) (string, string) {
		if owner == "ada" {
			return "cluster-a", "/home/ada/studio"
		}
		return "", ""
	}
	t.Cleanup(func() { m.A.Providers.SpaceFolder = prev })
	m.A.Cfg.Auth.JWKSURL = "https://platform.example/jwks"
	r := b.req(false)
	tools := m.chatTools(r, true)
	for ws, at := range map[string]string{"/home/ada/studio": "space-ada:AGENTS.md", "/home/ada/studio/agents/reviewer": "space-ada:agents/reviewer/AGENTS.md"} {
		tg := &pwTarget{site: "cluster-a", workspace: ws}
		if k := m.folderKind(r, tg); k != folderInLibrary {
			t.Errorf("%s: %v", ws, k)
		}
		m.studioSection(context.Background(), r, tg, tools)
		if s := b.space.files[at]; !strings.Contains(s, "studio_tools: begin") || !strings.Contains(s, "search_kb") {
			t.Errorf("%s: %q", at, s)
		}
	}
	n := len(b.space.files)
	m.studioSection(context.Background(), r, &pwTarget{site: "cluster-a", workspace: "/home/ada/studio", folderPicked: true}, tools)
	m.studioSection(context.Background(), r, &pwTarget{site: "cluster-a", workspace: b.kb}, tools)
	m.studioSection(context.Background(), r, &pwTarget{site: "relay:m2", workspace: "/home/ada/studio", relay: true}, tools)
	if len(b.space.files) != n || b.inMain("AGENTS.md") {
		t.Errorf("wrote more: %v", b.space.files)
	}
}

// What the working folder is to the libraries.
func TestFolderKinds(t *testing.T) {
	m, _ := pwModule(t, &fakePW{}, nil)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	root := m.A.Cfg.KBRoot
	for _, c := range []struct {
		t    pwTarget
		want folderKind
	}{
		{pwTarget{site: agentsites.Local, workspace: root}, folderInLibrary},
		{pwTarget{site: agentsites.Local, workspace: root + "/a/b", folderPicked: true}, folderInLibrary},
		{pwTarget{site: agentsites.Local, workspace: root + "-other", folderPicked: true}, folderElsewhere},
		{pwTarget{site: "cluster-a", workspace: "/home/ada/studio-chat"}, folderScratch},
		{pwTarget{site: "cluster-a", workspace: "/home/ada/studio-chat", folderPicked: true}, folderElsewhere},
		{pwTarget{site: "relay:m2", workspace: root, relay: true}, folderElsewhere},
	} {
		if got := m.folderKind(r, &c.t); got != c.want {
			t.Errorf("%+v: %v, not %v", c.t, got, c.want)
		}
	}
}

// A chat that continues a session on another machine gives it the Studio's
// instructions with its first message, once, where pw code there has the
// Studio's tools; its name stays. Elsewhere it is told nothing.
func TestAContinuedSessionIsToldOfTheTools(t *testing.T) {
	for _, c := range []struct {
		name         string
		set          bool
		created      string
		history      []any
		told, has    bool
		note, notOne string
	}{
		{"set up before it opened", true, "2026-11-20T12:00:00Z", nil, true, true, "pw code there has this Studio's tools", "its own tools"},
		{"it has used one", true, "2026-11-01T12:00:00Z", []any{map[string]any{"kind": "tool_call", "toolName": "mcp__studio__search_kb"}}, true, true, "pw code there has this Studio's tools", "its own tools"},
		{"set up after it opened", true, "2026-11-01T12:00:00Z", nil, true, false, "pw code there has this Studio's tools", "its own tools"},
		{"not set up", false, "2026-11-20T12:00:00Z", nil, false, false, "its own tools", "has this Studio's tools"},
	} {
		t.Run(c.name, func(t *testing.T) {
			local, remote := &fakePW{script: helloTurn}, &fakePW{script: helloTurn, detail: map[string]any{"history": c.history, "createdAt": c.created, "workspace": "/home/me/solver"}}
			names := (&renames{}).on(remote)
			m, base, _, _ := relayModule(t, local, remote)
			toolsReach(m, nil)
			if c.set {
				r := httptest.NewRequest("GET", "/", nil)
				m.bridgePrefs().set(userID(r), "m2", bridgePref{URL: testToolsURL})
				s := m.bridgePrefs()
				p := s.all[userID(r)]["m2"]
				p.At = "2026-11-10T00:00:00Z"
				s.all[userID(r)]["m2"] = p
			}
			_, ask, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1"}`)
			if q, _ := ask["confirm"].(string); strings.Contains(q, "pw code there has this Studio's tools") != c.set {
				t.Errorf("confirm: %s", q)
			}
			_, out, _ := send(t, http.MethodPost, base+"/api/chat/pwcode/continue", `{"machine":"m2","session":"s1","confirmed":true}`)
			id, _ := out["conversationId"].(string)
			l := m.Conv.Get(id).PWCode
			if l == nil || l.HasTools != c.has || l.Untold != c.told {
				t.Fatalf("link %+v", l)
			}
			if n := *m.Conv.Get(id).Messages[0].Content; !strings.Contains(n, c.note) || strings.Contains(n, c.notOne) {
				t.Errorf("note %q", n)
			}
			startMessage(t, base, id, "u1", nil)
			startMessage(t, base, id, "u2", nil)
			if len(remote.messages) != 2 || !strings.HasPrefix(remote.messages[0], "Start here.") || strings.Contains(remote.messages[0], relayInstructionsHead) != c.told || strings.Contains(remote.messages[1], relayInstructionsHead) {
				t.Fatalf("sent %q", remote.messages)
			}
			if c.told && (!strings.Contains(remote.messages[0], "mcp__studio__search_kb") || !strings.Contains(remote.messages[0], "knowledge base") || !strings.Contains(remote.messages[0], "If this session has no mcp__studio__ tools") || strings.Contains(remote.messages[0], "our docs, this folder")) {
				t.Errorf("told %q", remote.messages[0])
			}
			if n := names.got(); n != "" {
				t.Errorf("renamed %s", n)
			}
		})
	}
}
