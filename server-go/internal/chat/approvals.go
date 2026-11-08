package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
)

// approvals.ts: a call to a tool that changes something waits here until
// the person who asked answers it. An answer runs only the call its record
// holds, once: the record is taken out before the call runs. Records are
// kept beside the index (held-tool-calls.json) and expire.

// HeldCall is one held call, its keys in approvals.ts's order.
type HeldCall struct {
	ConversationID string            `json:"conversationId"`
	MessageID      string            `json:"messageId"`
	ToolCallID     string            `json:"toolCallId"`
	Tool           string            `json:"tool"`
	Args           string            `json:"args"`
	Viewer         string            `json:"viewer"`
	ViewerName     string            `json:"viewerName"`
	Turn           []json.RawMessage `json:"turn"`
	Groups         []string          `json:"groups"`
	Hash           string            `json:"hash"`
	Instead        json.RawMessage   `json:"instead"`
	HeldAt         string            `json:"heldAt"`
	ExpiresAt      string            `json:"expiresAt"`
}

func approvalTTL() time.Duration {
	if n, err := strconv.ParseFloat(os.Getenv("STUDIO_APPROVAL_TTL_MS"), 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Millisecond))
	}
	return 30 * time.Minute
}

type heldStore struct {
	file string
	mu   sync.Mutex
	recs []*HeldCall
	read bool
}

func newHeldStore(indexBase string) *heldStore {
	return &heldStore{file: filepath.Join(indexBase, "held-tool-calls.json")}
}

func (s *heldStore) load() {
	if s.read {
		return
	}
	s.read = true
	b, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	var recs []*HeldCall
	if json.Unmarshal(b, &recs) == nil {
		for _, r := range recs {
			s.put(r)
		}
	}
}

func (s *heldStore) find(conv, call string) int {
	for i, r := range s.recs {
		if r.ConversationID == conv && r.ToolCallID == call {
			return i
		}
	}
	return -1
}

// put is Map.set: a record for the same call takes the old one's place.
func (s *heldStore) put(r *HeldCall) {
	if i := s.find(r.ConversationID, r.ToolCallID); i >= 0 {
		s.recs[i] = r
		return
	}
	s.recs = append(s.recs, r)
}

// persist writes the store whole, dropping expired records.
func (s *heldStore) persist() {
	now := time.Now()
	kept := s.recs[:0]
	for _, r := range s.recs {
		if t, ok := parseISO(r.ExpiresAt); ok && t.Before(now) {
			continue
		}
		kept = append(kept, r)
	}
	s.recs = kept
	b, _ := jsonobj.Marshal(orEmptyHeld(s.recs))
	tmp := s.file + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.file)
	}
}

func orEmptyHeld(v []*HeldCall) []*HeldCall {
	if v == nil {
		return []*HeldCall{}
	}
	return v
}

func (s *heldStore) hold(r *HeldCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.put(r)
	s.persist()
}

func (s *heldStore) get(conv, call string) *HeldCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if i := s.find(conv, call); i >= 0 {
		return s.recs[i]
	}
	return nil
}

func (s *heldStore) in(conv string) []*HeldCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var out []*HeldCall
	for _, r := range s.recs {
		if r.ConversationID == conv {
			out = append(out, r)
		}
	}
	return out
}

func (s *heldStore) release(r *HeldCall) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	i := s.find(r.ConversationID, r.ToolCallID)
	if i < 0 {
		return false
	}
	s.recs = append(s.recs[:i], s.recs[i+1:]...)
	s.persist()
	return true
}

// changesAvailable is whether the tools that change something are offered
// to this request, and if not, why.
func (m *Module) changesAvailable(viewer *Viewer, key *string, owner, username *string) Changes {
	if !m.Plat.PlatformGateway() {
		return Changes{Why: "no platform is connected"}
	}
	if viewer == nil {
		return Changes{Why: "nobody in this chat can approve a change"}
	}
	if key == nil || *key == "" {
		if m.A.Cfg.AuthEnabled() {
			return Changes{Why: "changes run only with your own platform key, which you can add under Settings, Model access"}
		}
		return Changes{Why: "no platform credential is configured"}
	}
	if m.A.Cfg.AuthEnabled() && owner != nil && *owner != "" && deref(owner) != deref(username) {
		return Changes{Why: "this conversation belongs to someone else"}
	}
	return Changes{OK: true}
}

// newHold is the record for a new hold.
func newHold(conv, messageID, toolCallID, tool, canonical string, v *Viewer, turn []json.RawMessage, groups []string, now time.Time) *HeldCall {
	instead := jsonobj.New()
	for _, n := range insteadOf(tool) {
		instead.SetValue(n, toolHash(n))
	}
	ib, _ := jsonobj.Marshal(instead)
	if turn == nil {
		turn = []json.RawMessage{}
	}
	if groups == nil {
		groups = []string{}
	}
	return &HeldCall{
		ConversationID: conv, MessageID: messageID, ToolCallID: toolCallID, Tool: tool, Args: canonical,
		Viewer: v.ID, ViewerName: v.Name, Turn: turn, Groups: groups,
		Hash: toolHash(tool), Instead: ib,
		HeldAt: isoMS(now), ExpiresAt: isoMS(now.Add(approvalTTL())),
	}
}

func isoMS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func parseISO(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

// answer is approvals.ts's Answer.
type answer struct {
	ConversationID string  `json:"conversationId"`
	MessageID      string  `json:"messageId"`
	ToolCallID     string  `json:"toolCallId"`
	Allowed        bool    `json:"allowed"`
	Tool           string  `json:"tool"`
	Args           *string `json:"args"`
	Feedback       string  `json:"feedback"`
}

// takeAnswer checks an answer against its held call and, when it may
// proceed, takes the record out.
func (m *Module) takeAnswer(a answer, viewer *Viewer, now time.Time) (*HeldCall, string, string) {
	rec := m.held.get(a.ConversationID, a.ToolCallID)
	if rec == nil {
		return nil, "", "This request is no longer waiting for an answer; nothing ran. Ask again to run it."
	}
	if viewer == nil || viewer.ID != rec.Viewer {
		return nil, "", "Only " + rec.ViewerName + ", who asked for this, can answer it; nothing ran."
	}
	if t, ok := parseISO(rec.ExpiresAt); !ok || !t.After(now) {
		m.held.release(rec)
		return nil, "", "This request expired before it was answered; nothing ran. Ask again to run it."
	}
	if a.MessageID != rec.MessageID {
		return nil, "", "The answer does not belong to this request; nothing ran."
	}
	if toolHash(rec.Tool) != rec.Hash {
		m.held.release(rec)
		return nil, "", "The " + rec.Tool + " tool changed after this was asked; nothing ran. Ask again to run it."
	}
	if a.Args != nil {
		held := map[string]json.RawMessage{}
		_ = json.Unmarshal([]byte(rec.Args), &held)
		var sent any
		var filled any
		if json.Unmarshal([]byte(*a.Args), &sent) == nil {
			filled = sent
			if _, ok := sent.(map[string]any); ok {
				merged, _ := jsonobj.Parse([]byte(rec.Args))
				if merged == nil {
					merged = jsonobj.New()
				}
				sb, _ := jsonobj.Parse([]byte(*a.Args))
				for _, k := range sb.Keys() {
					merged.Set(k, sb.Get(k))
				}
				mb, _ := jsonobj.Marshal(merged)
				filled = json.RawMessage(mb)
			}
		}
		user := ""
		if u, ok := jsonobj.String(held["user"]); ok {
			user = u
		}
		var canonical, problem string
		if filled == nil {
			problem = "x"
		} else {
			_, canonical, problem = prepareArgs(rec.Tool, filled, user)
		}
		if problem != "" || canonical != rec.Args {
			return nil, "", "The approval was for other arguments than the call that was asked for; nothing ran."
		}
	}
	tool := rec.Tool
	if a.Allowed && a.Tool != "" && a.Tool != rec.Tool {
		instead, _ := jsonobj.Parse(rec.Instead)
		if instead == nil || !instead.Has(a.Tool) {
			return nil, "", a.Tool + " cannot be run in place of " + rec.Tool + "; nothing ran."
		}
		if h, _ := jsonobj.String(instead.Get(a.Tool)); toolHash(a.Tool) != h {
			m.held.release(rec)
			return nil, "", "The " + a.Tool + " tool changed after this was asked; nothing ran. Ask again to run it."
		}
		tool = a.Tool
	}
	if !m.held.release(rec) {
		return nil, "", "This request was already answered; nothing more ran."
	}
	return rec, tool, ""
}

var doneVerb = map[string]string{
	"start_cluster": "started", "resume_cluster": "resumed", "stop_cluster": "stopped", "destroy_cluster": "destroyed", "cancel_workflow_run": "cancelled",
}

// answerHeld settles an answer: refuses it, records a denial, or runs the
// held call (or the alternative chosen) with the viewer's key.
func (m *Module) answerHeld(ctx context.Context, a answer, viewer *Viewer, key *string, enabled func(string) bool) *Answered {
	if pending := m.held.get(a.ConversationID, a.ToolCallID); a.Allowed && pending != nil && viewer != nil && viewer.ID == pending.Viewer {
		tool := pending.Tool
		if a.Tool != "" && a.Tool != pending.Tool {
			tool = a.Tool
		}
		if key == nil || *key == "" {
			return &Answered{Why: "Running this needs your own platform key, which you can add under Settings, Model access; nothing ran."}
		}
		if !enabled(tool) {
			return &Answered{Why: tool + " is turned off for this deployment; nothing ran."}
		}
	}
	rec, tool, why := m.takeAnswer(a, viewer, time.Now())
	if rec == nil {
		return &Answered{Why: why}
	}
	hr := heldRec(rec)
	if !a.Allowed {
		said := ""
		if f := jsTrim(a.Feedback); f != "" {
			said = " They said: " + f
		}
		return &Answered{OK: true, Rec: hr, Tool: rec.Tool, Result: "The user declined this call, so it did not run. Do not call it again unless the user asks." + said, Summary: "declined"}
	}
	args, err := jsonobj.Parse([]byte(rec.Args))
	if err != nil || args == nil {
		args = jsonobj.New()
	}
	out, failed := m.runPlatformTool(ctx, tool, args, *key)
	target := ""
	for _, k := range []string{"clusterName", "slug"} {
		if args.Has(k) && !jsonobj.IsNullish(args.Get(k)) {
			target = jsText(args.Get(k))
			break
		}
	}
	swapped := tool != rec.Tool
	result := out
	if swapped {
		result = "The user chose " + tool + " in place of " + rec.Tool + ", so " + tool + " ran. " + out
	}
	var summary string
	if failed {
		summary = "failed: " + jsval.Slice(out, 160)
	} else {
		verb := doneVerb[tool]
		if verb == "" {
			verb = "ran"
		}
		summary = verb
		if target != "" {
			summary += " " + target
		}
		if swapped {
			summary += " (in place of " + rec.Tool + ")"
		}
	}
	return &Answered{OK: true, Rec: hr, Tool: tool, Result: result, Summary: summary, Failed: failed}
}

func heldRec(r *HeldCall) *HeldRec {
	turn := []llm.Message{}
	for _, raw := range r.Turn {
		var msg llm.Message
		if json.Unmarshal(raw, &msg) == nil {
			turn = append(turn, msg)
		}
	}
	return &HeldRec{ConversationID: r.ConversationID, MessageID: r.MessageID, ToolCallID: r.ToolCallID, Tool: r.Tool, Turn: turn, Groups: r.Groups}
}

func holders(recs []*HeldCall) []conversations.Holder {
	out := make([]conversations.Holder, len(recs))
	for i, r := range recs {
		out[i] = conversations.Holder{ToolCallID: r.ToolCallID, Viewer: r.Viewer, ViewerName: r.ViewerName}
	}
	return out
}

/* ---- attachments in the prompt ---- */

const attachContextCap = 6000

// attachmentContext is attachments.ts's: each attached file's text for the
// prompt, up to eight files.
func (m *Module) attachmentContext(ids []string) string {
	if len(ids) > 8 {
		ids = ids[:8]
	}
	var blocks []string
	for _, id := range ids {
		rel, err := decodeAttachID(id)
		if err != nil {
			continue
		}
		fc, err := readFileContent(m, rel)
		if err != nil || fc == nil {
			blocks = append(blocks, "[Attachment not found: "+id+"]")
			continue
		}
		head := "[Attached file: " + filepath.Base(rel) + "; stored in the knowledge base at " + rel + "]"
		content, _ := fc.Get("content")
		text, _ := content.(string)
		if jsTrim(text) != "" {
			if jsval.Len(text) > attachContextCap {
				text = jsval.Slice(text, attachContextCap) + "\n…(truncated; read_original can fetch more)"
			}
			blocks = append(blocks, head+"\n"+text)
		} else {
			blocks = append(blocks, head+"\n(No extracted text yet; use read_kb_file, read_original or search_kb on that path if needed.)")
		}
	}
	return strings.Join(blocks, "\n\n")
}

func readFileContent(m *Module, rel string) (*kb.Obj, error) {
	return kb.ReadFileContent(rel, m.A.Cfg.KBRoot, kb.RootsFrom(m.A.Cfg).ExtractCache())
}
