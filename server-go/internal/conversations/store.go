// Package conversations is server/src/conversations.ts: the server-side
// conversation store, one JSON file beside the index
// (INDEX_BASE/conversations.json, JSON.stringify(all, null, 1), written
// through a rename), the markdown export of each conversation into the
// knowledge base (chat-sessions/), the steering queue of a running reply,
// and the seven conversation routes.
//
// The file is reread when its size, modification time or inode changes,
// so a note another process appends (Node's run watcher, while it runs)
// is seen, and the Node store rereads it the same way.
package conversations

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

const maxConversations = 500

// ExportDir is the knowledge-base folder the transcripts go to.
const ExportDir = "chat-sessions"

// NoReplyNote is NO_REPLY_NOTE.
const NoReplyNote = "No reply was saved for this message. It was stopped or interrupted, for example by a restart of the Studio, before the assistant answered."

// Hooks are what the store asks of other modules.
type Hooks struct {
	// Label is applyTagsCore(paths, add, []) on the main folder.
	Label func(ctx context.Context, paths, add []string) error
	// Reindex is reindexForDir(rel).
	Reindex func(ctx context.Context, rel string) error
	// Log reports a failed export reindex.
	Log func(msg string)
}

// Store is the module's state.
type Store struct {
	file   string
	kbRoot string
	hooks  Hooks
	now    func() time.Time

	mu    sync.Mutex
	all   []*Conversation
	stamp string

	writeMu sync.Mutex

	exportDirLabeled bool
	ownerLabeled     map[string]bool
	indexTimer       *time.Timer

	answering map[string]int
	steers    map[string][]Steer

	// exports counts the chat exports being written (exportSoon).
	exports sync.WaitGroup
}

// exportSoon writes a conversation's export to the library without
// holding up the caller.
func (s *Store) exportSoon(c *Conversation) {
	s.exports.Add(1)
	go func() {
		defer s.exports.Done()
		s.exportToKB(c)
	}()
}

// Flush waits for the exports being written, and stops a pending reindex
// of the export folder; a test calls it before its folder is removed.
func (s *Store) Flush() {
	s.exports.Wait()
	s.mu.Lock()
	if s.indexTimer != nil {
		s.indexTimer.Stop()
	}
	s.mu.Unlock()
}

// Steer is a message sent while a reply runs.
type Steer struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// New is the store of indexBase's conversations.json.
func New(indexBase, kbRoot string, hooks Hooks) *Store {
	return &Store{file: filepath.Join(indexBase, "conversations.json"), kbRoot: kbRoot, hooks: hooks, now: time.Now,
		ownerLabeled: map[string]bool{}, answering: map[string]int{}, steers: map[string][]Steer{}}
}

// Now is the store's clock as an ISO string.
func (s *Store) Now() string { return iso(s.now()) }

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func stampOf(file string) string {
	st, err := os.Stat(file)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d:%s", st.Size(), st.ModTime().UnixNano(), inode(st))
}

// load is load(): the cache, reread when the file changed. Call with mu.
func (s *Store) load() []*Conversation {
	st := stampOf(s.file)
	if s.all != nil && st == s.stamp {
		return s.all
	}
	b, err := os.ReadFile(s.file)
	var all []*Conversation
	if err == nil && json.Unmarshal(b, &all) == nil {
		s.all = all
	} else if s.all == nil || err == nil {
		// an unreadable file is no conversations, as JSON.parse failing
		// is; a file gone after a read keeps what is in memory
		if s.all == nil {
			s.all = []*Conversation{}
		}
	}
	s.stamp = st
	return s.all
}

// persist writes the snapshot through a rename. Call with mu.
func (s *Store) persist() {
	b, err := jsonobj.Marshal(s.all)
	if err != nil {
		return
	}
	if len(s.all) == 0 {
		b = []byte("[]")
	} else if b, err = jsonobj.IndentJS(b, " "); err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tmp := fmt.Sprintf("%s.%d.tmp", s.file, os.Getpid())
	if os.WriteFile(tmp, b, 0o644) != nil {
		return
	}
	// The stamp is the written file's, taken before the rename: one taken
	// after could be a write of Node's that landed in between, which would
	// then never be read.
	st := stampOf(tmp)
	if os.Rename(tmp, s.file) == nil {
		s.stamp = st
	}
}

func (s *Store) find(id string) *Conversation {
	for _, c := range s.load() {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// Get is getConversation(id): a copy, or nil.
func (s *Store) Get(id string) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.find(id)
	if c == nil {
		return nil
	}
	return c.Clone()
}

// List is every conversation, as stored (copies).
func (s *Store) List() []*Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Conversation, 0, len(s.load()))
	for _, c := range s.load() {
		out = append(out, c.Clone())
	}
	return out
}

// UUID is crypto.randomUUID().
func UUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func strp(s string) *string { return &s }

// RecordUserTurn is recordUserTurn.
func (s *Store) RecordUserTurn(conversationID, userMessageID, content string, parentID *string, owner string) {
	if conversationID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := iso(s.now())
	c := s.find(conversationID)
	if c == nil {
		var o *string
		if owner != "" {
			o = strp(owner)
		}
		c = &Conversation{ID: conversationID, Owner: o, CreatedAt: now, UpdatedAt: now, Messages: []Message{}}
		s.all = append(s.load(), c)
	}
	if owner != "" && c.OwnerName() == "" {
		c.Owner = strp(owner)
	}
	if userMessageID != "" {
		for _, m := range c.Messages {
			if m.ID == userMessageID {
				return
			}
		}
	}
	id := userMessageID
	if id == "" {
		id = UUID()
	}
	c.Messages = append(c.Messages, Message{ID: id, Role: "user", Content: strp(content), ParentID: parentID, Timestamp: now})
	if userMessageID != "" {
		c.ActiveBranchID = strp(userMessageID)
	}
	c.UpdatedAt = now
	s.persist()
}

// AppendNote is appendConversationNote.
func (s *Store) AppendNote(conversationID, content string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.find(conversationID)
	if c == nil {
		return false
	}
	var parent *string
	if n := len(c.Messages); n > 0 {
		parent = strp(c.Messages[n-1].ID)
	}
	now := s.now()
	c.Messages = append(c.Messages, Message{ID: fmt.Sprintf("note-%s-%s", base36(now.UnixMilli()), randBase36(5)), Role: "assistant", Content: strp(content), ParentID: parent, Timestamp: iso(now)})
	c.UpdatedAt = iso(s.now())
	s.persist()
	return true
}

// RecordAssistantTurn is recordAssistantTurn.
func (s *Store) RecordAssistantTurn(conversationID string, m Message) {
	if conversationID == "" {
		return
	}
	s.mu.Lock()
	c := s.find(conversationID)
	if c == nil {
		s.mu.Unlock()
		return
	}
	c.Messages = append(c.Messages, m)
	c.ActiveBranchID = strp(m.ID)
	c.UpdatedAt = m.Timestamp
	s.persist()
	snap := c.Clone()
	s.mu.Unlock()
	s.exportSoon(snap)
}

// SettleHeld is settleHeldPart.
func (s *Store) SettleHeld(conversationID, messageID, toolCallID, name, status, result string, d *Decision, answer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.find(conversationID)
	if c == nil {
		return false
	}
	for i := range c.Messages {
		m := &c.Messages[i]
		if m.ID != messageID {
			continue
		}
		if m.Parts == nil {
			return false
		}
		for j := range m.Parts {
			p := &m.Parts[j]
			if p.ID != toolCallID {
				continue
			}
			if p.Kind == "tool_call" {
				p.Name, p.Status, p.Result, p.Decision = name, status, strp(result), d
			} else {
				p.Resolved, p.Answer = true, answer
			}
		}
		s.persist()
		return true
	}
	return false
}

// ToolsUsedIn is toolsUsedIn(id, 8).
func (s *Store) ToolsUsedIn(conversationID string) []string {
	if conversationID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.find(conversationID)
	if c == nil {
		return nil
	}
	msgs := c.Messages
	if len(msgs) > 8 {
		msgs = msgs[len(msgs)-8:]
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Kind == "tool_call" && !seen[p.Name] {
				seen[p.Name] = true
				out = append(out, p.Name)
			}
		}
	}
	return out
}

/* ---- answering and steering ---- */

// BeginAnswer is beginAnswer(id): the conversation is being answered until
// the returned function runs.
func (s *Store) BeginAnswer(conversationID string) func() {
	if conversationID == "" {
		return func() {}
	}
	s.mu.Lock()
	s.answering[conversationID]++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			n := s.answering[conversationID] - 1
			if n > 0 {
				s.answering[conversationID] = n
			} else {
				delete(s.answering, conversationID)
				delete(s.steers, conversationID)
			}
		})
	}
}

// QueueSteer is queueSteer.
func (s *Store) QueueSteer(conversationID, id, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.answering[conversationID] == 0 {
		return false
	}
	list := s.steers[conversationID]
	for _, x := range list {
		if x.ID == id {
			return true
		}
	}
	s.steers[conversationID] = append(list, Steer{id, text})
	return true
}

// TakeSteers is takeSteers.
func (s *Store) TakeSteers(conversationID string) []Steer {
	if conversationID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.steers[conversationID]
	delete(s.steers, conversationID)
	return list
}

func (s *Store) isAnswering(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answering[id] > 0
}

/* ---- read-time decorations ---- */

// WithUnanswered is withUnanswered(c).
func (s *Store) WithUnanswered(c *Conversation) *Conversation {
	if s.isAnswering(c.ID) {
		return c
	}
	parents := map[string]bool{}
	for _, m := range c.Messages {
		if m.ParentID != nil && *m.ParentID != "" {
			parents[*m.ParentID] = true
		}
	}
	any := false
	for _, m := range c.Messages {
		if m.Role == "user" && !parents[m.ID] {
			any = true
		}
	}
	if !any {
		return c
	}
	d := c.Clone()
	for i := range d.Messages {
		if d.Messages[i].Role == "user" && !parents[d.Messages[i].ID] {
			d.Messages[i].Error = NoReplyNote
		}
	}
	return d
}

// Holder is a call held for someone: who may answer it.
type Holder struct{ ToolCallID, Viewer, ViewerName string }

// WithHolders is withHolders(c, viewerId).
func WithHolders(c *Conversation, held []Holder, viewerID string) *Conversation {
	others := map[string]string{}
	for _, h := range held {
		if h.Viewer != viewerID {
			others[h.ToolCallID] = h.ViewerName
		}
	}
	if len(others) == 0 {
		return c
	}
	d := c.Clone()
	for i := range d.Messages {
		for j := range d.Messages[i].Parts {
			p := &d.Messages[i].Parts[j]
			if name, ok := others[p.ID]; ok && p.Kind == "approval" && !p.Resolved {
				p.WaitingOn = name
			}
		}
	}
	return d
}

// WithOwnCommands is withOwnCommands(c, viewerId).
func WithOwnCommands(c *Conversation, viewerID string, authEnabled bool) *Conversation {
	ran := false
	for _, m := range c.Messages {
		for _, p := range m.Parts {
			if p.Kind == "tool_call" && len(p.Gufi) > 0 {
				ran = true
			}
		}
	}
	if !ran {
		return c
	}
	d := c.Clone()
	for i := range d.Messages {
		for j := range d.Messages[i].Parts {
			p := &d.Messages[i].Parts[j]
			if p.Kind != "tool_call" || len(p.Gufi) == 0 {
				continue
			}
			own := !authEnabled || (p.GufiViewer != "" && p.GufiViewer == viewerID)
			if !own {
				p.Gufi = nil
			}
			p.GufiViewer = ""
		}
	}
	return d
}

/* ---- export to the knowledge base ---- */

func (s *Store) exportPath(c *Conversation) string {
	created := c.CreatedAt
	if len(created) > 10 {
		created = created[:10]
	}
	id := c.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return filepath.Join(s.kbRoot, ExportDir, created+"-"+id+".md")
}

var ownerTagRe = regexp.MustCompile(`[^a-z0-9_-]+`)

func sliceRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Store) exportToKB(c *Conversation) {
	ctx := context.Background()
	if os.MkdirAll(filepath.Join(s.kbRoot, ExportDir), 0o755) != nil {
		return
	}
	s.mu.Lock()
	label := !s.exportDirLabeled
	s.exportDirLabeled = true
	s.mu.Unlock()
	if label && s.hooks.Label != nil {
		if s.hooks.Label(ctx, []string{ExportDir}, []string{"chat-session"}) != nil {
			s.mu.Lock()
			s.exportDirLabeled = false
			s.mu.Unlock()
		}
	}
	firstUser := ""
	for _, m := range c.Messages {
		if m.Role == "user" {
			firstUser = m.Text()
			break
		}
	}
	title := "Untitled conversation"
	if c.Title != nil {
		title = *c.Title
	} else if firstUser != "" {
		title = sliceRunes(firstUser, 70)
	}
	started := c.CreatedAt
	if len(started) > 16 {
		started = started[:16]
	}
	started = strings.Replace(started, "T", " ", 1)
	by := ""
	if c.OwnerName() != "" {
		by = " by " + c.OwnerName()
	}
	lines := []string{"# " + title, "", fmt.Sprintf("Chat session %s, started %s%s.", c.ID, started, by), ""}
	for _, m := range c.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		head := "User"
		if m.Role == "assistant" {
			head = "Assistant"
			if m.Model != nil && *m.Model != "" {
				head += " (" + *m.Model + ")"
			}
		}
		lines = append(lines, "## "+head, "")
		body := strings.TrimSpace(m.Text())
		if body == "" {
			body = "(empty)"
		}
		lines = append(lines, body, "")
	}
	if os.WriteFile(s.exportPath(c), []byte(strings.Join(lines, "\n")), 0o644) != nil {
		return
	}
	if c.OwnerName() != "" {
		s.mu.Lock()
		first := !s.ownerLabeled[c.ID]
		s.ownerLabeled[c.ID] = true
		s.mu.Unlock()
		if first && s.hooks.Label != nil {
			rel, _ := filepath.Rel(s.kbRoot, s.exportPath(c))
			tag := "user-" + ownerTagRe.ReplaceAllString(strings.ToLower(c.OwnerName()), "-")
			if s.hooks.Label(ctx, []string{filepath.ToSlash(rel)}, []string{tag}) != nil {
				s.mu.Lock()
				delete(s.ownerLabeled, c.ID)
				s.mu.Unlock()
			}
		}
	}
	s.scheduleReindex(5*time.Second, true)
}

func (s *Store) scheduleReindex(after time.Duration, logFailure bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexTimer != nil {
		s.indexTimer.Stop()
	}
	s.indexTimer = time.AfterFunc(after, func() {
		if s.hooks.Reindex == nil {
			return
		}
		if err := s.hooks.Reindex(context.Background(), ExportDir); err != nil && logFailure && s.hooks.Log != nil {
			s.hooks.Log("chat-session export reindex failed (sweep will retry): " + err.Error())
		}
	})
}

// Delete removes the conversations ids names that keep(c) allows; it
// returns the ids removed.
func (s *Store) Delete(ids map[string]bool, keep func(*Conversation) bool) []string {
	s.mu.Lock()
	var gone []*Conversation
	var rest []*Conversation
	for _, c := range s.load() {
		if ids[c.ID] && keep(c) {
			gone = append(gone, c)
		} else {
			rest = append(rest, c)
		}
	}
	if len(gone) > 0 {
		if rest == nil {
			rest = []*Conversation{}
		}
		s.all = rest
		s.persist()
	}
	s.mu.Unlock()
	var out []string
	for _, c := range gone {
		out = append(out, c.ID)
		_ = os.Remove(s.exportPath(c))
	}
	if len(gone) > 0 {
		s.scheduleReindex(2*time.Second, false)
	}
	return out
}

// Create is POST /api/chat/conversations's write.
func (s *Store) Create(title *string, owner string) *Conversation {
	now := iso(s.now())
	var o *string
	if owner != "" {
		o = strp(owner)
	}
	c := &Conversation{ID: UUID(), Title: title, Owner: o, CreatedAt: now, UpdatedAt: now, Messages: []Message{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	all := append(s.load(), c)
	if len(all) > maxConversations {
		all = all[len(all)-maxConversations:]
	}
	s.all = all
	s.persist()
	return c.Clone()
}

// Rename sets a conversation's title; nil when it is gone.
func (s *Store) Rename(id string, title *string) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.find(id)
	if c == nil {
		return nil
	}
	c.Title = title
	c.UpdatedAt = iso(s.now())
	s.persist()
	return c.Clone()
}

// Sorted is the conversations newest first by creation.
func Sorted(cs []*Conversation) []*Conversation {
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].CreatedAt > cs[j].CreatedAt })
	return cs
}

const b36 = "0123456789abcdefghijklmnopqrstuvwxyz"

func base36(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{b36[n%36]}, b...)
		n /= 36
	}
	return string(b)
}

func randBase36(n int) string {
	b := make([]byte, n)
	r := make([]byte, n)
	_, _ = rand.Read(r)
	for i := range b {
		b[i] = b36[int(r[i])%36]
	}
	return string(b)
}
