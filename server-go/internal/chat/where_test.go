package chat

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	kbpkg "github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// spaceFake is the person's space: the libraries All lists, and the files
// in the space, by library id and path. Every path it is handed is kept,
// so a test sees one that tried to leave the space.
type spaceFake struct {
	kbpkg.Resolver
	libs   []kbpkg.Library
	files  map[string]string
	dirs   map[string]bool
	handed []string
	// down is why the space cannot be reached, as a 409; "" when it can.
	down string
}

func (s *spaceFake) All(*http.Request) ([]kbpkg.Library, error) { return s.libs, nil }
func (s *spaceFake) hand(rels ...string) {
	s.handed = append(s.handed, rels...)
}
func (s *spaceFake) List(_ *http.Request, lib kbpkg.Library, rel string) ([]kbpkg.Entry, error) {
	var out []kbpkg.Entry
	for k := range s.dirs {
		if p, ok := strings.CutPrefix(k, lib.ID+":"); ok && path.Dir(p) == path.Clean("./"+rel) || ok && rel == "" && !strings.Contains(p, "/") {
			out = append(out, kbpkg.Entry{Name: path.Base(p), Type: "dir"})
		}
	}
	return out, nil
}
func (s *spaceFake) Mkdir(_ *http.Request, lib kbpkg.Library, rel string) error {
	s.hand(rel)
	s.dirs[lib.ID+":"+rel] = true
	return nil
}
func (s *spaceFake) Put(_ *http.Request, lib kbpkg.Library, rel string, src io.Reader, _ int64) error {
	s.hand(rel)
	if s.down != "" {
		return apierr.New(409, "%s", s.down)
	}
	b, _ := io.ReadAll(src)
	s.files[lib.ID+":"+rel] = string(b)
	return nil
}
func (s *spaceFake) Get(_ *http.Request, lib kbpkg.Library, rel string, max int64) ([]byte, error) {
	s.hand(rel)
	if s.down != "" {
		return nil, apierr.New(409, "%s", s.down)
	}
	if s.dirs[lib.ID+":"+rel] {
		return nil, apierr.New(409, "a folder")
	}
	b, ok := s.files[lib.ID+":"+rel]
	if !ok {
		return nil, apierr.New(404, "not found")
	}
	if int64(len(b)) > max {
		return nil, apierr.New(413, "too large")
	}
	return []byte(b), nil
}
func (s *spaceFake) Rename(_ *http.Request, lib kbpkg.Library, rel, name string) (string, error) {
	s.hand(rel)
	to := path.Join(path.Dir(rel), name)
	s.files[lib.ID+":"+to] = s.files[lib.ID+":"+rel]
	delete(s.files, lib.ID+":"+rel)
	return to, nil
}
func (s *spaceFake) Move(_ *http.Request, lib kbpkg.Library, rels []string, dest string) ([]kbpkg.Moved, []kbpkg.Skipped, error) {
	s.hand(append(rels, dest)...)
	var moved []kbpkg.Moved
	for _, rel := range rels {
		to := path.Join(dest, path.Base(rel))
		s.files[lib.ID+":"+to] = s.files[lib.ID+":"+rel]
		delete(s.files, lib.ID+":"+rel)
		moved = append(moved, kbpkg.Moved{From: rel, To: to})
	}
	return moved, nil, nil
}
func (s *spaceFake) Delete(*http.Request, kbpkg.Library, []string) ([]string, []kbpkg.Skipped, error) {
	return nil, nil, nil
}
func (s *spaceFake) Changed(*http.Request, kbpkg.Library, []string) {}

type whereBed struct {
	m     *Module
	kb    string
	space *spaceFake
}

// newWhereBed is a Studio with spaces: ada has a space (My library), and
// the main library is changed only by those the X-Admin header marks.
func newWhereBed(t *testing.T) *whereBed {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(filepath.Join(kb, "team"), 0o755)
	os.WriteFile(filepath.Join(kb, "team", "plan.md"), []byte("# Plan\n"), 0o644)
	os.WriteFile(filepath.Join(kb, "team", "budget.md"), []byte("# Budget\n"), 0o644)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	t.Setenv("KB_LABEL", "Team library")
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "STUDIO_NODE": "off"}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kbpkg.Register(mux, a)
	Register(mux, a)
	space := &spaceFake{files: map[string]string{"space-ada:notes/idea.md": "# Idea\n", "space-ada:notes/todo.md": "# To do\n"}, dirs: map[string]bool{"space-ada:notes": true}, libs: []kbpkg.Library{
		{ID: kbpkg.PrimaryID, Label: "Team library", Writable: true, Primary: true},
		{ID: "space-ada", Label: "My library", Space: true, Writable: true},
	}}
	prevR, prevS, prevW := kbpkg.LibraryResolver, kbpkg.SpaceFiles, kbpkg.MainWriter
	kbpkg.LibraryResolver, kbpkg.SpaceFiles = space, space
	kbpkg.MainWriter = func(r *http.Request) (bool, string) {
		return r.Header.Get("X-Admin") == "1", "On a Studio with sign-in, the main library is read only: only its administrators change it."
	}
	t.Cleanup(func() { kbpkg.LibraryResolver, kbpkg.SpaceFiles, kbpkg.MainWriter = prevR, prevS, prevW })
	return &whereBed{m: Default, kb: kb, space: space}
}

func (b *whereBed) req(admin bool) *http.Request {
	r := auth.WithUser(httptest.NewRequest("POST", "/api/chat/stream", nil), &auth.User{ID: "1", Username: "ada"})
	if admin {
		r.Header.Set("X-Admin", "1")
	}
	return r
}

func (b *whereBed) call(r *http.Request, conv, tool, args string) *Outcome {
	out, _ := b.m.executeInGo(r, tool, args, ToolCtx{ConversationID: sp(conv)})
	if out == nil {
		return &Outcome{Failed: true}
	}
	return out
}

func (b *whereBed) inMain(rel string) bool {
	_, err := os.Stat(filepath.Join(b.kb, rel))
	return err == nil
}

// The library a write goes to when the call names none: from the chat's
// own files, or the person is asked.
func TestWhereAChatsWriteGoes(t *testing.T) {
	b := newWhereBed(t)
	m := b.m

	t.Run("mostly in My library: written there", func(t *testing.T) {
		r := b.req(true)
		ctx := ToolCtx{ConversationID: sp("c-mine")}
		m.note(ctx, "space-ada", "notes/idea.md")
		m.note(ctx, "space-ada", "notes/todo.md")
		m.note(ctx, "space-ada", "notes/idea.md")
		if d := m.writeAsks(r, ctx, `{"path":"summary.md","content":"# S\n"}`); d != nil {
			t.Fatalf("asked: %+v", d)
		}
		out := b.call(r, "c-mine", "write_kb_file", `{"path":"summary.md","content":"# S\n"}`)
		if out.Failed || b.space.files["space-ada:summary.md"] != "# S\n" || b.inMain("summary.md") {
			t.Fatalf("%+v %v", out, b.space.files)
		}
		if !strings.Contains(out.Result, "Saved to [My library/summary.md](#open=file:summary.md&lib=space-ada)") || !strings.Contains(out.Result, "most of the files this chat has worked with are in My library") {
			t.Errorf("result does not name the library and why: %s", out.Result)
		}
	})

	t.Run("mostly in the main library, an administrator: written there", func(t *testing.T) {
		r := b.req(true)
		ctx := ToolCtx{ConversationID: sp("c-team-admin")}
		m.note(ctx, kbpkg.PrimaryID, "team/plan.md")
		m.note(ctx, kbpkg.PrimaryID, "team/budget.md")
		out := b.call(r, "c-team-admin", "write_kb_file", `{"path":"team/review.md","content":"# R\n"}`)
		if out.Failed || !b.inMain("team/review.md") || b.space.files["space-ada:team/review.md"] != "" {
			t.Fatalf("%+v", out)
		}
		if !strings.Contains(out.Result, "Saved to [Team library/team/review.md]") {
			t.Errorf("result: %s", out.Result)
		}
	})

	t.Run("mostly in the main library, not an administrator: asked, the main library not offered", func(t *testing.T) {
		r := b.req(false)
		ctx := ToolCtx{ConversationID: sp("c-team-person")}
		m.note(ctx, kbpkg.PrimaryID, "team/plan.md")
		m.note(ctx, kbpkg.PrimaryID, "team/budget.md")
		d := m.writeAsks(r, ctx, `{"path":"review.md","content":"# R\n"}`)
		if d == nil {
			t.Fatal("not asked")
		}
		var shown []string
		for _, o := range d.options {
			shown = append(shown, o.Shown)
			if o.Library == kbpkg.PrimaryID {
				t.Errorf("the main library offered to a person who may not write there: %+v", o)
			}
		}
		if strings.Join(shown, "|") != "My library/review.md|My library/team/review.md" || !strings.Contains(d.why, "only its administrators change") {
			t.Errorf("options %v why %q", shown, d.why)
		}
		// Run without the card (a pw code session's tools): nothing is
		// written, and the model is told to ask.
		out := b.call(r, "c-team-person", "write_kb_file", `{"path":"review.md","content":"# R\n"}`)
		if !out.Failed || !strings.Contains(out.Result, "Ask the user where to save it") || b.space.files["space-ada:review.md"] != "" || b.inMain("review.md") {
			t.Errorf("%+v", out)
		}
	})

	t.Run("mixed: asked", func(t *testing.T) {
		r := b.req(true)
		ctx := ToolCtx{ConversationID: sp("c-mixed")}
		m.note(ctx, kbpkg.PrimaryID, "team/plan.md")
		m.note(ctx, "space-ada", "notes/idea.md")
		d := m.writeAsks(r, ctx, `{"path":"both.md","content":"x"}`)
		if d == nil || !strings.Contains(d.why, "more than one library") || len(d.options) < 2 {
			t.Fatalf("%+v", d)
		}
	})

	t.Run("no context yet: asked", func(t *testing.T) {
		r := b.req(true)
		d := m.writeAsks(r, ToolCtx{ConversationID: sp("c-empty")}, `{"path":"first.md","content":"x"}`)
		if d == nil || !strings.Contains(d.why, "not worked with any files yet") {
			t.Fatalf("%+v", d)
		}
		if d.options[0].Shown != "My library/first.md" || d.options[1].Shown != "Team library/first.md" {
			t.Errorf("options %+v", d.options)
		}
	})

	t.Run("a library the person names wins over the context", func(t *testing.T) {
		r := b.req(true)
		ctx := ToolCtx{ConversationID: sp("c-named")}
		m.note(ctx, kbpkg.PrimaryID, "team/plan.md")
		m.note(ctx, kbpkg.PrimaryID, "team/budget.md")
		if d := m.writeAsks(r, ctx, `{"path":"My library/mine.md","content":"x"}`); d != nil {
			t.Fatalf("asked: %+v", d)
		}
		out := b.call(r, "c-named", "write_kb_file", `{"path":"My library/mine.md","content":"x"}`)
		if out.Failed || b.space.files["space-ada:mine.md"] != "x" || b.inMain("My library/mine.md") || b.inMain("mine.md") {
			t.Fatalf("%+v", out)
		}
		ctx2 := ToolCtx{ConversationID: sp("c-named-2")}
		m.note(ctx2, "space-ada", "notes/idea.md")
		out = b.call(r, "c-named-2", "write_kb_file", `{"path":"shared.md","content":"y","library":"Team library"}`)
		if out.Failed || !b.inMain("shared.md") || b.space.files["space-ada:shared.md"] != "" {
			t.Fatalf("%+v", out)
		}
	})

	t.Run("a person who is not an administrator naming the main library is refused", func(t *testing.T) {
		r := b.req(false)
		for _, args := range []string{`{"path":"Team library/x.md","content":"x"}`, `{"path":"x.md","content":"x","library":"kb"}`} {
			out := b.call(r, "c-refused", "write_kb_file", args)
			if !out.Failed || !strings.Contains(out.Result, "only its administrators") || b.inMain("x.md") {
				t.Errorf("%s: %+v", args, out)
			}
		}
	})
}

// A path never leaves the library it names, in a write, an edit, a move, a
// copy or a rename: the space is never handed one with "..".
func TestChatPathsStayInTheirLibrary(t *testing.T) {
	b := newWhereBed(t)
	r := b.req(true)
	for tool, args := range map[string]string{
		"write_kb_file":  `{"path":"My library/../../bob/x.md","content":"x"}`,
		"edit_kb_file":   `{"path":"My library/../bob/notes.md","append":"x"}`,
		"move_kb_files":  `{"paths":["My library/notes/idea.md"],"dest":"My library/../../bob"}`,
		"copy_kb_files":  `{"paths":["My library/../bob/notes.md"],"dest":"My library/notes"}`,
		"rename_kb_file": `{"path":"My library/notes/idea.md","name":"../../bob/idea.md"}`,
	} {
		out := b.call(r, "c-escape", tool, args)
		if out != nil && !out.Failed && !strings.Contains(out.Result, "Nothing was") && !strings.Contains(out.Result, "Skipped") && !strings.HasPrefix(out.Result, "Tool error") {
			t.Errorf("%s ran: %+v", tool, out)
		}
	}
	for _, h := range b.space.handed {
		if strings.Contains(h, "..") {
			t.Errorf("the space was handed %q", h)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(b.kb), "bob")); err == nil {
		t.Error("a folder was made outside the library")
	}
}

// Edit, move, copy and rename work in the library the path names, and
// their results name it.
func TestChatChangesInMyLibrary(t *testing.T) {
	b := newWhereBed(t)
	r := b.req(false)
	f := b.space.files
	out := b.call(r, "c-ops", "edit_kb_file", `{"path":"My library/notes/idea.md","edits":[{"find":"# Idea","replace":"# Better idea"}]}`)
	if out.Failed || f["space-ada:notes/idea.md"] != "# Better idea\n" || !strings.Contains(out.Result, "Edited My library/notes/idea.md") {
		t.Fatalf("edit %+v", out)
	}
	// A path in only one of the person's libraries needs no name.
	out = b.call(r, "c-ops", "edit_kb_file", `{"path":"notes/todo.md","append":"- call"}`)
	if out.Failed || !strings.Contains(f["space-ada:notes/todo.md"], "- call") {
		t.Fatalf("edit by path %+v", out)
	}
	out = b.call(r, "c-ops", "copy_kb_files", `{"paths":["My library/notes/idea.md"],"dest":"My library/notes"}`)
	if out.Failed || f["space-ada:notes/idea copy.md"] != "# Better idea\n" || !strings.Contains(out.Result, "Copied My library/notes/idea.md to [My library/notes/idea copy.md]") {
		t.Fatalf("copy %+v %v", out, keys(f))
	}
	out = b.call(r, "c-ops", "rename_kb_file", `{"path":"My library/notes/idea copy.md","name":"idea-2.md"}`)
	if out.Failed || f["space-ada:notes/idea-2.md"] == "" || !strings.Contains(out.Result, "Renamed My library/notes/idea copy.md to [My library/notes/idea-2.md]") {
		t.Fatalf("rename %+v %v", out, keys(f))
	}
	out = b.call(r, "c-ops", "move_kb_files", `{"paths":["My library/notes/idea-2.md"],"dest":"My library/archive"}`)
	if out.Failed || f["space-ada:archive/idea-2.md"] == "" || !b.space.dirs["space-ada:archive"] || !strings.Contains(out.Result, "Moved My library/notes/idea-2.md to [My library/archive/idea-2.md]") {
		t.Fatalf("move %+v %v", out, keys(f))
	}
	// Between libraries is transfer_files' work.
	out = b.call(r, "c-ops", "move_kb_files", `{"paths":["My library/archive/idea-2.md"],"dest":"Team library/team"}`)
	if out == nil || !strings.Contains(out.Result, "transfer_files") || f["space-ada:archive/idea-2.md"] == "" {
		t.Errorf("between libraries %+v", out)
	}
	// None of it touched the main library, which this person may not change.
	if b.inMain("notes") || b.inMain("archive") {
		t.Error("the main library changed")
	}
}

// The ask: the write waits, the person's answer runs it in the place they
// chose, and later writes in the chat go there without asking.
func TestTheAnswerToWhereIsUsedAndKept(t *testing.T) {
	b := newWhereBed(t)
	m := b.m
	r := b.req(true)
	title := "Where"
	c := m.Conv.Create(&title, "")
	ctx := ToolCtx{ConversationID: sp(c.ID)}
	raw := `{"path":"plan.md","content":"# P\n"}`
	d := m.writeAsks(r, ctx, raw)
	if d == nil {
		t.Fatal("not asked")
	}
	rec := newHold(c.ID, "msg-1", "call-1", "write_kb_file", raw, &Viewer{ID: "1", Name: "ada"}, nil, nil, m.held.now())
	rec.Where = d.options
	m.held.hold(rec)
	part := wherePart("call-1", d.options, d.why, rec.ExpiresAt)
	if pb, _ := part.MarshalJSON(); !strings.Contains(string(pb), `"where"`) || !strings.Contains(string(pb), "Where should this be saved?") {
		t.Errorf("card %s", pb)
	}
	viewer := &Viewer{ID: "1", Name: "ada"}
	// An answer with a place that was not offered writes nothing.
	bad := `{"content":"# P\n","library":"space-bob","path":"plan.md"}`
	a := answer{ConversationID: c.ID, MessageID: "msg-1", ToolCallID: "call-1", Allowed: true, Args: &bad}
	if got := m.answerHeld(r, a, viewer, nil, func(string) bool { return true }); got.OK {
		t.Fatalf("an answer not offered ran: %+v", got)
	}
	m.held.hold(rec)
	var mine whereOption
	for _, o := range d.options {
		if o.Library == "space-ada" {
			mine = o
		}
	}
	a.Args = &mine.Args
	got := m.answerHeld(r, a, viewer, nil, func(string) bool { return true })
	if !got.OK || got.Failed || b.space.files["space-ada:plan.md"] != "# P\n" || b.inMain("plan.md") || !strings.Contains(got.Result, "The user chose My library/plan.md") {
		t.Fatalf("%+v %v", got, keys(b.space.files))
	}
	// Kept: the next new file in this chat goes there unasked.
	if d := m.writeAsks(r, ctx, `{"path":"next.md","content":"x"}`); d != nil {
		t.Fatalf("asked again: %+v", d)
	}
	out := b.call(r, c.ID, "write_kb_file", `{"path":"next.md","content":"x"}`)
	if out.Failed || b.space.files["space-ada:next.md"] != "x" || !strings.Contains(out.Result, "the person chose My library") {
		t.Fatalf("%+v", out)
	}
	// And after a restart: the choice is kept beside the index.
	m.where = nil
	m.whereOnce = sync.Once{}
	if m.chosen(c.ID) != "own" {
		t.Errorf("the choice was not kept")
	}
}

// Without spaces (a Studio of one, or a person with no space), writes go
// to the main library as before and nothing is asked.
func TestWithoutSpacesWritesGoToTheMainLibrary(t *testing.T) {
	b := newWhereBed(t)
	kbpkg.LibraryResolver, kbpkg.SpaceFiles = nil, nil
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	r.Header.Set("X-Admin", "1")
	if d := b.m.writeAsks(r, ToolCtx{ConversationID: sp("c-one")}, `{"path":"a.md","content":"x"}`); d != nil {
		t.Fatalf("asked: %+v", d)
	}
	out := b.call(r, "c-one", "write_kb_file", `{"path":"a.md","content":"x"}`)
	if out.Failed || !b.inMain("a.md") || strings.Contains(out.Result, "Saved to") || !strings.Contains(out.Result, "Wrote a.md") {
		t.Fatalf("%+v", out)
	}
}

// Reads name their library at the front of a path, and count toward the
// chat's context.
func TestReadsNameTheirLibraryByPath(t *testing.T) {
	b := newWhereBed(t)
	r := b.req(false)
	got := b.m.libraryFromPath(r, map[string]any{"path": "My library/notes/idea.md"})
	if got["library"] != "space-ada" || got["path"] != "notes/idea.md" {
		t.Errorf("%v", got)
	}
	got = b.m.libraryFromPath(r, map[string]any{"paths": []any{"Team library/team/plan.md", "Team library/team/budget.md"}})
	if got["library"] != kbpkg.PrimaryID || got["paths"].([]any)[1] != "team/budget.md" {
		t.Errorf("%v", got)
	}
	got = b.m.libraryFromPath(r, map[string]any{"path": "notes/idea.md"})
	if _, ok := got["library"]; ok {
		t.Errorf("a path with no library's name: %v", got)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The loop's hold of a write whose place is not clear: the card carries
// the places, and the held record keeps them for the answer.
func TestAHeldWriteCarriesItsPlaces(t *testing.T) {
	b := newWhereBed(t)
	m := b.m
	r := b.req(true)
	title := "Held"
	c := m.Conv.Create(&title, "")
	raw := `{"path":"plan.md","content":"x"}`
	d := m.writeAsks(r, ToolCtx{ConversationID: sp(c.ID)}, raw)
	if d == nil {
		t.Fatal("not asked")
	}
	approval, err := (goTools{m: m}).Hold(r, map[string]any{
		"conversationId": c.ID, "messageId": "m1", "toolCallId": "t1", "tool": "write_kb_file", "canonical": raw,
		"args": json.RawMessage(raw), "turn": []any{}, "groups": []string{}, "where": d.options, "whereWhy": d.why,
	})
	if err != nil {
		t.Fatal(err)
	}
	var card struct {
		Header string        `json:"header"`
		Where  []whereOption `json:"where"`
	}
	json.Unmarshal(approval, &card)
	if card.Header != "Where should this be saved?" || len(card.Where) != len(d.options) || card.Where[0].Args == "" {
		t.Errorf("card %s", approval)
	}
	rec := m.held.get(c.ID, "t1")
	if rec == nil || len(rec.Where) != len(d.options) {
		t.Fatalf("held %+v", rec)
	}
	// Declining writes nothing.
	got := m.answerHeld(r, answer{ConversationID: c.ID, MessageID: "m1", ToolCallID: "t1"}, &Viewer{ID: rec.Viewer, Name: rec.ViewerName}, nil, func(string) bool { return true })
	if !got.OK || !strings.Contains(got.Result, "declined") || b.inMain("plan.md") || b.space.files["space-ada:plan.md"] != "" {
		t.Errorf("%+v", got)
	}
}

// A library the person names in their own words wins, though the model
// did not pass it on.
func TestTheLibraryThePersonNamesInWordsWins(t *testing.T) {
	b := newWhereBed(t)
	m := b.m
	say := func(text string) string {
		title := "Words"
		c := m.Conv.Create(&title, "")
		m.Conv.RecordUserTurn(c.ID, "u1", text, nil, "")
		return c.ID
	}
	raw := `{"path":"meeting-note.md","content":"x"}`
	// My library, though the chat's files are all in the shared one.
	conv := say("Save a short note to my library called meeting-note.md.")
	m.note(ToolCtx{ConversationID: sp(conv)}, kbpkg.PrimaryID, "team/plan.md")
	if d := m.writeAsks(b.req(true), ToolCtx{ConversationID: sp(conv)}, raw); d != nil {
		t.Fatalf("asked: %+v", d)
	}
	out := b.call(b.req(true), conv, "write_kb_file", raw)
	if out.Failed || b.space.files["space-ada:meeting-note.md"] != "x" || !strings.Contains(out.Result, "the user asked for My library") {
		t.Fatalf("%+v", out)
	}
	// The shared library, by an administrator.
	conv = say("Put the note in the shared library please.")
	out = b.call(b.req(true), conv, "write_kb_file", raw)
	if out.Failed || !b.inMain("meeting-note.md") {
		t.Fatalf("%+v", out)
	}
	os.Remove(filepath.Join(b.kb, "meeting-note.md"))
	// The shared library, by a person who may not change it: asked, with
	// My library offered, never refused or written there.
	conv = say("Save a note to the shared library.")
	d := m.writeAsks(b.req(false), ToolCtx{ConversationID: sp(conv)}, raw)
	if d == nil || d.options[0].Library != "space-ada" {
		t.Fatalf("%+v", d)
	}
	for _, o := range d.options {
		if o.Library == kbpkg.PrimaryID {
			t.Errorf("offered the shared library: %+v", o)
		}
	}
	// Both named: asked.
	conv = say("Copy the shared library's plan into my library as a note.")
	if d := m.writeAsks(b.req(true), ToolCtx{ConversationID: sp(conv)}, raw); d == nil {
		t.Error("not asked when both are named")
	}
}

// A space that cannot be reached is not taken for a file already there:
// the write says why it failed.
func TestAnUnreachableSpaceSaysWhy(t *testing.T) {
	b := newWhereBed(t)
	b.space.down = "This library is read with your own platform key: add your ACTIVATE API key under Settings, Accounts."
	out := b.call(b.req(true), "c-down", "write_kb_file", `{"path":"My library/meeting-note.md","content":"x"}`)
	if !out.Failed || strings.Contains(out.Result, "already exists") || !strings.Contains(out.Result, "your own platform key") {
		t.Fatalf("%+v", out)
	}
	// A folder is still told from a file.
	b.space.down = ""
	out = b.call(b.req(true), "c-down", "write_kb_file", `{"path":"My library/notes","content":"x"}`)
	if !out.Failed || !strings.Contains(out.Result, "already exists") {
		t.Fatalf("a folder: %+v", out)
	}
}
