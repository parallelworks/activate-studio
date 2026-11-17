//go:build !windows

package transfers

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// In a private location (a person's space), a link someone with write
// access made there does not carry the owner's writes, folders, moves or
// removals to where it points, even inside the location; elsewhere a link
// inside the location is followed as before.
func TestPrivateWritesDoNotFollowLinks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, filepath.Join(root, "private", "diary.txt"), "mine")
	write(t, filepath.Join(root, "team", "notes.txt"), "ours")
	if err := os.Symlink(filepath.Join(root, "private"), filepath.Join(root, "team", "door")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "private", "diary.txt"), filepath.Join(root, "team", "diary-link")); err != nil {
		t.Fatal(err)
	}
	fs := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "space", Private: true}
	if _, err := fs.Write(ctx, "team/door/dropped.txt", bytes.NewReader([]byte("x")), 0, 0o644); err == nil {
		t.Fatal("a write through a link was taken")
	}
	if err := fs.Mkdir(ctx, "team/door/sub"); err == nil {
		t.Fatal("a folder through a link was made")
	}
	if _, err := fs.Write(ctx, "team/staged", bytes.NewReader([]byte("y")), 0, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Land(ctx, []LandPair{{Staged: "team/staged", Final: "team/door/landed.txt"}}); err == nil {
		t.Fatal("a move through a link was taken")
	}
	if _, err := fs.RemoveTrees(ctx, []string{"team/door/diary.txt"}); err == nil {
		t.Fatal("a removal through a link was taken")
	}
	got, _ := os.ReadDir(filepath.Join(root, "private"))
	if len(got) != 1 || got[0].Name() != "diary.txt" {
		t.Fatalf("private now holds %v", got)
	}
	// Writing onto a link to a file never writes where it points.
	if _, err := fs.Write(ctx, "team/diary-link", bytes.NewReader([]byte("over")), 0, 0o644); err == nil {
		t.Fatal("a write onto a link was taken")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "private", "diary.txt")); string(b) != "mine" {
		t.Fatalf("the diary was changed: %q", b)
	}
	// The link itself may still be moved or removed.
	if _, err := fs.RemoveTrees(ctx, []string{"team/door"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "private", "diary.txt")); err != nil {
		t.Fatal("removing the link removed what it pointed to")
	}
	// A plain folder is written as before.
	if _, err := fs.Write(ctx, "team/new/plain.txt", bytes.NewReader([]byte("z")), 0, 0o644); err != nil {
		t.Fatal(err)
	}
	// Outside a space a link inside the location is followed, as before.
	if err := os.Symlink(filepath.Join(root, "private"), filepath.Join(root, "team", "door")); err != nil {
		t.Fatal(err)
	}
	open := &RemoteFS{R: &localRunner{}, SiteID: "h", Dir: root, Name: "loc"}
	if err := open.Mkdir(ctx, "team/door/sub"); err != nil {
		t.Fatal(err)
	}
}
