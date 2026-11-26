package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
	"github.com/parallelworks/activate-studio/server-go/internal/people"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// Where people sign in to this Studio, the grounded model at /v1 answers
// only people of this Studio: a caller whose own ACTIVATE key the platform
// says is the key of someone who has signed in here (internal/people) or of
// an administrator. /v1 is outside sign-in, so the key's owner is all it
// knows of the caller. The answer is then theirs: it draws on the main
// library, which every person here reads, and on no one's own library.

// RagNeedsPerson is the refusal of a caller who is not a person of this
// Studio.
const RagNeedsPerson = "Where people sign in to this Studio, its grounded model answers only people of this Studio, each with their own ACTIVATE key. This call did not bring the key of a person who has signed in here."

// RagNoPlatform is the refusal where the Studio cannot ask the platform
// whose a key is: then it answers no one.
const RagNoPlatform = "Where people sign in to this Studio, its grounded model answers only people of this Studio, and this Studio cannot ask the platform whose key a call brings, so it answers no one here. Use the Assistant in the Studio."

// keyOwners is what the platform said of each key lately, by a hash of the
// key: its owner's account, or "" for a key it did not take.
type keyOwners struct {
	mu sync.Mutex
	at map[string]keyOwner
}

type keyOwner struct {
	account string
	when    time.Time
}

// ownerFor is how long an answer is kept: a found owner for five minutes,
// a refused key for one.
func ownerFor(account string) time.Duration {
	if account == "" {
		return time.Minute
	}
	return 5 * time.Minute
}

var owners = &keyOwners{at: map[string]keyOwner{}}

// platformOrigin is the platform whose gateway the calls go to, where its
// API answers whose a key is: "" when the gateway is no platform's.
var platformOrigin = func(m *Module) string {
	if m.pf == nil || !m.pf.PlatformGateway() {
		return ""
	}
	return m.pf.Origin()
}

// whoamiHook answers the platform's whoami in tests.
var whoamiHook func(ctx context.Context, origin, key string) (account string, ok bool, err error)

// platformAccountOf is the ACTIVATE account whose key this is, as the platform's
// GET /api/auth/whoami answers with it as the bearer, kept briefly. ok is
// false for a key the platform does not take or that is no person's; err
// is a platform that did not answer. The key is never logged.
func (m *Module) platformAccountOf(ctx context.Context, origin, key string) (string, bool, error) {
	id := remote.KeyID(key)
	owners.mu.Lock()
	hit, cached := owners.at[id]
	owners.mu.Unlock()
	if cached && time.Since(hit.when) < ownerFor(hit.account) {
		return hit.account, hit.account != "", nil
	}
	var account string
	var ok bool
	var err error
	if whoamiHook != nil {
		account, ok, err = whoamiHook(ctx, origin, key)
	} else {
		account, ok, err = whoami(ctx, origin, key)
	}
	if err != nil {
		return "", false, err
	}
	if !ok {
		account = ""
	}
	owners.mu.Lock()
	owners.at[id] = keyOwner{account, time.Now()}
	owners.mu.Unlock()
	return account, account != "", nil
}

func whoami(ctx context.Context, origin, key string) (string, bool, error) {
	res, cancel, err := platform.Request(ctx, http.MethodGet, origin+"/api/auth/whoami", map[string]string{"Authorization": "Bearer " + key}, nil, 15*time.Second)
	if err != nil {
		return "", false, err
	}
	defer cancel()
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", false, nil
	}
	if res.StatusCode != http.StatusOK {
		return "", false, &platformAnswer{res.StatusCode}
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	who := strings.TrimSpace(string(b))
	var s string
	if json.Unmarshal(b, &s) == nil {
		who = strings.TrimSpace(s)
	}
	// A person's account: "user:<name>" or the bare name. Anything else
	// (a service's credential, an organization's) is no person's.
	name := strings.TrimPrefix(who, "user:")
	if name == "" || strings.ContainsAny(name, ":/ {}\"") {
		return "", false, nil
	}
	return name, true, nil
}

type platformAnswer struct{ status int }

func (e *platformAnswer) Error() string {
	return "the platform answered " + http.StatusText(e.status)
}

// v1Person is the person of this Studio a call to /v1 is made for, where
// people sign in: "" with why it is refused when there is none, with the
// refusal's words. Without sign-in every caller is its one person.
func (m *Module) v1Person(r *http.Request) (person *auth.User, why, words string) {
	if !m.A.Cfg.AuthEnabled() {
		return nil, "", ""
	}
	b := bearerOf(r)
	switch {
	case b == "":
		return nil, "no key", RagNeedsPerson
	case headerString(r, "X-Studio-Injected-Key") == "1":
		return nil, "a call the registration's forwarder marked", RagNeedsPerson
	case m.studiosKey(b):
		return nil, "the Studio's own key", RagNeedsPerson
	}
	origin := platformOrigin(m)
	if origin == "" {
		return nil, "no platform to ask whose key this is", RagNoPlatform
	}
	account, ok, err := m.platformAccountOf(r.Context(), origin, b)
	if err != nil || !ok {
		why := "a key the platform does not take as a person's"
		if err != nil {
			why = "the platform did not say whose key this is (" + err.Error() + ")"
		}
		return nil, why, RagNeedsPerson
	}
	if !people.Of(m.A).Known(account) && !mainlib.Admin(mainlib.Person{AuthEnabled: true, Username: account, ID: "user:" + account}) {
		return nil, "the key of an account that has not signed in here", RagNeedsPerson
	}
	return &auth.User{ID: "user:" + account, Username: account}, "", ""
}

// refusePerson answers a call v1Person refuses, in the shape OpenAI clients
// read, and logs it: the reason, never the key.
func (m *Module) refusePerson(w http.ResponseWriter, model, why, words string) error {
	m.rag.logCall(&ragCall{At: nowISO(), Model: model, Mode: "?", AuthSource: "none", DurationMs: new(int64),
		Status: "error", Detail: sp("rejected: " + why + "; only people of this Studio are answered here")})
	m.A.Log.Info("grounded model call refused", "model", model, "why", why)
	return reqbody.JSON(w, 403, reqbody.O("error", reqbody.O("message", words, "type", "person")))
}
