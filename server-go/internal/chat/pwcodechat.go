package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsites"
	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/extensions"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/keycookie"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/userlibs"
)

// pw code as the chat engine. A conversation answered by pw code keeps one
// pw code session (conversations.PWCodeLink): each message the person sends
// goes to that session, and the session's events stream back into the chat
// as the reply. The session has remote control on, so it also shows in PW
// Code on the web, and it carries this Studio's MCP server with a token
// sealed for the person, so its library tools search and read what they
// may.
//
// Whose pw code runs it: on a Studio one person uses, this host's daemon
// (the person's own account). Where sign-in is on, the person's own daemon
// on the Studio's cluster, reached with their own ACTIVATE sign-in or key,
// never the Studio's account. With no credential of theirs, pw code chat
// is not offered.

const (
	engineStudio = "studio"
	enginePWCode = "pwcode"
	engineAuto   = "auto"

	chatTokenPrefix = "studio-chat."
	chatTokenKind   = "chat-mcp"
	chatTokenTTL    = 7 * 24 * time.Hour
	// pwcodeTurnLimit bounds one turn the Studio follows.
	pwcodeTurnLimit = 4 * time.Hour
	// pwcodeContextTurns and pwcodeContextChars bound what a session is
	// told of the conversation it has not seen.
	pwcodeContextTurns = 12
	pwcodeContextChars = 2000
)

// pwState is the engine's state on the module.
type pwState struct {
	sealOnce sync.Once
	seal     *keycookie.Sealer

	mu    sync.Mutex
	avail map[string]pwAvail
	// down is, by site, why pw code last could not reach the Studio's tools.
	down map[string]pwAvail
	// turns is the turn the Studio follows in each conversation.
	turns map[string]*pwTurn
	// here is, by viewer, the daemon their pw code chat reached a moment
	// ago, for the Code page's reads.
	here map[string]pwHereAt
}

// following: the Studio still follows a turn in the conversation.
func (m *Module) following(convID string) bool {
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	_, ok := m.pw.turns[convID]
	return ok
}

func (m *Module) follow(t *pwTurn) {
	m.pw.mu.Lock()
	if m.pw.turns == nil {
		m.pw.turns = map[string]*pwTurn{}
	}
	m.pw.turns[t.convID] = t
	m.pw.mu.Unlock()
}

func (m *Module) unfollow(t *pwTurn) {
	m.pw.mu.Lock()
	if m.pw.turns[t.convID] == t {
		delete(m.pw.turns, t.convID)
	}
	m.pw.mu.Unlock()
}

type pwAvail struct {
	why string
	at  time.Time
	// ttl is how long the answer holds: a minute, and longer when this
	// host's pw code did not start, so a chat does not wait on it often.
	ttl time.Duration
}

func (m *Module) pwSealer() *keycookie.Sealer {
	m.pw.sealOnce.Do(func() { m.pw.seal = keycookie.New(m.A.Cfg.IndexBase) })
	return m.pw.seal
}

// chatToken is what a pw code chat session's MCP bearer seals: the person
// it acts for (nil on a Studio without sign-in), the conversation, and
// when it stops working.
type chatToken struct {
	Kind string     `json:"k"`
	User *auth.User `json:"u,omitempty"`
	Conv string     `json:"c"`
	Exp  int64      `json:"e"`
}

func (m *Module) issueChatToken(u *auth.User, conv string) (string, error) {
	b, err := json.Marshal(chatToken{Kind: chatTokenKind, User: u, Conv: conv, Exp: time.Now().Add(chatTokenTTL).Unix()})
	if err != nil {
		return "", err
	}
	sealed, err := m.pwSealer().Encrypt(b)
	if err != nil {
		return "", err
	}
	return chatTokenPrefix + sealed, nil
}

// openChatToken is the token's grant, or false for anything else (a task
// token, a forged or expired one).
func (m *Module) openChatToken(tok string) (*chatToken, bool) {
	rest, ok := strings.CutPrefix(tok, chatTokenPrefix)
	if !ok {
		return nil, false
	}
	b, err := m.pwSealer().Decrypt(rest)
	if err != nil {
		return nil, false
	}
	var t chatToken
	if json.Unmarshal(b, &t) != nil || t.Kind != chatTokenKind || time.Now().Unix() > t.Exp {
		return nil, false
	}
	return &t, true
}

// pwTarget is where a person's pw code chat runs.
type pwTarget struct {
	client    pwcode.Client
	site      string
	workspace string
	// inSpace: workspace is the top of the person's own library.
	inSpace bool
	// persona is the agent instructions the session's folder carries.
	persona string
	// chatModel is the model chosen under the message box, which a
	// session moves to when its own is not offered (pwcodemodel.go);
	// modelMoved says the session moved for this message.
	chatModel  string
	modelMoved bool
	// modelStuck says the session should have moved and could not.
	modelStuck bool
	// relay: the session is on another machine, reached through the
	// platform (pwcoderelay.go).
	relay bool
}

func pwErr(status int, msg string) error { return &apierr.Error{Status: status, Message: msg} }

// pwSites is the agent sites service, or nil where agents are not set up
// (a test seam).
var pwSites = agentsites.Current

// pwcodeUnavailable says why pw code chat is not offered to r's viewer
// (cheap: no workspace is made), or "" when it is. Answers are kept a
// minute per viewer.
func (m *Module) pwcodeUnavailable(ctx context.Context, r *http.Request) string {
	who := userID(r)
	m.pw.mu.Lock()
	if a, ok := m.pw.avail[who]; ok && time.Since(a.at) < a.ttl {
		m.pw.mu.Unlock()
		return a.why
	}
	m.pw.mu.Unlock()
	why, ttl := m.pwcodeCheck(ctx, r)
	m.pw.mu.Lock()
	if m.pw.avail == nil {
		m.pw.avail = map[string]pwAvail{}
	}
	m.pw.avail[who] = pwAvail{why: why, at: time.Now(), ttl: ttl}
	m.pw.mu.Unlock()
	return why
}

// pwcodeCheck is pwcodeUnavailable's answer and how long it holds. pw code
// is offered only under ACTIVATE: a Studio with no platform connected (one
// run on its own, with any OpenAI-compatible endpoint) answers with the
// Studio assistant, and asks nothing of pw code.
func (m *Module) pwcodeCheck(ctx context.Context, r *http.Request) (string, time.Duration) {
	s := pwSites()
	if s == nil {
		return "pw code is not set up on this Studio.", time.Minute
	}
	if m.A.Cfg.AuthEnabled() {
		if user(r) == nil {
			return "Sign in to use pw code.", time.Minute
		}
		if m.viewerPlatformKey(r) == "" {
			return "pw code runs as you: sign in with ACTIVATE under Settings, Model access.", time.Minute
		}
		if userlibs.HomeCluster() == "" {
			return "This Studio does not know the cluster it runs on (STUDIO_HOME_CLUSTER), so it cannot reach your pw code.", time.Minute
		}
		return "", time.Minute
	}
	needs := "pw code needs ACTIVATE: sign the pw CLI in, or add your ACTIVATE API key under Settings, Model access."
	if f := m.A.Providers.PlatformAvailable; f == nil || !f(r, userID(r)) || m.GatewayKey() == "" {
		return needs, time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	st, err := s.Daemon.EnsureDaemon(cctx)
	if err != nil {
		return "pw code did not start here: " + err.Error(), 5 * time.Minute
	}
	if why := pwcode.UnusableReason(st); why != "" {
		return why, 5 * time.Minute
	}
	return "", time.Minute
}

// viewerPlatformKey is the viewer's own ACTIVATE credential (their sign-in
// first, then a pasted key), or "".
func (m *Module) viewerPlatformKey(r *http.Request) string {
	if m.pf == nil {
		return ""
	}
	v, err := m.pf.ViewerOf(r)
	if err != nil || v.UserID() == "" {
		return ""
	}
	return v.PlatformKey()
}

// engineFor is the engine that answers this message: the composer's choice
// when it names one, then pw code for a chat that continues a session, else
// the deployment's setting, where "auto" is pw code
// when it is offered to this person. A choice of pw code that cannot run
// is an error; auto falls back to the Studio assistant.
func (m *Module) engineFor(r *http.Request, asked, convID string) (string, error) {
	setting := m.eff().ChatEngine
	// A chat that continues a pw code session is answered there unless the
	// person chose the Studio assistant for it.
	continued, relayed := false, false
	if c := m.Conv.Get(convID); asked == "" && c != nil && c.PWCode != nil && c.PWCode.Continued {
		continued, relayed = true, c.PWCode.Relay
	}
	switch {
	case asked == engineStudio:
		return engineStudio, nil
	case relayed:
		// The session runs on another machine: what it needs is the relay,
		// not this Studio's own pw code.
		if why := m.relayUnavailable(r); why != "" {
			return "", pwErr(409, why)
		}
		return enginePWCode, nil
	case asked == enginePWCode || continued || (asked == "" && setting == enginePWCode):
		if why := m.pwcodeUnavailable(r.Context(), r); why != "" {
			return "", pwErr(409, why)
		}
		return enginePWCode, nil
	case setting == engineStudio:
		return engineStudio, nil
	}
	if m.pwcodeUnavailable(r.Context(), r) == "" && m.toolsDown(m.pwSite()) == "" {
		return enginePWCode, nil
	}
	return engineStudio, nil
}

// pwcodeTarget is the daemon and folder for a conversation's session: the
// site and folder it already has, else the person's own (My library's
// folder when it is on the Studio's cluster, else ~/studio-chat).
func (m *Module) pwcodeTarget(ctx context.Context, r *http.Request, link *conversations.PWCodeLink) (*pwTarget, error) {
	if link != nil && link.Relay {
		return m.relayTarget(r, link)
	}
	s := pwSites()
	if s == nil {
		return nil, pwErr(409, "pw code is not set up on this Studio.")
	}
	if !m.A.Cfg.AuthEnabled() {
		if link != nil && link.Site == agentsites.Local && link.Workspace != "" {
			return &pwTarget{client: s.Daemon.Local(), site: agentsites.Local, workspace: link.Workspace}, nil
		}
		return &pwTarget{client: s.Daemon.Local(), site: agentsites.Local, workspace: m.A.Cfg.KBRoot}, nil
	}
	u := user(r)
	if u == nil {
		return nil, pwErr(401, "Sign in to use pw code.")
	}
	key := m.viewerPlatformKey(r)
	if key == "" {
		return nil, pwErr(409, "pw code runs as you: sign in with ACTIVATE under Settings, Model access.")
	}
	site := userlibs.HomeCluster()
	if site == "" {
		return nil, pwErr(409, "This Studio does not know the cluster it runs on (STUDIO_HOME_CLUSTER), so it cannot reach your pw code.")
	}
	if link != nil && link.Site == site && link.Workspace != "" {
		return &pwTarget{client: s.Daemon.Remote(site, key), site: site, workspace: link.Workspace}, nil
	}
	ws, inSpace := "", false
	if f := m.A.Providers.SpaceFolder; f != nil {
		if ssite, folder := f(u.Username); folder != "" && sameCluster(ssite, site) {
			ws, inSpace = folder, true
		}
	}
	if ws == "" {
		var err error
		if ws, err = s.MakeWorkspace(ctx, site, key, "~/studio-chat"); err != nil {
			return nil, err
		}
	}
	return &pwTarget{client: s.Daemon.Remote(site, key), site: site, workspace: ws, inSpace: inSpace}, nil
}

// sameCluster: a and b name one cluster (pw://user/name or name).
func sameCluster(a, b string) bool {
	last := func(s string) string {
		s = strings.TrimSuffix(s, "/")
		if i := strings.LastIndex(s, "/"); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	return a != "" && last(a) == last(b)
}

// pwcodePrompt is what the Studio tells each pw code chat session: that
// the studio MCP server is the fastest way into the libraries, and which of
// its tools to reach for, by the names pw code gives them.
func (m *Module) pwcodePrompt(tools []ToolSpec) string {
	e := m.eff()
	has := map[string]bool{}
	for _, s := range tools {
		has[s.Name] = true
	}
	t := func(n string) string { return studioToolPrefix + n }
	var b strings.Builder
	fmt.Fprintf(&b, "You are answering in %s, an ACTIVATE Studio, through the person's chat.", e.AppName)
	b.WriteString(" The studio MCP server is this Studio's index and tools. It answers from the index in under a second, across every library, including libraries on hosts this session cannot reach, so use it before the shell for anything about the libraries and their files:")
	b.WriteString("\n- Find files with " + t("search_kb") + " (file names, the words inside files, and meaning), not find, grep or ls. " + t("list_libraries") + " names the libraries; its library argument takes an id or \"all\", and without one it searches the libraries the person chose for this chat.")
	b.WriteString("\n- Ask " + t("query_corpus") + " about many files at once: the largest, newest or oldest, recent changes, totals by type, and the biggest folders.")
	b.WriteString("\n- Read a file with " + t("read_kb_file") + ", which returns the text the index holds, PDFs and Office files included. " + t("read_original") + " opens the file itself, and " + t("list_kb_dir") + " lists a folder.")
	if has["write_kb_file"] {
		b.WriteString("\n- Save what the person asks to keep with " + t("write_kb_file") + " (Markdown, PDF, Word or Excel), which writes it into the library and indexes it, and change a library file with " + t("edit_kb_file") + ". Its description gives the document layout and writing style, and its result reports a check of the writing.")
		if _, err := os.Stat(filepath.Join(m.A.Cfg.KBRoot, conventionsFile)); err == nil {
			b.WriteString(" This library's conventions, " + conventionsFile + " at its top, set its own writing and document style; read it with " + t("read_kb_file") + " before you write a document, and follow it where it differs.")
		}
	}
	if has["delegate"] {
		b.WriteString("\n- When the person asks to delegate work, hand it to an agent or have it done in the background, call " + t("delegate") + " with the objective (and the agent instructions they name): it starts a pw code session of its own in a library folder, and the person follows it on the Code page. Do not do delegated work yourself in this chat. Give the person the link its result names, and check on it with " + t("agent_status") + " when they ask.")
	}
	if has["run_workflow"] {
		b.WriteString("\n- Platform workflows, clusters and runs: " + t("list_workflows") + ", " + t("run_workflow") + ", " + t("workflow_runs") + " and " + t("hpc_status") + ".")
	}
	b.WriteString("\nUse the shell for work these tools do not cover, such as running code. Name each file you used by its library path; " + t("show_in_viewer") + " gives a link that opens it in the Studio. Your working folder is where you may write other files the person asks for.")
	if m := strings.TrimSpace(e.KbMission); m != "" {
		b.WriteString("\n\nAbout the libraries: ")
		b.WriteString(m)
	}
	return b.String()
}

// pwcodeSession is the conversation's live session on t with the Studio's
// tools connected, made when it has none, its own is gone, or its own lost
// the tools; and whether it is new. A session that cannot reach the tools
// at any of this Studio's addresses is a *studioToolsError.
func (m *Module) pwcodeSession(ctx context.Context, r *http.Request, t *pwTarget, c *conversations.Conversation) (*conversations.PWCodeLink, bool, error) {
	// A session continued from the Code page is the person's own: it is
	// checked, never archived or replaced.
	if l := c.PWCode; l != nil && l.Continued && l.Session != "" {
		if err := m.continuedSession(ctx, r, t, l); err != nil {
			return nil, false, err
		}
		return l, false, nil
	}
	tools := m.chatTools(r, true)
	want := specNames(tools)
	// A session is kept while its MCP token has a day left.
	if l := c.PWCode; l != nil && l.Site == t.site && l.Session != "" && l.Persona == t.persona && pwFresh(l.CreatedAt) {
		if d, err := pwcode.GetSession(ctx, t.client, l.Session); err == nil && jsonv.String(jsonv.Path(d, "archivedAt")) == "" {
			ck := ensureStudioMCP(ctx, t.client, l.Session, want)
			if ck.OK {
				m.setToolsDown(t.site, "")
				m.pwcodeModel(ctx, r, t, l)
				return l, false, nil
			}
			m.A.Log.Warn("pw code chat: the session lost the Studio's tools, so another starts", "session", l.Session, "detail", ck.Detail)
			_, _ = pwcode.ArchiveSession(ctx, t.client, l.Session)
		}
	}
	token, err := m.issueChatToken(user(r), c.ID)
	if err != nil {
		return nil, false, err
	}
	e := m.eff()
	// The tools that only read run without asking, in read-only mode too.
	allowed := []any{}
	for _, s := range tools {
		if chatReadTools[s.Name] || chatNoAsk[s.Name] {
			allowed = append(allowed, studioToolPrefix+s.Name)
		}
	}
	prompt := m.pwcodePrompt(tools)
	var tried []string
	for _, url := range m.mcpURLs(t.site != agentsites.Local) {
		agents := jsonv.NewObject("studio", jsonv.NewObject(
			"description", "ACTIVATE Studio chat",
			"prompt", prompt,
			"mcpServers", []any{jsonv.NewObject(studioMCPName, jsonv.NewObject(
				"type", "http", "url", url,
				"headers", jsonv.NewObject("Authorization", "Bearer "+token)))},
		))
		agentsJSON, err := jsonv.Marshal(agents)
		if err != nil {
			return nil, false, err
		}
		req := jsonv.NewObject("workspace", t.workspace, "remoteControl", true, "agent", "studio", "agentsJson", string(agentsJSON), "allowedTools", allowed)
		if e.PWCodePermissionMode != "" {
			req.Set("permissionMode", e.PWCodePermissionMode)
		}
		if e.PWCodeModel != "" {
			req.Set("model", e.PWCodeModel)
		}
		s, err := pwcode.CreateSession(ctx, t.client, req)
		if err != nil {
			return nil, false, err
		}
		id := jsonv.String(jsonv.Path(s, "id"))
		if id == "" {
			return nil, false, errors.New("pw code made no session")
		}
		if ck := ensureStudioMCP(ctx, t.client, id, want); !ck.OK {
			m.A.Log.Warn("pw code chat: the session could not reach the Studio's tools", "url", url, "detail", ck.Detail)
			tried = append(tried, url+" ("+shortMCPError(ck.Detail)+")")
			_, _ = pwcode.ArchiveSession(ctx, t.client, id)
			continue
		}
		link := &conversations.PWCodeLink{Site: t.site, Session: id, Workspace: t.workspace, CreatedAt: m.Conv.Now(), MCPURL: url, Persona: t.persona}
		if st, err := pwcode.SessionState(ctx, t.client, id); err == nil {
			link.URL = jsonv.String(jsonv.Path(st, "remoteUrl"))
			link.Model = jsonv.String(jsonv.Path(st, "model"))
		}
		m.pwcodeModel(ctx, r, t, link)
		m.Conv.SetPWCode(c.ID, link)
		m.setToolsDown(t.site, "")
		return link, true, nil
	}
	why := strings.Join(tried, " or ")
	m.setToolsDown(t.site, why)
	return nil, false, &studioToolsError{detail: why}
}

// pwFresh: a session made at created still has a day of its token left.
func pwFresh(created string) bool {
	at, err := time.Parse(time.RFC3339Nano, created)
	return err == nil && time.Since(at) < chatTokenTTL-24*time.Hour
}

// pwcodeContext is the conversation the session has not seen, before the
// person's new message: every earlier turn for a new session, else those
// after its last reply.
func pwcodeContext(c *conversations.Conversation, fresh bool, current string) string {
	msgs := c.Messages
	start := 0
	if !fresh && c.PWCode != nil && c.PWCode.LastMessage != "" {
		start = len(msgs)
		for i, mm := range msgs {
			if mm.ID == c.PWCode.LastMessage {
				start = i + 1
			}
		}
	}
	var lines []string
	for _, mm := range msgs[start:] {
		if mm.ID == current || mm.Content == nil || strings.TrimSpace(*mm.Content) == "" {
			continue
		}
		who := "The person"
		if mm.Role == "assistant" {
			who = "The Studio assistant"
			if mm.Model != nil && strings.HasPrefix(*mm.Model, "pw code") {
				who = "You, in an earlier session"
			}
		}
		text := *mm.Content
		if r := []rune(text); len(r) > pwcodeContextChars {
			text = string(r[:pwcodeContextChars]) + " [cut]"
		}
		lines = append(lines, who+": "+text)
	}
	if len(lines) > pwcodeContextTurns {
		lines = lines[len(lines)-pwcodeContextTurns:]
	}
	if len(lines) == 0 {
		return ""
	}
	return "Earlier in this conversation, before you joined it:\n\n" + strings.Join(lines, "\n\n") + "\n\nThe person's new message:\n\n"
}

// streamPWCode answers the message with the conversation's pw code session.
func (m *Module) streamPWCode(r *http.Request, res *sseWriter, body *streamBody, convID string) error {
	ctx := r.Context()
	content, msgID := "", deref(body.UserMessageID)
	for i := len(body.Messages) - 1; i >= 0; i-- {
		var mm struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if json.Unmarshal(body.Messages[i], &mm) == nil && mm.Role == "user" {
			content, _ = mm.Content.(string)
			break
		}
	}
	owner := ""
	if u := user(r); u != nil {
		owner = u.Username
	}
	// pw code works as the chat's owner: another person's message to an
	// existing chat does not start or reach a session there.
	if old := m.Conv.Get(convID); old != nil {
		if _, err := m.ownPWChat(r, convID); err != nil {
			res.event("error", reqbody.O("message", "pw code: "+apiMessage(err), "kind", "pwcode"))
			return nil
		}
	}
	m.Conv.RecordUserTurn(convID, msgID, content, body.ParentMessageID, owner)
	fail := func(err error) error {
		res.event("error", reqbody.O("message", "pw code: "+apiMessage(err), "kind", "pwcode"))
		return nil
	}
	if strings.TrimSpace(content) == "" {
		return fail(errors.New("there is no message to send"))
	}
	c := m.Conv.Get(convID)
	if c == nil {
		return fail(errors.New("the conversation is gone"))
	}
	// A session works in the folder of the conversation's agent
	// instructions; choosing others starts a session in theirs.
	persona := ""
	if body.Agent != nil {
		persona = extensions.NormalizeName(*body.Agent)
	}
	prior := c.PWCode
	if prior != nil && !prior.Continued && prior.Persona != persona {
		prior = nil
	}
	t, err := m.pwcodeTarget(ctx, r, prior)
	if err != nil {
		return fail(err)
	}
	t.chatModel = StripAvailabilityMark(jsString(body.Model))
	if old := c.PWCode; old != nil && prior == nil && !old.Continued && old.Session != "" && old.Site == t.site {
		_, _ = pwcode.ArchiveSession(ctx, t.client, old.Session)
	}
	if prior == nil && (c.PWCode == nil || !c.PWCode.Continued) {
		m.personaFolder(r, t, persona)
	} else if prior != nil {
		t.persona = prior.Persona
	}
	link, fresh, err := m.pwcodeSession(ctx, r, t, c)
	if err != nil {
		// The Studio assistant answers when pw code cannot have the tools.
		var down *studioToolsError
		if errors.As(err, &down) {
			return err
		}
		return fail(err)
	}
	if c = m.Conv.Get(convID); c == nil {
		return fail(errors.New("the conversation is gone"))
	}
	// The session's tool calls run in the scope of this message; the
	// session is told of a scope it has not seen.
	scopeNote := ""
	if note := m.pwScope(r, body, link); !t.relay && (note != link.ScopeNote || (fresh && note != "")) {
		link.ScopeNote = note
		scopeNote = "Scope from the Studio's chat bar: " + firstNonEmpty(note, "the person's default libraries.") + "\n\n"
	}
	m.Conv.SetPWCode(convID, link)
	res.event("pwcode", reqbody.O("session", link.Session, "url", link.URL, "site", link.Site, "workspace", link.Workspace, "model", link.Model, "modelNote", link.ModelNote, "modelMoved", t.modelMoved, "modelStuck", t.modelStuck))
	d, err := pwcode.GetSession(ctx, t.client, link.Session)
	if err != nil {
		return fail(err)
	}
	after := int64(0)
	if n, ok := jsonv.Num(jsonv.Path(d, "lastSeq")); ok {
		after = int64(n)
	}
	text := scopeNote + pwcodeContext(c, fresh, msgID) + content

	// The turn runs on past the request, so its reply is recorded when the
	// person has left; the request writes its events while it lasts.
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pwcodeTurnLimit)
	turn := &pwTurn{m: m, res: res, convID: convID, link: link, parent: body.UserMessageID, started: time.Now(), done: make(chan struct{})}
	events := make(chan *jsonv.Object, 256)
	followErr := make(chan error, 1)
	go func() {
		followErr <- pwcode.Follow(tctx, t.client, link.Session, after, func(ev *jsonv.Object) bool {
			select {
			case events <- ev:
				return true
			case <-tctx.Done():
				return false
			}
		})
		close(events)
	}()
	sent, err := pwcode.SendMessage(tctx, t.client, link.Session, text)
	if err != nil {
		cancel()
		return fail(err)
	}
	turn.id = jsonv.String(jsonv.Path(sent, "turnId"))
	m.follow(turn)
	go func() {
		defer cancel()
		turn.run(events, followErr)
	}()
	select {
	case <-turn.done:
	case <-ctx.Done():
	}
	return nil
}

// pwTurn follows one turn of a pw code session into the chat.
type pwTurn struct {
	m       *Module
	res     *sseWriter
	convID  string
	link    *conversations.PWCodeLink
	parent  *string
	id      string
	started time.Time
	done    chan struct{}

	content  strings.Builder
	parts    []conversations.Part
	model    string
	failed   string
	finished bool
	// stopped: the person stopped the turn (Stop), so its ending in an
	// error is what they asked for (under m.pw.mu).
	stopped bool
}

func (t *pwTurn) mine(ev *jsonv.Object) bool {
	id := jsonv.String(jsonv.Path(ev, "turnId"))
	return id == "" || t.id == "" || id == t.id
}

func (t *pwTurn) run(events <-chan *jsonv.Object, followErr <-chan error) {
	// The turn stops being followed before done is closed, so a status read
	// as soon as the reply ends does not still say answering.
	defer close(t.done)
	defer t.m.unfollow(t)
	for ev := range events {
		if t.handle(ev) {
			t.finish()
			return
		}
	}
	if err := <-followErr; err != nil && t.failed == "" {
		t.failed = "lost the session's events: " + err.Error()
	}
	if t.failed == "" {
		t.failed = "the session's events ended before the reply did"
	}
	t.finish()
}

// studioToolPrefix is how pw code names the Studio's MCP tools. The chat
// shows them by their own names, which its renderers know.
const studioToolPrefix = "mcp__studio__"

// handle passes one event on; true when the turn has ended.
func (t *pwTurn) handle(ev *jsonv.Object) bool {
	str := func(k string) string { return jsonv.String(jsonv.Path(ev, k)) }
	switch str("type") {
	case "state":
		if u := jsonv.String(jsonv.Path(ev, "state", "remoteUrl")); u != "" && u != t.link.URL {
			t.link.URL = u
			t.res.event("pwcode", reqbody.O("session", t.link.Session, "url", u, "site", t.link.Site, "workspace", t.link.Workspace, "model", t.link.Model, "modelNote", t.link.ModelNote))
		}
	case "chunk":
		if t.mine(ev) {
			s := str("content")
			t.content.WriteString(s)
			t.res.event("content", reqbody.O("text", s))
		}
	case "reasoning":
		if s := str("content"); s != "" && t.mine(ev) {
			t.res.event("reasoning", reqbody.O("text", s))
		}
	case "tool_start":
		if t.mine(ev) {
			id, name := str("callId"), strings.TrimPrefix(str("name"), studioToolPrefix)
			t.parts = append(t.parts, conversations.Part{Kind: "tool_call", ID: id, Name: name, Args: str("args"), Status: "running"})
			t.res.event("tool", reqbody.O("phase", "call", "id", id, "name", name, "args", str("args")))
		}
	case "tool_end":
		if t.mine(ev) {
			id, name, result := str("callId"), strings.TrimPrefix(str("name"), studioToolPrefix), str("result")
			// pw code marks a failed call by its result's first word.
			bad := jsonv.Truthy(jsonv.Path(ev, "isError")) || strings.HasPrefix(result, "Error:")
			for i := range t.parts {
				if t.parts[i].ID == id {
					status := "ok"
					if bad {
						status = "error"
					}
					t.parts[i].Status = status
					r := sliceRunes(result, 4000)
					t.parts[i].Result = &r
				}
			}
			t.res.event("tool", reqbody.O("phase", "result", "id", id, "name", name, "summary", firstLine(result, 160), "error", bad))
		}
	case "approval_request":
		a := jsonv.Obj(jsonv.Path(ev, "approval"))
		if a != nil {
			// A Studio tool is asked for by its own name.
			for _, k := range []string{"tool", "command"} {
				if v := jsonv.String(jsonv.Path(a, k)); strings.HasPrefix(v, studioToolPrefix) {
					a.Set(k, strings.TrimPrefix(v, studioToolPrefix))
				}
			}
			t.res.event("pwcode_approval", reqbody.O("conversationId", t.convID, "session", t.link.Session, "url", t.link.URL, "approval", a))
		}
	case "approval_resolved":
		t.res.event("pwcode_approval_resolved", reqbody.O("conversationId", t.convID, "approvalId", str("approvalId")))
	case "warning":
		if s := str("content"); strings.HasPrefix(s, "mcp studio:") {
			t.m.A.Log.Warn("pw code chat: the Studio's MCP server did not connect", "session", t.link.Session, "detail", s)
			t.res.event("reasoning", reqbody.O("text", "\n↳ the Studio's library tools did not connect to pw code\n"))
		}
	case "error":
		if t.mine(ev) {
			t.failed = firstNonEmpty(str("content"), str("message"), "pw code reported an error")
			return true
		}
	case "done":
		if t.mine(ev) {
			if s := str("content"); s != "" {
				if t.content.Len() == 0 {
					t.res.event("content", reqbody.O("text", s))
				}
				t.content.Reset()
				t.content.WriteString(s)
			}
			t.model = jsonv.String(jsonv.Path(ev, "turn", "model"))
			return true
		}
	}
	return false
}

func (t *pwTurn) finish() {
	if t.finished {
		return
	}
	t.finished = true
	m := t.m
	messageID := "pwc-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	content := t.content.String()
	m.pw.mu.Lock()
	stopped := t.stopped
	m.pw.mu.Unlock()
	if stopped && t.failed != "" {
		// A stopped turn ends with the daemon's error for it (its model
		// call cancelled): it was stopped, not failed.
		t.failed = ""
		if strings.TrimSpace(content) == "" {
			content = "Stopped."
			t.res.event("content", reqbody.O("text", content))
		}
	}
	if t.failed != "" && strings.TrimSpace(content) == "" {
		content = "pw code did not finish this reply: " + t.failed
		t.res.event("content", reqbody.O("text", content))
	}
	model := t.model
	if model == "" {
		model = t.link.Model
	}
	shown := "pw code"
	if model != "" {
		shown += " · " + model
	}
	calls := 0
	for i := range t.parts {
		if t.parts[i].Status == "running" {
			t.parts[i].Status = "error"
		}
		calls++
	}
	reasoning := ""
	if calls > 0 {
		s := "s"
		if calls == 1 {
			s = ""
		}
		reasoning = fmt.Sprintf("pw code ran %d tool call%s in %ds.", calls, s, int(max(1, time.Since(t.started).Round(time.Second).Seconds())))
		t.res.event("reasoning", reqbody.O("text", reasoning))
	}
	m.Conv.RecordAssistantTurn(t.convID, conversations.Message{
		ID: messageID, Role: "assistant", Content: sp(content), ParentID: t.parent, Timestamp: nowISO(),
		Model: sp(shown), HasModel: true, Reasoning: reasoning, Parts: t.parts, FinishReason: "stop",
	})
	t.link.LastMessage = messageID
	m.Conv.SetPWCode(t.convID, t.link)
	if t.failed != "" {
		m.A.Log.Warn("pw code chat turn did not finish", "session", t.link.Session, "why", t.failed)
	}
	t.res.event("done", reqbody.O("content", content, "finishReason", "stop", "model", shown, "messageId", messageID))
	t.res.end()
}

// sliceRunes is s cut to n runes.
func sliceRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// apiMessage is an error's message for the person: an apierr's own text.
func apiMessage(err error) string {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return sliceRunes(s, n)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---- routes ----

func (m *Module) pwcodeRoutes(h func(pattern string, fn func(http.ResponseWriter, *http.Request) error)) {
	// What answers in chat for this person, and whether pw code is offered.
	h("GET /api/chat/engine", func(w http.ResponseWriter, r *http.Request) error {
		why := m.pwcodeUnavailable(r.Context(), r)
		setting := m.eff().ChatEngine
		def := engineStudio
		if why == "" && setting != engineStudio {
			def = enginePWCode
		}
		var reason, tools any
		if why != "" {
			reason = why
		} else if down := m.toolsDown(m.pwSite()); down != "" {
			// pw code could not reach the Studio's tools a few minutes ago:
			// it stays a choice, and auto answers with the Studio assistant.
			tools = "pw code could not reach this Studio's tools at " + down + " a few minutes ago, so the Studio assistant answers by default for now."
			if setting == engineAuto {
				def = engineStudio
			}
		}
		return reqbody.OK(w, reqbody.O("setting", setting, "default", def, "pwcode", why == "", "reason", reason, "toolsDown", tools))
	})
	// The conversation's pw code session as it is now: whether a turn is
	// running, what it waits to be approved, and where it shows on the web,
	// so a page that lost the reply's stream picks the turn up again.
	h("GET /api/chat/pwcode/{id}", func(w http.ResponseWriter, r *http.Request) error {
		// A chat pw code has not answered has nothing to show.
		if c, err := m.visibleChat(r, r.PathValue("id")); err != nil {
			return err
		} else if c.PWCode == nil || c.PWCode.Session == "" {
			return reqbody.OK(w, reqbody.O("session", nil, "status", "idle", "answering", false, "approvals", []any{}))
		}
		c, t, err := m.ownPWCode(r)
		if err != nil {
			return err
		}
		d, err := pwcode.GetSession(r.Context(), t.client, c.PWCode.Session)
		if err != nil {
			return err
		}
		approvals := jsonv.Arr(jsonv.Path(d, "approvals"))
		if approvals == nil {
			approvals = []any{}
		}
		// How the session's studio server stands, for the chat bar's menu.
		var tools any
		if t.relay {
			tools = reqbody.O("connected", false, "count", 0, "relay", true)
		} else if s, err := studioMCPState(r.Context(), t.client, c.PWCode.Session); err == nil && s != nil {
			v, _ := s.Get("connected")
			_, n := serverTools(s)
			tools = reqbody.O("connected", jsonv.Truthy(v) && jsonv.String(jsonv.Path(s, "err")) == "", "count", n, "error", jsonv.String(jsonv.Path(s, "err")))
		}
		var codePage any
		if c.PWCode.Continued && c.PWCode.Machine != "" {
			codePage = CodePageLink(c.PWCode.Machine, c.PWCode.Session)
		}
		return reqbody.OK(w, reqbody.O("session", c.PWCode.Session, "url", c.PWCode.URL, "status", jsonv.String(jsonv.Path(d, "status")),
			"answering", m.following(c.ID), "approvals", approvals, "tools", tools, "continued", c.PWCode.Continued, "codePage", codePage, "model", c.PWCode.Model, "modelNote", c.PWCode.ModelNote,
			"relay", c.PWCode.Relay, "machine", relayMachine(c.PWCode)))
	})
	h("POST /api/chat/pwcode/{id}/interrupt", func(w http.ResponseWriter, r *http.Request) error {
		c, t, err := m.ownPWCode(r)
		if err != nil {
			return err
		}
		if _, err := pwcode.InterruptSession(r.Context(), t.client, c.PWCode.Session); err != nil {
			return err
		}
		m.pw.mu.Lock()
		if turn := m.pw.turns[c.ID]; turn != nil {
			turn.stopped = true
		}
		m.pw.mu.Unlock()
		return reqbody.OK(w, reqbody.O("ok", true))
	})
	h("POST /api/chat/pwcode/{id}/approvals/{approval}", func(w http.ResponseWriter, r *http.Request) error {
		c, t, err := m.ownPWCode(r)
		if err != nil {
			return err
		}
		approval := r.PathValue("approval")
		if !approvalRe(approval) {
			return pwErr(400, "not an approval id")
		}
		rb, err := reqbody.Read(r)
		if err != nil {
			return err
		}
		var in struct {
			Allowed bool   `json:"allowed"`
			Message string `json:"message"`
			// An ask's answers, one per question, or chat to talk them
			// through instead; a plan's feedback with a rejection.
			Answers []askAnswer `json:"answers"`
			Chat    bool        `json:"chat"`
			Kind    string      `json:"kind"`
			// Mode is what an approved plan leaves plan mode into.
			Mode string `json:"mode"`
		}
		if rb.Present {
			_ = json.Unmarshal(rb.Raw, &in)
		}
		if in.Kind == "ask" {
			answer, err := askAnswerBody(in.Answers, in.Chat)
			if err != nil {
				return err
			}
			if _, err := pwcode.AnswerApproval(r.Context(), t.client, c.PWCode.Session, approval, answer); err != nil {
				return err
			}
			return reqbody.OK(w, reqbody.O("ok", true))
		}
		answer := jsonv.NewObject("allowed", in.Allowed)
		if in.Kind == "plan" && in.Allowed {
			answer.Set("mode", planMode(in.Mode, t.relay))
		} else if in.Kind == "plan" && !in.Allowed {
			if msg := sliceRunes(strings.TrimSpace(in.Message), 2000); msg != "" {
				answer.Set("feedback", msg)
			}
		} else if !in.Allowed {
			msg := sliceRunes(strings.TrimSpace(in.Message), 500)
			if msg == "" {
				msg = "Denied from the Studio."
			}
			answer.Set("denyMessage", msg)
		}
		if _, err := pwcode.AnswerApproval(r.Context(), t.client, c.PWCode.Session, approval, answer); err != nil {
			return err
		}
		return reqbody.OK(w, reqbody.O("ok", true))
	})
}

// planMode is the mode an approved plan's session leaves plan mode into.
// pw code changes the mode only when the answer names one, so without it
// the session stays in plan mode and refuses every edit. The direct path
// takes auto, as the terminal's Approve does; through the relay the
// machine refuses a mode above its remote ceiling, accept-edits unless its
// owner widened it, so that is the default there.
func planMode(asked string, relay bool) string {
	switch asked {
	case "auto", "accept-edits":
		if !(relay && asked == "auto") {
			return asked
		}
	}
	if relay {
		return "accept-edits"
	}
	return "auto"
}

// ownPWChat is the viewer's own chat id: 404 where they cannot see it, 403
// where it is someone else's shared with them.
func (m *Module) ownPWChat(r *http.Request, id string) (*conversations.Conversation, error) {
	c, err := m.visibleChat(r, id)
	if err != nil {
		return nil, err
	}
	if m.A.Cfg.AuthEnabled() {
		u := user(r)
		if u == nil || c.Owner == nil || *c.Owner != u.Username {
			return nil, pwErr(403, "Only the person whose chat this is can direct its pw code session.")
		}
	}
	return c, nil
}

func approvalRe(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, c := range s {
		if !(c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// ownPWCode is the viewer's own conversation with a pw code session, and
// that session's daemon reached as them.
func (m *Module) ownPWCode(r *http.Request) (*conversations.Conversation, *pwTarget, error) {
	c, err := m.ownPWChat(r, r.PathValue("id"))
	if err != nil {
		return nil, nil, err
	}
	if c.PWCode == nil || c.PWCode.Session == "" {
		return nil, nil, pwErr(409, "This chat has no pw code session.")
	}
	t, err := m.pwcodeTarget(r.Context(), r, c.PWCode)
	if err != nil {
		return nil, nil, err
	}
	if t.site != c.PWCode.Site {
		return nil, nil, pwErr(409, "This chat's pw code session is on "+c.PWCode.Site+", which this Studio does not reach for you.")
	}
	return c, t, nil
}
