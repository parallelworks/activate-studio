package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// The assistant's transfers, over the transfers service (internal/
// transfers, docs/TRANSFERS.md): transfer_locations, browse_location and
// transfer_status read; transfer_files and cancel_transfer change
// something and wait for the user's approval as a held call does.
//
// transfer_files plans first, so a plan the service refuses goes back to
// the model with nothing asked. A held call is also a transfer record
// awaiting approval, which the Transfers page lists with its
// conversation. Approving queues the job and the call's result returns at
// once; the job runs apart from the reply, so neither the reply ending nor
// the page closing stops it. Declining cancels the record. When an
// approved transfer ends, one note goes into its conversation, written
// after any reply that is streaming there has been stored.

func init() {
	goExecutors["transfer_locations"] = (*Module).toolTransferLocations
	goExecutors["browse_location"] = (*Module).toolBrowseLocation
	goExecutors["transfer_status"] = (*Module).toolTransferStatus
	goExecutors["transfer_files"] = (*Module).toolTransferHeldOnly
	goExecutors["cancel_transfer"] = (*Module).toolTransferHeldOnly
}

// transferAsksFirst are the transfer tools whose calls wait for the
// user's approval.
var transferAsksFirst = map[string]bool{"transfer_files": true, "cancel_transfer": true}

func isTransferAsksFirst(name string) bool { return transferAsksFirst[name] }

// localAsksFirst are the held tools that act in the Studio itself, so
// they need no platform and no platform key: the library delete and the
// transfers.
func localAsksFirst(name string) bool { return isKBAsksFirst(name) || isTransferAsksFirst(name) }

const (
	// browseShown is how many entries of a folder browse_location returns.
	browseShown = 200
	// transferPathsMax is how many paths one transfer_files call may name.
	transferPathsMax = 200
)

// transfersLink is the Transfers page, at a transfer when id is given.
func transfersLink(id string) string {
	if id == "" {
		return "#view=transfers"
	}
	return "#view=transfers?transfer=" + id
}

/* ---- the service and the viewer ---- */

// transfers is the transfers service, its ends heard once it is first
// used.
func (m *Module) transfers() *transfers.Service {
	m.xferMu.Lock()
	defer m.xferMu.Unlock()
	if m.xfer == nil {
		m.xfer = m.xferOf()
		m.xfer.OnEnd(m.transferEnded)
	}
	return m.xfer
}

// transferViewer is who the transfer tools act as: the request's verified
// viewer, with their own platform key for clusters.
func (m *Module) transferViewer(r *http.Request) (transfers.Viewer, error) {
	return m.xferViewer(r)
}

// heldTransferViewer is the viewer of a held call answered or expired
// with no request at hand: enough to settle a record, never to reach a
// cluster.
func (m *Module) heldTransferViewer(viewer string) transfers.Viewer {
	auth := m.A.Cfg.AuthEnabled()
	if !auth {
		return transfers.Viewer{}
	}
	return transfers.Viewer{ID: viewer, AuthOn: true}
}

// errText is an error as the service says it to a person.
func errText(err error) string {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

// inChat is why a transfer tool does not answer here, "" in a chat.
func inChat(name string, ctx ToolCtx) string {
	if ctx.ConversationID == nil || *ctx.ConversationID == "" {
		return name + " answers only in a Studio chat."
	}
	return ""
}

/* ---- the reading tools ---- */

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func (m *Module) toolTransferLocations(r *http.Request, _ map[string]any, tc ToolCtx) (*Outcome, error) {
	if why := inChat("transfer_locations", tc); why != "" {
		return &Outcome{Result: why, Summary: "not available here", Failed: true}, nil
	}
	v, err := m.transferViewer(r)
	if err != nil {
		return &Outcome{Result: "The locations could not be read: " + errText(err), Summary: "error", Failed: true}, nil
	}
	locs := m.transfers().Locations(r.Context(), v)
	if len(locs) == 0 {
		return &Outcome{Result: "No transfer locations are available.", Summary: "no locations"}, nil
	}
	lines := []string{"Transfer locations (id, kind: label, where; writable, indexed, reachable):"}
	usable := 0
	for _, l := range locs {
		line := l.ID + ", " + l.Kind + ": " + l.Label
		if l.Detail != "" {
			line += ", " + l.Detail
		}
		line += "; " + yesNo(l.Writable, "writable", "read only") + ", " + yesNo(l.Indexed, "indexed", "not indexed")
		if !l.Browsable {
			line += ", paths typed by hand"
		}
		if l.Available {
			usable++
			line += ", reachable"
		} else {
			line += ", not reachable"
			if l.Why != "" {
				line += ": " + l.Why
			}
		}
		lines = append(lines, line)
	}
	return &Outcome{Result: capOutput(strings.Join(lines, "\n")), Summary: countOf(len(locs), "location", "locations") + ", " + itoaInt(usable) + " reachable"}, nil
}

func (m *Module) toolBrowseLocation(r *http.Request, args map[string]any, tc ToolCtx) (*Outcome, error) {
	if why := inChat("browse_location", tc); why != "" {
		return &Outcome{Result: why, Summary: "not available here", Failed: true}, nil
	}
	loc := jsTrim(argString(args, "location"))
	if loc == "" {
		return &Outcome{Result: "location is required: an id from transfer_locations.", Summary: "error", Failed: true}, nil
	}
	var at *string
	if p, ok := args["path"].(string); ok && jsTrim(p) != "" {
		s := jsTrim(p)
		at = &s
	}
	v, err := m.transferViewer(r)
	if err != nil {
		return &Outcome{Result: "The folder could not be listed: " + errText(err), Summary: "error", Failed: true}, nil
	}
	l, err := m.transfers().Browse(r.Context(), v, loc, at)
	if err != nil {
		return &Outcome{Result: "The folder could not be listed: " + errText(err), Summary: "error", Failed: true}, nil
	}
	where := l.Location + ":" + l.Path
	if l.Path == "" {
		where = l.Location + " (top)"
	}
	lines := []string{where}
	if l.Parent != nil {
		lines[0] += ", parent " + orStr(*l.Parent, "the top")
	}
	for i, e := range l.Entries {
		if i == browseShown {
			lines = append(lines, fmt.Sprintf("...and %d more not shown", len(l.Entries)-browseShown))
			break
		}
		switch e.Type {
		case "dir":
			lines = append(lines, e.Name+"/")
		case "link":
			lines = append(lines, e.Name+" (link, not transferred)")
		default:
			lines = append(lines, e.Name+"  "+byteText(e.Size))
		}
	}
	if len(l.Entries) == 0 {
		lines = append(lines, "(empty)")
	}
	if l.Truncated {
		lines = append(lines, "The folder has more entries than the listing returns.")
	}
	return &Outcome{Result: capOutput(strings.Join(lines, "\n")), Summary: countOf(len(l.Entries), "entry", "entries") + " in " + where}, nil
}

// transferLine is a transfer in a line: id, label, state and counts.
func transferLine(t transfers.Transfer) string {
	line := t.ID + "  " + t.Label + ": " + t.State
	files, bytes := t.Files.Total, t.Bytes.Total
	if files >= 0 {
		line += fmt.Sprintf(", files %d of %d", t.Files.Done, files)
	} else {
		line += fmt.Sprintf(", files %d (counting)", t.Files.Done)
	}
	if bytes >= 0 {
		line += ", " + byteText(t.Bytes.Done) + " of " + byteText(bytes)
	}
	if t.Replaced > 0 {
		line += fmt.Sprintf(", %d replaced", t.Replaced)
	}
	if t.Deleted > 0 {
		line += fmt.Sprintf(", %d deleted", t.Deleted)
	}
	if t.Error != "" {
		line += "; " + oneLine(t.Error, 200)
	}
	return line
}

func (m *Module) toolTransferStatus(r *http.Request, args map[string]any, tc ToolCtx) (*Outcome, error) {
	if why := inChat("transfer_status", tc); why != "" {
		return &Outcome{Result: why, Summary: "not available here", Failed: true}, nil
	}
	v, err := m.transferViewer(r)
	if err != nil {
		return &Outcome{Result: "Transfers could not be read: " + errText(err), Summary: "error", Failed: true}, nil
	}
	s := m.transfers()
	if id := jsTrim(argString(args, "id")); id != "" {
		t, err := s.Get(v, id)
		if err != nil {
			return &Outcome{Result: errText(err), Summary: "not found", Failed: true}, nil
		}
		return &Outcome{Result: transferLine(t), Summary: t.State}, nil
	}
	page, err := s.List(v, transfers.ListQuery{State: "active", Limit: 50})
	if err != nil {
		return &Outcome{Result: "Transfers could not be read: " + errText(err), Summary: "error", Failed: true}, nil
	}
	if len(page.Transfers) == 0 {
		return &Outcome{Result: "No transfers are active.", Summary: "none active"}, nil
	}
	lines := make([]string, len(page.Transfers))
	for i, t := range page.Transfers {
		lines[i] = transferLine(t)
	}
	return &Outcome{Result: capOutput(strings.Join(lines, "\n")), Summary: countOf(len(lines), "active transfer", "active transfers")}, nil
}

// toolTransferHeldOnly is a changing transfer tool reached outside a held
// call: from somewhere no one can approve it, so it does not run.
func (m *Module) toolTransferHeldOnly(*http.Request, map[string]any, ToolCtx) (*Outcome, error) {
	return &Outcome{Result: "This tool runs only after the user approves the call in a Studio chat. Nothing ran.", Summary: "needs the user's approval", Failed: true}, nil
}

/* ---- holding a call ---- */

// sortedJSON is v with its keys sorted, the form a held call's arguments
// are kept and compared in.
func sortedJSON(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// argObject is a call's arguments as an object, or false.
func argObject(raw any) (map[string]any, bool) {
	var v any
	switch x := raw.(type) {
	case string:
		if jsTrim(x) == "" {
			x = "{}"
		}
		if json.Unmarshal([]byte(x), &v) != nil {
			return nil, false
		}
	case json.RawMessage:
		if json.Unmarshal(x, &v) != nil {
			return nil, false
		}
	case nil:
		return nil, false
	default:
		b, err := json.Marshal(x)
		if err != nil || json.Unmarshal(b, &v) != nil {
			return nil, false
		}
	}
	o, ok := v.(map[string]any)
	return o, ok
}

// canonicalTransferArgs is a held transfer call's arguments as kept: what
// an answer's arguments are compared with. Nothing is checked here; the
// plan did that.
func canonicalTransferArgs(raw any) (*jsonobj.Object, string, string) {
	o, ok := argObject(raw)
	if !ok {
		return nil, "", "The arguments were not valid JSON."
	}
	s := sortedJSON(o)
	obj, _ := jsonobj.Parse([]byte(s))
	return obj, s, ""
}

func boolArg(args map[string]any, k string) (*bool, string) {
	v, ok := args[k]
	if !ok || v == nil {
		return nil, ""
	}
	switch b := v.(type) {
	case bool:
		return &b, ""
	case string:
		if b == "true" || b == "false" {
			t := b == "true"
			return &t, ""
		}
	}
	return nil, "The " + k + " argument must be true or false."
}

// transferRequest is a transfer_files call as a plan request, or what is
// wrong with it.
func transferRequest(args map[string]any) (transfers.Request, map[string]any, string) {
	var q transfers.Request
	action := strings.ToLower(jsTrim(argString(args, "action")))
	if action == "" {
		action = transfers.ActionCopy
	}
	switch action {
	case transfers.ActionCopy, transfers.ActionMove, transfers.ActionSync:
	default:
		return q, nil, "action must be copy, move or sync."
	}
	src, dst := jsTrim(argString(args, "source")), jsTrim(argString(args, "destination"))
	if src == "" || dst == "" {
		return q, nil, "source and destination are required: location ids from transfer_locations."
	}
	paths := pathArgs(args["paths"])
	if len(paths) == 0 {
		if action != transfers.ActionSync {
			return q, nil, "paths is required: the files or folders to send, relative to the source's root."
		}
		paths = []string{""}
	}
	if len(paths) > transferPathsMax {
		return q, nil, "At most " + itoaInt(transferPathsMax) + " paths per call; name their folders instead."
	}
	folder := folderArg(argString(args, "folder"))
	q.Action = action
	q.Source.Location, q.Source.Paths = src, paths
	q.Destination.Location, q.Destination.Path = dst, folder
	o := &q.Options
	if mv := jsTrim(argString(args, "mover")); mv != "" && mv != "auto" {
		switch mv {
		case transfers.MoverRsync, transfers.MoverCopy, transfers.MoverConduit, transfers.MoverRclone:
			o.Mover = mv
		default:
			return q, nil, "mover must be auto, rsync, rclone, studio-copy or conduit."
		}
	}
	for _, k := range []string{"replace", "deleteExtra", "checksum", "preserveTimes"} {
		b, problem := boolArg(args, k)
		if problem != "" {
			return q, nil, problem
		}
		if b == nil {
			continue
		}
		switch k {
		case "replace":
			o.Replace = *b
		case "deleteExtra":
			o.DeleteExtra = *b
		case "checksum":
			o.Checksum = b
		case "preserveTimes":
			o.PreserveTimes = b
		}
	}
	if o.DeleteExtra && action != transfers.ActionSync {
		return q, nil, "deleteExtra applies to sync only."
	}
	o.Label = jsval.Slice(jsTrim(argString(args, "label")), 120)
	list := make([]any, len(paths))
	for i, p := range paths {
		list[i] = p
	}
	kept := map[string]any{"action": action, "source": src, "paths": list, "destination": dst, "folder": folder}
	if o.Mover != "" {
		kept["mover"] = o.Mover
	}
	if o.Replace {
		kept["replace"] = true
	}
	if o.DeleteExtra {
		kept["deleteExtra"] = true
	}
	if o.Checksum != nil {
		kept["checksum"] = *o.Checksum
	}
	if o.PreserveTimes != nil {
		kept["preserveTimes"] = *o.PreserveTimes
	}
	if o.Label != "" {
		kept["label"] = o.Label
	}
	return q, kept, ""
}

// prepareTransfer plans a transfer_files call or finds the transfer a
// cancel_transfer call names. A refusal is the problem, with nothing held;
// otherwise the canonical arguments name the plan (transfer_files) or the
// transfer (cancel_transfer), and args carry what the card shows.
func (m *Module) prepareTransfer(r *http.Request, name, raw string) *Prepared {
	args, ok := argObject(raw)
	if !ok {
		return &Prepared{Problem: "The arguments were not valid JSON. Nothing ran."}
	}
	v, err := m.transferViewer(r)
	if err != nil {
		return &Prepared{Problem: "The transfer could not be prepared: " + errText(err) + " Nothing ran."}
	}
	s := m.transfers()
	if name == "cancel_transfer" {
		id := jsTrim(argString(args, "id"))
		if id == "" {
			return &Prepared{Problem: "id is required: the transfer's id. Nothing ran."}
		}
		t, err := s.Get(v, id)
		if err != nil {
			return &Prepared{Problem: errText(err) + "; check the id with transfer_status. Nothing ran."}
		}
		switch t.State {
		case transfers.StateSucceeded, transfers.StateFailed, transfers.StateCancelled, transfers.StateInterrupted:
			return &Prepared{Problem: "That transfer has already ended (" + t.State + "). Nothing ran."}
		case transfers.StateVerifying:
			return &Prepared{Problem: "That transfer is checking what arrived and cannot be cancelled; it ends in a moment. Nothing ran."}
		}
		canonical := sortedJSON(map[string]any{"id": t.ID})
		card, _ := json.Marshal(map[string]any{"id": t.ID, "label": t.Label, "state": t.State})
		return &Prepared{Args: card, Canonical: canonical}
	}
	q, kept, problem := transferRequest(args)
	if problem != "" {
		return &Prepared{Problem: problem + " Nothing ran."}
	}
	plan, err := s.Plan(r.Context(), v, q)
	if err != nil {
		return &Prepared{Problem: "The transfer was not planned: " + errText(err) + ". Nothing was asked and nothing ran."}
	}
	kept["plan"] = plan.ID
	card, _ := json.Marshal(map[string]any{"plan": plan})
	return &Prepared{Args: card, Canonical: sortedJSON(kept)}
}

// holdTransfer records a held transfer_files call as a transfer awaiting
// approval in its conversation, and is the canonical arguments with the
// plan's id given way to the transfer's, and the approval card.
func (m *Module) holdTransfer(r *http.Request, conv, toolCallID, tool, canonical string, cardArgs json.RawMessage, expiresAt string) (string, *jsonobj.Object, error) {
	held, _ := argObject(canonical)
	if held == nil {
		held = map[string]any{}
	}
	var card struct {
		Plan  *transfers.Plan `json:"plan"`
		ID    string          `json:"id"`
		Label string          `json:"label"`
		State string          `json:"state"`
	}
	_ = json.Unmarshal(cardArgs, &card)
	// A provider may send the same tool call id again in a later round:
	// the transfer an unanswered call with that id held is settled first.
	if old := m.held.get(conv, toolCallID); old != nil && old.Tool == "transfer_files" {
		m.dropHeldTransfer(old, "replaced by a new request in the conversation")
	}
	if tool == "cancel_transfer" {
		return canonical, cancelApprovalPart(card.ID, card.Label, card.State, toolCallID, expiresAt), nil
	}
	planID, _ := held["plan"].(string)
	if planID == "" || card.Plan == nil {
		return "", nil, fmt.Errorf("transfer_files held without its plan")
	}
	v, err := m.transferViewer(r)
	if err != nil {
		return "", nil, err
	}
	t, err := m.transfers().Hold(r.Context(), v, planID, conv)
	if err != nil {
		return "", nil, err
	}
	delete(held, "plan")
	held["transfer"] = t.ID
	return sortedJSON(held), transferApprovalPart(card.Plan, t.ID, toolCallID, expiresAt), nil
}

// heldTransferID is the transfer a held call names.
func heldTransferID(args string) string {
	o, _ := argObject(args)
	if o == nil {
		return ""
	}
	for _, k := range []string{"transfer", "id"} {
		if s, ok := o[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// dropHeldTransfer cancels the transfer record of a held transfer_files
// call that will not be answered; one already settled is left as it is.
func (m *Module) dropHeldTransfer(rec *HeldCall, why string) {
	if rec == nil || rec.Tool != "transfer_files" {
		return
	}
	id := heldTransferID(rec.Args)
	if id == "" {
		return
	}
	if err := m.transfers().Deny(m.heldTransferViewer(rec.Viewer), id); err == nil {
		m.A.Log.Info("held transfer settled", "transfer", id, "why", why)
	}
}

// expireHeldTransfer runs when a held transfer's approval has expired:
// the record, if nobody answered, leaves the waiting list.
func (m *Module) expireHeldTransfer(conv, toolCallID, transferID string) {
	rec := m.held.get(conv, toolCallID)
	if rec != nil && heldTransferID(rec.Args) == transferID {
		if t, ok := parseISO(rec.ExpiresAt); ok && t.After(m.held.now()) {
			return
		}
		m.held.release(rec)
	}
	m.dropHeldTransfer(&HeldCall{Tool: "transfer_files", Args: sortedJSON(map[string]any{"transfer": transferID}), Viewer: heldViewerOr(rec)}, "its approval expired")
}

func heldViewerOr(rec *HeldCall) string {
	if rec == nil {
		return ""
	}
	return rec.Viewer
}

/* ---- the approval card ---- */

var actionWord = map[string]string{transfers.ActionCopy: "Copy", transfers.ActionMove: "Move", transfers.ActionSync: "Sync"}

// planCount is a plan's files and folders in words.
func planCount(p *transfers.Plan) string {
	if p.Files < 0 {
		return "files counted when it starts"
	}
	return countOf(p.Files, "file", "files")
}

// endText is one end of a plan or a transfer: its label and folder.
func endText(e transfers.End) string {
	if e.Path != nil && *e.Path != "" {
		return e.Label + ": " + *e.Path
	}
	return e.Label
}

// transferApprovalPart is the approval card for a held transfer_files
// call: a title the package's block shows, and the plan the card draws
// as the Transfers page's confirm dialog does.
func transferApprovalPart(p *transfers.Plan, transferID, toolCallID, expiresAt string) *jsonobj.Object {
	size := ""
	if p.Bytes >= 0 && p.Files > 0 {
		size = " (" + byteText(p.Bytes) + ")"
	}
	header := actionWord[p.Action] + " " + planCount(p) + size + " to " + endText(p.Destination) + "?"
	reason := "It runs as a transfer on the Transfers page. The chat does not wait for it, and a note is added here when it ends."
	if p.Action == transfers.ActionMove {
		reason += " The originals are removed once every file is checked at the destination."
	}
	if p.DeletesCount > 0 {
		reason += " Files at the destination that the source does not have are deleted."
	}
	o := reqbody.O("kind", "approval", "id", toolCallID, "approvalKind", "confirm", "toolName", "transfer_files", "subject", "call", "header", header, "reason", reason)
	o.SetValue("canAllowDirectory", false)
	o.SetValue("expiresAt", expiresAt)
	o.SetValue("transfer", reqbody.O("id", transferID, "link", transfersLink(transferID), "plan", p))
	return o
}

func cancelApprovalPart(id, label, state, toolCallID, expiresAt string) *jsonobj.Object {
	reason := "Stops it and clears what it had sent so far; nothing at the destination is replaced or deleted."
	if state == transfers.StateAwaiting {
		reason = "It has not started; cancelling it settles its approval as declined."
	}
	o := reqbody.O("kind", "approval", "id", toolCallID, "approvalKind", "confirm", "toolName", "cancel_transfer", "subject", "call",
		"header", "Cancel the transfer "+orStr(label, id)+"?", "command", id, "reason", reason)
	o.SetValue("canAllowDirectory", false)
	o.SetValue("expiresAt", expiresAt)
	return o
}

/* ---- answering a held call ---- */

// answerTransfer runs an approved transfer call, or settles a declined
// one. An approved transfer_files call returns once the job is queued.
func (m *Module) answerTransfer(r *http.Request, rec *HeldCall, allowed bool) (result, summary string, failed bool) {
	id := heldTransferID(rec.Args)
	s := m.transfers()
	if !allowed {
		if rec.Tool == "transfer_files" && id != "" {
			v, err := m.transferViewer(r)
			if err != nil {
				v = m.heldTransferViewer(rec.Viewer)
			}
			_ = s.Deny(v, id)
		}
		return "", "", false
	}
	v, err := m.transferViewer(r)
	if err != nil {
		return "Nothing ran: " + errText(err), "not run", true
	}
	if rec.Tool == "cancel_transfer" {
		ok, why := s.Cancel(v, id)
		if !ok {
			return "The transfer was not cancelled: " + why + ".", "not cancelled", true
		}
		return "Cancelled transfer " + id + ".", "cancelled " + shortTransferID(id), false
	}
	m.markApproved(id)
	t, err := s.Approve(context.WithoutCancel(r.Context()), v, id)
	if err != nil {
		m.unmarkApproved(id)
		why := errText(err)
		return "The transfer did not start: " + why + ". Nothing ran; plan it again if the user still wants it.", "not started: " + jsval.Slice(why, 160), true
	}
	result = fmt.Sprintf("Transfer %s started: %s, %s from %s to %s by %s. It runs on its own and the Transfers page (%s) shows its progress. "+
		"Do not wait for it or check on it unless the user asks: a note is added to this conversation when it ends.",
		t.ID, t.Label, transferSize(t), strings.Join(sourceNames(t.Source), ", "), endText(t.Destination), t.Mover, transfersLink(t.ID))
	result += indexNoteText(t.IndexNotes)
	return result, "started transfer " + shortTransferID(t.ID), false
}

func shortTransferID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func sourceNames(e transfers.End) []string {
	if len(e.Paths) == 0 || (len(e.Paths) == 1 && e.Paths[0] == "") {
		return []string{e.Label}
	}
	out := make([]string, 0, len(e.Paths))
	for i, p := range e.Paths {
		if i == 5 {
			out = append(out, fmt.Sprintf("%d more", len(e.Paths)-5))
			break
		}
		out = append(out, e.Label+": "+p)
	}
	return out
}

func transferSize(t transfers.Transfer) string {
	if t.Files.Total < 0 {
		return "files counted as it runs"
	}
	s := countOf(int(t.Files.Total), "file", "files")
	if t.Bytes.Total >= 0 {
		s += " (" + byteText(t.Bytes.Total) + ")"
	}
	return s
}

/* ---- the note when a transfer ends ---- */

func (m *Module) markApproved(id string) {
	m.xferMu.Lock()
	m.approved[id] = true
	m.xferMu.Unlock()
}

func (m *Module) unmarkApproved(id string) {
	m.xferMu.Lock()
	delete(m.approved, id)
	m.xferMu.Unlock()
}

// transferEnded writes the note for an assistant's transfer that ran, once.
// A held transfer declined, expired or replaced before it was approved
// gets none: its card already says so.
func (m *Module) transferEnded(t transfers.Transfer) {
	if t.StartedBy != transfers.ByAssistant || t.ConversationID == "" {
		return
	}
	m.xferMu.Lock()
	ran := m.approved[t.ID] || t.StartedAt != "" || t.State != transfers.StateCancelled
	delete(m.approved, t.ID)
	seen := m.noted[t.ID]
	m.noted[t.ID] = true
	m.xferMu.Unlock()
	if !ran || seen {
		return
	}
	m.notes.add(t.ConversationID, transferNote(t), func(conv, text string) {
		if !m.Conv.AppendNote(conv, text) {
			m.A.Log.Warn("transfer note not delivered: no such conversation", "conversation", conv, "transfer", t.ID)
		}
	})
}

// oneLine is s on one line, at most n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if jsval.Len(s) > n {
		s = jsval.Slice(s, n) + "..."
	}
	return s
}

// transferNote is the conversation's note for a transfer that ended. A
// reason, which may carry a host's own words, is set as code so it is
// read as quoted text.
func transferNote(t transfers.Transfer) string {
	files := t.Files.Total
	if files < 0 {
		files = t.Files.Done
	}
	what := actionWord[t.Action] + " of " + countOf(int(files), "file", "files") + " to " + endText(t.Destination)
	quoted := func(s string) string {
		return "`" + strings.ReplaceAll(oneLine(s, 200), "`", "'") + "`"
	}
	switch t.State {
	case transfers.StateSucceeded:
		extra := []string{byteText(t.Bytes.Done)}
		if t.Replaced > 0 {
			extra = append(extra, itoaInt(t.Replaced)+" replaced")
		}
		if t.Deleted > 0 {
			extra = append(extra, itoaInt(t.Deleted)+" deleted")
		}
		return what + " finished: " + strings.Join(extra, ", ") + "." + indexNoteText(t.IndexNotes)
	case transfers.StateCancelled:
		return what + " was cancelled."
	case transfers.StateInterrupted:
		if t.Error != "" {
			return what + " was interrupted: " + quoted(t.Error) + "."
		}
		return what + " was interrupted."
	default:
		if t.Error != "" {
			return what + " failed: " + quoted(t.Error) + "."
		}
		return what + " failed."
	}
}

// indexNoteText is a transfer's index notes as sentences after a space:
// when a library other than the main one shows the change.
func indexNoteText(notes []string) string {
	out := ""
	for _, n := range notes {
		out += " " + n + "."
	}
	return out
}

// byteText is a size as the Transfers page says it.
func byteText(n int64) string {
	const unit = 1024
	if n < unit {
		return countOf(int(n), "byte", "bytes")
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// noteQueue keeps a conversation's notes back while a reply streams
// there, so a note lands after the reply is stored, on the reply's
// branch, never between the question and its answer.
type noteQueue struct {
	mu        sync.Mutex
	answering map[string]int
	pending   map[string][]func()
}

func newNoteQueue() *noteQueue {
	return &noteQueue{answering: map[string]int{}, pending: map[string][]func(){}}
}

// begin marks a reply streaming in conv until the returned func runs,
// which writes the notes held back.
func (q *noteQueue) begin(conv string) func() {
	if conv == "" {
		return func() {}
	}
	q.mu.Lock()
	q.answering[conv]++
	q.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			q.answering[conv]--
			var run []func()
			if q.answering[conv] <= 0 {
				delete(q.answering, conv)
				run = q.pending[conv]
				delete(q.pending, conv)
			}
			q.mu.Unlock()
			for _, fn := range run {
				fn()
			}
		})
	}
}

func (q *noteQueue) add(conv, text string, write func(conv, text string)) {
	q.mu.Lock()
	if q.answering[conv] > 0 {
		q.pending[conv] = append(q.pending[conv], func() { write(conv, text) })
		q.mu.Unlock()
		return
	}
	q.mu.Unlock()
	write(conv, text)
}

// scheduleTransferExpiry settles a held transfer nobody answered once its
// approval expires.
func (m *Module) scheduleTransferExpiry(conv, toolCallID, canonical string) {
	id := heldTransferID(canonical)
	if id == "" {
		return
	}
	time.AfterFunc(approvalTTL()+time.Second, func() { m.expireHeldTransfer(conv, toolCallID, id) })
}
