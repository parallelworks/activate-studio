package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// spaceStandIn is a person's space: All lists it, and its writes are kept.
type spaceStandIn struct {
	kbpkg.Resolver
	kbpkg.SpaceFS
	libs  []kbpkg.Library
	files map[string]string
	dirs  []string
}

func (s *spaceStandIn) All(*http.Request) ([]kbpkg.Library, error) { return s.libs, nil }
func (s *spaceStandIn) Mkdir(_ *http.Request, _ kbpkg.Library, rel string) error {
	s.dirs = append(s.dirs, rel)
	return nil
}
func (s *spaceStandIn) Put(_ *http.Request, lib kbpkg.Library, rel string, src io.Reader, _ int64) error {
	b, _ := io.ReadAll(src)
	s.files[lib.ID+":"+rel] = string(b)
	return nil
}
func (s *spaceStandIn) Changed(*http.Request, kbpkg.Library, []string) {}

// TestSaveChat saves a chat to the main library from the route and from
// the tool, says where it would go first, and saves to the person's own
// space, never a folder shared with them, when they have one.
func TestSaveChat(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "STUDIO_NODE": "off"}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	var labeled []string
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	a.Providers.ApplyLabels = func(_ context.Context, _ *http.Request, paths, add, _ []string) error {
		labeled = append(labeled, strings.Join(paths, ",")+"="+strings.Join(add, ","))
		return nil
	}
	m := Default
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	title := "Pump curves"
	c := m.Conv.Create(&title, "")
	m.Conv.RecordUserTurn(c.ID, "u1", "Which pump fits?", nil, "")
	m.Conv.RecordAssistantTurn(c.ID, conversations.Message{ID: "a1", Role: "assistant", Content: sp("The P-200."), ParentID: sp("u1"), Timestamp: m.Conv.Now()})
	rel := "chat-sessions/" + conversations.TranscriptName(c)
	if _, err := os.Stat(filepath.Join(kb, "chat-sessions")); !os.IsNotExist(err) {
		t.Fatalf("a reply wrote chat-sessions: %v", err)
	}

	res, err := http.Get(srv.URL + "/api/chat/conversations/" + c.ID + "/save")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	json.NewDecoder(res.Body).Decode(&d)
	res.Body.Close()
	if d["library"] != kbpkg.PrimaryID || d["path"] != rel || d["space"] != false || d["shared"] != false {
		t.Errorf("where it would go %v", d)
	}
	if _, err := os.Stat(filepath.Join(kb, "chat-sessions")); !os.IsNotExist(err) {
		t.Fatalf("asking where wrote the file: %v", err)
	}

	res, err = http.Post(srv.URL+"/api/chat/conversations/"+c.ID+"/save", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	b, err := os.ReadFile(filepath.Join(kb, rel))
	if res.StatusCode != 200 || err != nil || !strings.Contains(string(b), "## Assistant\n\nThe P-200.") {
		t.Fatalf("saved %d %v %q", res.StatusCode, err, b)
	}
	if strings.Join(labeled, " ") != "chat-sessions=chat-session" {
		t.Errorf("labels %v", labeled)
	}
	if res, _ := http.Post(srv.URL+"/api/chat/conversations/nope/save", "application/json", nil); res.StatusCode != 404 {
		t.Errorf("an unknown chat: %d", res.StatusCode)
	}

	// The tool replaces the copy with the chat as it stands now.
	m.Conv.RecordUserTurn(c.ID, "u2", "Save this chat please.", sp("a1"), "")
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	out, ok := m.executeInGo(r, "save_chat_to_library", `{}`, ToolCtx{ConversationID: sp(c.ID)})
	b, _ = os.ReadFile(filepath.Join(kb, rel))
	if !ok || out.Failed || !strings.Contains(out.Result, "as "+rel) || !strings.Contains(string(b), "Save this chat please.") {
		t.Errorf("tool %+v %q", out, b)
	}
	if out, _ := m.executeInGo(r, "save_chat_to_library", `{}`, ToolCtx{}); !strings.Contains(out.Result, "no conversation to save") {
		t.Errorf("without a conversation %+v", out)
	}

	// A signed-in person with a space of their own: it goes there, and the
	// main library is left as it was.
	stand := &spaceStandIn{files: map[string]string{}, libs: []kbpkg.Library{
		{ID: kbpkg.PrimaryID, Label: "kb", Writable: true, Primary: true},
		{ID: "from-bob", Label: "Bob's notes", Space: true, Writable: true, SharedFrom: "bob"},
		{ID: "mine", Label: "My space", Space: true, Writable: true},
	}}
	prevR, prevS := kbpkg.LibraryResolver, kbpkg.SpaceFiles
	kbpkg.LibraryResolver, kbpkg.SpaceFiles = stand, stand
	t.Cleanup(func() { kbpkg.LibraryResolver, kbpkg.SpaceFiles = prevR, prevS })
	os.Remove(filepath.Join(kb, rel))
	alice := auth.WithUser(httptest.NewRequest("POST", "/api/chat/stream", nil), &auth.User{ID: "1", Username: "alice"})
	out, _ = m.executeInGo(alice, "save_chat_to_library", `{}`, ToolCtx{ConversationID: sp(c.ID)})
	if got := stand.files["mine:"+rel]; !strings.Contains(got, "Save this chat please.") || len(stand.files) != 1 || !strings.Contains(out.Result, "My space") || !strings.Contains(out.Result, "only they can read") {
		t.Errorf("space %+v %v", out, stand.files)
	}
	if len(stand.dirs) != 1 || stand.dirs[0] != "chat-sessions" {
		t.Errorf("folders made %v", stand.dirs)
	}
	if _, err := os.Stat(filepath.Join(kb, rel)); !os.IsNotExist(err) {
		t.Errorf("a person with a space also saved to the main library: %v", err)
	}
}
