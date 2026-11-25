package chat

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/agentsmd"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/extensions"
	"github.com/parallelworks/activate-studio/server-go/internal/folders"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/userlibs"
)

// Where a new pw code chat starts. Its composer shows the machine and the
// folder before the first message, as the Code page's start does, and the
// person may choose others; with no choice the session works where
// pwcodeTarget puts it. Another folder on the machine the Studio drives for
// them gets a session of the Studio's own there, with its tools. Another of
// their machines gets a session through ACTIVATE, as the Code page starts
// one, which the chat then drives as a relayed session (pwcoderelay.go).
//
// Everything here is read as the person: the machines with their own
// ACTIVATE credential, and the folders of the machine the Studio drives
// through their own pw code there, or with their own credential.

// startPick is where the person chose to start a chat: a machine ("" for
// the one the Studio drives), a folder ("" for the default), and the
// permission mode on another machine. A session started on the Code page
// also says its effort, its Claude account and its first message.
type startPick struct {
	machine, folder, mode, model string
	allocation                   *string
	effort, account, prompt      string
	// tools is whether the person chose to add the Studio's tools on the
	// machine with this start, nil where they were not asked.
	tools *bool
}

// startFor is where the session this message makes starts: what the
// composer chose, for a chat's first session, and the folder chosen then
// for a later one. It is empty where the default holds, and for a message
// to a session the chat has.
func (m *Module) startFor(r *http.Request, body *streamBody, c *conversations.Conversation, prior *conversations.PWCodeLink) (startPick, error) {
	if old := c.PWCode; old != nil && old.Session != "" {
		if prior == nil && old.Picked {
			return startPick{folder: old.Workspace}, nil
		}
		return startPick{}, nil
	}
	p := startPick{machine: strings.TrimSpace(deref(body.PWCodeMachine)), folder: strings.TrimSpace(deref(body.PWCodeFolder)), mode: strings.TrimSpace(deref(body.PWCodeMode)), tools: body.PWCodeTools}
	if p.machine == directMachine {
		p.machine = ""
	}
	if p.machine != "" && !pwcode.IsRouteID(p.machine) {
		return startPick{}, pwErr(400, "Not a machine.")
	}
	if p.folder != "" {
		if !strings.HasPrefix(p.folder, "/") || strings.ContainsAny(p.folder, "\x00\r\n") || len(p.folder) > 4096 {
			return startPick{}, pwErr(400, "The folder must be a full path on the machine.")
		}
		p.folder = path.Clean(p.folder)
	}
	if p.machine != "" {
		return p, nil
	}
	if p.folder == "" || p.folder == m.startFolder(r, "") {
		return startPick{}, nil
	}
	return startPick{folder: p.folder}, nil
}

// startFolder is the folder pwcodeTarget gives a new chat with persona,
// without making it.
func (m *Module) startFolder(r *http.Request, persona string) string {
	in := func(root string) string {
		if persona == "" || agentsmd.Default == nil {
			return root
		}
		return path.Join(root, "agents", persona)
	}
	if !m.A.Cfg.AuthEnabled() {
		return in(filepath.Clean(m.A.Cfg.KBRoot))
	}
	u := user(r)
	if u == nil {
		return ""
	}
	if f := m.spaceFolderOn(u.Username, userlibs.HomeCluster()); f != "" {
		if ownSpace(r) == "" {
			return f
		}
		return in(f)
	}
	return studioChatFolder
}

// startElsewhere starts a session on another of the person's machines, as
// the Code page does: through ACTIVATE as them, in the folder they chose,
// in a permission mode within the machine's remote ceiling. The chat is
// linked to it as to a session continued there.
func (m *Module) startElsewhere(ctx context.Context, r *http.Request, body *streamBody, p startPick, persona string) (*conversations.PWCodeLink, error) {
	client, err := m.relayClient(r, p.machine)
	if err != nil {
		return nil, err
	}
	mach, err := codesessions.PlatformMachine(r, m.pf, p.machine)
	if err != nil {
		return nil, err
	}
	str := func(k string) string { return jsonv.String(jsonv.Path(mach, k)) }
	name := firstNonEmpty(str("name"), p.machine)
	if persona != "" {
		return nil, pwErr(409, personaElsewhere(persona, name))
	}
	if p.folder == "" {
		return nil, pwErr(400, "Choose the folder on "+name+" the session works in.")
	}
	if started, _ := jsonv.Path(mach, "remoteStart").(bool); str("status") != "connected" || !started {
		return nil, pwErr(409, name+" does not take new sessions from ACTIVATE now. Choose another machine, or start pw code there.")
	}
	if p.mode, err = m.startMode(mach, name, p.mode); err != nil {
		return nil, err
	}
	p.model = strings.TrimSpace(deref(body.PWCodeModel))
	if p.model == "" {
		if set := m.eff().PWCodeModel; !pwcode.IsClaudeModel(set) || (jsonv.Path(mach, "claudeInstalled") == true && jsonv.Path(mach, "claudeCodeAllowed") == true) {
			p.model = set
		}
	} else {
		p.allocation = body.PWCodeAllocation
	}
	return m.startRelayed(ctx, r, client, mach, p, StripAvailabilityMark(jsString(body.Model)))
}

// startRelayed starts a session on mach through ACTIVATE, as the viewer.
// The platform's create takes no tools or instructions on the releases
// there are, so the Studio's tools come one of three ways: from pw code's
// settings on the machine, which the Studio adds them to first where the
// person chose that; with the create request, or attached before the first
// turn, where the machine's pw code says it takes them. With them, the
// first message brings the Studio's instructions (relayInstructions).
// Without them the session starts all the same, and its chat says so.
func (m *Module) startRelayed(ctx context.Context, r *http.Request, client *pwcode.RelayClient, mach *jsonv.Object, p startPick, chatModel string) (*conversations.PWCodeLink, error) {
	name := firstNonEmpty(jsonv.String(jsonv.Path(mach, "name")), p.machine)
	has, give, toolsURL, toolsWhy := m.machineTools(r, mach, p.tools)
	attach := give == "attach"
	// Given with the create request, it has them from its first turn.
	has = has || give == "create"
	req := jsonv.NewObject("workspace", p.folder, "permissionMode", p.mode)
	for _, f := range [][2]string{{"model", p.model}, {"effort", p.effort}, {"claudeAccount", p.account}, {"allocation", deref(p.allocation)}} {
		if f[1] != "" {
			req.Set(f[0], f[1])
		}
	}
	if p.prompt != "" && !attach {
		text := p.prompt
		if has {
			text += m.relayInstructions(r)
		}
		req.Set("prompt", text)
	}
	if give == "create" {
		req.Set("mcpServers", []any{jsonv.NewObject("name", studioMCPName, "url", toolsURL, "platformAuth", true)})
		allows := []any{}
		for _, rule := range machineRules(m.bridgeAllows(r)) {
			allows = append(allows, rule)
		}
		req.Set("allowedTools", allows)
	}
	s, err := pwcode.CreateSession(auth.ContextWithUser(ctx, user(r)), client, req)
	// An ACTIVATE that takes the server but not the rules, or neither after
	// all, refuses the field: the session starts with what it does take.
	for _, field := range []string{"allowedTools", "mcpServers"} {
		var refused *pwcode.DaemonError
		if give != "create" || !errors.As(err, &refused) || refused.Status != http.StatusUnprocessableEntity {
			break
		}
		req.Delete(field)
		if field == "mcpServers" {
			has, toolsWhy = false, "ACTIVATE did not take them as the session started"
			if p.prompt != "" && !attach {
				req.Set("prompt", p.prompt)
			}
		}
		s, err = pwcode.CreateSession(auth.ContextWithUser(ctx, user(r)), client, req)
	}
	if err != nil {
		code := 502
		var de *pwcode.DaemonError
		if errors.As(err, &de) && (de.Status == 400 || de.Status == 403 || de.Status == 404 || de.Status == 409 || de.Status == 422) {
			code = de.Status
		}
		return nil, pwErr(code, "ACTIVATE did not start a session on "+name+": "+apiMessage(err))
	}
	id := firstNonEmpty(jsonv.String(jsonv.Path(s, "id")), jsonv.String(jsonv.Path(s, "session", "id")))
	if id == "" {
		return nil, pwErr(502, "ACTIVATE started a session on "+name+" but did not say which.")
	}
	link := &conversations.PWCodeLink{Site: pwcode.RelaySite(p.machine), Relay: true, Session: id, Workspace: firstNonEmpty(jsonv.String(jsonv.Path(s, "workspace")), p.folder),
		URL:   firstNonEmpty(jsonv.String(jsonv.Path(s, "url")), strings.TrimRight(m.pf.Origin(), "/")+"/code/"+url.PathEscape(p.machine)+"/"+url.PathEscape(id)),
		Model: firstNonEmpty(jsonv.String(jsonv.Path(s, "model")), p.model), CreatedAt: m.Conv.Now(), Machine: p.machine, MachineName: name, Picked: true}
	// A model the person's connection does not offer is moved off, as a
	// session made here is.
	m.pwcodeModel(ctx, r, &pwTarget{client: client, picked: p.model, chatModel: chatModel}, link)
	link.Continued, link.ToolsError = true, toolsWhy
	if attach {
		key, err := codesessions.ViewerKey(r, m.pf)
		if err == nil {
			err = m.bridgeAttach()(r, key, p.machine, id, toolsURL)
		}
		if err != nil {
			m.A.Log.Warn("pw code chat: a new session on another machine could not take the Studio's tools", "machine", p.machine, "session", id, "err", err.Error())
			link.ToolsError = firstLine(apiMessage(err), 300)
		}
		has = err == nil
	}
	if p.prompt != "" && attach {
		text := p.prompt
		if has {
			text += m.relayInstructions(r)
		}
		if _, err := pwcode.SendMessage(ctx, client, id, text); err != nil {
			_, _ = pwcode.ArchiveSession(ctx, client, id)
			return nil, pwErr(502, "The session started on "+name+", but it did not take the message: "+apiMessage(err))
		}
	}
	if !has && give == "" && toolsURL != "" && m.bridgePrefs().get(userID(r), p.machine).URL == toolsURL {
		// It reached them as it started, through an entry the person added.
		has, link.ToolsError = true, ""
	}
	link.HasTools, link.Untold = has, has && p.prompt == ""
	m.A.Log.Info("pw code chat: started a session on another machine through the platform", "user", userID(r), "machine", p.machine, "session", id, "mode", p.mode, "tools", has)
	return link, nil
}

// machineTools is how a session about to start on mach gets this Studio's
// tools for the viewer: pw code there has them (has), as the Studio last set
// it up or sets it up now (bridgeAtStart), or the session is given them, with
// its create request ("create") or attached as it starts ("attach"), at
// toolsURL. why is what kept them from being added there.
func (m *Module) machineTools(r *http.Request, mach *jsonv.Object, chose *bool) (has bool, give, toolsURL, why string) {
	toolsURL, why = m.bridgeReach(r)
	if why != "" {
		return false, "", "", ""
	}
	b := machOf(mach)
	p := m.bridgePrefs().get(userID(r), b.ID)
	switch {
	case p.URL == toolsURL:
		return true, "", toolsURL, ""
	case !b.gives():
		has, why = m.bridgeAtStart(r, b, toolsURL, chose)
		return has, "", toolsURL, why
	case p.Declined:
		return false, "", toolsURL, ""
	case b.Create:
		return false, "create", toolsURL, ""
	}
	return false, "attach", toolsURL, ""
}

// relayInstructionsHead opens the Studio's instructions after what the
// person typed. The web draws the message without them (carried.ts).
const relayInstructionsHead = "\n\nFrom ACTIVATE Studio, for this session (the person did not type this):\n\n"

// relayInstructions is what a session on another machine with the Studio's
// tools is told, after the person's first message: what pwcodePrompt tells
// one the Studio makes. The platform's create takes no instructions, and
// pw code gives the model none that an MCP server sends.
func (m *Module) relayInstructions(r *http.Request) string {
	return relayInstructionsHead + m.pwcodePrompt(m.personTools(r, m.personCtx(r)), folderElsewhere, false) +
		"\n\nIf this session has no " + studioToolPrefix + " tools, it opened before they were added here: leave this aside and use your own tools. If pw code says a studio tool is not available in this mode, stop trying it and tell the person so: a wider permission mode for this session asks them before it."
}

// startFromCode is codesessions.StartInChat: a Code page session starts as
// a chat's does, and has its chat from the start. On the machine the
// Studio drives for the viewer it is a session of the Studio's own, with
// its agent; on another of theirs it starts through ACTIVATE (startRelayed).
func (m *Module) startFromCode(r *http.Request, machine string, mach, body *jsonv.Object) (*jsonv.Object, bool, error) {
	ctx := r.Context()
	str := func(k string) string { return jsonv.String(jsonv.Path(body, k)) }
	p := startPick{machine: machine, folder: path.Clean(str("workspace")), mode: str("permissionMode"), model: str("model"), effort: str("effort"), account: str("claudeAccount"), prompt: str("prompt")}
	if a := str("allocation"); a != "" {
		p.allocation = &a
	}
	if v, ok := jsonv.Path(body, codesessions.StartTools).(bool); ok {
		p.tools = &v
		body = body.Clone()
		body.Delete(codesessions.StartTools)
	}
	here, hereErr := m.pwcodeHereCached(ctx, r)
	own := hereErr == nil && (mach == nil || m.startIsOwn(mach, here))
	if !own && (mach == nil || m.relayUnavailable(r) != "") {
		return nil, false, nil
	}
	owner := ""
	if u := user(r); u != nil {
		owner = u.Username
	}
	title := sp(firstLine(firstNonEmpty(p.prompt, "pw code in "+shortWorkspace(p.folder)), 70))
	answer := func(c *conversations.Conversation, l *conversations.PWCodeLink) *jsonv.Object {
		return jsonv.NewObject("id", l.Session, "machineId", machine, "name", "", "status", "idle", "workspace", l.Workspace, "model", l.Model,
			"createdAt", l.CreatedAt, "updatedAt", l.CreatedAt, "conversationId", c.ID, "shownOn", proxyMachine(l))
	}
	if !own {
		client, err := m.relayClient(r, machine)
		if err != nil {
			return nil, false, err
		}
		l, err := m.startRelayed(ctx, r, client, mach, p, "")
		if err != nil {
			return nil, false, err
		}
		c := m.Conv.CreateWith(title, owner, nil, l)
		if p.prompt != "" {
			m.recordSent(r, c, &pwTarget{client: client, site: l.Site, workspace: l.Workspace, relay: true}, p.prompt, 0)
			m.nameTold(r, c, &pwTarget{client: client, site: l.Site, workspace: l.Workspace, relay: true}, p.prompt)
		}
		return answer(c, l), true, nil
	}
	t, err := m.pwcodeTargetIn(ctx, r, nil, p.folder)
	if err != nil {
		return nil, false, err
	}
	t.start, t.picked, t.pickedAlloc = p, p.model, p.allocation
	c := m.Conv.CreateWith(title, owner, nil, nil)
	gone := func() {
		m.Conv.Delete(map[string]bool{c.ID: true}, func(*conversations.Conversation) bool { return true })
	}
	l, _, err := m.pwcodeSession(ctx, r, t, c)
	var down *studioToolsError
	if errors.As(err, &down) {
		// Not silently without them: the chat's note says they are missing.
		l, err = m.startPlain(ctx, r, t, body, down.detail)
	}
	if err != nil {
		gone()
		return nil, false, err
	}
	m.Conv.SetPWCode(c.ID, l)
	if now := m.Conv.Get(c.ID); now != nil {
		c = now
	}
	if p.prompt != "" {
		if _, err := pwcode.SendMessage(ctx, t.client, l.Session, p.prompt); err != nil {
			_, _ = pwcode.ArchiveSession(ctx, t.client, l.Session)
			gone()
			return nil, false, pwErr(502, "The session started, but it did not take the message: "+apiMessage(err))
		}
		m.recordSent(r, c, t, p.prompt, 0)
	}
	return answer(c, l), true, nil
}

// startPlain starts a Code page session on the machine the Studio drives
// without its tools, which could not be reached: why is kept for the chat.
func (m *Module) startPlain(ctx context.Context, r *http.Request, t *pwTarget, body *jsonv.Object, why string) (*conversations.PWCodeLink, error) {
	req := body.Clone()
	req.Delete("prompt")
	req.Set("remoteControl", true)
	s, err := pwcode.CreateSession(auth.ContextWithUser(ctx, user(r)), t.client, req)
	if err != nil {
		return nil, err
	}
	id := jsonv.String(jsonv.Path(s, "id"))
	if id == "" {
		return nil, errors.New("pw code made no session")
	}
	l := &conversations.PWCodeLink{Site: t.site, Session: id, Workspace: t.workspace, CreatedAt: m.Conv.Now(), Continued: true, Picked: true, ToolsError: why}
	if st, err := pwcode.SessionState(ctx, t.client, id); err == nil {
		l.URL, l.Model = jsonv.String(jsonv.Path(st, "remoteUrl")), jsonv.String(jsonv.Path(st, "model"))
	}
	return l, nil
}

// personaElsewhere is why a persona does not go to another machine.
func personaElsewhere(persona, machine string) string {
	return "The " + persona + " persona works only on this Studio's own pw code machine: a session on " + machine + " has no way to take it from the Studio. Chat without it, or choose the Studio's machine."
}

// startMode is the permission mode a session on mach starts in: the one
// asked for, refused above the machine's remote ceiling, else the Studio's
// own for pw code, narrowed to the ceiling, else read-only.
func (m *Module) startMode(mach *jsonv.Object, name, asked string) (string, error) {
	ceiling, why := remoteCeiling(mach)
	if asked != "" {
		rank, ok := modeRank[asked]
		if !ok {
			return "", pwErr(400, "The Studio does not start a session in "+asked+" mode.")
		}
		if rank > modeRank[ceiling] {
			return "", pwErr(403, name+" takes at most "+ceiling+" from ACTIVATE"+why+". Choose a narrower mode.")
		}
		return asked, nil
	}
	mode := m.eff().PWCodePermissionMode
	if _, ok := modeRank[mode]; !ok {
		return "read-only", nil
	}
	if modeRank[mode] > modeRank[ceiling] {
		mode = ceiling
	}
	if mode == "auto" && !takesMode(mach, "auto") {
		mode = "accept-edits"
	}
	return mode, nil
}

// takesMode says whether mach's pw code lists mode; one that lists none is
// older than auto.
func takesMode(mach *jsonv.Object, mode string) bool {
	for _, v := range jsonv.Arr(jsonv.Path(mach, "permissionModes")) {
		if s, _ := v.(string); s == mode {
			return true
		}
	}
	return false
}

// remoteCeiling is the widest mode mach takes from ACTIVATE, and what to
// add to a refusal. It fails closed: an unread machine, or a ceiling the
// Studio does not rank, is taken to allow read-only.
func remoteCeiling(mach *jsonv.Object) (ceiling, why string) {
	if mach == nil {
		return "read-only", " (the Studio could not read the machine's limit from ACTIVATE, so it allows read-only)"
	}
	ceiling = firstNonEmpty(jsonv.String(jsonv.Path(mach, "remoteMaxPermissionMode")), defaultRemoteCeiling)
	if _, ok := modeRank[ceiling]; !ok {
		return "read-only", " (ACTIVATE names a limit the Studio does not know, " + ceiling + ", so it allows read-only)"
	}
	return ceiling, ""
}

func (m *Module) pwcodeStartRoutes(h func(pattern string, fn func(http.ResponseWriter, *http.Request) error)) {
	codesessions.StartInChat = m.startFromCode
	h("GET /api/chat/pwcode/start", m.startListing)
	h("GET /api/chat/pwcode/start/machines/{machine}/directories", m.startDirectories)
	h("GET /api/chat/pwcode/start/machines/{machine}/palette", m.startPalette)
}

// startListing is GET /api/chat/pwcode/start?persona=: the machines a new
// pw code chat of the viewer's may start on, the one the Studio drives for
// them first, the folder it starts in by default, and the folders their
// sessions worked in lately. Where pw code is not offered to them, there
// are none, and nothing is asked of the platform.
func (m *Module) startListing(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if why := m.pwcodeUnavailable(ctx, r); why != "" {
		return reqbody.OK(w, reqbody.O("available", false, "reason", why, "machines", []any{}, "sessions", []any{}))
	}
	// The person's daemon and the platform are read together: either can
	// take a while, and a daemon that does not answer holds nothing up.
	hereCh := m.startHere(r)
	var ms, ss []*jsonv.Object
	if m.relayUnavailable(r) == "" {
		ms, ss, _ = codesessions.PlatformListing(r, m.pf)
		m.sawReleases(r, ms)
	}
	here := <-hereCh
	own := m.startMachine(r, here)
	machines, recent := []any{own}, []any{}
	add := func(machine string, s *jsonv.Object) {
		if ws := jsonv.String(jsonv.Path(s, "workspace")); ws != "" {
			recent = append(recent, jsonv.NewObject("machineId", machine, "id", jsonv.String(jsonv.Path(s, "id")), "workspace", ws, "updatedAt", jsonv.String(jsonv.Path(s, "updatedAt"))))
		}
	}
	// The machine the Studio drives is listed once, whatever the platform
	// calls it.
	ownID := ""
	for _, mach := range ms {
		if m.startIsOwn(mach, here) {
			ownID = jsonv.String(jsonv.Path(mach, "id"))
			for _, k := range []string{"kind", "imageUrl", "csp", "schedulerType", "os"} {
				if v := jsonv.Path(mach, k); v != nil {
					own.Set(k, v)
				}
			}
			continue
		}
		machines = append(machines, codesessions.ClaudeCodeShown(r, mach))
	}
	for _, s := range ss {
		id := jsonv.String(jsonv.Path(s, "machineId"))
		if id == ownID && id != "" {
			id = directMachine
		}
		add(id, s)
	}
	if ownID == "" && here != nil {
		if list, err := pwcode.ListSessions(ctx, here.client); err == nil {
			for _, x := range sessionRows(list) {
				if o := jsonv.Obj(x); o != nil {
					add(directMachine, o)
				}
			}
		}
	}
	persona := extensions.NormalizeName(r.URL.Query().Get("persona"))
	return reqbody.OK(w, reqbody.O("available", true, "machines", machines, "sessions", recent,
		"machine", directMachine, "folder", m.startFolder(r, persona)))
}

// startHereWait is how long the composer's machines wait for the person's
// own daemon, which under sign-in a pw ssh reaches.
const startHereWait = 5 * time.Second

// startHere asks the viewer's daemon who it is, answering nil when it has
// not said in time; what it says later is kept for the next read.
func (m *Module) startHere(r *http.Request) <-chan *pwHere {
	got, out := make(chan *pwHere, 1), make(chan *pwHere, 1)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	// A request of its own: resolving the viewer writes to the request.
	own := r.WithContext(ctx)
	go func() {
		defer cancel()
		h, _ := m.pwcodeHereCached(ctx, own)
		got <- h
	}()
	go func() {
		select {
		case h := <-got:
			out <- h
		case <-time.After(startHereWait):
			out <- nil
		}
	}()
	return out
}

// startMachine is the machine the Studio drives for the viewer, as the
// composer's picker takes it: a session starts there directly, whatever
// the machine takes from ACTIVATE.
func (m *Module) startMachine(r *http.Request, here *pwHere) *jsonv.Object {
	name, signIn := "", m.A.Cfg.AuthEnabled()
	var st any
	if here != nil {
		name, st = here.hostname, here.status
	}
	if name == "" {
		if signIn {
			name = agentSiteName(userlibs.HomeCluster())
		} else {
			name, _ = os.Hostname()
		}
	}
	own := m.ownMachine(r, name, st)
	if signIn {
		own.Set("kind", "existing")
	}
	own.Set("remoteStart", true)
	own.Set("directories", true)
	return own
}

// agentSiteName is a site's last part (pw://user/name is name).
func agentSiteName(site string) string {
	return site[strings.LastIndex(strings.TrimSuffix(site, "/"), "/")+1:]
}

// startIsOwn: the platform's machine is the one the Studio drives for the
// viewer, by its daemon, or, where that does not answer, by the cluster's
// name.
func (m *Module) startIsOwn(mach *jsonv.Object, here *pwHere) bool {
	str := func(k string) string { return jsonv.String(jsonv.Path(mach, k)) }
	if here != nil {
		return sameMachine(&codesessions.Machine{ID: str("id"), DaemonID: str("daemonId"), Hostname: str("hostname")}, here)
	}
	return m.A.Cfg.AuthEnabled() && str("kind") != "local" && sameCluster(str("name"), userlibs.HomeCluster())
}

// startDirectories lists the folders of one directory for the composer's
// folder picker, as the viewer: on the machine the Studio drives for them,
// or on another of theirs through the platform.
func (m *Module) startDirectories(w http.ResponseWriter, r *http.Request) error {
	machine, q := r.PathValue("machine"), r.URL.Query()
	if why := m.pwcodeUnavailable(r.Context(), r); why != "" {
		return writeProblem(w, http.StatusConflict, why)
	}
	client, err := m.machineTarget(r, machine)
	if err != nil {
		return writeErr(w, err)
	}
	listsItself := machine != directMachine
	if !listsItself {
		if here, err := m.pwcodeHereCached(r.Context(), r); err == nil {
			listsItself = jsonv.Path(here.status, "directories") == true
		}
	}
	if !listsItself {
		v, err := m.listFolder(r, q.Get("path"), q.Get("filter"))
		if err != nil {
			return writeErr(w, err)
		}
		return reqbody.OK(w, v)
	}
	v, err := client.Call(r.Context(), http.MethodGet, "/v1/directories?"+url.Values{"path": {q.Get("path")}, "filter": {q.Get("filter")}}.Encode(), nil, 30*time.Second)
	if err != nil {
		return writeErr(w, err)
	}
	return reqbody.OK(w, v)
}

// listFolder lists dir's folders on the machine the Studio drives for the
// viewer, where pw code does not list folders itself: on a Studio of one,
// this computer's, read by the Studio, which runs as the person; under
// sign-in, the cluster's, over pw ssh with the person's own credential and
// as their account there.
func (m *Module) listFolder(r *http.Request, dir, filter string) (*folders.Listing, error) {
	var l *folders.Listing
	if !m.A.Cfg.AuthEnabled() {
		l = folders.Local(dir)
	} else {
		key, site := m.viewerPlatformKey(r), m.pwSite()
		if key == "" || site == "" {
			return nil, pwErr(409, "pw code runs as you: sign in with ACTIVATE under Settings, Accounts.")
		}
		res, err := m.bridgeRun()(r.Context(), site, key, []string{"sh", "-c", folders.Script, "studio-folders", dir}, 30*time.Second)
		if err != nil {
			return nil, pwErr(502, "The Studio could not reach "+agentSiteName(site)+" as you: "+firstLine(apiMessage(err), 300))
		}
		l = folders.Parse(res.Stdout)
	}
	if l == nil {
		return nil, errNotListed(dir)
	}
	return l.Filter(filter), nil
}

func errNotListed(dir string) error {
	return pwErr(http.StatusBadRequest, dir+" is not a folder your account there can read.")
}
