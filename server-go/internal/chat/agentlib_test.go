package chat

import (
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

func TestFindSessions(t *testing.T) {
	row := func(id, name, auto, preview, ws, at string) any {
		o := jsonv.NewObject("id", id, "status", "idle", "workspace", ws, "updatedAt", at)
		if name != "" {
			o.Set("name", name)
		}
		if auto != "" {
			o.Set("autoName", auto)
		}
		if preview != "" {
			o.Set("preview", preview)
		}
		return o
	}
	rows := []any{
		row("20261101-100000-aaaaaaaa", "", "Site survey report draft", "Write the north site survey report as a PDF", "/lib/agents/a", "2026-11-01"),
		row("20261102-100000-bbbbbbbb", "budget", "", "Tidy the budget workbook", "/lib/finance", "2026-11-02"),
		row("20261103-100000-cccccccc", "", "Survey photos", "Sort the survey photos", "/lib/photos", "2026-11-03"),
	}
	hits := findSessions(rows, "the survey report session")
	if len(hits) != 2 || tmpl(prop(hits[0], "id")) != "20261101-100000-aaaaaaaa" {
		t.Fatalf("hits %v", hits)
	}
	if hits := findSessions(rows, "Budget"); len(hits) != 1 || sessionScore(hits[0], "Budget") != 1000 {
		t.Fatalf("exact name: %v", hits)
	}
	if hits := findSessions(rows, "pw code agent session"); len(hits) != 0 {
		t.Fatalf("stop words matched: %v", hits)
	}
	line := sessionLine(rows[0])
	if !strings.Contains(line, "Site survey report draft") || !strings.Contains(line, "asked: Write the north site") {
		t.Fatal(line)
	}
	if !sessionIDRe.MatchString("20261111-195119-7f57c8de") || sessionIDRe.MatchString("survey report") {
		t.Fatal("session id pattern")
	}
}

func TestAgentSettled(t *testing.T) {
	st := func(status string, goal any, approvals []any) any {
		o := jsonv.NewObject("status", status, "approvals", approvals)
		if goal != nil {
			o.Set("goal", goal)
		}
		return o
	}
	cases := []struct {
		st   any
		want bool
	}{
		{st("running", nil, nil), false},
		{st("idle", nil, nil), true},
		{st("idle", jsonv.NewObject("active", true, "paused", false), nil), false},
		{st("idle", jsonv.NewObject("active", true, "paused", true), nil), true},
		{st("idle", jsonv.NewObject("active", false), nil), true},
		{st("awaiting_approval", nil, nil), true},
		{st("idle", jsonv.NewObject("active", true, "paused", false), []any{jsonv.NewObject("id", "a1")}), true},
	}
	for i, c := range cases {
		if got := agentSettled(c.st); got != c.want {
			t.Errorf("%d: settled %v", i, got)
		}
	}
}
