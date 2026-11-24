package chat

import (
	"net/http"
	"strings"
)

// The guide to the Studio's tools: which to reach for, and for what. A pw
// code chat's prompt (pwcodePrompt) and the MCP server's instructions
// (mcpInstructions) are both made of these lines, so they say the same.

// guideLead is why the tools come before the shell.
const guideLead = "It answers from the index in under a second, across every library, including libraries on hosts this session cannot reach, so use it before the shell for anything about the libraries and their files:"

// toolGuide is the guide for a caller that has the tools in has, each named
// as tool names it. A tool the caller does not have is not named.
type toolGuide struct {
	has  map[string]bool
	tool func(string) string
	// chat: a chat of the Studio's, whose person chose its libraries.
	chat bool
}

func newToolGuide(names []string, tool func(string) string, chat bool) toolGuide {
	g := toolGuide{has: map[string]bool{}, tool: tool, chat: chat}
	for _, n := range names {
		g.has[n] = true
	}
	return g
}

// read is the lines on finding, counting and reading files.
func (g toolGuide) read() string {
	var b strings.Builder
	if g.has["search_kb"] {
		b.WriteString("\n- Find files with " + g.tool("search_kb") + " (file names, the words inside files, and meaning), not find, grep or ls.")
		if g.has["list_libraries"] {
			b.WriteString(" " + g.tool("list_libraries") + " names the libraries; its library argument takes an id or \"all\", and without one it searches ")
		} else {
			b.WriteString(" Its library argument takes an id or \"all\"; without one it searches ")
		}
		if g.chat {
			b.WriteString("the libraries the person chose for this chat.")
		} else {
			b.WriteString("the Studio's main library.")
		}
	}
	if g.has["query_corpus"] {
		b.WriteString("\n- Ask " + g.tool("query_corpus") + " about many files at once: the largest, newest or oldest, recent changes, totals by type, and the biggest folders.")
	}
	if g.has["read_kb_file"] {
		b.WriteString("\n- Read a file with " + g.tool("read_kb_file") + ", which returns the text the index holds, PDFs and Office files included.")
	}
	switch original, list := g.has["read_original"], g.has["list_kb_dir"]; {
	case original && list:
		b.WriteString(" " + g.tool("read_original") + " opens the file itself, and " + g.tool("list_kb_dir") + " lists a folder.")
	case original:
		b.WriteString(" " + g.tool("read_original") + " opens the file itself.")
	case list:
		b.WriteString(" " + g.tool("list_kb_dir") + " lists a folder.")
	}
	return b.String()
}

// write is the line on saving files.
func (g toolGuide) write() string {
	if !g.has["write_kb_file"] {
		return ""
	}
	s := "\n- Save what the person asks to keep with " + g.tool("write_kb_file") + " (Markdown, PDF, Word or Excel), which writes it into the library and indexes it"
	if g.has["edit_kb_file"] {
		s += ", and change a library file with " + g.tool("edit_kb_file")
	}
	return s + "."
}

// platform is the line on the platform's tools.
func (g toolGuide) platform() string {
	if !g.has["run_workflow"] {
		return ""
	}
	return "\n- Platform workflows, clusters and runs: " + g.tool("list_workflows") + ", " + g.tool("run_workflow") + ", " + g.tool("workflow_runs") + " and " + g.tool("hpc_status") + "."
}

// rest is what the shell is for, and how to name a file.
func (g toolGuide) rest() string {
	s := "\nUse the shell for work these tools do not cover, such as running code. Name each file you used by its library path"
	if g.has["show_in_viewer"] {
		s += "; " + g.tool("show_in_viewer") + " gives a link that opens it in the Studio"
	}
	return s + "."
}

// mcpSpecs is the tools an MCP caller is listed, and may call: a pw code
// chat's, a person's own session's, or those any MCP client gets.
func (m *Module) mcpSpecs(r *http.Request, person bool, chat *chatToken) []ToolSpec {
	switch {
	case person:
		return m.personTools(r, m.personCtx(r))
	case chat != nil:
		return m.chatTools(r, grantDelegates(chat.Conv))
	}
	return m.exposedSpecs()
}

// mcpInstructions is the MCP server's instructions to a caller with specs:
// the guide, by the tools' own names, since each client gives them a prefix
// of its own. It is in every session's context, so it leaves out what each
// tool's description says.
func (m *Module) mcpInstructions(specs []ToolSpec, chat bool) string {
	g := newToolGuide(specNames(specs), func(n string) string { return n }, chat)
	read := g.read()
	if read == "" {
		return ""
	}
	return "This server is the index and tools of " + m.eff().AppName + ", an ACTIVATE Studio. " + guideLead + read + g.write() + g.platform() + g.rest()
}
