package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// The model each person chose last for pw code here, beside a new chat's
// message box, in a session's own composer or at the Code page's start. A
// new pw code chat starts with it where Settings names no model, whatever
// browser it is in.

// lastModel is that model, with the allocation that bills it ("" for none).
type lastModel struct {
	Model      string `json:"model"`
	Allocation string `json:"allocation,omitempty"`
	At         string `json:"at,omitempty"`
}

// lastModelStore keeps them by person in INDEX_BASE/pwcode-models.json.
type lastModelStore struct {
	file string
	mu   sync.Mutex
	all  map[string]lastModel
}

func (s *lastModelStore) load() {
	if s.all != nil {
		return
	}
	s.all = map[string]lastModel{}
	if b, err := os.ReadFile(s.file); err == nil {
		_ = json.Unmarshal(b, &s.all)
	}
}

func (s *lastModelStore) get(user string) lastModel {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	return s.all[user]
}

// set keeps l for user, or forgets theirs where it names no model.
func (s *lastModelStore) set(user string, l lastModel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	had, ok := s.all[user]
	if ok == (l.Model != "") && had.Model == l.Model && had.Allocation == l.Allocation {
		return
	}
	if l.Model == "" {
		delete(s.all, user)
	} else {
		l.At = time.Now().UTC().Format(time.RFC3339)
		s.all[user] = l
	}
	b, _ := json.MarshalIndent(s.all, "", "  ")
	tmp := s.file + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.file)
	}
}

func (m *Module) lastModels() *lastModelStore {
	m.pw.mu.Lock()
	defer m.pw.mu.Unlock()
	if m.pw.last == nil {
		m.pw.last = &lastModelStore{file: filepath.Join(m.A.Cfg.IndexBase, "pwcode-models.json")}
	}
	return m.pw.last
}

// keepsModel is whether there is a person to keep a model for: the one
// signed in, or on a Studio without sign-in everyone there.
func (m *Module) keepsModel(r *http.Request) bool {
	return !m.A.Cfg.AuthEnabled() || user(r) != nil
}

// choseModel keeps the model the viewer chose for a session, on another
// machine where relay. An allocation of nil says nothing of what bills it,
// so the one it had stays.
func (m *Module) choseModel(r *http.Request, model string, allocation *string, relay bool) {
	model = strings.TrimSpace(model)
	if model == "" || (relay && machinesModel(model)) || len(model) > 300 || len(deref(allocation)) > 300 || !m.keepsModel(r) {
		return
	}
	l := lastModel{Model: model, Allocation: deref(allocation)}
	if had := m.lastModels().get(userID(r)); allocation == nil && had.Model == model {
		l.Allocation = had.Allocation
	}
	m.lastModels().set(userID(r), l)
}

// machinesModel is whether a model is one a machine has of its own (an
// inference server set up there), which no other machine knows.
func machinesModel(id string) bool {
	return !platformModel(id) && !pwcode.IsClaudeModel(id)
}

// startModel is the model the viewer's new chats start with where they pick
// none: the one they chose last, unless Settings names one.
func (m *Module) startModel(r *http.Request) lastModel {
	if m.eff().PWCodeModel != "" || !m.keepsModel(r) {
		return lastModel{}
	}
	l := m.lastModels().get(userID(r))
	if l.Model == "" || (pwcode.IsClaudeModel(l.Model) && pwcode.ClaudeCodeOff(auth.ContextWithUser(r.Context(), user(r))) != "") {
		return lastModel{}
	}
	return l
}

// createWithLast makes a session, on last where it names a model. One that
// pw code refuses for its model (gone, or not for this person any more) is
// made without it, and the model is kept no longer.
func (m *Module) createWithLast(ctx context.Context, r *http.Request, c pwcode.Client, req *jsonv.Object, last lastModel) (*jsonv.Object, error) {
	if last.Model == "" {
		return pwcode.CreateSession(ctx, c, req)
	}
	on := req.Clone()
	on.Set("model", last.Model)
	if last.Allocation != "" {
		on.Set("allocation", last.Allocation)
	}
	s, err := pwcode.CreateSession(ctx, c, on)
	var de *pwcode.DaemonError
	if !errors.As(err, &de) {
		return s, err
	}
	switch de.Status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
	default:
		return s, err
	}
	s, again := pwcode.CreateSession(ctx, c, req)
	if again != nil && again.Error() != pwcode.NoModel {
		// Refused for something else too.
		return nil, err
	}
	m.lastModels().set(userID(r), lastModel{})
	return s, again
}
