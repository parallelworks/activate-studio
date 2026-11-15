package chat

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// transferWorld is a chat module over a transfers service of its own: a
// main library and a read-only one on this computer, and Downloads.
type transferWorld struct {
	t         *testing.T
	m         *Module
	s         *transfers.Service
	tools     goTools
	r         *http.Request
	kb, ro    string
	downloads string
	conv      string
}

func newTransferWorld(t *testing.T) *transferWorld {
	t.Helper()
	dir := t.TempDir()
	w := &transferWorld{t: t, kb: filepath.Join(dir, "kb"), ro: filepath.Join(dir, "ro"), downloads: filepath.Join(dir, "home", "Downloads")}
	for _, f := range []string{"reports/a.txt", "reports/b.txt", "reports/sub/c.txt", "keep.md"} {
		p := filepath.Join(w.kb, f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("text of "+f), 0o644)
	}
	os.MkdirAll(w.ro, 0o755)
	os.MkdirAll(w.downloads, 0o755)
	set := transfers.DefaultSettings()
	// studio-copy only: the run does not depend on this host's rsync.
	set.Movers.Rsync.Enabled = false
	w.s = transfers.New(transfers.Deps{
		IndexBase: filepath.Join(dir, "ix-transfers"),
		Getenv:    func(string) string { return "" },
		Libraries: func(context.Context, transfers.Viewer) []transfers.LibraryLoc {
			return []transfers.LibraryLoc{
				{ID: "main", Label: "Knowledge base", Writable: true, FS: &transfers.LocalFS{Dir: w.kb}},
				{ID: "ref", Label: "Reference", FS: &transfers.LocalFS{Dir: w.ro}},
			}
		},
		Clusters: func(context.Context, transfers.Viewer) ([]string, error) { return nil, nil },
		Settings: &transfers.MemorySettings{S: set},
		Home:     filepath.Join(dir, "home"),
	})
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": w.kb, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	w.m = New(a)
	w.m.xferOf = func() *transfers.Service { return w.s }
	w.m.xferViewer = func(*http.Request) (transfers.Viewer, error) { return transfers.Viewer{}, nil }
	w.tools = goTools{m: w.m}
	w.r = httptest.NewRequest("POST", "/api/chat/stream", nil)
	w.conv = w.m.Conv.Create(nil, "").ID
	return w
}

// hold prepares and holds a call as the loop does, returning the card,
// or the problem when nothing is held.
func (w *transferWorld) hold(tool, args, messageID, callID string) (map[string]any, string) {
	w.t.Helper()
	prep, err := w.tools.Prepare(w.r, tool, args, "")
	if err != nil {
		w.t.Fatal(err)
	}
	if prep.Problem != "" {
		return nil, prep.Problem
	}
	content := "asking"
	approval, err := w.tools.Hold(w.r, map[string]any{
		"conversationId": w.conv, "messageId": messageID, "toolCallId": callID, "tool": tool,
		"canonical": prep.Canonical, "args": prep.Args, "turn": []llm.Message{{Role: "assistant", Content: &content}}, "groups": []string{"transfers"},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	var card map[string]any
	json.Unmarshal(approval, &card)
	return card, ""
}

func (w *transferWorld) answer(messageID, callID string, allowed bool) *Answered {
	w.t.Helper()
	got, _ := w.tools.Answer(w.r, map[string]any{"conversationId": w.conv, "messageId": messageID, "toolCallId": callID, "allowed": allowed}, nil)
	return got
}

func (w *transferWorld) all() []transfers.Transfer {
	w.t.Helper()
	p, err := w.s.List(transfers.Viewer{}, transfers.ListQuery{State: "all", Limit: 50})
	if err != nil {
		w.t.Fatal(err)
	}
	return p.Transfers
}

func (w *transferWorld) wait(id string) transfers.Transfer {
	w.t.Helper()
	for i := 0; i < 500; i++ {
		t, err := w.s.Get(transfers.Viewer{}, id)
		if err == nil {
			switch t.State {
			case transfers.StateSucceeded, transfers.StateFailed, transfers.StateCancelled, transfers.StateInterrupted:
				return t
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.t.Fatalf("transfer %s did not end", id)
	return transfers.Transfer{}
}

// notes is the conversation's notes, once n of them are there (or after
// a short wait, to show no more come).
func (w *transferWorld) notes(n int) []string {
	w.t.Helper()
	var out []string
	for i := 0; i < 100; i++ {
		out = nil
		for _, msg := range w.m.Conv.Get(w.conv).Messages {
			if strings.HasPrefix(msg.ID, "note-") {
				out = append(out, deref(msg.Content))
			}
		}
		if len(out) > n {
			break
		}
		if len(out) == n && i >= 10 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return out
}

func TestTransferToolsAreHeldLocalTools(t *testing.T) {
	for _, n := range []string{"transfer_files", "cancel_transfer"} {
		if !asksFirst(n) || !localAsksFirst(n) || toolHash(n) == "" {
			t.Errorf("%s asks first, needs no platform key, and has a hash", n)
		}
	}
	for _, n := range []string{"transfer_locations", "browse_location", "transfer_status"} {
		if asksFirst(n) {
			t.Errorf("%s reads only", n)
		}
		if toolGroupOf(n) != "transfers" {
			t.Errorf("%s is in the transfers group", n)
		}
	}
	if !liveTools["transfer_status"] || !changingTools["transfer_files"] || !changingTools["cancel_transfer"] {
		t.Error("transfer_status is live; transfer_files and cancel_transfer change state")
	}
	for _, n := range []string{"copy_kb_files", "move_kb_files"} {
		for _, s := range builtinSpecs {
			if s.Name == n && !strings.Contains(s.Description, "use transfer_files") {
				t.Errorf("%s names transfer_files for anything outside the main library", n)
			}
		}
	}
}

// TestTransferCue: the group comes with words about moving data between
// places, and stays away from copies and moves inside the library, which
// the editing tools do.
func TestTransferCue(t *testing.T) {
	fires := []string{
		"copy the reports folder to my gpu-cluster",
		"transfer these files to Downloads",
		"rsync the results from the cluster",
		"sync drafts with the archive host",
		"move data/run1 to scratch on the cluster",
		"what is the status of my transfers",
		"use CONDUIT for the dataset",
		"download the results to my computer",
		"copy notes/plan.md to my desktop",
		"send the figures to another library",
		"is the data mover set up",
	}
	quiet := []string{
		"copy drafts/report.md to archive/",
		"make a copy of notes.md",
		"move these files into the old folder",
		"rename report.md to report-v1.md",
		"what does the heat transfer section say",
		"duplicate the template and move it to drafts",
		"copy the intro paragraph into the summary",
		"summarize the transfer function chapter",
	}
	for _, s := range fires {
		if !initialToolGroups(s, nil)["transfers"] {
			t.Errorf("%q should offer the transfer tools", s)
		}
	}
	for _, s := range quiet {
		if initialToolGroups(s, nil)["transfers"] {
			t.Errorf("%q should not offer the transfer tools", s)
		}
	}
	// A library copy still brings the editing tools.
	if g := initialToolGroups("copy drafts/report.md to archive/", nil); !g["editing"] {
		t.Errorf("library copy groups %v", g)
	}
}

func TestTransferPlanRefusedAsksNothing(t *testing.T) {
	w := newTransferWorld(t)
	os.WriteFile(filepath.Join(w.downloads, "a.txt"), []byte("already here"), 0o644)
	cases := map[string]string{
		"missing path":         `{"source":"library:main","paths":["reports/nope.txt"],"destination":"computer:Downloads"}`,
		"outside the root":     `{"source":"library:main","paths":["../etc"],"destination":"computer:Downloads"}`,
		"read-only":            `{"source":"library:main","paths":["keep.md"],"destination":"library:ref"}`,
		"name clash":           `{"source":"library:main","paths":["reports/a.txt"],"destination":"computer:Downloads"}`,
		"no such location":     `{"source":"library:main","paths":["keep.md"],"destination":"cluster:nowhere"}`,
		"deleteExtra not sync": `{"source":"library:main","paths":["keep.md"],"destination":"computer:Downloads","deleteExtra":true}`,
		"bad action":           `{"action":"zip","source":"library:main","paths":["keep.md"],"destination":"computer:Downloads"}`,
	}
	for what, args := range cases {
		card, problem := w.hold("transfer_files", args, "srv-1", "call_"+strings.ReplaceAll(what, " ", "_"))
		if card != nil || problem == "" || !strings.Contains(problem, "Nothing") {
			t.Errorf("%s: held %v, problem %q", what, card, problem)
		}
	}
	if n := len(w.all()); n != 0 {
		t.Errorf("a refused plan left %d transfers", n)
	}
	if hs, _ := w.tools.Held(w.r, w.conv); len(hs) != 0 {
		t.Errorf("held %+v", hs)
	}
	// The same copy with replace is a plan, and says what it replaces.
	card, problem := w.hold("transfer_files", `{"source":"library:main","paths":["reports/a.txt"],"destination":"computer:Downloads","replace":true}`, "srv-1", "call_r")
	if problem != "" {
		t.Fatal(problem)
	}
	plan := card["transfer"].(map[string]any)["plan"].(map[string]any)
	if plan["replacesCount"].(float64) != 1 {
		t.Errorf("plan %v", plan)
	}
}

func TestTransferHeldApprovedRunsOnceAndNotes(t *testing.T) {
	w := newTransferWorld(t)
	os.MkdirAll(filepath.Join(w.downloads, "from-chat"), 0o755)
	args := `{"action":"copy","source":"library:main","paths":["reports"],"destination":"computer:Downloads","folder":"from-chat"}`
	card, problem := w.hold("transfer_files", args, "srv-1", "call_0")
	if problem != "" {
		t.Fatal(problem)
	}
	if card["header"] != "Copy 3 files (67 bytes) to Downloads: from-chat?" || card["toolName"] != "transfer_files" || card["approvalKind"] != "confirm" {
		t.Errorf("card %v", card)
	}
	xfer := card["transfer"].(map[string]any)
	plan := xfer["plan"].(map[string]any)
	if plan["files"].(float64) != 3 || plan["mover"] != "studio-copy" || plan["action"] != "copy" {
		t.Errorf("plan %v", plan)
	}
	id := xfer["id"].(string)
	if xfer["link"] != "#view=transfers?transfer="+id {
		t.Errorf("link %v", xfer["link"])
	}
	// Held: the transfer is in the list, waiting, with its conversation.
	list := w.all()
	if len(list) != 1 || list[0].ID != id || list[0].State != transfers.StateAwaiting || list[0].ConversationID != w.conv || list[0].StartedBy != "assistant" {
		t.Fatalf("list %+v", list)
	}
	if _, err := os.Stat(filepath.Join(w.downloads, "from-chat", "reports")); err == nil {
		t.Fatal("copied before approval")
	}

	got := w.answer("srv-1", "call_0", true)
	if !got.OK || got.Failed || !strings.HasPrefix(got.Result, "Transfer "+id+" started: ") || !strings.Contains(got.Result, "#view=transfers?transfer="+id) || got.Summary != "started transfer "+id[:8] {
		t.Fatalf("approved %+v", got)
	}
	tr, _ := w.s.Get(transfers.Viewer{}, id)
	if tr.StartedBy != "assistant" || tr.ConversationID != w.conv || tr.State == transfers.StateAwaiting {
		t.Errorf("started %+v", tr)
	}
	if end := w.wait(id); end.State != transfers.StateSucceeded {
		t.Fatalf("ended %+v", end)
	}
	if b, _ := os.ReadFile(filepath.Join(w.downloads, "from-chat", "reports", "sub", "c.txt")); string(b) != "text of reports/sub/c.txt" {
		t.Errorf("landed %q", b)
	}
	notes := w.notes(1)
	if len(notes) != 1 || notes[0] != "Copy of 3 files to Downloads: from-chat finished: 67 bytes." {
		t.Errorf("notes %q", notes)
	}

	// The same answer again runs nothing more.
	if again := w.answer("srv-1", "call_0", true); again.OK {
		t.Errorf("answered twice: %+v", again)
	}
	// A provider that repeats its call ids: the same id in a later message
	// is a new request, held and run on its own.
	card, problem = w.hold("transfer_files", `{"source":"library:main","paths":["keep.md"],"destination":"computer:Downloads"}`, "srv-2", "call_0")
	if problem != "" {
		t.Fatal(problem)
	}
	second := card["transfer"].(map[string]any)["id"].(string)
	if second == id {
		t.Fatal("the same transfer twice")
	}
	if got := w.answer("srv-2", "call_0", true); !got.OK || got.Failed {
		t.Fatalf("second %+v", got)
	}
	w.wait(second)
	if again := w.answer("srv-2", "call_0", true); again.OK {
		t.Errorf("second answered twice: %+v", again)
	}
	if n := len(w.all()); n != 2 {
		t.Errorf("%d transfers, want 2", n)
	}
	if notes := w.notes(2); len(notes) != 2 || notes[1] != "Copy of 1 file to Downloads finished: 15 bytes." {
		t.Errorf("notes %q", notes)
	}
}

func TestTransferDeniedCancelsTheRecord(t *testing.T) {
	w := newTransferWorld(t)
	dst := filepath.Join(w.downloads, "mirror")
	os.MkdirAll(dst, 0o755)
	os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("old"), 0o644)
	card, problem := w.hold("transfer_files", `{"action":"sync","source":"library:main","paths":["reports"],"destination":"computer:Downloads","folder":"mirror","deleteExtra":true}`, "srv-1", "call_1")
	if problem != "" {
		t.Fatal(problem)
	}
	plan := card["transfer"].(map[string]any)["plan"].(map[string]any)
	if plan["deletesCount"].(float64) != 1 || plan["deletes"].([]any)[0] != "mirror/stale.txt" || !strings.Contains(card["reason"].(string), "deleted") {
		t.Errorf("card %v", card)
	}
	id := card["transfer"].(map[string]any)["id"].(string)
	d := w.answer("srv-1", "call_1", false)
	if !d.OK || d.Summary != "declined" || !strings.HasPrefix(d.Result, "The user declined this call") {
		t.Errorf("denied %+v", d)
	}
	if tr := w.wait(id); tr.State != transfers.StateCancelled {
		t.Errorf("record %+v", tr)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); err != nil {
		t.Error("deleted after a denial")
	}
	if notes := w.notes(0); len(notes) != 0 {
		t.Errorf("a declined transfer has no note: %q", notes)
	}

	// An expired hold leaves the waiting list too.
	t.Setenv("STUDIO_APPROVAL_TTL_MS", "1")
	clock := time.Now()
	w.m.held.now = func() time.Time { return clock }
	card, _ = w.hold("transfer_files", `{"source":"library:main","paths":["keep.md"],"destination":"computer:Downloads"}`, "srv-2", "call_2")
	id = card["transfer"].(map[string]any)["id"].(string)
	clock = clock.Add(time.Second)
	if e := w.answer("srv-2", "call_2", true); e.OK || !strings.HasPrefix(e.Why, "This request expired") {
		t.Errorf("expired %+v", e)
	}
	if tr := w.wait(id); tr.State != transfers.StateCancelled {
		t.Errorf("expired record %+v", tr)
	}
}

func TestCancelTransferIsHeld(t *testing.T) {
	w := newTransferWorld(t)
	card, _ := w.hold("transfer_files", `{"source":"library:main","paths":["keep.md"],"destination":"computer:Downloads"}`, "srv-1", "call_1")
	id := card["transfer"].(map[string]any)["id"].(string)
	if _, problem := w.hold("cancel_transfer", `{"id":"nope"}`, "srv-2", "call_2"); !strings.Contains(problem, "transfer_status") {
		t.Errorf("unknown id %q", problem)
	}
	c, problem := w.hold("cancel_transfer", `{"id":"`+id+`"}`, "srv-2", "call_2")
	if problem != "" || c["header"] != "Cancel the transfer Copy keep.md to Downloads?" {
		t.Fatalf("card %v %q", c, problem)
	}
	if tr, _ := w.s.Get(transfers.Viewer{}, id); tr.State != transfers.StateAwaiting {
		t.Fatal("cancelled before approval")
	}
	got := w.answer("srv-2", "call_2", true)
	if !got.OK || got.Failed || got.Summary != "cancelled "+id[:8] {
		t.Errorf("cancel %+v", got)
	}
	if tr := w.wait(id); tr.State != transfers.StateCancelled {
		t.Errorf("after %+v", tr)
	}
	if _, problem := w.hold("cancel_transfer", `{"id":"`+id+`"}`, "srv-3", "call_3"); !strings.Contains(problem, "already ended") {
		t.Errorf("ended %q", problem)
	}
}

func TestTransferReadingTools(t *testing.T) {
	w := newTransferWorld(t)
	conv := w.conv
	ctx := ToolCtx{ConversationID: &conv}
	out, _ := w.m.executeInGo(w.r, "transfer_locations", `{}`, ctx)
	if out.Failed || !strings.Contains(out.Result, "library:main, library: Knowledge base") || !strings.Contains(out.Result, "library:ref, library: Reference") || !strings.Contains(out.Result, "computer:Downloads") {
		t.Errorf("locations %+v", out)
	}
	out, _ = w.m.executeInGo(w.r, "browse_location", `{"location":"library:main","path":"reports"}`, ctx)
	if out.Failed || !strings.Contains(out.Result, "a.txt  21 bytes") || !strings.Contains(out.Result, "sub/") {
		t.Errorf("browse %+v", out)
	}
	if out, _ = w.m.executeInGo(w.r, "browse_location", `{"location":"library:main","path":"../x"}`, ctx); !out.Failed {
		t.Errorf("outside %+v", out)
	}
	if out, _ = w.m.executeInGo(w.r, "transfer_status", `{}`, ctx); out.Result != "No transfers are active." {
		t.Errorf("none %+v", out)
	}
	card, _ := w.hold("transfer_files", `{"source":"library:main","paths":["keep.md"],"destination":"computer:Downloads"}`, "srv-1", "call_1")
	id := card["transfer"].(map[string]any)["id"].(string)
	out, _ = w.m.executeInGo(w.r, "transfer_status", `{}`, ctx)
	if !strings.HasPrefix(out.Result, id+"  Copy keep.md to Downloads: awaiting-approval, files 0 of 1") {
		t.Errorf("active %+v", out)
	}
	if out, _ = w.m.executeInGo(w.r, "transfer_status", `{"id":"`+id+`"}`, ctx); out.Summary != "awaiting-approval" {
		t.Errorf("one %+v", out)
	}
	// Outside a chat they answer nothing, and the changing ones never run.
	if out, _ = w.m.executeInGo(w.r, "browse_location", `{"location":"library:main"}`, ToolCtx{}); !out.Failed {
		t.Errorf("no chat %+v", out)
	}
	if out, _ = w.m.executeInGo(w.r, "transfer_files", `{"source":"library:main","paths":["keep.md"],"destination":"computer:Downloads"}`, ctx); !out.Failed || !strings.Contains(out.Result, "Nothing ran") {
		t.Errorf("unheld %+v", out)
	}
}

// TestTransferNoteWaitsForTheReply: a note for a transfer that ends while
// a reply streams in its conversation lands after the reply.
func TestTransferNoteWaitsForTheReply(t *testing.T) {
	q := newNoteQueue()
	var wrote []string
	write := func(conv, text string) { wrote = append(wrote, conv+": "+text) }
	end := q.begin("c1")
	q.add("c1", "first", write)
	q.add("c2", "other", write)
	if len(wrote) != 1 || wrote[0] != "c2: other" {
		t.Fatalf("wrote %q", wrote)
	}
	end()
	end()
	if len(wrote) != 2 || wrote[1] != "c1: first" {
		t.Errorf("wrote %q", wrote)
	}
	q.add("c1", "second", write)
	if len(wrote) != 3 {
		t.Errorf("wrote %q", wrote)
	}

	note := transferNote(transfers.Transfer{Action: "sync", State: transfers.StateFailed, Destination: transfers.End{Label: "gpu-cluster"},
		Files: transfers.Count{Done: 1, Total: 4}, Error: "rsync: `rm -rf` said\n[click](http://x) no"})
	if note != "Sync of 4 files to gpu-cluster failed: `rsync: 'rm -rf' said [click](http://x) no`." {
		t.Errorf("note %q", note)
	}
	// A library whose index Studio does not update says when it catches up.
	note = transferNote(transfers.Transfer{Action: "copy", State: transfers.StateSucceeded, Destination: transfers.End{Label: "Reference"},
		Files: transfers.Count{Done: 2, Total: 2}, Bytes: transfers.Count{Done: 10, Total: 10},
		IndexNotes: []string{"The index of Reference shows this change after the index is rebuilt where it is built"}})
	if note != "Copy of 2 files to Reference finished: 10 bytes. The index of Reference shows this change after the index is rebuilt where it is built." {
		t.Errorf("note %q", note)
	}
}
