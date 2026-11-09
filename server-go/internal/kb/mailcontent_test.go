package kb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The viewer shows an email as its message, not as raw MIME.
func TestReadFileContentShowsEmail(t *testing.T) {
	root := t.TempDir()
	msg := "From: a@example.com\r\nSubject: =?UTF-8?Q?Caf=C3=A9_plans?=\r\nContent-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\nMeet at the caf=C3=A9.\r\n"
	dir := filepath.Join(root, "Mail", "INBOX.mbox", "Messages")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "42.emlx"), []byte(fmt.Sprintf("%d\n%s<?xml version=\"1.0\"?><plist/>\n", len(msg), msg)), 0o644)
	os.WriteFile(filepath.Join(root, "note.eml"), []byte(msg), 0o644)
	for _, rel := range []string{"Mail/INBOX.mbox/Messages/42.emlx", "note.eml"} {
		o, err := ReadFileContent(rel, root, filepath.Join(root, ".cache"))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(o)
		var got map[string]any
		json.Unmarshal(b, &got)
		if got["kind"] != "text" || got["source"] != "extracted" || got["format"] != "text" || got["truncated"] != false {
			t.Errorf("%s: %s", rel, b)
		}
		c, _ := got["content"].(string)
		if !strings.Contains(c, "Subject: Café plans\n") || !strings.Contains(c, "Meet at the café.") || strings.Contains(c, "plist") {
			t.Errorf("%s: content %q", rel, c)
		}
		if !strings.HasPrefix(string(b), `{"path":`) {
			t.Errorf("key order: %s", b)
		}
	}
}
