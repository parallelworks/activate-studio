package chat

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// A chat goes into a library only when someone saves it: Save to library
// in the chat, or save_chat_to_library when the person asks the assistant.
// The transcript goes to chat-sessions/ in the person's own space when
// they have one, where only they can read it, and otherwise to the main
// library. Saving the chat again replaces the copy saved before.

func init() {
	goExecutors["save_chat_to_library"] = (*Module).toolSaveChat
}

// maxTranscript is the most a saved transcript may be.
const maxTranscript = 20 << 20

// chatDest is where a chat is saved.
type chatDest struct {
	Library string `json:"library"`
	Label   string `json:"label"`
	// Path is the transcript's path in the library.
	Path string `json:"path"`
	// Space is the person's own space, closed to other accounts.
	Space bool `json:"space"`
	// Shared: others who use this Studio can read the main library.
	Shared bool `json:"shared"`
	lib    kb.Library
}

// chatDest is where c is saved for the person r is from.
func (m *Module) chatDest(r *http.Request, c *conversations.Conversation) (chatDest, error) {
	rel := conversations.ExportDir + "/" + conversations.TranscriptName(c)
	if kb.LibraryResolver != nil && kb.SpaceFiles != nil && user(r) != nil {
		if libs, err := kb.LibraryResolver.All(r); err == nil {
			for _, l := range libs {
				if l.Space && l.SharedFrom == "" {
					return chatDest{Library: l.ID, Label: l.Label, Path: rel, Space: true, lib: l}, nil
				}
			}
		}
	}
	if kb.Default == nil {
		return chatDest{}, apierr.New(409, "there is no library to save the chat to here")
	}
	lib, ok, err := kb.Default.Libs.Get("")
	if err != nil {
		return chatDest{}, err
	}
	if !ok {
		return chatDest{}, apierr.New(409, "the main library is not set up here")
	}
	if err := kb.RequireWritable(lib); err != nil {
		return chatDest{}, apierr.New(409, "%s", err.Error())
	}
	return chatDest{Library: lib.ID, Label: lib.Label, Path: rel, Shared: m.A.Cfg.AuthEnabled(), lib: lib}, nil
}

// saveChat writes c's transcript where chatDest says.
func (m *Module) saveChat(r *http.Request, c *conversations.Conversation) (chatDest, error) {
	d, err := m.chatDest(r, c)
	if err != nil {
		return d, err
	}
	body := conversations.Transcript(c)
	if len(body) > maxTranscript {
		return d, apierr.New(413, "this chat is over %d MB as a transcript", maxTranscript>>20)
	}
	if d.Space {
		// An error here is a folder already there; Put says if it is not.
		_ = kb.SpaceFiles.Mkdir(r, d.lib, conversations.ExportDir)
		if err := kb.SpaceFiles.Put(r, d.lib, d.Path, strings.NewReader(body), maxTranscript); err != nil {
			return d, err
		}
		kb.SpaceFiles.Changed(r, d.lib, []string{d.Path})
		return d, nil
	}
	return d, m.saveChatHere(r.Context(), c, d.Path, body)
}

// saveChatHere writes a transcript into the main library, labels it
// chat-session and with its owner, and indexes the folder.
func (m *Module) saveChatHere(ctx context.Context, c *conversations.Conversation, rel, body string) error {
	abs, err := kb.Resolve(rel, m.A.Cfg.KBRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".chat-*.part")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(body)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), abs)
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	if label := m.A.Providers.ApplyLabels; label != nil {
		if err := label(ctx, nil, []string{conversations.ExportDir}, []string{"chat-session"}, nil); err != nil {
			m.A.Log.Warn("labeling chat-sessions: " + err.Error())
		}
		if tag := conversations.OwnerTag(c); tag != "" {
			if err := label(ctx, nil, []string{rel}, []string{tag}, nil); err != nil {
				m.A.Log.Warn("labeling a saved chat: " + err.Error())
			}
		}
	}
	if index := m.A.Providers.IndexDirMS; index != nil {
		if _, err := index(ctx, conversations.ExportDir); err != nil {
			// The sweep indexes it later.
			m.A.Log.Warn("indexing a saved chat: " + err.Error())
		}
	}
	return nil
}

// visibleChat is conversation id as the person r is from may see it.
func (m *Module) visibleChat(r *http.Request, id string) (*conversations.Conversation, error) {
	who, _ := m.Convs.Who(r)
	return m.Convs.FindVisible(id, who.Username)
}

// chatSaveRoutes: GET says where the chat would go, POST saves it.
func (m *Module) chatSaveRoutes(h func(pattern string, fn func(http.ResponseWriter, *http.Request) error)) {
	h("GET /api/chat/conversations/{id}/save", func(w http.ResponseWriter, r *http.Request) error {
		c, err := m.visibleChat(r, r.PathValue("id"))
		if err != nil {
			return err
		}
		d, err := m.chatDest(r, c)
		if err != nil {
			return err
		}
		return reqbody.OK(w, d)
	})
	h("POST /api/chat/conversations/{id}/save", func(w http.ResponseWriter, r *http.Request) error {
		c, err := m.visibleChat(r, r.PathValue("id"))
		if err != nil {
			return err
		}
		d, err := m.saveChat(r, c)
		if err != nil {
			return err
		}
		return reqbody.OK(w, d)
	})
}

// toolSaveChat is save_chat_to_library.
func (m *Module) toolSaveChat(r *http.Request, _ map[string]any, ctx ToolCtx) (*Outcome, error) {
	if ctx.ConversationID == nil || *ctx.ConversationID == "" {
		return &Outcome{Result: "This chat is not stored in the Studio, so there is no conversation to save.", Summary: "no conversation"}, nil
	}
	c, err := m.visibleChat(r, *ctx.ConversationID)
	if err != nil {
		return &Outcome{Result: "Not saved: " + err.Error(), Summary: "not saved"}, nil
	}
	d, err := m.saveChat(r, c)
	if err != nil {
		return &Outcome{Result: "Not saved: " + err.Error(), Summary: "not saved"}, nil
	}
	res := fmt.Sprintf("Saved this conversation, up to the user's last message, to %s as %s.", d.Label, d.Path)
	switch {
	case d.Space:
		res += " That is the user's own space, which only they can read."
	case d.Shared:
		res += " Everyone who uses this Studio can read that library."
	}
	res += " Saving it again replaces that copy. Tell the user where it is."
	return &Outcome{Result: res, Summary: "saved to " + d.Label}, nil
}
