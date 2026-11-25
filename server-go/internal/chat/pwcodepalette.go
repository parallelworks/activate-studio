package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/extensions"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// What "/" lists in a pw code chat. A session lists its own commands; a
// chat with none yet lists what pw code has on the machine it starts on:
// the skills, custom commands and prompts of the folder there, which the
// daemon gives for any folder, and pw code's own commands, which it gives
// only for a live session. Those are the same in every session of one pw
// code release, so the Studio keeps the list a session last gave, by
// release, and until it has one, the few the session view runs itself.

// terminalCommands are pw code's own that answer only in its terminal: a
// session driven from elsewhere refuses them, or says to run them there.
var terminalCommands = []string{"agents", "approve", "auth", "rewind", "settings"}

// notListed are the commands "/" leaves out: those the Studio does not
// send to a session, by any name the list gives them, and the terminal's.
func notListed(commands []any) []any {
	out := slices.Clone(terminalCommands)
	for n := range refusedAmong(commands) {
		out = append(out, n)
	}
	slices.Sort(out)
	list := make([]any, len(out))
	for i, n := range out {
		list[i] = n
	}
	return list
}

// seedCommands are pw code's own that the session view runs by itself, by
// routes and pickers of their own, with /help: listed until a session has
// said what pw code has.
var seedCommands = []any{
	jsonv.NewObject("name", "clear", "usage", "/clear", "description", "Clear conversation and start fresh"),
	jsonv.NewObject("name", "compact", "usage", "/compact [focus]", "description", "Manually compact conversation history, optionally focusing the summary"),
	jsonv.NewObject("name", "goal", "usage", "/goal [condition|edit|clear|pause|resume]", "description", "Keep working toward a condition, checked after every turn, until it is met"),
	jsonv.NewObject("name", "help", "usage", "/help", "description", "Show available commands"),
	jsonv.NewObject("name", "model", "usage", "/model [model-name]", "description", "Show or change the current model"),
	jsonv.NewObject("name", "plan", "usage", "/plan [open|<description>]", "description", "Enable plan mode, or view the current session plan"),
}

// keptReleases is how many releases' commands are kept.
const keptReleases = 8

// pwCommandStore keeps pw code's own commands, by release.
type pwCommandStore struct {
	file string
	mu   sync.Mutex
	all  map[string]*pwRelease
}

type pwRelease struct {
	At       string          `json:"at"`
	Commands json.RawMessage `json:"commands"`
}

func (m *Module) pwCommands() *pwCommandStore {
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	if m.pw.commands == nil {
		m.pw.commands = &pwCommandStore{file: filepath.Join(m.A.Cfg.IndexBase, "pwcode-commands.json")}
	}
	return m.pw.commands
}

func (s *pwCommandStore) load() {
	if s.all != nil {
		return
	}
	s.all = map[string]*pwRelease{}
	if b, err := os.ReadFile(s.file); err == nil {
		_ = json.Unmarshal(b, &s.all)
	}
}

func release(version string) string { return strings.TrimPrefix(strings.TrimSpace(version), "v") }

// of is pw code's own commands in a release, and whether a session of that
// release gave them: those another gave last otherwise.
func (s *pwCommandStore) of(version string) (list []any, exact bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	best := s.all[release(version)]
	exact = best != nil && release(version) != ""
	if !exact {
		best = nil
		for _, r := range s.all {
			if r != nil && (best == nil || r.At > best.At) {
				best = r
			}
		}
	}
	if best != nil {
		if v, err := jsonv.Decode(best.Commands); err == nil {
			list = jsonv.Arr(v)
		}
	}
	if len(list) == 0 {
		return seedCommands, false
	}
	return list, exact
}

// every is pw code's own commands in all the releases kept.
func (s *pwCommandStore) every() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	var out []any
	for _, r := range s.all {
		if r == nil {
			continue
		}
		if v, err := jsonv.Decode(r.Commands); err == nil {
			out = append(out, jsonv.Arr(v)...)
		}
	}
	return out
}

// learn keeps the commands of a session's palette for its release.
func (s *pwCommandStore) learn(version string, palette any) {
	list := ownCommands(palette)
	key := release(version)
	if key == "" || len(list) == 0 {
		return
	}
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	if had := s.all[key]; had != nil && string(had.Commands) == string(b) {
		return
	}
	s.all[key] = &pwRelease{At: time.Now().UTC().Format(time.RFC3339Nano), Commands: b}
	for len(s.all) > keptReleases {
		oldest := ""
		for k, r := range s.all {
			if oldest == "" || r == nil || r.At < s.all[oldest].At {
				oldest = k
			}
			if r == nil {
				break
			}
		}
		delete(s.all, oldest)
	}
	if b, err = json.Marshal(s.all); err != nil {
		return
	}
	tmp := s.file + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.file)
	}
}

func commandName(v any) string { return jsonv.String(jsonv.Path(v, "name")) }

// ownCommands is pw code's own among a session's palette: not the skills
// and custom commands its folder or its person has, nor an MCP server's
// prompts.
func ownCommands(palette any) []any {
	mine := map[string]bool{"": true, "claude-code": true}
	for _, p := range jsonv.Arr(jsonv.Path(palette, "prompts")) {
		mine[jsonv.String(p)] = true
	}
	var out []any
	for _, c := range jsonv.Arr(jsonv.Path(palette, "commands")) {
		if n := commandName(c); !mine[n] && jsonv.Path(c, "mcp") != true && !strings.HasPrefix(n, "mcp__") {
			out = append(out, c)
		}
	}
	return out
}

// learnFrom keeps what a chat's session listed. One that runs on Claude
// Code lists its own.
func (m *Module) learnFrom(r *http.Request, l *conversations.PWCodeLink, palette any) {
	if l == nil || pwcode.IsClaudeModel(l.Model) {
		return
	}
	m.pwCommands().learn(m.releaseOn(r, proxyMachine(l)), palette)
}

// learnAtStart asks a session just made what it lists, once per release.
func (m *Module) learnAtStart(r *http.Request, c pwcode.Client, l *conversations.PWCodeLink) {
	if pwcode.IsClaudeModel(l.Model) {
		return
	}
	machine, route := proxyMachine(l), "/v1/sessions/"+url.PathEscape(l.Session)+"/palette"
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	// A request of its own: resolving the viewer writes to the request.
	own := r.WithContext(ctx)
	m.pw.bg.Go(func() {
		defer cancel()
		if machine == directMachine {
			_, _ = m.pwcodeHereCached(ctx, own)
		}
		version := m.releaseOn(own, machine)
		if _, exact := m.pwCommands().of(version); exact || version == "" {
			return
		}
		if v, err := c.Call(ctx, http.MethodGet, route, nil, paletteWait); err == nil {
			m.pwCommands().learn(version, v)
		}
	})
}

// releaseOn is the pw code release on a machine, as last seen there: ""
// where the Studio has not.
func (m *Module) releaseOn(r *http.Request, machine string) string {
	who := userID(r)
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	if machine == directMachine {
		if a, ok := m.pw.here[who]; ok {
			return jsonv.String(jsonv.Path(a.h.status, "version"))
		}
		return ""
	}
	return m.pw.releases[who+"\x00"+machine]
}

// sawReleases keeps the releases the platform lists the viewer's machines
// with.
func (m *Module) sawReleases(r *http.Request, machines []*jsonv.Object) {
	who := userID(r)
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	if m.pw.releases == nil {
		m.pw.releases = map[string]string{}
	}
	for _, mach := range machines {
		if id, v := jsonv.String(jsonv.Path(mach, "id")), jsonv.String(jsonv.Path(mach, "pwVersion")); id != "" {
			m.pw.releases[who+"\x00"+id] = v
		}
	}
}

// paletteWait is how long a machine has to say what it has.
const paletteWait = 15 * time.Second

// startPalette is GET /api/chat/pwcode/start/machines/{machine}/palette
// ?workspace=&model=&persona=: what a pw code chat that starts there can
// run, as the daemon's /v1/palette gives it with pw code's own commands.
// ?own=1 gives those alone, asking no machine.
func (m *Module) startPalette(w http.ResponseWriter, r *http.Request) error {
	machine, q := r.PathValue("machine"), r.URL.Query()
	if why := m.pwcodeUnavailable(r.Context(), r); why != "" {
		return writeProblem(w, http.StatusConflict, why)
	}
	client, err := m.machineTarget(r, machine)
	if err != nil {
		return writeErr(w, err)
	}
	if q.Get("own") != "" {
		return reqbody.OK(w, startListed(nil, m.ownList(r, machine), nil))
	}
	ctx, cancel := context.WithTimeout(r.Context(), paletteWait)
	defer cancel()
	// The listing says which release the machine runs, and which sessions
	// on it are live.
	type listing struct {
		mach *jsonv.Object
		rows []*jsonv.Object
	}
	listed := make(chan listing, 1)
	own := r.WithContext(ctx)
	go func() {
		var l listing
		defer func() { listed <- l }()
		if machine == directMachine {
			here, err := m.pwcodeHereCached(ctx, own)
			if err != nil {
				return
			}
			l.mach = m.ownMachine(own, here.hostname, here.status)
			if _, exact := m.pwCommands().of(m.releaseOn(own, machine)); !exact {
				if v, err := pwcode.ListSessions(ctx, here.client); err == nil {
					for _, x := range sessionRows(v) {
						if o := jsonv.Obj(x); o != nil {
							l.rows = append(l.rows, o)
						}
					}
				}
			}
			return
		}
		machines, rows, _ := codesessions.PlatformListing(own, m.pf)
		m.sawReleases(own, machines)
		for _, mach := range machines {
			if jsonv.String(jsonv.Path(mach, "id")) == machine {
				l.mach = codesessions.ClaudeCodeShown(own, mach)
			}
		}
		for _, s := range rows {
			if jsonv.String(jsonv.Path(s, "machineId")) == machine {
				l.rows = append(l.rows, s)
			}
		}
	}()
	asked := url.Values{"workspace": {m.paletteFolder(ctx, r, client, machine, q)}}
	if model := strings.TrimSpace(q.Get("model")); model != "" {
		asked.Set("model", model)
	}
	read := func() (any, error) {
		return client.Call(ctx, http.MethodGet, "/v1/palette?"+asked.Encode(), nil, paletteWait)
	}
	palette, err := read()
	// A folder the first message makes is not there to be read. What is
	// the person's own wherever they work is.
	var refused *pwcode.DaemonError
	if errors.As(err, &refused) && refused.Status == http.StatusUnprocessableEntity && asked.Get("workspace") != "/" {
		asked.Set("workspace", "/")
		palette, err = read()
	}
	if err != nil {
		m.A.Log.Info("pw code chat: what a machine has for a new chat could not be read", "user", userID(r), "machine", machine, "err", err.Error())
		palette = nil
	}
	l := <-listed
	if _, exact := m.pwCommands().of(m.releaseOn(r, machine)); !exact {
		m.learnLive(ctx, client, m.releaseOn(r, machine), l.rows)
	}
	return reqbody.OK(w, startListed(palette, m.ownList(r, machine), l.mach))
}

func (m *Module) ownList(r *http.Request, machine string) []any {
	list, _ := m.pwCommands().of(m.releaseOn(r, machine))
	return list
}

// startListed is a new chat's palette: pw code's own commands, then what
// the folder has, less those "/" leaves out.
func startListed(palette any, own []any, mach *jsonv.Object) *jsonv.Object {
	out, has := []any{}, map[string]bool{}
	for _, n := range notListed(slices.Concat(own, jsonv.Arr(jsonv.Path(palette, "commands")))) {
		has[n.(string)] = true
	}
	add := func(list []any) {
		for _, c := range list {
			if n := commandName(c); n != "" && !has[n] {
				has[n] = true
				out = append(out, c)
			}
		}
	}
	if jsonv.Path(mach, "claudeInstalled") == true && jsonv.Path(mach, "claudeCodeAllowed") == true {
		add([]any{jsonv.NewObject("name", "claude-code", "description", "Claude Code account")})
	}
	add(own)
	add(jsonv.Arr(jsonv.Path(palette, "commands")))
	prompts, mcp := jsonv.Arr(jsonv.Path(palette, "prompts")), jsonv.Arr(jsonv.Path(palette, "mcpPrompts"))
	if prompts == nil {
		prompts = []any{}
	}
	if mcp == nil {
		mcp = []any{}
	}
	return jsonv.NewObject("commands", out, "mcpPrompts", mcp, "prompts", prompts)
}

// paletteFolder is the folder a new chat would work in, as a full path.
func (m *Module) paletteFolder(ctx context.Context, r *http.Request, c pwcode.Client, machine string, q url.Values) string {
	f := strings.TrimSpace(q.Get("workspace"))
	if f == "" && machine == directMachine {
		f = m.startFolder(r, extensions.NormalizeName(q.Get("persona")))
	}
	if rest, ok := strings.CutPrefix(f, "~"); ok {
		v, err := c.Call(ctx, http.MethodGet, "/v1/directories?path=", nil, paletteWait)
		home := firstNonEmpty(jsonv.String(jsonv.Path(v, "home")), jsonv.String(jsonv.Path(v, "path")))
		if err != nil || home == "" {
			return "/"
		}
		f = home + rest
	}
	if !strings.HasPrefix(f, "/") {
		return "/"
	}
	return f
}

// learnTries is how many live sessions are asked at most.
const learnTries = 3

// learnLive asks a live session of rows what its pw code has. One that is
// not live is left: asking would take it up.
func (m *Module) learnLive(ctx context.Context, c pwcode.Client, version string, rows []*jsonv.Object) {
	if version == "" {
		return
	}
	tries := 0
	for _, s := range rows {
		str := func(k string) string { return jsonv.String(jsonv.Path(s, k)) }
		st := str("status")
		live := str("liveHost") != "" || (st != "" && st != "idle")
		if !live || st == "live_elsewhere" || str("archivedAt") != "" || str("id") == "" || pwcode.IsClaudeModel(str("model")) {
			continue
		}
		if v, err := c.Call(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(str("id"))+"/palette", nil, paletteWait); err == nil {
			m.pwCommands().learn(version, v)
			return
		}
		if tries++; tries == learnTries {
			return
		}
	}
}
