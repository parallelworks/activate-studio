package wfparser

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

/* ---- JavaScript value helpers ---- */

func obj(v any) *jsonv.Object { return jsonv.Obj(v) }

func get(o *jsonv.Object, k string) any {
	if o == nil {
		return jsonv.Undefined
	}
	v, ok := o.Get(k)
	if !ok {
		return jsonv.Undefined
	}
	return v
}

func undefined(v any) bool { return v == jsonv.Undefined }

func truthy(v any) bool { return !undefined(v) && jsonv.Truthy(v) }

func asObject(v any) *jsonv.Object {
	if o := obj(v); o != nil {
		return o
	}
	return jsonv.NewObject()
}

// spread is {...v}: an object's own keys, an array's or a string's indexes.
func spread(v any) *jsonv.Object {
	switch x := v.(type) {
	case *jsonv.Object:
		return x.Clone()
	case []any:
		o := jsonv.NewObject()
		for i, it := range x {
			o.Set(strconv.Itoa(i), it)
		}
		return o
	case string:
		o := jsonv.NewObject()
		for i, u := range utf16.Encode([]rune(x)) {
			o.Set(strconv.Itoa(i), string(utf16.Decode([]uint16{u})))
		}
		return o
	}
	return jsonv.NewObject()
}

// DeepClone is structuredClone of a decoded value.
func DeepClone(v any) any {
	switch x := v.(type) {
	case *jsonv.Object:
		o := jsonv.NewObject()
		for _, k := range x.Keys() {
			e, _ := x.Get(k)
			o.Set(k, DeepClone(e))
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = DeepClone(e)
		}
		return out
	}
	return v
}

func without(o *jsonv.Object, keys ...string) *jsonv.Object {
	c := o.Clone()
	for _, k := range keys {
		c.Delete(k)
	}
	return c
}

/* ---- convertToDynamicForm ---- */

func convertValue(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = convertValue(e)
		}
		return out
	case *jsonv.Object:
		return ConvertToDynamicForm(x)
	}
	return v
}

func iterObj(o *jsonv.Object) *jsonv.Object {
	ret := jsonv.NewObject()
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		if k == "$meta" {
			ret.Set(k, DeepClone(v))
		} else {
			ret.Set(k, convertValue(v))
		}
	}
	return ret
}

var renamed = map[string]string{
	"instance-type":       "instances",
	"color-picker":        "color",
	"header":              "label",
	"slurm-accounts":      "dynamicAccountDropdown",
	"slurm-qos":           "dynamicQOSDropdown",
	"slurm-partitions":    "dynamicPartitionDropdown",
	"pbs-queues":          "dynamicPbsQueuesDropdown",
	"organization-groups": "groups",
}

// ConvertToDynamicForm is convertToDynamicForm: a workflow's input schema
// as the run form takes it.
func ConvertToDynamicForm(v any) any {
	o := obj(v)
	if o == nil {
		return convertValue(v)
	}
	typ := get(o, "type")
	if !truthy(typ) {
		if truthy(get(o, "size")) && truthy(get(o, "text")) {
			ret := iterObj(without(o, "text"))
			ret.Set("type", "label")
			ret.Set("label", get(o, "text"))
			return ret
		}
		return iterObj(o)
	}
	t, _ := typ.(string)
	switch {
	case t == "group":
		items := get(o, "items")
		ret := iterObj(without(o, "items"))
		if !undefined(items) || undefined(get(ret, "options")) {
			ret.Set("options", convertValue(items))
		}
		if truthy(get(o, "flatten")) {
			ret.Set("type", "group")
		} else {
			ret.Set("type", "object")
		}
		return ret
	case t == "list":
		template := get(o, "template")
		ic := get(o, "items-collapsible")
		ret := iterObj(without(o, "template", "items-collapsible"))
		if truthy(ic) {
			ret.Set("itemsCollapsible", ic)
		}
		if !truthy(template) && truthy(get(o, "options")) {
			ret.Set("options", convertValue(get(o, "options")))
			return ret
		}
		ret.Set("options", iterObj(asObject(template)))
		return ret
	case t == "number":
		if get(o, "slider") == true {
			ret := iterObj(without(o, "slider"))
			ret.Set("type", "range")
			return ret
		}
		return iterObj(o)
	case t == "dropdown":
		optionKey := get(o, "option-key")
		options := get(o, "options")
		if arr, ok := options.([]any); ok {
			out := make([]any, len(arr))
			for i, e := range arr {
				eo := obj(e)
				if _, isStr := get(eo, "value").(string); eo != nil && undefined(get(eo, "label")) && isStr {
					c := eo.Clone()
					c.Set("label", get(eo, "value"))
					out[i] = c
				} else {
					out[i] = e
				}
			}
			options = out
		}
		other := without(o, "option-key", "options")
		other.Set("options", options)
		ret := iterObj(other)
		if truthy(optionKey) {
			ret.Set("depends_on", optionKey)
		}
		return ret
	case t == "string" && truthy(get(o, "textarea")):
		ret := iterObj(o)
		ret.Set("type", "textarea")
		return ret
	case t == "storage":
		ret := iterObj(o)
		sub := get(o, "subtype")
		if undefined(sub) || sub == nil {
			sub = get(o, "subType")
		}
		ret.Set("subType", sub)
		return ret
	}
	if to, ok := renamed[t]; ok {
		ret := iterObj(o)
		ret.Set("type", to)
		return ret
	}
	return iterObj(o)
}

/* ---- the inputs schema ---- */

// InputsSchema is workflowInputsSchema: on.execute.inputs, when an object.
func InputsSchema(yml any) *jsonv.Object {
	return obj(get(obj(get(obj(get(obj(yml), "on")), "execute")), "inputs"))
}

// HasUserInputs is workflowHasUserInputs.
func HasUserInputs(yml any) bool {
	in := InputsSchema(yml)
	return in != nil && in.Len() > 0
}

func wizardFlattens(schema *jsonv.Object) bool {
	wizard := obj(get(obj(get(schema, "$meta")), "wizard"))
	return !(wizard != nil && get(wizard, "flatten") == false)
}

func containsExpr(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, "${{")
	case []any:
		for _, e := range x {
			if containsExpr(e) {
				return true
			}
		}
	case *jsonv.Object:
		for _, k := range x.Keys() {
			e, _ := x.Get(k)
			if containsExpr(e) {
				return true
			}
		}
	}
	return false
}

func nestedFieldNames(items *jsonv.Object) []string {
	var out []string
	for _, k := range items.Keys() {
		it := obj(get(items, k))
		if k == "$meta" || it == nil {
			continue
		}
		out = append(out, k)
		if get(it, "type") == "group" && truthy(get(it, "flatten")) && obj(get(it, "items")) != nil {
			out = append(out, nestedFieldNames(obj(get(it, "items")))...)
		}
	}
	return out
}

func listTemplateOf(f *jsonv.Object) *jsonv.Object {
	for _, k := range []string{"template", "items", "options"} {
		if o := obj(get(f, k)); o != nil {
			return o
		}
	}
	return nil
}

// filterIgnored is filterIgnoredFieldsInternal with the expression parser
// uninitialized, as in the server, where parseStringsOfObj is the identity.
func filterIgnored(schema *jsonv.Object, values any) *jsonv.Object {
	result := spread(values)
	for _, name := range schema.Keys() {
		fs := obj(get(schema, name))
		if name == "$meta" || fs == nil {
			continue
		}
		ignore := get(fs, "ignore")
		items := obj(get(fs, "items"))
		options := obj(get(fs, "options"))
		typ := get(fs, "type")
		switch {
		case ignore == true || ignore == "true":
			if !containsExpr(get(result, name)) {
				result.Delete(name)
			}
			if typ == "group" && items != nil {
				for _, n := range nestedFieldNames(items) {
					if !containsExpr(get(result, n)) {
						result.Delete(n)
					}
				}
			}
		case typ == "object" && listTemplateOf(fs) != nil:
			if sec := obj(get(result, name)); sec != nil {
				result.Set(name, filterIgnored(listTemplateOf(fs), sec))
			}
		case typ == "list" && listTemplateOf(fs) != nil:
			lt := listTemplateOf(fs)
			switch x := get(result, name).(type) {
			case []any:
				out := make([]any, len(x))
				for i, it := range x {
					if obj(it) != nil {
						out[i] = filterIgnored(lt, it)
					} else {
						out[i] = it
					}
				}
				result.Set(name, out)
			case *jsonv.Object:
				result.Set(name, filterIgnored(lt, x))
			}
		case typ == "step" && options != nil:
			if wizardFlattens(schema) {
				nested := filterIgnored(options, result)
				for _, k := range options.Keys() {
					if k == "$meta" {
						continue
					}
					if v, ok := nested.Get(k); ok {
						result.Set(k, v)
					} else {
						result.Delete(k)
					}
				}
				result.Delete(name)
			} else {
				result.Set(name, filterIgnored(options, asObject(get(result, name))))
			}
		case typ == "group" && items != nil:
			if truthy(get(fs, "flatten")) {
				nested := filterIgnored(items, result)
				for _, k := range items.Keys() {
					if k == "$meta" {
						continue
					}
					if v, ok := nested.Get(k); ok {
						result.Set(k, v)
					} else {
						result.Delete(k)
					}
				}
			} else if g := obj(get(result, name)); g != nil {
				result.Set(name, filterIgnored(items, g))
			}
		}
	}
	return result
}

type leafFunc func(result *jsonv.Object, key string, field *jsonv.Object)

func walkSchemaFields(schema *jsonv.Object, values any, visit leafFunc) any {
	if schema == nil || !truthy(values) {
		return values
	}
	result := spread(values)
	for _, key := range schema.Keys() {
		fs := obj(get(schema, key))
		if key == "$meta" || fs == nil {
			continue
		}
		nested := get(result, key)
		items := obj(get(fs, "items"))
		options := obj(get(fs, "options"))
		typ := get(fs, "type")
		switch {
		case typ == "group" && items != nil:
			if truthy(get(fs, "flatten")) {
				assign(result, walkSchemaFields(items, result, visit))
			} else if obj(nested) != nil {
				result.Set(key, walkSchemaFields(items, nested, visit))
			}
		case typ == "step" && options != nil:
			if wizardFlattens(schema) {
				assign(result, walkSchemaFields(options, result, visit))
			} else if obj(nested) != nil {
				result.Set(key, walkSchemaFields(options, nested, visit))
			}
		case typ == "list":
			template := get(fs, "template")
			if !truthy(template) {
				template = get(fs, "options")
			}
			arr, isArr := nested.([]any)
			if t := obj(template); t != nil && isArr {
				out := make([]any, len(arr))
				for i, it := range arr {
					if obj(it) != nil {
						out[i] = walkSchemaFields(t, it, visit)
					} else {
						out[i] = it
					}
				}
				result.Set(key, out)
			}
		default:
			visit(result, key, fs)
		}
	}
	return result
}

func assign(dst *jsonv.Object, src any) {
	o := obj(src)
	if o == nil {
		return
	}
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		dst.Set(k, v)
	}
}

func computeURI(v any, user string) any {
	o := obj(v)
	if o == nil {
		return jsonv.Undefined
	}
	name := get(o, "name")
	switch {
	case get(o, "id") == "user-workspace":
		return "pw://" + user + "/user-workspace"
	case get(o, "type") == "managed-cluster" && truthy(name):
		return "pw://" + jsonv.String(name)
	case truthy(name):
		u := get(o, "user")
		us := user
		if truthy(u) {
			us = jsonv.String(u)
		}
		return "pw://" + us + "/" + jsonv.String(name)
	}
	return jsonv.Undefined
}

func bucketURI(v any) any {
	o := obj(v)
	if o != nil && truthy(get(o, "name")) && truthy(get(o, "user")) {
		return "pw://" + jsonv.String(get(o, "user")) + "/" + jsonv.String(get(o, "name"))
	}
	return v
}

func mapOrOne(v any, f func(any) any) any {
	if arr, ok := v.([]any); ok {
		out := make([]any, len(arr))
		for i, e := range arr {
			out[i] = f(e)
			if undefined(out[i]) {
				out[i] = nil // an array hole is null in JSON
			}
		}
		return out
	}
	return f(v)
}

// PrepareSubmittableValues is prepareSubmittableValues(yaml, values, user):
// fields marked ignore dropped, and compute and bucket choices written as
// pw:// addresses. user is the template literal's text of the username
// ("undefined" when there is none).
func PrepareSubmittableValues(workflowYAML, values any, user string) any {
	schema := InputsSchema(workflowYAML)
	if schema == nil {
		return values
	}
	var filtered any = values
	if truthy(values) {
		filtered = filterIgnored(schema, values)
	}
	return walkSchemaFields(schema, filtered, func(result *jsonv.Object, key string, fs *jsonv.Object) {
		v := get(result, key)
		switch v.(type) {
		case *jsonv.Object, []any:
		default:
			return
		}
		switch get(fs, "type") {
		case "compute-clusters":
			result.Set(key, mapOrOne(v, func(e any) any { return computeURI(e, user) }))
		case "bucket":
			result.Set(key, mapOrOne(v, bucketURI))
		}
	})
}

/* ---- jobs and steps ---- */

// StepLabel is getStepLabel(step, index).
func StepLabel(step any, index int) (any, error) {
	s := obj(step)
	if s == nil {
		if step == nil || undefined(step) {
			return nil, errors.New("Cannot read properties of " + jsonv.String(step) + " (reading 'name')")
		}
		return "Step " + strconv.Itoa(index), nil
	}
	if n := get(s, "name"); truthy(n) {
		return n, nil
	}
	if r := get(s, "run"); truthy(r) {
		rs, ok := r.(string)
		if !ok {
			return nil, errors.New("step.run.split is not a function")
		}
		for _, l := range strings.Split(rs, "\n") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			u := utf16.Encode([]rune(l))
			if len(u) > 64 {
				return string(utf16.Decode(u[:64])) + "...", nil
			}
			return l, nil
		}
	}
	if u := get(s, "uses"); truthy(u) {
		return u, nil
	}
	if truthy(get(s, "subworkflow")) {
		return "subworkflow", nil
	}
	return "Step " + strconv.Itoa(index), nil
}

// ErrCycle is getAllDeps on jobs whose needs loop, where Node's recursion
// overflows the stack.
var ErrCycle = errors.New("Maximum call stack size exceeded")

// AllDeps is getAllDeps(job, jobs): every job job needs, directly or
// through others, in the order the Set collects them.
func AllDeps(job string, jobs *jsonv.Object) ([]any, error) {
	return allDeps(job, jobs, map[string][]any{}, map[string]bool{})
}

func setKey(v any) string { return fmt.Sprintf("%T:%s", v, jsonv.String(v)) }

func allDeps(job string, jobs *jsonv.Object, memo map[string][]any, on map[string]bool) ([]any, error) {
	if d, ok := memo[job]; ok {
		return d, nil
	}
	if on[job] {
		return nil, ErrCycle
	}
	on[job] = true
	defer delete(on, job)
	needs, _ := get(obj(get(jobs, job)), "needs").([]any)
	out := []any{}
	seen := map[string]bool{}
	add := func(v any) {
		if k := setKey(v); !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	for _, n := range needs {
		add(n)
	}
	for _, n := range needs {
		d, err := allDeps(jsonv.String(n), jobs, memo, on)
		if err != nil {
			return nil, err
		}
		for _, a := range d {
			add(a)
		}
	}
	memo[job] = out
	return out, nil
}

// Spread is {...v}: an object's own keys, an array's or a string's indexes.
func Spread(v any) *jsonv.Object { return spread(v) }
