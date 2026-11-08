package workflows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// Validation of workflow YAML against the platform's own schema, published
// at <platform>/workflow.schema.json (workflowSchema.ts): fetched from the
// deployment's platform host, cached beside the index so a restart with the
// platform unreachable still validates, and refreshed daily.
//
// Node validates with ajv (draft 2020-12, allErrors); this is
// santhosh-tekuri/jsonschema, a pure-Go 2020-12 validator. The messages
// are ajv's wording for each keyword, as `${instancePath}: ${message}`,
// with additionalProperties' ("name") suffix. What differs: the order of
// the errors (each validator walks the schema its own way), the composite
// errors (ajv also reports `must match "then" schema` for a failed if/then,
// which this validator reports only through the then branch's own errors),
// and formats, which neither asserts.

const schemaRefresh = 24 * time.Hour

// SchemaVerdict is validateWorkflowDoc's answer. OK is nil when no schema
// could be had, which is not a pass.
type SchemaVerdict struct {
	OK     *bool
	Errors []string
	Source string
}

// JSON is {ok, errors, source}.
func (v SchemaVerdict) JSON() *jsonv.Object {
	var ok, src any
	if v.OK != nil {
		ok = *v.OK
	}
	if v.Source != "" {
		src = v.Source
	}
	errs := make([]any, len(v.Errors))
	for i, e := range v.Errors {
		errs[i] = e
	}
	return jsonv.NewObject("ok", ok, "errors", errs, "source", src)
}

// Schema is the validator of one platform's workflow schema.
type Schema struct {
	p         *platform.Platform
	mu        sync.Mutex
	compiled  *jsonschema.Schema
	fetchedAt time.Time
}

// NewSchema validates against p's platform.
func NewSchema(p *platform.Platform) *Schema { return &Schema{p: p} }

func (s *Schema) url() string {
	if o := s.p.Origin(); o != "" {
		return o + "/workflow.schema.json"
	}
	return ""
}

func (s *Schema) cacheFile() string { return filepath.Join(s.p.IndexBase(), "workflow.schema.json") }

func (s *Schema) load(ctx context.Context) []byte {
	if u := s.url(); u != "" {
		if res, err := platform.Get(ctx, u, nil, 8*time.Second); err == nil && res.OK && res.Body != nil {
			b := jsonv.MustMarshal(res.Body)
			os.WriteFile(s.cacheFile(), b, 0o644) // best-effort
			return b
		}
	}
	b, err := os.ReadFile(s.cacheFile())
	if err != nil {
		return nil
	}
	if _, err := jsonv.Decode(b); err != nil {
		return nil
	}
	return b
}

func (s *Schema) validator(ctx context.Context) *jsonschema.Schema {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.compiled != nil && time.Since(s.fetchedAt) < schemaRefresh {
		return s.compiled
	}
	b := s.load(ctx)
	if b == nil {
		return s.compiled
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return s.compiled
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	const loc = "workflow.schema.json"
	if c.AddResource(loc, doc) != nil {
		return s.compiled
	}
	sch, err := c.Compile(loc)
	if err != nil {
		return s.compiled // an uncompilable schema leaves the previous one in place
	}
	s.compiled, s.fetchedAt = sch, time.Now()
	return s.compiled
}

// Reset forgets the compiled schema (tests).
func (s *Schema) Reset() { s.mu.Lock(); s.compiled = nil; s.mu.Unlock() }

// Validate is validateWorkflowDoc: the path into the document, then the
// problem, for at most 20 errors.
func (s *Schema) Validate(ctx context.Context, doc any) SchemaVerdict {
	v := s.validator(ctx)
	src := s.url()
	if v == nil {
		return SchemaVerdict{Errors: []string{"The platform workflow schema could not be fetched or found in the cache; the document was not validated."}, Source: src}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonv.MustMarshal(doc)))
	if err != nil {
		inst = nil
	}
	yes, no := true, false
	err = v.Validate(inst)
	if err == nil {
		return SchemaVerdict{OK: &yes, Errors: []string{}, Source: src}
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return SchemaVerdict{OK: &no, Errors: []string{"(document root): " + err.Error()}, Source: src}
	}
	var out []string
	flatten(ve, &out)
	if len(out) == 0 {
		out = []string{"(document root): invalid"}
	}
	if len(out) > 20 {
		out = out[:20]
	}
	return SchemaVerdict{OK: &no, Errors: out, Source: src}
}

func pointer(loc []string) string {
	if len(loc) == 0 {
		return "(document root)"
	}
	var b strings.Builder
	for _, t := range loc {
		b.WriteString("/" + strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1"))
	}
	return b.String()
}

// flatten lists the errors as ajv does with allErrors: the failing
// keywords below a composite before the composite's own.
func flatten(e *jsonschema.ValidationError, out *[]string) {
	for _, c := range e.Causes {
		flatten(c, out)
	}
	where := pointer(e.InstanceLocation)
	switch k := e.ErrorKind.(type) {
	case *kind.Required:
		for _, m := range k.Missing {
			*out = append(*out, where+": must have required property '"+m+"'")
		}
	case *kind.AdditionalProperties:
		for _, p := range k.Properties {
			*out = append(*out, where+`: must NOT have additional properties ("`+p+`")`)
		}
	case *kind.DependentRequired:
		for _, m := range k.Missing {
			*out = append(*out, where+": must have property "+m+" when property "+k.Prop+" is present")
		}
	case *kind.Dependency:
		for _, m := range k.Missing {
			*out = append(*out, where+": must have property "+m+" when property "+k.Prop+" is present")
		}
	default:
		if msg := ajvMessage(e.ErrorKind); msg != "" {
			*out = append(*out, where+": "+msg)
		}
	}
}

func rat(r *big.Rat) string {
	if r == nil {
		return ""
	}
	f, _ := r.Float64()
	return jsonv.FormatNumber(f)
}

// ajvMessage is ajv's message for a keyword's failure, or "" for the
// errors that only group others.
func ajvMessage(k jsonschema.ErrorKind) string {
	switch k := k.(type) {
	case *kind.Type:
		return "must be " + strings.Join(k.Want, ",")
	case *kind.Enum:
		return "must be equal to one of the allowed values"
	case *kind.Const:
		return "must be equal to constant"
	case *kind.Pattern:
		return `must match pattern "` + k.Want + `"`
	case *kind.Format:
		return `must match format "` + k.Want + `"`
	case *kind.MinLength:
		return "must NOT have fewer than " + strconv.Itoa(k.Want) + " characters"
	case *kind.MaxLength:
		return "must NOT have more than " + strconv.Itoa(k.Want) + " characters"
	case *kind.MinItems:
		return "must NOT have fewer than " + strconv.Itoa(k.Want) + " items"
	case *kind.MaxItems:
		return "must NOT have more than " + strconv.Itoa(k.Want) + " items"
	case *kind.MinProperties:
		return "must NOT have fewer than " + strconv.Itoa(k.Want) + " properties"
	case *kind.MaxProperties:
		return "must NOT have more than " + strconv.Itoa(k.Want) + " properties"
	case *kind.Minimum:
		return "must be >= " + rat(k.Want)
	case *kind.Maximum:
		return "must be <= " + rat(k.Want)
	case *kind.ExclusiveMinimum:
		return "must be > " + rat(k.Want)
	case *kind.ExclusiveMaximum:
		return "must be < " + rat(k.Want)
	case *kind.MultipleOf:
		return "must be multiple of " + rat(k.Want)
	case *kind.OneOf:
		return "must match exactly one schema in oneOf"
	case *kind.AnyOf:
		return "must match a schema in anyOf"
	case *kind.Not:
		return "must NOT be valid"
	case *kind.FalseSchema:
		return "boolean schema is false"
	case *kind.UniqueItems:
		return fmt.Sprintf("must NOT have duplicate items (items ## %d and %d are identical)", k.Duplicates[1], k.Duplicates[0])
	case *kind.PropertyNames:
		return "property name must be valid"
	case *kind.Contains, *kind.MinContains:
		return "must contain at least 1 valid item(s)"
	case *kind.MaxContains:
		return "must contain at most " + strconv.Itoa(k.Want) + " valid item(s)"
	case *kind.AdditionalItems:
		return "must NOT have more than " + strconv.Itoa(k.Count) + " items"
	}
	return ""
}
