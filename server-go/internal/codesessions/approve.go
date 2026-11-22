package codesessions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// A session waiting on an approval (a command to run, a file outside its
// folder to read) can be answered from the Code page: Approve runs that
// one call, Deny refuses it with a note the agent reads. The answer goes
// where the page read the session: this host's daemon over its socket, or
// the platform's answer route as the viewer, where the machine's daemon
// decides whether it takes an answer from there. Neither grants anything
// standing: no folder, no rule for later calls.

// DenyNote is what the agent reads when a person denies a call here.
const DenyNote = "Denied from the Studio's Code page."

type approveBody struct {
	Machine  string `json:"machine"`
	ID       string `json:"id"`
	Approval string `json:"approval"`
	Allowed  bool   `json:"allowed"`
}

func (s *routes) approve(w http.ResponseWriter, r *http.Request) (any, error) {
	var b approveBody
	if err := platform.Decode(r, &b); err != nil {
		return nil, platform.KbError(400, "The answer was not JSON.")
	}
	if !idRe.MatchString(b.Machine) || !idRe.MatchString(b.ID) || !idRe.MatchString(b.Approval) {
		return nil, platform.KbError(400, "machine, id and approval are required")
	}
	answer := jsonv.NewObject("allowed", b.Allowed)
	if !b.Allowed {
		answer.Set("denyMessage", DenyNote)
	}
	if c := s.localDaemon(r); c != nil {
		m, err := s.localMachine(r.Context(), c)
		if err != nil {
			return nil, err
		}
		if b.Machine != m.ID {
			return nil, platform.KbError(404, "That session is not on this computer.")
		}
		if own, only := s.mine(r); only && !own[b.ID] {
			return nil, notYours()
		}
		if _, err := pwcode.AnswerApproval(r.Context(), c, b.ID, b.Approval, answer); err != nil {
			return nil, localAnswerError(err)
		}
		return map[string]any{"ok": true}, nil
	}
	if err := s.answerOnPlatform(r, b, answer); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// localAnswerError says why this host's daemon did not take an answer.
func localAnswerError(err error) error {
	var de *pwcode.DaemonError
	if errors.As(err, &de) && (de.Status == http.StatusNotFound || de.Status == http.StatusConflict) {
		return platform.KbError(409, "That approval is no longer waiting: the session went on, or someone answered it.")
	}
	return platform.KbError(502, "pw code on this computer did not take the answer: "+err.Error())
}

// answerOnPlatform posts the answer to the platform as the viewer.
func (s *routes) answerOnPlatform(r *http.Request, b approveBody, answer *jsonv.Object) error {
	origin := s.p.Origin()
	if origin == "" {
		return platform.KbError(409, "This Studio is not connected to an ACTIVATE platform, where pw code sessions are listed.")
	}
	key, err := viewerKey(r, s.p)
	if err != nil {
		return err
	}
	raw, err := jsonv.Marshal(answer)
	if err != nil {
		return err
	}
	path := "/api/code/machines/" + platform.EncodeURIComponent(b.Machine) + "/sessions/" + platform.EncodeURIComponent(b.ID) +
		"/approvals/" + platform.EncodeURIComponent(b.Approval)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, done, err := platform.Request(ctx, http.MethodPost, strings.TrimRight(origin, "/")+path,
		map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json", "Accept": "application/json"}, bytes.NewReader(raw), 30*time.Second)
	if err != nil {
		return platform.KbError(502, "The platform did not answer.")
	}
	defer done()
	defer res.Body.Close()
	said := platformMessage(res.Body)
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return nil
	case res.StatusCode == http.StatusUnauthorized:
		return platform.KbError(401, "The platform refused your credential; sign in again, or renew your API key under Settings, Model access.")
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusConflict:
		return platform.KbError(409, "That approval is no longer waiting: the session went on, or someone answered it.")
	case res.StatusCode == http.StatusForbidden:
		msg := "The session's machine does not take answers from the platform for it"
		if said != "" {
			msg += ": " + said
		}
		return platform.KbError(403, msg+". Answer it where the session runs.")
	}
	msg := "The platform answered " + http.StatusText(res.StatusCode) + " to the approval"
	if said != "" {
		msg += ": " + said
	}
	return platform.KbError(502, msg+".")
}

// platformMessage is the detail or message of a platform error body.
func platformMessage(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, 64<<10))
	v, err := jsonv.Decode(raw)
	o := jsonv.Obj(v)
	if err != nil || o == nil {
		return ""
	}
	for _, k := range []string{"detail", "message", "error", "title"} {
		if x, _ := o.Get(k); x != nil {
			if s := strings.TrimSpace(jsonv.String(x)); s != "" {
				return clip(oneLine(s), 300)
			}
		}
	}
	return ""
}
