package conversations

import (
	"log/slog"

	"encoding/json"
	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The cases of server/test/bulkDelete.test.mjs and unansweredTurn.test.mjs,
// which run against Node directly (their app injects a user).

func serve(t *testing.T, convs string, auth bool) (*Store, *http.ServeMux) {
	ix := t.TempDir()
	if convs != "" {
		os.WriteFile(filepath.Join(ix, "conversations.json"), []byte(convs), 0o644)
	}
	s := New(ix, t.TempDir(), Hooks{})
	rt := &Routes{Store: s, AuthEnabled: func() bool { return auth }, SharedHistory: func() bool { return true },
		Who:    func(*http.Request) (Who, bool) { return Who{UserID: "u-me", Username: "me", ViewerID: "u-me"}, true },
		ToNode: func(http.ResponseWriter, *http.Request, string) { t.Fatal("handed to Node") }}
	mux := http.NewServeMux()
	rt.Register(mux, func(p string, fn func(http.ResponseWriter, *http.Request) error) {
		mux.Handle(p, apierr.Handler(slog.New(slog.DiscardHandler), fn))
	})
	return s, mux
}

func do(mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	sort.Strings(out)
	return out
}

func TestBulkDeleteOwnAndUnownedOnly(t *testing.T) {
	conv := func(id, owner string) string {
		o := "null"
		if owner != "" {
			o = `"` + owner + `"`
		}
		return `{"id":"` + id + `","title":"` + id + `","owner":` + o + `,"createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-01T00:00:00Z","activeBranchId":null,"messages":[]}`
	}
	s, mux := serve(t, "["+strings.Join([]string{conv("a", "me"), conv("b", "me"), conv("c", "me"), conv("theirs", "someone-else"), conv("legacy", "")}, ",")+"]", true)
	code, body := do(mux, "POST", "/api/chat/conversations/delete", `{"ids":["a","b","theirs","legacy","nope"]}`)
	if code != 200 {
		t.Fatal(code)
	}
	if got := strs(body["deleted"]); strings.Join(got, ",") != "a,b,legacy" {
		t.Errorf("deleted %v", got)
	}
	if got := strs(body["skipped"]); strings.Join(got, ",") != "nope,theirs" {
		t.Errorf("skipped %v", got)
	}
	var left []string
	for _, c := range s.List() {
		left = append(left, c.ID)
	}
	sort.Strings(left)
	if strings.Join(left, ",") != "c,theirs" {
		t.Errorf("left %v", left)
	}
	for _, b := range []string{`{"ids":[]}`, `{}`} {
		if code, _ := do(mux, "POST", "/api/chat/conversations/delete", b); code != 400 {
			t.Errorf("%s: %d", b, code)
		}
	}
}

func TestUnansweredWhileAReplyStreams(t *testing.T) {
	msg := func(id, role, parent string) string {
		p := "null"
		if parent != "" {
			p = `"` + parent + `"`
		}
		return `{"id":"` + id + `","role":"` + role + `","content":"` + id + `","parentId":` + p + `,"timestamp":"2026-10-14T00:00:00Z"}`
	}
	s, mux := serve(t, `[{"id":"c1","title":"c1","owner":null,"createdAt":"2026-10-14T00:00:00Z","updatedAt":"2026-10-14T00:00:00Z","activeBranchId":"u2","messages":[`+
		msg("u1", "user", "")+","+msg("a1", "assistant", "u1")+","+msg("u2", "user", "a1")+`]}]`, false)
	errOf := func(id string) any {
		_, body := do(mux, "GET", "/api/chat/conversations/c1", "")
		for _, m := range body["messages"].([]any) {
			if mm := m.(map[string]any); mm["id"] == id {
				return mm["error"]
			}
		}
		return "missing"
	}
	if errOf("u2") != NoReplyNote || errOf("u1") != nil || errOf("a1") != nil {
		t.Fatal("the unanswered message is not marked alone")
	}
	end := s.BeginAnswer("c1")
	if errOf("u2") != nil {
		t.Error("marked while a reply streams")
	}
	end()
	end()
	if errOf("u2") != NoReplyNote {
		t.Error("not marked again once the reply ended")
	}
	b, _ := os.ReadFile(s.file)
	if strings.Contains(string(b), "error") {
		t.Error("the error was written to the stored conversation")
	}
}

func TestSteerQueuesForARunningReply(t *testing.T) {
	s, mux := serve(t, `[{"id":"c1","title":null,"owner":null,"createdAt":"x","updatedAt":"x","activeBranchId":null,"messages":[]}]`, false)
	if code, _ := do(mux, "POST", "/api/chat/steer", `{"conversationId":"c1","id":"s1"}`); code != 400 {
		t.Errorf("no text: %d", code)
	}
	if _, body := do(mux, "POST", "/api/chat/steer", `{"conversationId":"c1","id":"s1","text":"also this"}`); body["queued"] != false {
		t.Errorf("queued with no reply running: %v", body)
	}
	end := s.BeginAnswer("c1")
	defer end()
	if _, body := do(mux, "POST", "/api/chat/steer", `{"conversationId":"c1","id":"s2","text":" also this "}`); body["queued"] != true {
		t.Errorf("not queued: %v", body)
	}
	if st := s.TakeSteers("c1"); len(st) != 1 || st[0].Text != "also this" || st[0].ID != "s2" {
		t.Errorf("steers %+v", st)
	}
}
