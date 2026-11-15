package transfers

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/mcpclient"
)

// CONDUIT is an open source parallel transfer
// service between storage systems, reached through its MCP endpoint, an
// MCP server attached in Settings and named in Settings, Transfers.
//
//   - Only COPY is ever sent, for a move too. A move is the Studio's: it
//     removes the originals itself after checking the copy, since CONDUIT
//     can report success on a bad result (a large file left under a
//     temporary name), and with MOVE that would cost the source.
//   - Both ends must be in its namespace: a conduit location (typed paths)
//     or a Studio location with a mount that says where it sits there. The
//     destination must be a mounted Studio location, since Studio walks the
//     staging folder like any other.
//   - Its word is not the result; start_transfer accepts transfers it later
//     refuses, so the job reads get_transfer_status and then checks.
//   - It has no way to stop a transfer over MCP, so cancel says so.

// ConduitCaller calls a CONDUIT service's MCP tools.
type ConduitCaller interface {
	// Ready says whether the server is attached and offers the tools.
	Ready(ctx context.Context, server string) (bool, string)
	Call(ctx context.Context, server, tool string, args any) (mcpclient.Result, error)
	// Servers is every attached MCP server and whether it answers.
	Servers(ctx context.Context) []MCPServer
}

// MCPConduit is a ConduitCaller over the server's MCP client.
type MCPConduit struct{ C *mcpclient.Client }

var conduitNeeds = []string{"start_transfer", "get_transfer_status"}

func (m MCPConduit) Ready(ctx context.Context, server string) (bool, string) {
	if !m.C.HasServer(server) {
		return false, "no MCP server named " + server + " is attached in Settings"
	}
	tools, err := m.C.ServerTools(ctx, server)
	if err != nil {
		return false, "the service did not answer: " + err.Error()
	}
	for _, n := range conduitNeeds {
		found := false
		for _, t := range tools {
			found = found || t == n
		}
		if !found {
			return false, "the service has no " + n + " tool"
		}
	}
	return true, ""
}

func (m MCPConduit) Servers(ctx context.Context) []MCPServer {
	out := []MCPServer{}
	for _, st := range m.C.Status(ctx) {
		out = append(out, MCPServer{ID: st.Name, Label: st.Name, Connected: st.Error == nil})
	}
	return out
}

func (m MCPConduit) Call(ctx context.Context, server, tool string, args any) (mcpclient.Result, error) {
	return m.C.CallTool(ctx, server, tool, args, 30*time.Second)
}

type conduitMover struct{ s *Service }

func newConduitMover(s *Service) *conduitMover { return &conduitMover{s: s} }

func (c *conduitMover) Name() string { return MoverConduit }
func (c *conduitMover) PollEvery() time.Duration {
	if ms, err := strconv.Atoi(c.s.d.Getenv("STUDIO_CONDUIT_POLL_MS")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return 3 * time.Second
}

// conduitPath is where a location's path sits in a server's namespace, by
// the longest mount that covers it; ok false when none does.
func conduitPath(cs ConduitServer, locID, rel string) (string, bool) {
	best, bestLen := "", -1
	for _, m := range cs.Mounts {
		if m.Location == locID && within(rel, m.Path) && len(m.Path) > bestLen {
			best = path.Join(m.Conduit, strings.TrimPrefix(strings.TrimPrefix(rel, m.Path), "/"))
			bestLen = len(m.Path)
		}
	}
	return best, bestLen >= 0
}

// serverFor is the CONDUIT server both ends of a plan sit in, and why not.
func (c *conduitMover) serverFor(p *planRec) (ConduitServer, string) {
	if c.s.d.Conduit == nil {
		return ConduitServer{}, "no CONDUIT service is set up"
	}
	if p.src == nil || p.dst == nil {
		return ConduitServer{}, "no CONDUIT service is set up"
	}
	cs, on := p.set.conduit()
	if !on {
		return ConduitServer{}, "no CONDUIT service is switched on in Settings, Transfers"
	}
	srcOK := p.src.conduit != nil && p.src.conduit.Server == cs.Server
	if !srcOK {
		srcOK = len(p.items) > 0
		for _, it := range p.items {
			if _, ok := conduitPath(cs, p.src.ID, it); !ok {
				srcOK = false
			}
		}
	}
	if _, dstOK := conduitPath(cs, p.dst.ID, p.dstPath); srcOK && dstOK {
		return cs, ""
	}
	return ConduitServer{}, "both ends must be in a CONDUIT service's namespace (Settings, Transfers, mounts)"
}

func (c *conduitMover) CanReach(ctx context.Context, p *planRec) (bool, string) {
	if p.owner != "" {
		return false, "one token runs every transfer as one account, so a Studio with sign-in on does not use it"
	}
	cs, why := c.serverFor(p)
	if why != "" {
		return false, why
	}
	if p.Skipped.Hidden > 0 || p.Skipped.Links > 0 {
		return false, "it copies whole folders, and a folder here holds hidden entries or links that would go too"
	}
	if p.Action == ActionSync {
		return false, "it copies whole folders and cannot send only what changed"
	}
	if ok, why := c.s.d.Conduit.Ready(ctx, cs.Server); !ok {
		return false, why
	}
	return true, "both ends are on storage the CONDUIT service " + cs.Server + " serves"
}

// conduitRef is a mover reference: server and transfer id.
func conduitRef(server, id string) string { return server + "\x00" + id }

func (c *conduitMover) Start(ctx context.Context, j *MoveJob) (string, error) {
	p := &planRec{src: j.SrcLoc, dst: j.DstLoc, items: j.Items, dstPath: parentOf(j.Stage), set: j.Set}
	cs, why := c.serverFor(p)
	if why != "" {
		return "", errors.New(why)
	}
	var source []string
	for _, it := range j.Items {
		if j.SrcLoc.conduit != nil {
			source = append(source, it)
			continue
		}
		cp, _ := conduitPath(cs, j.SrcLoc.ID, it)
		source = append(source, cp)
	}
	dest, _ := conduitPath(cs, j.DstLoc.ID, j.Stage)
	if len(strings.Join(source, "\x00")) > 120000 {
		return "", errors.New("the list of paths is longer than CONDUIT accepts; name their folder")
	}
	res, err := c.s.d.Conduit.Call(ctx, cs.Server, "start_transfer", map[string]any{
		"action": "COPY", "source": source, "destination": dest + "/", "options": map[string]any{"recursive": true},
	})
	if err != nil {
		return "", errors.New("CONDUIT: " + err.Error())
	}
	var said struct {
		Submitted  *bool  `json:"submitted"`
		TransferID string `json:"transfer_id"`
	}
	if len(res.Structured) > 0 {
		json.Unmarshal(res.Structured, &said)
	}
	id := said.TransferID
	if id == "" {
		if m := regexp.MustCompile(`transfer_id=([\w-]+)`).FindStringSubmatch(res.Text); m != nil {
			id = m[1]
		}
	}
	if res.IsError || (said.Submitted != nil && !*said.Submitted) || id == "" {
		return "", errors.New("CONDUIT did not take the transfer: " + shortLine(res.Text))
	}
	return conduitRef(cs.Server, id), nil
}

func shortLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		s = "no reason given"
	}
	return s
}

var unitBytes = map[byte]float64{'b': 1, 'k': 1 << 10, 'm': 1 << 20, 'g': 1 << 30, 't': 1 << 40, 'p': 1 << 50}
var dataRe = regexp.MustCompile(`(?i)^\s*([\d.]+)\s*([bkmgtp])`)

// dataBytes reads dataTransferred, text whose unit follows the amount.
func dataBytes(s string) int64 {
	m := dataRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return int64(f * unitBytes[strings.ToLower(m[2])[0]])
}

// conduitStatus reads a get_transfer_status answer.
func conduitStatus(res mcpclient.Result) (MoverStatus, error) {
	var r struct {
		State   string `json:"state"`
		Active  *bool  `json:"active"`
		Details struct {
			Error            string `json:"error"`
			ErrorMessage     string `json:"errorMessage"`
			FilesTransferred any    `json:"filesTransferred"`
			DataTransferred  string `json:"dataTransferred"`
		} `json:"details"`
	}
	if len(res.Structured) > 0 {
		json.Unmarshal(res.Structured, &r)
	}
	if r.State == "" {
		if m := regexp.MustCompile(`state=(\w+)`).FindStringSubmatch(res.Text); m != nil {
			r.State = m[1]
		}
	}
	st := MoverStatus{Bytes: dataBytes(r.Details.DataTransferred)}
	switch v := r.Details.FilesTransferred.(type) {
	case float64:
		st.Files = int64(v)
	case string:
		st.Files, _ = strconv.ParseInt(v, 10, 64)
	}
	switch r.State {
	case "TRANSFER_FINALIZED":
		st.State = "done"
	case "TRANSFER_ABORTED", "TRANSFER_ABORT":
		st.State = "cancelled"
	case "TRANSFER_ERROR":
		msg := r.Details.ErrorMessage
		if msg == "" {
			msg = r.Details.Error
		}
		st.State, st.Message = "failed", "CONDUIT: "+shortLine(msg)
	case "":
		return st, errors.New("its status answer gave no state")
	default:
		st.State = "running"
	}
	return st, nil
}

func (c *conduitMover) Status(ctx context.Context, ref string) (MoverStatus, error) {
	server, id, _ := strings.Cut(ref, "\x00")
	res, err := c.s.d.Conduit.Call(ctx, server, "get_transfer_status", map[string]any{"transfer_id": id})
	if err != nil {
		return MoverStatus{}, err
	}
	if res.IsError {
		return MoverStatus{}, errors.New(shortLine(res.Text))
	}
	return conduitStatus(res)
}

// Cancel: abort is in CONDUIT's API but not among its MCP tools.
func (c *conduitMover) Cancel(ref string) bool { return false }

// conduitTempName is the name pftool leaves a large file under when it
// skips the final rename (name+YYYYMMDD_HHMMSS).
var conduitTempName = regexp.MustCompile(`\+\d{8}_\d{6}$`)
