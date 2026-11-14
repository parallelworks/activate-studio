package chat

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// suggest.ts: what a person might type next, read off the turn that just
// ended, from the tool calls stored with it.

var (
	runIDRe     = regexp.MustCompile(`(?i)\b([a-z0-9][a-z0-9_.-]*-\d{5})\b`)
	decisionRe  = regexp.MustCompile(`(?i)NEEDS DECISION:`)
	askTailRe   = regexp.MustCompile(`\?\s*$`)
	completedRe = regexp.MustCompile(`(?i)completed`)
	failedRe    = regexp.MustCompile(`(?i)\b(error|failed)\b`)
	citeRe      = regexp.MustCompile(`(?i)\[([^\]]+\.(?:md|pdf|docx|pptx|xlsx|txt))\]\(#open=file:[^)]+\)`)
)

type suggestion struct{ Text, Why string }

func firstGroup(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// SuggestNext is suggestNext(c).
func SuggestNext(c *conversations.Conversation) *jsonobj.Object {
	var m *conversations.Message
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == "assistant" {
			m = &c.Messages[i]
			break
		}
	}
	empty := []any{}
	if m == nil {
		return reqbody.O("after", nil, "suggestions", empty)
	}
	if c.Messages[len(c.Messages)-1].Role != "assistant" {
		return reqbody.O("after", m.ID, "suggestions", empty)
	}
	content := m.Text()
	var out []suggestion
	push := func(text, why string) {
		for _, s := range out {
			if s.Text == text {
				return
			}
		}
		if len(out) < 3 {
			out = append(out, suggestion{text, why})
		}
	}
	tail := ""
	for _, l := range strings.Split(jsTrim(content), "\n") {
		if l != "" {
			tail = l
		}
	}
	if decisionRe.MatchString(content) || askTailRe.MatchString(tail) {
		return reqbody.O("after", m.ID, "suggestions", empty)
	}
	for _, p := range m.Parts {
		if p.Kind == "approval" && !p.Resolved {
			return reqbody.O("after", m.ID, "suggestions", empty)
		}
	}
	var calls []conversations.Part
	for _, p := range m.Parts {
		if p.Kind == "tool_call" {
			calls = append(calls, p)
		}
	}
	argsOf := func(p conversations.Part) map[string]any {
		a := p.Args
		if a == "" {
			a = "{}"
		}
		var v map[string]any
		if json.Unmarshal([]byte(a), &v) != nil || v == nil {
			return map[string]any{}
		}
		return v
	}
	res := func(p conversations.Part) string {
		if p.Result == nil {
			return ""
		}
		return *p.Result
	}
	var failed []conversations.Part
	for _, p := range calls {
		if p.Status == "error" {
			failed = append(failed, p)
		}
	}
	for _, p := range calls {
		a := argsOf(p)
		dry, hasDry := a["dry_run"]
		if p.Name == "run_workflow" && hasDry && dry == false {
			slug := firstGroup(runIDRe, res(p))
			if slug == "" {
				slug = firstGroup(runIDRe, content)
			}
			if slug != "" {
				push("Follow run "+slug+" until it finishes and tell me the outcome", "a run was launched")
				push("Show me the output of run "+slug, "a run was launched")
			}
		}
		if p.Name == "run_workflow" && !(hasDry && dry == false) {
			name := "that workflow"
			if v, ok := a["name"]; ok && v != nil {
				name = jsString(v)
			}
			push("Launch "+name+" for real now", "the last run was a dry run")
		}
		if p.Name == "watch_run" || p.Name == "workflow_run_detail" {
			slug := jsString(a["run"])
			if slug == "" {
				slug = firstGroup(runIDRe, res(p))
			}
			if completedRe.MatchString(res(p)) && slug != "" {
				push("Summarize the results of "+slug+" and save them to the knowledge base", "the run completed")
			}
			if failedRe.MatchString(res(p)) && slug != "" {
				push("Explain why "+slug+" failed and fix the submission", "the run failed")
			}
		}
		if p.Name == "get_workflow" || p.Name == "validate_workflow" {
			if name := jsString(a["name"]); name != "" {
				push("Run "+name+" on a connected system as a small debug job", "a workflow was inspected")
			}
		}
		if p.Name == "hpc_environments" || p.Name == "list_clusters" {
			sys := a["cluster"]
			if sys == nil {
				sys = a["resource"]
			}
			if s := jsString(sys); s != "" {
				push("Run activatebatch on "+s+" as a debug job that prints the hostname", "systems were listed")
			} else {
				push("Run a small debug job on the system with the most free nodes", "systems were listed")
			}
		}
		if p.Name == "delegate" {
			push("Show me how the delegated agents are doing", "work was delegated")
		}
		if p.Name == "search_kb" {
			if cite := firstGroup(citeRe, content); cite != "" {
				push("Open "+cite+" and summarize it", "a search cited it")
			}
		}
		if p.Name == "write_kb_file" || p.Name == "edit_kb_file" {
			if path := jsString(a["path"]); path != "" {
				push("Open "+path+" in the library", "a file was written")
			}
		}
	}
	if len(failed) > 0 && len(out) < 3 {
		push("Retry the "+failed[0].Name+" step that failed and explain what went wrong", "a tool call failed")
	}
	if len(calls) == 0 && u16(content) > 400 {
		push("Give me the sources behind that, as clickable paths", "a long answer without tool calls")
		push("Save that as a short note in the knowledge base", "a long answer")
	}
	list := []*jsonobj.Object{}
	for _, s := range out {
		list = append(list, reqbody.O("text", s.Text, "why", s.Why))
	}
	return reqbody.O("after", m.ID, "suggestions", list)
}
