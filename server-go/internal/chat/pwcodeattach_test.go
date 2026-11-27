package chat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngBytes is a small PNG, as a pasted screenshot arrives.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00"), bytes.Repeat([]byte{7}, 64)...)

func attach(t *testing.T, m *Module, name string, data []byte) string {
	t.Helper()
	dir := filepath.Join(m.A.Cfg.KBRoot, attachDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return encodeAttachID(attachDir + "/" + name)
}

// A picture attached in the chat's composer goes to the chat's pw code
// session with the message, saved with the session and named by its
// placeholder, numbered after the session's own; another file is named by
// where it is in the library. Before, pw code was sent the text alone.
func TestAPictureAttachedInTheChatReachesPWCode(t *testing.T) {
	f := &fakePW{script: helloTurn}
	f.detail = map[string]any{"history": []any{map[string]any{"kind": "user", "text": "earlier [Image #1] and [Image #2]"}}}
	var saved []map[string]any
	f.routes = map[string]http.HandlerFunc{
		"POST /v1/sessions/s1/images": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			saved = append(saved, body)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"path":"/home/me/.pw/code/images/s1-3.png"}`)
		},
	}
	m, base := pwModule(t, f, nil)
	shot := attach(t, m, "Screenshot 2026-11-27 at 10.00.00.png", pngBytes)
	notes := attach(t, m, "notes.txt", []byte("the budget is in notes"))
	c := m.Conv.Create(nil, "")
	// A new chat's first message is handed to the session view to send,
	// which has no attachments: one that has them is sent from here.
	sse := post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1", "opensSession": true,
		"messages": []any{map[string]any{"role": "user", "content": "What is in this picture?", "attachment_ids": []string{shot, notes}}}})
	if strings.Contains(sse, "pwcode_first") || !strings.Contains(sse, "event: done") {
		t.Fatalf("the turn was not run here:\n%s", sse)
	}
	if len(saved) != 1 || saved[0]["seq"] != float64(3) || saved[0]["mime"] != "image/png" || saved[0]["data"] != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("saved with the session: %v", saved)
	}
	if len(f.messages) != 1 || !strings.HasPrefix(f.messages[0], "What is in this picture? [Image #3]") ||
		!strings.Contains(f.messages[0], "[Attached file: notes.txt; stored in the knowledge base at uploads/chat/notes.txt]") ||
		strings.Contains(f.messages[0], "Screenshot") {
		t.Fatalf("sent: %q", f.messages)
	}
	want := map[string]any{"placeholder": "[Image #3]", "path": "/home/me/.pw/code/images/s1-3.png", "mime": "image/png"}
	if len(f.images[0]) != 1 || !mapsEqual(f.images[0][0], want) {
		t.Fatalf("images sent: %v", f.images)
	}

	// A picture alone is a message too.
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u2",
		"messages": []any{map[string]any{"role": "user", "content": "", "attachment_ids": []string{shot}}}})
	if len(f.messages) != 2 || f.messages[1] != "[Image #3]" || len(f.images[1]) != 1 {
		t.Fatalf("a picture alone: %q %v", f.messages, f.images)
	}
}

// A file named like a picture that is not one, or a picture pw code does
// not take, is named by its place in the library and saved nowhere else.
func TestAFileThatIsNoPictureIsNamedNotSaved(t *testing.T) {
	f := &fakePW{script: helloTurn}
	f.routes = map[string]http.HandlerFunc{
		"POST /v1/sessions/s1/images": func(w http.ResponseWriter, r *http.Request) { t.Error("saved as an image") },
	}
	m, base := pwModule(t, f, nil)
	fake := attach(t, m, "photo.png", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"))
	c := m.Conv.Create(nil, "")
	post(t, base, map[string]any{"conversationId": c.ID, "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "Look", "attachment_ids": []string{fake}}}})
	if len(f.messages) != 1 || !strings.Contains(f.messages[0], "[Attached file: photo.png;") || len(f.images[0]) != 0 {
		t.Fatalf("sent: %q %v", f.messages, f.images)
	}
}

func mapsEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
