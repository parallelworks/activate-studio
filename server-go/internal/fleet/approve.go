package fleet

import (
	"context"
	"errors"
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// DenyNote is what the agent reads when a person denies a call from the
// Fleet page.
const DenyNote = "Denied from the Studio's Fleet page."

// maxApprovalCommand is how much of a call's command is sent whole, as
// on the Code page: Approve runs exactly what the person was shown, and a
// longer one is offered Deny alone.
const maxApprovalCommand = 64 << 10

// Approval is what an agent's session waits on, from the daemon's record
// (kind, tool, command, questions, plan, reason): a call to approve when
// its kind is confirm, else a plan or a question for the Code page.
type Approval struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Tool string `json:"tool,omitempty"`
	// Command is the call's command, whole up to maxApprovalCommand;
	// CommandCut says it is longer, and CommandLength how long it is.
	Command       string `json:"command,omitempty"`
	CommandCut    bool   `json:"commandCut,omitempty"`
	CommandLength int    `json:"commandLength,omitempty"`
	Reason        string `json:"reason,omitempty"`
	// Approvable says Approve may be offered: a confirm whose call is
	// shown whole. Deny is offered for any confirm.
	Approvable bool `json:"approvable"`
}

// approvalOf is the daemon's approval record x as the Fleet shows it.
func approvalOf(x any) Approval {
	ap := Approval{ID: text(jsonv.Path(x, "id")), Kind: text(jsonv.Path(x, "kind")), Tool: text(jsonv.Path(x, "tool")), Reason: text(jsonv.Path(x, "reason"))}
	cmd := text(jsonv.Path(x, "command"))
	if n := len([]rune(cmd)); n > maxApprovalCommand {
		ap.Command, ap.CommandCut, ap.CommandLength = string([]rune(cmd)[:maxApprovalCommand]), true, n
	} else {
		ap.Command, ap.CommandLength = cmd, n
	}
	ap.Approvable = ap.Kind == "confirm" && !ap.CommandCut && (ap.Command != "" || ap.Tool != "")
	return ap
}

// summary is the approval in a line, for the agent's note: what it runs,
// or what it asks.
func (ap Approval) summary() string {
	what := ap.Command
	if what == "" {
		what = ap.Reason
	}
	head := ap.Tool
	if head == "" {
		head = ap.Kind
	}
	if head != "" && what != "" {
		return head + ": " + what
	}
	return head + what
}

// Waiting is the first thing a's session waits on, or nil.
func (f *Fleet) Waiting(ctx context.Context, a *Agent) *Approval {
	c := f.Daemon()
	if c == nil || a.Session == "" {
		return nil
	}
	d, err := pwcode.GetSession(ctx, c, a.Session)
	if err != nil {
		return nil
	}
	for _, x := range jsonv.Arr(jsonv.Path(d, "approvals")) {
		if ap := approvalOf(x); ap.ID != "" {
			return &ap
		}
	}
	return nil
}

// Answer allows or denies the call approval in a's session, once.
func (f *Fleet) Answer(ctx context.Context, a *Agent, approval string, allowed bool) error {
	c := f.Daemon()
	if c == nil {
		return apierr.New(503, "pw code is not set up on this Studio")
	}
	if a.Session == "" {
		return apierr.New(409, "it has no session")
	}
	// Only a call the person was shown whole is approved here; a plan or
	// a question is answered on the Code page.
	ap := f.Waiting(ctx, a)
	if ap == nil || ap.ID != approval {
		return apierr.New(409, "That approval is no longer waiting: the session went on, or someone answered it.")
	}
	if ap.Kind != "confirm" {
		return apierr.New(409, "It waits on a %s, which is answered in its session on the Code page, not approved here.", ap.Kind)
	}
	if allowed && !ap.Approvable {
		return apierr.New(409, "Its call is not shown whole here, so it can only be denied.")
	}
	answer := jsonv.NewObject("allowed", allowed)
	if !allowed {
		answer.Set("denyMessage", DenyNote)
	}
	if _, err := pwcode.AnswerApproval(ctx, c, a.Session, approval, answer); err != nil {
		var de *pwcode.DaemonError
		if errors.As(err, &de) && (de.Status == http.StatusNotFound || de.Status == http.StatusConflict) {
			return apierr.New(409, "That approval is no longer waiting: the session went on, or someone answered it.")
		}
		return apierr.New(502, "pw code on this computer did not take the answer: %s", err.Error())
	}
	f.journal(a.ID, "message", map[bool]string{true: "approved", false: "denied"}[allowed]+" a call in its session", a.Session)
	return nil
}
