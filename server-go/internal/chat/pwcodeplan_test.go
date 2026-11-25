package chat

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"
)

// pw code keeps the mode of any session, /plan's too, as the mode of every
// later one that names none: a new chat starts as the settings say all the
// same, and a chat that plans goes on planning in the session that takes
// over from its own.
func TestPlanModeStaysInTheChatThatAskedForIt(t *testing.T) {
	kept := "plan"
	modes := []string{}
	f := &fakePW{script: helloTurn}
	f.routes = map[string]http.HandlerFunc{
		"POST /v1/sessions": func(w http.ResponseWriter, r *http.Request) {
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			mode, _ := req["permissionMode"].(string)
			if mode == "" {
				mode = kept
			}
			f.mu.Lock()
			modes = append(modes, mode)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": "s1", "status": "idle", "permissionMode": mode})
		},
		"DELETE /v1/sessions/s1": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			modes = append(modes, "deleted")
			f.mu.Unlock()
			w.WriteHeader(204)
		},
	}
	m, base := pwModule(t, f, nil)
	say := func(id string) {
		t.Helper()
		post(t, base, map[string]any{"conversationId": id, "userMessageId": "u" + time.Now().Format("150405.000000000"),
			"messages": []any{map[string]any{"role": "user", "content": "What is the budget?"}}})
	}
	took := func(want ...string) {
		t.Helper()
		f.mu.Lock()
		defer f.mu.Unlock()
		if !slices.Equal(modes, want) {
			t.Fatalf("sessions made: %q, want %q", modes, want)
		}
		modes = []string{}
	}
	fresh := m.Conv.Create(nil, "")
	say(fresh.ID)
	took("plan", "deleted", "accept-edits")

	// The same session goes on.
	say(fresh.ID)
	took()

	// The session is replaced while the chat plans: it plans on.
	f.mu.Lock()
	f.detail = map[string]any{"permissionMode": "plan"}
	f.mu.Unlock()
	old := func() {
		l := *m.Conv.Get(fresh.ID).PWCode
		l.CreatedAt = time.Now().Add(-chatTokenTTL).Format(time.RFC3339Nano)
		m.Conv.SetPWCode(fresh.ID, &l)
	}
	old()
	kept = "accept-edits"
	say(fresh.ID)
	took("plan")
	// And while it does not, it does not.
	f.mu.Lock()
	f.detail = map[string]any{"permissionMode": "accept-edits"}
	f.mu.Unlock()
	old()
	kept = "plan"
	say(fresh.ID)
	took("plan", "deleted", "accept-edits")
}

// The Studio's own setting, Plan first, is asked for and kept.
func TestPlanFirstSettingStartsInPlanMode(t *testing.T) {
	f := &fakePW{script: helloTurn}
	m, base := pwModule(t, f, map[string]string{"PWCODE_PERMISSION_MODE": "plan"})
	c := m.Conv.Create(nil, "")
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "What is the budget?"}}})
	if len(f.created) != 1 || f.created[0]["permissionMode"] != "plan" {
		t.Fatalf("%v", f.created)
	}
}
