package fleet

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
)

// A wake-up cut off by a restart before its session worked leaves the
// agent idle and able to wake, not working with nothing to follow.
func TestAWakeCutOffByARestart(t *testing.T) {
	f, _, _ := stand(t)
	a, err := f.Create(Input{Name: "Cut off", Goal: "Count.", Every: "1h"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	cur := f.agents[a.ID]
	cur.State, cur.Note, cur.Starting = "working", "wake-up 1 starting", true
	f.persist(cur)
	f.mu.Unlock()

	g := New(f.A, f.Daemon)
	got := g.Get(a.ID)
	if got.State != "idle" || got.Starting || got.NextTickAt == nil {
		t.Fatalf("after the restart: %+v", got)
	}
	if !g.Tick(a.ID, "now", true) {
		t.Fatal("it cannot be woken")
	}
	waitFor(t, "the wake-up", func() bool { g.mu.Lock(); defer g.mu.Unlock(); return !g.busy[a.ID] })
	if got := g.Get(a.ID); got.Starting || got.Session == "" {
		t.Fatalf("after the wake-up: %+v", got)
	}
}

// On a Studio with sign-in, an agent with no record of its owner (one
// taken over from the earlier engine) does not wake without the Studio's
// tools: it waits for its owner, and wakes once they open the Fleet page.
// The person's rights are theirs at each wake-up: a name or group taken
// out of the administrators takes the agent out of the main library.
func TestAnAgentActsForItsOwnerAsTheyAreNow(t *testing.T) {
	f, _, _ := stand(t)
	f.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	admins := "cy,group:leads"
	defer func(g func(string) string) { mainlib.Getenv = g }(mainlib.Getenv)
	mainlib.Getenv = func(k string) string {
		if k == "STUDIO_ADMINS" {
			return admins
		}
		return ""
	}
	ada := &auth.User{ID: "ada", Username: "ada", Groups: []string{"leads"}}
	a, err := f.Create(Input{Name: "Old one", Goal: "Count.", Every: "manual"}, ada)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.agents[a.ID].OwnerUser = nil
	f.persist(f.agents[a.ID])
	f.mu.Unlock()
	if f.Tick(a.ID, "now", true) {
		t.Fatal("it woke with no owner record")
	}
	if got := f.Get(a.ID); got.State != "paused" || got.Note != NoOwner {
		t.Fatalf("not waiting for its owner: %+v", got)
	}
	f.Seen(&auth.User{ID: "bob"})
	if f.Get(a.ID).OwnerUser != nil {
		t.Fatal("another person became its owner")
	}
	f.Seen(ada)
	if got := f.Get(a.ID); got.State != "idle" || got.OwnerUser == nil || got.OwnerUser.ID != "ada" {
		t.Fatalf("after its owner came: %+v", got)
	}

	inLib := func() bool { _, _, in := f.folder(f.Get(a.ID)); return in }
	if !inLib() {
		t.Fatal("an administrator by group works out of the main library")
	}
	// Ada leaves the group: her next visit carries it, and the agent
	// leaves the main library.
	f.Seen(&auth.User{ID: "ada", Username: "ada"})
	if inLib() {
		t.Fatal("a group she left still lets it write the main library")
	}
	// A name taken out of STUDIO_ADMINS counts at the next wake-up.
	cy := &auth.User{ID: "cy", Username: "cy"}
	c, _ := f.Create(Input{Name: "Cy's", Goal: "Count.", Every: "manual"}, cy)
	if _, _, in := f.folder(f.Get(c.ID)); !in {
		t.Fatal("an administrator by name works out of the main library")
	}
	admins = ""
	if _, _, in := f.folder(f.Get(c.ID)); in {
		t.Fatal("a name taken out of the administrators still writes the main library")
	}
}

// A restart after the wake-up's message reached the session finds the
// agent working in that session, not idle: the round goes on and is
// followed, and the next trigger does not message a busy session.
func TestARestartAfterTheMessageKeepsItWorking(t *testing.T) {
	f, d, _ := stand(t)
	d.goalHold = make(chan struct{})
	a, err := f.Create(Input{Name: "Mid-wake", Goal: "Count.", Every: "1h"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Tick(a.ID, "now", true) {
		t.Fatal("no wake-up")
	}
	waitFor(t, "the message", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return len(d.messages) == 1 })
	waitFor(t, "the record", func() bool { return !f.Get(a.ID).Starting })
	// The Studio stops here, before the goal is set.
	g := New(f.A, f.Daemon)
	got := g.Get(a.ID)
	close(d.goalHold)
	if got.State != "working" || got.Session == "" || got.Starting {
		t.Fatalf("after a restart mid-wake: %+v", got)
	}
	waitFor(t, "the wake-up", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.busy[a.ID] })
}

// A group's rights in an agent last while its owner is seen: a session is
// made anew when the rights it acts with change, so its token never
// carries an older record, and groups the owner has not been seen with
// for a week no longer count.
func TestGroupRightsAreCurrent(t *testing.T) {
	f, d, _ := stand(t)
	f.A.Cfg.Auth.JWKSURL = "https://id.platform.example/jwks"
	defer func(g func(string) string) { mainlib.Getenv = g }(mainlib.Getenv)
	mainlib.Getenv = func(k string) string {
		if k == "STUDIO_ADMINS" {
			return "group:leads"
		}
		return ""
	}
	ada := &auth.User{ID: "ada", Username: "ada", Groups: []string{"leads"}}
	a, err := f.Create(Input{Name: "Lead's", Goal: "Count.", Every: "manual"}, ada)
	if err != nil {
		t.Fatal(err)
	}
	round := func() string {
		t.Helper()
		if !f.Tick(a.ID, "now", true) {
			t.Fatalf("no wake-up: %+v", f.Get(a.ID))
		}
		waitFor(t, "the wake-up", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.busy[a.ID] })
		s := f.Get(a.ID).Session
		d.finish(s, "done", 1)
		f.follow(context.Background())
		return s
	}
	inLib := func() bool { _, _, in := f.folder(f.Get(a.ID)); return in }
	first := round()
	if !inLib() {
		t.Fatal("a group administrator works out of the main library")
	}
	if again := round(); again != first {
		t.Fatal("a new session with the same rights")
	}
	// Ada leaves the group and opens the Fleet page: the next wake-up is
	// a new session, out of the main library.
	f.Seen(&auth.User{ID: "ada", Username: "ada"})
	if inLib() {
		t.Fatal("still in the main library")
	}
	if third := round(); third == first {
		t.Fatal("the session made with her old rights was kept")
	}
	// Back in the group, then not seen for eight days: her groups no
	// longer count.
	f.Seen(ada)
	if !inLib() {
		t.Fatal("back in the group, not in the main library")
	}
	f.mu.Lock()
	f.agents[a.ID].OwnerSeenAt = time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	f.mu.Unlock()
	if inLib() {
		t.Fatal("groups from eight days ago still count")
	}

	// An administrator by name whose groups change keeps working in the
	// library, in a new session whose token carries the groups they have.
	admins := "group:leads,cy"
	mainlib.Getenv = func(k string) string {
		if k == "STUDIO_ADMINS" {
			return admins
		}
		return ""
	}
	cy := &auth.User{ID: "cy", Username: "cy", Groups: []string{"ops"}}
	c, _ := f.Create(Input{Name: "Cy's", Goal: "Count.", Every: "manual"}, cy)
	a = c
	s1 := round()
	f.Seen(&auth.User{ID: "cy", Username: "cy"})
	if s2 := round(); s2 == s1 || !inLib() {
		t.Fatalf("groups changed: session %s then %s, in the library %v", s1, s2, inLib())
	}
}

// Retest A: a standing agent's new session made on a model its owner's AI
// connection does not offer is moved to one it does before its first
// message, and the journal says so.
func TestAStandingAgentStartsOnAModelOffered(t *testing.T) {
	f, d, _ := stand(t)
	var asked []string
	f.A.Providers.AgentModel = func(r *http.Request, cur, chosen string) (string, string) {
		who := ""
		if u := auth.UserOf(r); u != nil {
			who = u.ID
		}
		asked = append(asked, who+" "+cur)
		if cur == "acct:anthropic/claude-x" {
			return "acct:codex/gpt-b", "pw code's model " + cur + " is not enabled for the AI connection, so the session runs on acct:codex/gpt-b"
		}
		return "", ""
	}
	a, err := f.Create(Input{Name: "Model", Goal: "Count.", Every: "manual"}, &auth.User{ID: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Tick(a.ID, "now", true) {
		t.Fatal("no wake-up")
	}
	waitFor(t, "the wake-up", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.busy[a.ID] })
	d.mu.Lock()
	models := append([]string(nil), d.models...)
	d.mu.Unlock()
	if len(asked) != 1 || asked[0] != "ada acct:anthropic/claude-x" {
		t.Fatalf("asked %v", asked)
	}
	if len(models) != 1 || models[0] != "acct:codex/gpt-b before 0 messages" {
		t.Fatalf("models %v", models)
	}
	found := false
	for _, j := range f.Journal(a.ID, 50) {
		if strings.Contains(j.Text, "runs on acct:codex/gpt-b") {
			found = true
		}
	}
	if !found {
		t.Fatal("the journal does not say the model moved")
	}
}
