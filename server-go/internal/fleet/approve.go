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

// Approval is a call an agent's session waits to have approved.
type Approval struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Waiting is the first call a's session waits to have approved, or nil.
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
		if id := text(jsonv.Path(x, "id")); id != "" {
			return &Approval{ID: id, Text: text(jsonv.Path(x, "text"))}
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
