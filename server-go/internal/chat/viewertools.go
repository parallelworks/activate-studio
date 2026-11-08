package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// apply_labels and show_in_viewer (tools.ts), and the pw CLI as tools.ts
// runs it.

func init() {
	goExecutors["apply_labels"] = (*Module).toolApplyLabels
	goExecutors["show_in_viewer"] = (*Module).toolShowInViewer
}

// errNodeTool sends a call to the tools still in Node: the library is one
// only Node knows.
var errNodeTool = errors.New("the tool runs in Node for this library")

func csvList(args map[string]any, k string) []string {
	out := []string{}
	for _, x := range strings.Split(argString(args, k), ",") {
		if x = jsTrim(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (m *Module) toolApplyLabels(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	if kb.Default == nil {
		return nil, errNodeTool
	}
	paths := csvList(args, "paths")
	add, remove := csvList(args, "add"), csvList(args, "remove")
	named := jsTrim(argString(args, "library"))
	want := named
	if want == "" {
		scoped := deref(ctx.Library)
		if strings.ToLower(scoped) == "all" {
			scoped = "kb"
		}
		want = scoped
	}
	if strings.ToLower(jsTrim(want)) == "all" {
		return nil, &toolError{`name one library (an id from list_libraries) to read or list; "all" is for search and query`}
	}
	rels := make([]string, len(paths))
	for i, p := range paths {
		rels[i] = edgeSlashes.ReplaceAllString(p, "")
	}
	keys, updated, stored, label, known, err := kb.Default.ApplyLabelsIn(r.Context(), want, rels, add, remove)
	if !known && err == nil {
		return nil, errNodeTool
	}
	if err != nil {
		return nil, &toolError{err.Error()}
	}
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + ": [" + strings.Join(updated[k], ", ") + "]"
	}
	where := "on the files, and re-indexed"
	if stored != "files" {
		where = "kept by the Studio for " + label
	}
	return &Outcome{Result: "Labels updated (" + where + "):\n" + strings.Join(lines, "\n"), Summary: itoaInt(len(paths)) + " paths labeled"}, nil
}

var (
	dagNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_./-]`)
	suffixRe      = regexp.MustCompile(`(?i)\.[a-z0-9]+$`)
)

func (m *Module) toolShowInViewer(r *http.Request, args map[string]any, ctx ToolCtx) (*Outcome, error) {
	kind := "file"
	if args["kind"] == "workflow_dag" {
		kind = "workflow_dag"
	}
	target := argString(args, "target")
	if kind == "file" {
		if fc, err := readFileContent(m, target); err != nil || fc == nil {
			if _, err := kb.ListDir(target, m.A.Cfg.KBRoot, false); err != nil {
				return &Outcome{Result: "Not found in the knowledge base: " + target, Summary: "not found"}, nil
			}
		}
	} else {
		target = dagNameUnsafe.ReplaceAllString(target, "")
		if _, err := m.pwToolCLI(r.Context(), []string{"workflows", "get", target}, 30*time.Second, deref(ctx.UserKey)); err != nil {
			return nil, err
		}
	}
	href := "#open=" + kind + ":" + encodeURIComponent(target)
	base := target[strings.LastIndex(target, "/")+1:]
	label := "Open the " + target + " DAG in the Library"
	if kind == "file" {
		label = "Open " + base + " in the Library"
	}
	suffix := strings.ToLower(suffixRe.FindString(target))
	link := "[" + label + "](" + href + ")"
	in := func(list ...string) bool { return kind == "file" && contains(list, suffix) }
	switch {
	case kind == "workflow_dag":
		return &Outcome{Result: "Show the interactive DAG inline in your reply with this markdown, then the link:\n![" + target + " DAG](/?embed=dag&workflow=" + encodeURIComponent(target) + ")\n" + link, Summary: "inline DAG + link for " + target}, nil
	case in(".stl", ".step", ".stp"):
		return &Outcome{Result: "Show the interactive 3D model inline in your reply with this markdown, then the link:\n![" + base + "](/?embed=model&path=" + encodeURIComponent(target) + ")\n" + link, Summary: "inline 3D viewer + link for " + target}, nil
	case in(".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"):
		return &Outcome{Result: "This is an image; show it inline in your reply with this markdown, then the link:\n![" + base + "](/api/kb/raw?path=" + encodeURIComponent(target) + ")\n" + link, Summary: "inline image + link for " + target}, nil
	case in(".pdf", ".docx", ".pptx", ".xlsx", ".doc", ".ppt", ".xls", ".odt", ".odp", ".ods"):
		return &Outcome{Result: "This document renders as page images; you can show its first page inline with this markdown, then the link:\n![" + base + " page 1](/api/kb/pdf-page?path=" + encodeURIComponent(target) + "&page=1)\n" + link, Summary: "inline page + link for " + target}, nil
	}
	return m.showInViewerRest(kind, target, label, href)
}

/* ---- the pw CLI for tools ---- */

const noCLIMessage = "The Studio cannot find the pw CLI, so platform actions (workflows, clusters, status) cannot run from here. " +
	"Install it, or choose where it is under Settings, Model access, pw CLI; in a container, bind the CLI and its credentials in, or set PW_CLI to its path."

var (
	upgradeNoise = regexp.MustCompile(`(?i)new version of the PW CLI|pw upgrade`)
	refusedHead  = regexp.MustCompile(`"status":\s*40[13]\b`)
	refusedTitle = regexp.MustCompile(`"title":\s*"(Unauthorized|Forbidden)"`)
	refusedErr   = regexp.MustCompile(`\b(401|Unauthorized)\b`)
	missingCred  = regexp.MustCompile(`(?i)no context configured|could not extract platform host|invalid credential format`)
)

func cliNoise(stderr string) string {
	var kept []string
	for _, l := range strings.Split(stderr, "\n") {
		if !upgradeNoise.MatchString(l) {
			kept = append(kept, l)
		}
	}
	return jsTrim(strings.Join(kept, "\n"))
}

// cliAuthFailure is the CLI's answer when its credential was refused or missing.
func cliAuthFailure(stdout, stderr string, personal bool) string {
	head := []rune(jsTrim(stdout))
	if len(head) > 300 {
		head = head[:300]
	}
	h := string(head)
	refused := (strings.HasPrefix(h, "{") && refusedHead.MatchString(h) && refusedTitle.MatchString(h)) || refusedErr.MatchString(stderr)
	missing := missingCred.MatchString(stderr + "\n" + h)
	if !refused && !missing {
		return ""
	}
	if personal {
		return "The platform refused the user's own key for this command (unauthorized). It may have expired or been revoked; ask the user to add a current ACTIVATE API key under Settings, Model access. This is a credential problem, not a problem with the command or its inputs."
	}
	state := "missing"
	if refused {
		state = "refused by the platform"
	}
	return "No usable platform credential for this command: the user has no personal ACTIVATE key in this Studio, and the deployment's own credential was " + state + ". Ask the user to add their ACTIVATE API key under Settings, Model access. This is a credential problem, not a problem with the command or its inputs."
}

// jsonFromCli is the first JSON value in the CLI's output.
func jsonFromCli(text string) (any, bool) {
	i := strings.IndexAny(text, "{[")
	if i < 0 {
		return nil, false
	}
	var v any
	if json.NewDecoder(strings.NewReader(text[i:])).Decode(&v) != nil {
		return nil, false
	}
	return v, true
}

// problemMessage is the platform's problem document the CLI printed for a
// refused request.
func problemMessage(stdout string) string {
	if !strings.Contains(stdout, "{") {
		return ""
	}
	v, ok := jsonFromCli(stdout)
	doc, isObj := v.(map[string]any)
	if !ok || !isObj {
		return ""
	}
	status, isNum := doc["status"].(float64)
	if !isNum || status < 400 {
		return ""
	}
	first := reqbody.JSString(doc["detail"])
	if !reqbody.Truthy(doc["detail"]) {
		first = reqbody.JSString(doc["title"])
		if !reqbody.Truthy(doc["title"]) {
			first = "status " + reqbody.Number(status)
		}
	}
	parts := []string{first}
	if errs, ok := doc["errors"].([]any); ok {
		for _, x := range errs[:min(len(errs), 8)] {
			e, _ := x.(map[string]any)
			at := strings.ReplaceAll(regexp.MustCompile(`^#/?`).ReplaceAllString(stringField(e, "pointer"), ""), "/", ".")
			if at == "" {
				at = "input"
			}
			d := "invalid"
			if v, ok := e["detail"]; ok && v != nil {
				d = reqbody.JSString(v)
			} else if v, ok := e["code"]; ok && v != nil {
				d = reqbody.JSString(v)
			}
			parts = append(parts, at+": "+d)
		}
	}
	return "The platform refused the request (" + reqbody.Number(status) + "): " + strings.Join(parts, "; ")
}

func stringField(o map[string]any, k string) string {
	if o == nil || o[k] == nil {
		return ""
	}
	return reqbody.JSString(o[k])
}

// pwToolCLI is tools.ts's pwCli: the CLI with the caller's own key when
// they added one, its failures said as what they are.
func (m *Module) pwToolCLI(ctx context.Context, args []string, timeout time.Duration, userKey string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.Plat.CLI(), args...)
	cmd.Env = os.Environ()
	if userKey != "" {
		cmd.Env = append(cmd.Env, "PW_API_KEY="+userKey)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limitBuf{&stdout, customMaxBuffer}, &limitBuf{&stderr, customMaxBuffer}
	err := cmd.Run()
	if err != nil && (errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)) {
		return "", &toolError{noCLIMessage}
	}
	if denied := cliAuthFailure(stdout.String(), stderr.String(), userKey != ""); denied != "" {
		return "", &toolError{denied}
	}
	if err != nil {
		if p := problemMessage(stdout.String()); p != "" {
			return "", &toolError{p}
		}
		if n := cliNoise(stderr.String()); n != "" {
			return "", &toolError{n}
		}
		return "", &toolError{"Command failed: " + m.Plat.CLI() + " " + strings.Join(args, " ") + "\n"}
	}
	return stdout.String(), nil
}

func (m *Module) showInViewerRest(kind, target, label, href string) (*Outcome, error) {
	opens := "the Library viewer renders the workflow DAG"
	if kind == "file" {
		opens = "the Library viewer opens the file and reveals it in its directory"
	}
	return &Outcome{Result: "Link ready. Include this markdown link verbatim in your reply so the user can open it (" + opens + "): [" + label + "](" + href + ")", Summary: "link for " + target}, nil
}
