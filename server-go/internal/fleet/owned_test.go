package fleet

import (
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// A standing agent's session belongs to the person it acts for, and no
// one else (the Code page shows it only to them).
func TestSessionsOf(t *testing.T) {
	f, _, _ := stand(t)
	alex, err := f.Create(Input{Name: "Alex digest", Goal: "Digest.", Every: "manual"}, &auth.User{ID: "alex-id"})
	if err != nil {
		t.Fatal(err)
	}
	sam, _ := f.Create(Input{Name: "Sam digest", Goal: "Digest.", Every: "manual"}, &auth.User{ID: "sam-id"})
	none, _ := f.Create(Input{Name: "Nobody's", Goal: "Digest.", Every: "manual"}, nil)
	f.mu.Lock()
	f.agents[alex.ID].Session, f.agents[sam.ID].Session, f.agents[none.ID].Session = "s-alex", "s-sam", "s-none"
	f.mu.Unlock()
	if got := f.SessionsOf("alex-id"); len(got) != 1 || got[0] != "s-alex" {
		t.Fatalf("alex: %v", got)
	}
	if got := f.SessionsOf(""); len(got) != 0 {
		t.Fatalf("nobody: %v", got)
	}
}
