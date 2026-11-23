package chat

import (
	"net/http"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// A session on another of the person's machines is continued through the
// platform's relay (pwcode/relay.go): the chat's messages, answers and Stop
// go to it as the person, with the credential the Code page reads their
// sessions with, and it keeps running where it is. Nothing is attached or
// moved, so its commands run on its own machine, in its own folder.

// relayClient is the relay to the viewer's sessions on machine, as them.
func (m *Module) relayClient(r *http.Request, machine string) (*pwcode.RelayClient, error) {
	if m.pf == nil || m.pf.Origin() == "" {
		return nil, pwErr(409, "This Studio is not connected to an ACTIVATE platform, which carries messages to sessions on other machines.")
	}
	key, err := codesessions.ViewerKey(r, m.pf)
	if err != nil {
		return nil, err
	}
	return pwcode.Relay(m.pf.Origin(), key, machine), nil
}

// relayUnavailable says why sessions on other machines cannot be driven
// here for r's viewer, or "" when they can.
func (m *Module) relayUnavailable(r *http.Request) string {
	if _, err := m.relayClient(r, ""); err != nil {
		return apiMessage(err)
	}
	return ""
}

// relayTarget is where a relayed chat's messages go.
func (m *Module) relayTarget(r *http.Request, link *conversations.PWCodeLink) (*pwTarget, error) {
	c, err := m.relayClient(r, link.Machine)
	if err != nil {
		return nil, err
	}
	return &pwTarget{client: c, site: link.Site, workspace: link.Workspace, relay: true}, nil
}

// relayConfirm is what the person is asked before a chat drives a session
// on another machine: where it runs, and that it stays there.
func relayConfirm(machine, workspace, status, liveHost string, signIn bool) string {
	where := machine
	if workspace != "" {
		where += ", in " + shortWorkspace(workspace)
	}
	s := "This session runs on " + where + ", and stays there: this chat sends your messages, answers and Stop to it through ACTIVATE, as you, and its commands run on " + machine + ". It keeps its own tools, not the Studio's library tools."
	switch status {
	case "running":
		s += " It is working on a reply now; the chat follows from your next message."
	case "awaiting_approval":
		s += " It is waiting for an answer, which you can give in the chat."
	case "live_elsewhere":
		host := liveHost
		if host == "" {
			host = "another host"
		}
		s += " It is open on " + host + " now, which may answer for it: messages from here can be refused until it is closed there."
	}
	s += " If it is also open in a terminal or in PW Code on the web, messages from there go to it too."
	if signIn {
		s += " Only you can see the chat in this Studio, even where chats are shared, since it holds the session's turns from your machine."
	}
	return s
}

// relayNote is what a relayed chat's opening note adds.
func relayNote(machine string) string {
	return " It keeps running on " + machine + ", where its commands run: this chat reaches it through ACTIVATE, and it has its own tools, not the Studio's library tools."
}

// relayMachine is a relayed link's machine as the person knows it, or nil
// for a chat whose session the Studio reaches directly.
func relayMachine(l *conversations.PWCodeLink) any {
	if !l.Relay {
		return nil
	}
	return firstNonEmpty(l.MachineName, strings.TrimPrefix(l.Site, "relay:"))
}

// askAnswer answers one of a question's parts, as pw code takes it
// (serverapi.AskQuestionAnswer): the options picked by label, and what the
// person typed.
type askAnswer struct {
	Question string   `json:"question"`
	Picked   []string `json:"picked"`
	Text     string   `json:"text"`
}

// askAnswerBody is the approval answer to an ask: the answers, in the
// question's order, or chat to talk the questions through instead.
func askAnswerBody(in []askAnswer, chat bool) (*jsonv.Object, error) {
	if chat {
		return jsonv.NewObject("chat", true), nil
	}
	if len(in) == 0 || len(in) > 20 {
		return nil, pwErr(400, "Answer each question, or choose to talk them through.")
	}
	out := []any{}
	for _, a := range in {
		picked := []any{}
		for _, p := range a.Picked {
			if p = strings.TrimSpace(p); p != "" && len(picked) < 20 {
				picked = append(picked, sliceRunes(p, 500))
			}
		}
		text := sliceRunes(strings.TrimSpace(a.Text), 4000)
		if len(picked) == 0 && text == "" {
			return nil, pwErr(400, "Answer each question, or choose to talk them through.")
		}
		o := jsonv.NewObject("question", sliceRunes(a.Question, 2000), "picked", picked)
		if text != "" {
			o.Set("text", text)
		}
		out = append(out, o)
	}
	return jsonv.NewObject("answers", out), nil
}
