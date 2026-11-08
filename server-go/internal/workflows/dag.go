package workflows

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/wfparser"
)

// The DAG viewer's routes of routes.ts: GET /api/workflows (the account's
// list), GET /api/workflows/{name} and GET /api/kb/dag (one workflow
// shaped for the viewer, from the platform or from a YAML in the library).

// platformAPI is routes.ts's platformApi: a platform API read with the
// viewer's own platform key, else the deployment's; nil when there is
// none or the answer is not a JSON success.
func (r *Routes) platformAPI(ctx context.Context, req *http.Request, path string) any {
	r.p.EnsureFreshAccess(8 * time.Second)
	v, err := r.p.ViewerOf(req)
	if err != nil {
		return nil
	}
	key, _ := r.p.KeyFor(v)
	return r.p.APIJSON(ctx, path, key, 20*time.Second)
}

// credentialRemedy names the caller's situation when neither the API nor
// the CLI could answer.
func (r *Routes) credentialRemedy(req *http.Request, cliError string) string {
	yours := "you have not added your own under Settings, Model access"
	if v, err := r.p.ViewerOf(req); err == nil && v.Cred != nil {
		if v.Cred.BaseURL != "" {
			yours = "your stored credential is a provider key, which covers chat only and is never sent to the platform"
		} else {
			yours = "your stored credential was rejected by the platform API; an AI gateway API key works for chat but does not grant platform access, so use a platform token or platform API key"
		}
	}
	return "no platform credential available (this deployment has none, and " + yours + "), and the pw CLI on this host is not usable (" +
		sliceJS(cliError, 120) + "). Add a platform token or API key in Settings, authenticate the CLI here, or set PW_API_KEY."
}

// plainCLI is routes.ts's execFile(PW_CLI, args, {timeout: 20 s}): the
// deployment's environment, the error `stderr || message`.
func (r *Routes) plainCLI(ctx context.Context, args []string) (string, error) {
	out, err := r.p.Exec(ctx, args, platform.ExecOptions{Timeout: 20 * time.Second})
	if err != nil {
		return "", plainError(platform.ErrText(err))
	}
	return out, nil
}

var lsRow = regexp.MustCompile(`^(\S+)\s+(\S+)$`)

func (r *Routes) workflowList(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	if via := r.platformAPI(ctx, req, "/api/workflows"); truthy(via) {
		rows := []any{}
		for _, wf := range listOf(via, "workflows") {
			name := jsStr(coalesce(at(wf, "name"), ""))
			if name != "" {
				rows = append(rows, jsonv.NewObject("name", name, "type", jsStr(coalesce(at(wf, "type"), ""))))
			}
		}
		return jsonv.NewObject("workflows", rows), nil
	}
	out, err := r.plainCLI(ctx, []string{"workflows", "ls"})
	if err != nil {
		return nil, platform.KbError(502, "Cannot list workflows: "+r.credentialRemedy(req, err.Error()))
	}
	rows := []any{}
	lines := strings.Split(jsTrim(out), "\n")
	for _, l := range lines[1:] {
		if m := lsRow.FindStringSubmatch(jsTrim(l)); m != nil {
			rows = append(rows, jsonv.NewObject("name", m[1], "type", m[2]))
		}
	}
	return jsonv.NewObject("workflows", rows), nil
}

var notNameChar = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func (r *Routes) workflowItem(w http.ResponseWriter, req *http.Request) (any, error) {
	ctx := req.Context()
	name := notNameChar.ReplaceAllString(req.PathValue("name"), "")
	wf := r.platformAPI(ctx, req, "/api/workflows/"+platform.EncodeURIComponent(name))
	if wf == nil {
		out, err := r.plainCLI(ctx, []string{"workflows", "get", name, "-o", "json"})
		if err != nil {
			return nil, platform.KbError(502, "Cannot read workflow "+name+": "+r.credentialRemedy(req, err.Error()))
		}
		if wf, err = jsonv.DecodeString(out); err != nil {
			return nil, err
		}
	}
	if wf == nil {
		return nil, plainError("Cannot read properties of null (reading 'name')")
	}
	return DagPayload(at(wf, "name"), at(wf, "displayName"), at(wf, "yaml"))
}

// DagPayload is dagPayload: one workflow document shaped for the DAG
// viewer, with its jobs as nodes and edges, its form and its YAML text.
// name and displayName are left out of the answer when undefined.
func DagPayload(name, displayName, doc any) (*jsonv.Object, error) {
	jobsV := coalesce(at(doc, "jobs"), jsonv.NewObject())
	jobs := wfparser.Spread(jobsV)
	nodes, edges := []any{}, []any{}
	for _, id := range jobs.Keys() {
		def, _ := jobs.Get(id)
		steps := coalesce(at(def, "steps"), []any{})
		list, ok := steps.([]any)
		if !ok {
			return nil, plainError("(def?.steps ?? []).map is not a function")
		}
		labels := make([]any, len(list))
		for i, s := range list {
			l, err := wfparser.StepLabel(s, i)
			if err != nil {
				return nil, err
			}
			labels[i] = l
		}
		deps, err := wfparser.AllDeps(id, jobs)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, jsonv.NewObject("id", id, "steps", labels, "dependsOn", deps))
	}
	for _, job := range jobs.Keys() {
		def, _ := jobs.Get(job)
		switch needs := coalesce(at(def, "needs"), []any{}).(type) {
		case []any:
			for _, dep := range needs {
				edges = append(edges, jsonv.NewObject("from", dep, "to", job))
			}
		case string:
			for _, c := range needs { // for-of over a string reads its characters
				edges = append(edges, jsonv.NewObject("from", string(c), "to", job))
			}
		default:
			return nil, plainError("def?.needs ?? [] is not iterable")
		}
	}
	var form any
	raw := at(doc, "on", "execute", "inputs")
	if truthy(raw) && wfparser.HasUserInputs(doc) {
		form = wfparser.ConvertToDynamicForm(raw)
	}
	text := doc
	if nullish(text) {
		text = jsonv.NewObject()
	}
	y := doc
	if undef(y) {
		y = jsonv.Undefined
	}
	return jsonv.NewObject("name", name, "displayName", displayName, "nodes", nodes, "edges", edges, "yaml", y, "form", form,
		"yamlText", wfparser.DumpYAML(text)), nil
}

// readError is the message Node's fs.readFile gives for the common cases.
func readError(abs string, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "ENOENT: no such file or directory, open '" + abs + "'"
	case errors.Is(err, syscall.EISDIR):
		return "EISDIR: illegal operation on a directory, read"
	case errors.Is(err, fs.ErrPermission):
		return "EACCES: permission denied, open '" + abs + "'"
	}
	return err.Error()
}

// kbDag is GET /api/kb/dag: a workflow YAML in the library drawn as a DAG
// without being registered, with the schema's verdict.
func (r *Routes) kbDag(w http.ResponseWriter, req *http.Request) (any, error) {
	rel := platform.Q(req, "path")
	base := r.p.KBRoot()
	abs := filepath.Join(base, leadSlashes.ReplaceAllString(rel, ""))
	if !filepath.IsAbs(abs) {
		abs, _ = filepath.Abs(abs)
	}
	if abs != base && !strings.HasPrefix(abs, base+string(filepath.Separator)) {
		return nil, platform.KbError(400, "path escapes the knowledge base root")
	}
	b, err := os.ReadFile(abs)
	if err == nil {
		// fs.readFile on a directory fails (EISDIR); os.ReadFile does too.
		var doc any
		if doc, err = jsonv.ParseYAML(string(b)); err == nil {
			return r.dagOf(req.Context(), rel, doc)
		}
		return nil, platform.KbError(422, "Not parseable as workflow YAML: "+sliceJS(err.Error(), 160))
	}
	return nil, platform.KbError(422, "Not parseable as workflow YAML: "+sliceJS(readError(abs, err), 160))
}

func (r *Routes) dagOf(ctx context.Context, rel string, doc any) (any, error) {
	if jsonv.Obj(doc) == nil && jsonv.Arr(doc) == nil || !truthy(at(doc, "jobs")) {
		return nil, platform.KbError(422, "This YAML has no jobs section, so there is no DAG to draw.")
	}
	switch at(doc, "jobs").(type) {
	case *jsonv.Object, []any:
	default:
		return nil, platform.KbError(422, "This YAML has no jobs section, so there is no DAG to draw.")
	}
	verdict := r.Schema.Validate(ctx, doc)
	name := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		name = rel[i+1:]
	}
	out, err := DagPayload(name, jsonv.Undefined, doc)
	if err != nil {
		return nil, err
	}
	errs := []any{}
	if verdict.OK != nil && !*verdict.OK {
		errs = strs(verdict.Errors)
	}
	out.Set("schemaErrors", errs)
	return out, nil
}
