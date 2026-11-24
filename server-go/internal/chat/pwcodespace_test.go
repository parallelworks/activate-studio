package chat

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// A pw code session working in the person's own space writes there with
// its own tools, on disk: the turn's end has the space indexed again, or
// a file it made has only its name in the index until a rebuild.
func TestAPWCodeTurnInMySpaceHasItIndexed(t *testing.T) {
	b := newWhereBed(t)
	m := b.m
	prev := m.A.Providers.SpaceFolder
	m.A.Providers.SpaceFolder = func(owner string) (string, string) {
		if owner == "ada" {
			return "cluster-a", "/home/ada/studio"
		}
		return "", ""
	}
	t.Cleanup(func() { m.A.Providers.SpaceFolder = prev })
	r := b.req(false)
	in := &conversations.PWCodeLink{Site: "cluster-a", Session: "s1", Workspace: "/home/ada/studio"}

	for _, ws := range []string{"/home/ada/studio-chat", "/home/ada"} {
		if m.workspaceSpace(r, &conversations.PWCodeLink{Site: "cluster-a", Workspace: ws}) != nil {
			t.Errorf("%s is not in the space", ws)
		}
	}
	if m.workspaceSpace(r, &conversations.PWCodeLink{Site: "cluster-b", Workspace: "/home/ada/studio"}) != nil {
		t.Error("the same folder on another cluster is not the space")
	}
	if m.workspaceSpace(r, &conversations.PWCodeLink{Site: "cluster-a", Workspace: "/home/ada/studio/agents/review"}) == nil {
		t.Error("a folder in the space is the space")
	}

	turn := func(tools ...string) []string {
		b.space.changed = nil
		c := m.Conv.Create(nil, "")
		tr := &pwTurn{m: m, convID: c.ID, link: in, done: make(chan struct{}), spaceChanged: m.workspaceSpace(r, in)}
		for i, name := range tools {
			raw, _ := json.Marshal(map[string]any{"type": "tool_start", "name": name, "args": "{}", "callId": "c" + string(rune('1'+i))})
			ev, _ := jsonv.Decode(raw)
			tr.handle(ev.(*jsonv.Object))
		}
		tr.content.WriteString("Done.")
		tr.finish()
		return b.space.changed
	}
	if got := turn("Bash"); !slices.Equal(got, []string{"space-ada"}) {
		t.Errorf("a shell command in the space: %v", got)
	}
	if got := turn("Read", "Grep", "Write"); !slices.Equal(got, []string{"space-ada"}) {
		t.Errorf("a file written in the space: %v", got)
	}
	if got := turn("Read", "Glob", "mcp__studio__write_kb_file"); len(got) != 0 {
		t.Errorf("reads, and a Studio tool that tells the index itself: %v", got)
	}
	if got := turn(); len(got) != 0 {
		t.Errorf("an answer with no tools: %v", got)
	}
}
