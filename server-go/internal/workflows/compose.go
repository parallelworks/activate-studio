package workflows

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/workflows/wfparser"
)

// Composing several workflows into one that runs each as a subworkflow,
// the way the platform's `uses:` step does (workflowCompose.ts). Inputs
// are merged by name; a conflicting definition is reported, and the first
// is kept.

// ComposeStep is one workflow of the chain.
type ComposeStep struct {
	// Uses is the workflow to run, e.g. cloudinit or marketplace/cloudinit/v1.0.1.
	Uses string
	// ID is the job id; derived from Uses when empty.
	ID string
	// Name is the step's label.
	Name string
	// Needs are the job ids it waits for; nil for a straight chain.
	Needs []string
}

// ComposeResult is composeWorkflows' answer.
type ComposeResult struct {
	YAML      *jsonv.Object
	YAMLText  string
	Inputs    []string
	Conflicts []string
	Missing   []string
}

// JSON is {yaml, yamlText, inputs, conflicts, missing}.
func (c *ComposeResult) JSON() *jsonv.Object {
	return jsonv.NewObject("yaml", c.YAML, "yamlText", c.YAMLText, "inputs", strs(c.Inputs), "conflicts", strs(c.Conflicts), "missing", strs(c.Missing))
}

func strs(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

var (
	versionSeg = regexp.MustCompile(`^v?\d`)
	notJobChar = regexp.MustCompile(`[^a-z0-9_]+`)
	edgeUnders = regexp.MustCompile(`^_+|_+$`)
	notIDChar  = regexp.MustCompile(`[^a-z0-9_-]`)
)

func jobID(uses string, taken map[string]bool) string {
	base := "job"
	for _, p := range strings.Split(uses, "/") {
		if p != "" && !versionSeg.MatchString(p) {
			base = p
		}
	}
	base = edgeUnders.ReplaceAllString(notJobChar.ReplaceAllString(strings.ToLower(base), "_"), "")
	if base == "" {
		base = "job"
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		if c := base + "_" + strconv.Itoa(i); !taken[c] {
			return c
		}
	}
}

// sourceName is the workflow's own name, without a marketplace prefix or version.
func sourceName(uses string) string {
	var parts []string
	for _, p := range strings.Split(uses, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) > 0 && (parts[0] == "marketplace" || parts[0] == "workflow") {
		parts = parts[1:]
	}
	for _, p := range parts {
		if !versionSeg.MatchString(p) {
			return p
		}
	}
	return uses
}

// usesRef is the reference the platform schema accepts: a bare name is
// one of your own workflows, workflow/<name>.
func usesRef(uses string) string {
	switch strings.Split(uses, "/")[0] {
	case "marketplace", "workflow", "github", "parallelworks":
		return uses
	}
	return "workflow/" + uses
}

// Compose is composeWorkflows; chain false leaves steps without needs
// unchained.
func Compose(ctx context.Context, p *platform.Platform, steps []ComposeStep, chain bool) (*ComposeResult, error) {
	if len(steps) == 0 {
		return nil, errors.New("at least one workflow is required")
	}
	taken := map[string]bool{}
	ids := make([]string, len(steps))
	for i, s := range steps {
		id := s.ID
		if id == "" {
			id = jobID(s.Uses, taken)
		}
		id = notIDChar.ReplaceAllString(strings.ToLower(id), "_")
		taken[id] = true
		ids[i] = id
	}
	inputs := jsonv.NewObject()
	var conflicts, missing []string
	per := map[string][]string{}
	record := func(key string, def any, from string) {
		if existing, ok := inputs.Get(key); ok {
			if string(jsonv.MustMarshal(existing)) != string(jsonv.MustMarshal(def)) {
				conflicts = append(conflicts, key+": "+from+" declares it differently; keeping the first definition")
			}
			return
		}
		inputs.Set(key, wfparser.Spread(def))
	}
	for i, s := range steps {
		name := sourceName(s.Uses)
		var doc any
		if r := p.WorkflowRecord(ctx, name, p.GatewayKey()); r != nil {
			doc = r
		} else if out, err := p.Exec(ctx, []string{"workflows", "get", name, "-o", "json"}, platform.ExecOptions{Timeout: 30 * time.Second, MaxBuffer: 16 << 20}); err != nil {
			missing = append(missing, name)
		} else if v, err := jsonv.DecodeString(out); err != nil {
			missing = append(missing, name)
		} else {
			doc = v
		}
		declared := jsonv.Obj(jsonv.Path(doc, "yaml", "on", "execute", "inputs"))
		var names []string
		if declared != nil {
			for _, key := range declared.Keys() {
				if strings.HasPrefix(key, "$") {
					continue
				}
				def, _ := declared.Get(key)
				if d := jsonv.Obj(def); d != nil && jsonv.Path(d, "type") == "group" {
					if items := jsonv.Obj(jsonv.Path(d, "items")); items != nil {
						for _, k := range items.Keys() {
							if strings.HasPrefix(k, "$") {
								continue
							}
							names = append(names, k)
							v, _ := items.Get(k)
							record(k, v, name)
						}
					}
					continue
				}
				names = append(names, key)
				record(key, def, name)
			}
		}
		per[ids[i]] = names
	}
	jobs := jsonv.NewObject()
	for i, s := range steps {
		with := jsonv.NewObject()
		for _, k := range per[ids[i]] {
			with.Set(k, "${{ inputs."+k+" }}")
		}
		needs := s.Needs
		if needs == nil && chain && i > 0 {
			needs = []string{ids[i-1]}
		}
		job := jsonv.NewObject()
		if len(needs) > 0 {
			job.Set("needs", strs(needs))
		}
		label := s.Name
		if label == "" {
			label = "Run " + sourceName(s.Uses)
		}
		step := jsonv.NewObject("name", label, "uses", usesRef(s.Uses))
		if with.Len() > 0 {
			step.Set("with", with)
		}
		job.Set("steps", []any{step})
		jobs.Set(ids[i], job)
	}
	yaml := jsonv.NewObject("permissions", []any{"*"}, "on", jsonv.NewObject("execute", jsonv.NewObject("inputs", inputs)), "jobs", jobs)
	return &ComposeResult{YAML: yaml, YAMLText: wfparser.DumpYAML(yaml), Inputs: inputs.Keys(), Conflicts: nonNil(conflicts), Missing: nonNil(missing)}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
