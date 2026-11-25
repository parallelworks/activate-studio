package notices

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/chat"
	"github.com/parallelworks/activate-studio/server-go/internal/codesessions"
	"github.com/parallelworks/activate-studio/server-go/internal/delegation"
	"github.com/parallelworks/activate-studio/server-go/internal/fleet"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

const single = "single"

// Service is the notices of one Studio.
type Service struct {
	Src Sources
	// Auth: people sign in to this Studio.
	Auth  func() bool
	now   func() time.Time
	store store
}

// Register serves /api/notices.
func Register(mux *http.ServeMux, a *app.App) {
	s := New(a.Cfg.IndexBase, a.Cfg.AuthEnabled, sources(a))
	s.Routes(mux, a)
}

// New is the notices kept under indexBase, from src.
func New(indexBase string, authOn func() bool, src Sources) *Service {
	return &Service{Src: src, Auth: authOn, now: time.Now, store: store{dir: indexBase + "/notices"}}
}

// Routes: the notices, a chat seen, and notices dismissed. Each answers
// with the notices as they now are, so a page that marks the chat in
// front of the person asks once.
func (s *Service) Routes(mux *http.ServeMux, a *app.App) {
	h := func(fn func(r *http.Request, owner string) (any, error)) http.HandlerFunc {
		return platform.Handler(a.Log, func(w http.ResponseWriter, r *http.Request) (any, error) {
			owner, err := s.owner(r)
			if err != nil {
				return nil, err
			}
			w.Header().Set("Cache-Control", "no-store")
			return fn(r, owner)
		})
	}
	mux.HandleFunc("GET /api/notices", h(func(r *http.Request, owner string) (any, error) {
		return s.answer(r, owner, nil)
	}))
	mux.HandleFunc("POST /api/notices/seen", h(func(r *http.Request, owner string) (any, error) {
		var b struct {
			Conversations []string `json:"conversations"`
			Session       string   `json:"session"`
		}
		if err := body(r, &b); err != nil {
			return nil, err
		}
		return s.seen(r, owner, b.Conversations, b.Session)
	}))
	mux.HandleFunc("POST /api/notices/dismiss", h(func(r *http.Request, owner string) (any, error) {
		var b struct {
			IDs []string `json:"ids"`
		}
		if err := body(r, &b); err != nil {
			return nil, err
		}
		if len(b.IDs) == 0 || len(b.IDs) > 500 {
			return nil, apierr.New(400, "ids is the notices to dismiss")
		}
		return s.dismiss(r, owner, b.IDs)
	}))
}

func body(r *http.Request, out any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<18))
	if err != nil {
		return err
	}
	if json.Unmarshal(b, out) != nil {
		return apierr.New(400, "the request is not valid JSON of the expected shape")
	}
	return nil
}

// owner is whose notices r reads: the signed-in person's, or with sign-in
// off the one person's.
func (s *Service) owner(r *http.Request) (string, error) {
	if !s.Auth() {
		return single, nil
	}
	if u := auth.UserOf(r); u != nil && u.ID != "" {
		return u.ID, nil
	}
	return "", apierr.New(401, "authentication required")
}

// answer reads the sources with nothing locked (the listing is a request
// to the platform), then settles them against the person's record.
func (s *Service) answer(r *http.Request, owner string, change func(rec *record, now time.Time)) (Answer, error) {
	src := read(r, s.Src)
	var out Answer
	now := s.now()
	err := s.store.with(owner, now, func(rec *record) {
		if change != nil {
			change(rec, now)
		}
		out = build(r, src, rec, now)
	})
	return out, err
}

// read is src with each source already read for r.
func read(r *http.Request, src Sources) Sources {
	machines, sessions, err := src.Listing(r)
	chats, agents, work, fails, libs := src.Chats(r), src.Fleet(r), src.Delegated(r), src.Transfers(r), src.Libraries(r)
	listed := map[string]bool{}
	for _, x := range sessions {
		if o := jsonv.Obj(x); o != nil {
			listed[str(o, "id")] = true
		}
	}
	unlisted := map[string]bool{}
	for _, c := range chats {
		if c.Session != "" && c.Local && !listed[c.Session] {
			unlisted[c.Session] = true
		}
	}
	local, sum := src.Local(r, unlisted), src.Summary(r)
	return Sources{
		Listing:   func(*http.Request) ([]any, []any, error) { return machines, sessions, err },
		Chats:     func(*http.Request) []chat.ChatState { return chats },
		Local:     func(*http.Request, map[string]bool) map[string]*jsonv.Object { return local },
		Fleet:     func(*http.Request) []fleet.Agent { return agents },
		Delegated: func(*http.Request) []delegation.Record { return work },
		Transfers: func(*http.Request) []transfers.Transfer { return fails },
		Summary:   func(*http.Request) *transfers.Summary { return sum },
		Libraries: func(*http.Request) []Library { return libs },
	}
}

func (s *Service) seen(r *http.Request, owner string, conversations []string, session string) (Answer, error) {
	if len(conversations) > 50 {
		return Answer{}, apierr.New(400, "too many conversations")
	}
	chats := s.Src.Chats(r)
	return s.answer(r, owner, func(rec *record, now time.Time) {
		for _, c := range chats {
			if contains(conversations, c.ID) {
				rec.see(c)
			}
		}
		if session == "" {
			return
		}
		// The end of delegated work is news until its session is opened.
		for _, d := range s.Src.Delegated(r) {
			if d.Session == session && (d.State == "finished" || d.State == "stopped") {
				rec.dismiss("delegated:"+d.Session+":"+d.State+":"+d.Finished, now)
			}
		}
	})
}

func (s *Service) dismiss(r *http.Request, owner string, ids []string) (Answer, error) {
	chats := s.Src.Chats(r)
	for _, id := range ids {
		if t, ok := strings.CutPrefix(id, "transfer:"); ok {
			s.Src.TransferSeen(r, t)
		}
	}
	return s.answer(r, owner, func(rec *record, now time.Time) {
		for _, id := range ids {
			if len(id) > 2000 {
				continue
			}
			// A dismissed reply is a seen one, so the chat's mark in the list
			// goes too.
			for _, c := range chats {
				if id == "reply:"+c.ID+":"+c.Reply.ID {
					rec.see(c)
				}
			}
			rec.dismiss(id, now)
		}
	})
}

func esc(s string) string { return url.QueryEscape(s) }

// sources are the Studio's own.
func sources(a *app.App) Sources {
	return Sources{
		Listing: codesessions.Listing,
		Chats: func(r *http.Request) []chat.ChatState {
			if chat.Default == nil {
				return nil
			}
			return chat.Default.ChatStates(r)
		},
		Local: func(r *http.Request, ids map[string]bool) map[string]*jsonv.Object {
			if chat.Default == nil {
				return nil
			}
			return chat.Default.LocalSessions(r, ids)
		},
		Fleet: func(r *http.Request) []fleet.Agent {
			if fleet.Default == nil || !a.Settings.Effective().DelegationEnabled {
				return nil
			}
			// An administrator may see everyone's agents; a notice is the
			// agent's own person's.
			u := auth.UserOf(r)
			out := []fleet.Agent{}
			for _, g := range fleet.Default.For(r) {
				if !a.Cfg.AuthEnabled() || (u != nil && g.Owner != nil && *g.Owner == u.ID) {
					out = append(out, g)
				}
			}
			return out
		},
		Delegated: func(r *http.Request) []delegation.Record {
			if delegation.Default == nil {
				return nil
			}
			return delegation.Default.Mine(r)
		},
		Transfers: func(r *http.Request) []transfers.Transfer {
			v, err := transfers.ViewerOf(a, r)
			if err != nil {
				return nil
			}
			return transfers.Of(a).Failures(v)
		},
		Summary: func(r *http.Request) *transfers.Summary {
			v, err := transfers.ViewerOf(a, r)
			if err != nil {
				return nil
			}
			sum := transfers.Of(a).Summary(v)
			return &sum
		},
		TransferSeen: func(r *http.Request, id string) {
			if v, err := transfers.ViewerOf(a, r); err == nil {
				_, _ = transfers.Of(a).Seen(v, id)
			}
		},
		Libraries: func(r *http.Request) []Library { return failedLibraries(a, r) },
	}
}

// failedLibraries is the libraries of the person's own whose index failed,
// and those of the Studio for the people who administer it.
func failedLibraries(a *app.App, r *http.Request) []Library {
	reg := libraries.Of(a)
	out := []Library{}
	add := func(l Library, ok bool) {
		if ok {
			out = append(out, l)
		}
	}
	if ok, _ := mainlib.Request(a, r); ok {
		for _, d := range reg.AddedDefs() {
			if d.Build != nil {
				add(failed(d.ID, d.Label, d.Build.State, d.Build.Error))
			}
		}
	}
	id := ""
	if u := auth.UserOf(r); u != nil {
		id = u.ID
	}
	if owner := reg.Users().OwnerOf(id); owner != "" {
		for _, d := range reg.Users().Load(owner) {
			add(failed(d.ID, d.Label, d.State, d.Error))
		}
		for _, d := range reg.Owned(owner) {
			if d.Build != nil {
				add(failed(d.ID, d.Label, d.Build.State, d.Build.Error))
			}
		}
	}
	return out
}
