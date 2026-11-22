package fleet

import (
	"testing"

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
