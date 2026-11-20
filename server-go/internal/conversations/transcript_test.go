package conversations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoTranscriptUnlessSaved: recording a reply writes nothing into the
// library, and deleting a chat leaves a transcript saved from it.
func TestNoTranscriptUnlessSaved(t *testing.T) {
	dir := t.TempDir()
	kb := filepath.Join(dir, "kb")
	os.MkdirAll(kb, 0o755)
	s := New(dir, kb, Hooks{})
	title := "Pump curves"
	c := s.Create(&title, "alice")
	s.RecordUserTurn(c.ID, "u1", "Which pump fits?", nil, "alice")
	model := "small-model"
	s.RecordAssistantTurn(c.ID, Message{ID: "a1", Role: "assistant", Content: strp("The P-200."), Model: &model, ParentID: strp("u1"), Timestamp: s.Now()})
	if _, err := os.Stat(filepath.Join(kb, ExportDir)); !os.IsNotExist(err) {
		t.Fatalf("a reply wrote %s: %v", ExportDir, err)
	}

	got := Transcript(s.Get(c.ID))
	for _, want := range []string{"# Pump curves\n", "Chat session " + c.ID + ", started ", " by alice.", "## User\n\nWhich pump fits?\n", "## Assistant (small-model)\n\nThe P-200.\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript lacks %q:\n%s", want, got)
		}
	}
	name := TranscriptName(c)
	if name != c.CreatedAt[:10]+"-"+c.ID[:8]+".md" {
		t.Errorf("name %q", name)
	}
	if OwnerTag(c) != "user-alice" {
		t.Errorf("owner tag %q", OwnerTag(c))
	}

	saved := filepath.Join(kb, ExportDir, name)
	os.MkdirAll(filepath.Dir(saved), 0o755)
	os.WriteFile(saved, []byte(got), 0o644)
	if gone := s.Delete(map[string]bool{c.ID: true}, func(*Conversation) bool { return true }); len(gone) != 1 {
		t.Fatalf("deleted %v", gone)
	}
	if _, err := os.Stat(saved); err != nil {
		t.Errorf("deleting the chat removed its saved transcript: %v", err)
	}
}
