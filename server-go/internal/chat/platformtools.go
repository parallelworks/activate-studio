package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// platformTools.ts: the platform operations the chat calls over the
// platform API with the viewer's own key. A tool over a POST changes
// something and runs only from an approved hold (approvals.go).

// PlatformTool is one of PLATFORM_TOOLS.
type PlatformTool struct {
	Name       string          `json:"name"`
	Operation  string          `json:"operation"`
	Method     string          `json:"method"`
	Path       string          `json:"path"`
	Parameters json.RawMessage `json:"parameters"`
	Fields     []string        `json:"fields"`
	FromViewer []string        `json:"fromViewer"`
	Defaults   []string        `json:"defaults"`
	Instead    []string        `json:"instead"`

	props *jsonobj.Object
}

var (
	platformByName = map[string]*PlatformTool{}
	// AsksFirstTools are the tools whose calls wait for the user's approval.
	AsksFirstTools []string
)

func setPlatformTools(list []PlatformTool) {
	for i := range list {
		t := &list[i]
		t.Parameters = compact(t.Parameters)
		var p struct {
			Properties json.RawMessage `json:"properties"`
		}
		_ = json.Unmarshal(t.Parameters, &p)
		if o, err := jsonobj.Parse(p.Properties); err == nil {
			t.props = o
		} else {
			t.props = jsonobj.New()
		}
		platformByName[t.Name] = t
		if t.Method == "POST" {
			AsksFirstTools = append(AsksFirstTools, t.Name)
		}
	}
}

func isPlatformTool(name string) bool { _, ok := platformByName[name]; return ok }

// asksFirst is whether the tool's calls wait for the user's approval.
func asksFirst(name string) bool {
	t := platformByName[name]
	return (t != nil && t.Method == "POST") || isKBAsksFirst(name)
}

// insteadOf is the tools the user may run in place of a held call.
func insteadOf(name string) []string {
	out := []string{}
	if t := platformByName[name]; t != nil {
		for _, n := range t.Instead {
			if isPlatformTool(n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// toolHash identifies what a call to the tool does.
func toolHash(name string) string {
	if isKBAsksFirst(name) {
		return kbToolHash(name)
	}
	t := platformByName[name]
	if t == nil {
		return ""
	}
	parts := []string{t.Name, t.Operation, t.Method, t.Path, string(t.Parameters), strings.Join(t.FromViewer, ","), strings.Join(t.Defaults, ",")}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

var pathParam = regexp.MustCompile(`\{(\w+)\}`)

// prepareArgs is a call's arguments in canonical form, or what is wrong
// with them. raw is the model's argument text, or a decoded value.
func prepareArgs(name string, raw any, viewerUsername string) (args *jsonobj.Object, canonical string, problem string) {
	if isKBAsksFirst(name) {
		return prepareKBArgs(name, raw)
	}
	t := platformByName[name]
	if t == nil {
		return nil, "", "Unknown tool " + name
	}
	var given map[string]json.RawMessage
	var input json.RawMessage
	switch v := raw.(type) {
	case string:
		if jsTrim(v) == "" {
			input = json.RawMessage("{}")
		} else {
			input = json.RawMessage(v)
		}
	case json.RawMessage:
		input = v
	case nil:
		return nil, "", "The arguments were not valid JSON."
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, "", "The arguments were not valid JSON."
		}
		input = b
	}
	if !json.Valid(input) {
		return nil, "", "The arguments were not valid JSON."
	}
	if !jsval.IsObject(input) || json.Unmarshal(input, &given) != nil {
		return nil, "", "The arguments were not valid JSON."
	}
	args = jsonobj.New()
	for _, key := range t.props.Keys() {
		var schema struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		}
		_ = json.Unmarshal(t.props.Get(key), &schema)
		v, ok := given[key]
		if !ok || jsonobj.IsNullish(v) || string(v) == `""` {
			continue
		}
		var x any
		_ = json.Unmarshal(v, &x)
		switch schema.Type {
		case "array":
			items, isArr := x.([]any)
			if !isArr {
				items = []any{x}
			}
			list := []string{}
			for _, it := range items {
				switch s := it.(type) {
				case string:
					list = append(list, s)
				case float64:
					list = append(list, jsval.NumberString(s))
				default:
					return nil, "", fmt.Sprintf("The %s argument must be a list of strings.", key)
				}
			}
			if len(list) > 0 {
				args.SetValue(key, list)
			}
			continue
		case "boolean":
			switch b := x.(type) {
			case bool:
				args.SetValue(key, b)
			case string:
				if b != "true" && b != "false" {
					return nil, "", fmt.Sprintf("The %s argument must be true or false.", key)
				}
				args.SetValue(key, b == "true")
			default:
				return nil, "", fmt.Sprintf("The %s argument must be true or false.", key)
			}
			continue
		}
		var s string
		switch y := x.(type) {
		case string:
			s = y
		case float64:
			s = jsval.NumberString(y)
		case bool:
			s = fmt.Sprint(y)
		default:
			return nil, "", fmt.Sprintf("The %s argument must be a string, number or true/false.", key)
		}
		s = jsTrim(s)
		if s == "" {
			continue
		}
		if schema.Enum != nil && !slices.Contains(schema.Enum, s) {
			return nil, "", fmt.Sprintf("The %s argument must be one of %s.", key, strings.Join(schema.Enum, ", "))
		}
		args.SetValue(key, s)
	}
	for _, p := range t.Defaults {
		if args.Has(p) {
			continue
		}
		if viewerUsername == "" {
			return nil, "", fmt.Sprintf("The %s argument is required.", p)
		}
		args.SetValue(p, viewerUsername)
	}
	for _, m := range pathParam.FindAllStringSubmatch(t.Path, -1) {
		p := m[1]
		if slices.Contains(t.FromViewer, p) {
			continue
		}
		if s, ok := jsonobj.String(args.Get(p)); !ok || s == "" {
			return nil, "", fmt.Sprintf("The %s argument is required.", p)
		}
	}
	keys := args.Keys()
	sort.Strings(keys)
	sorted := jsonobj.New()
	for _, k := range keys {
		sorted.Set(k, args.Get(k))
	}
	b, _ := jsonobj.Marshal(sorted)
	return args, string(b), ""
}

// callSignature is the call as the approval card shows it.
func callSignature(name string, args *jsonobj.Object) string {
	order := args.Keys()
	if t := platformByName[name]; t != nil {
		order = t.props.Keys()
	}
	shown := []string{}
	for _, k := range order {
		if args.Has(k) {
			shown = append(shown, k+": "+string(args.Get(k)))
		}
	}
	return name + "(" + strings.Join(shown, ", ") + ")"
}

type effect struct {
	title                     func(a *jsonobj.Object) string
	reason                    string
	insteadLabel, insteadNote string
}

func argText(a *jsonobj.Object, k string) string {
	if s, ok := jsonobj.String(a.Get(k)); ok {
		return s
	}
	if !a.Has(k) {
		return "undefined"
	}
	return string(a.Get(k))
}

var effects = map[string]effect{
	"start_cluster":  {title: func(a *jsonobj.Object) string { return "Start cluster " + argText(a, "clusterName") + "?" }, reason: "Provisions its controller and resources. It starts billing."},
	"resume_cluster": {title: func(a *jsonobj.Object) string { return "Resume cluster " + argText(a, "clusterName") + "?" }, reason: "Restarts its controller. It starts billing again."},
	"stop_cluster": {title: func(a *jsonobj.Object) string { return "Stop cluster " + argText(a, "clusterName") + "?" }, reason: "Stops its controller to stop its compute billing. Its disks and settings are kept, and it can be resumed.",
		insteadLabel: "Stop it instead", insteadNote: "Keeps its disks and settings, so it can be resumed."},
	"destroy_cluster": {title: func(a *jsonobj.Object) string { return "Destroy cluster " + argText(a, "clusterName") + "?" }, reason: "Destroys its provisioned resources, including its disks, so nothing bills. Its definition stays, so it can be started again from scratch.",
		insteadLabel: "Destroy it instead", insteadNote: "Deletes its disks too, so nothing bills."},
	"cancel_workflow_run": {title: func(a *jsonobj.Object) string { return "Cancel run " + argText(a, "slug") + "?" }, reason: "Cancels the workflow run."},
	"delete_kb_files": {title: func(a *jsonobj.Object) string {
		if p := kbPaths(a); len(p) == 1 {
			return "Delete " + p[0] + "?"
		}
		return "Delete " + itoaInt(len(kbPaths(a))) + " files?"
	}, reason: "Deletes these files from the library, with their extracted text and previews, and updates the index. The Studio cannot undo this."},
}

// approvalPart is the approval part the chat draws for a held call.
func approvalPart(name string, args *jsonobj.Object, toolCallID, expiresAt string) *jsonobj.Object {
	e, has := effects[name]
	var instead []*jsonobj.Object
	for _, other := range insteadOf(name) {
		label := "Run " + other + " instead"
		if oe := effects[other]; oe.insteadLabel != "" {
			label = oe.insteadLabel
		}
		alt := reqbody.O("tool", other, "label", label)
		if oe := effects[other]; oe.insteadNote != "" {
			alt.SetValue("description", oe.insteadNote)
		}
		instead = append(instead, alt)
	}
	header := "Run " + name + "?"
	if has {
		header = e.title(args)
	}
	command := callSignature(name, args)
	if isKBAsksFirst(name) {
		command = strings.Join(kbPaths(args), "\n")
	}
	o := reqbody.O("kind", "approval", "id", toolCallID, "approvalKind", "confirm", "toolName", name, "subject", "call", "header", header, "command", command)
	if has {
		o.SetValue("reason", e.reason)
	}
	o.SetValue("canAllowDirectory", false)
	o.SetValue("expiresAt", expiresAt)
	if len(instead) > 0 {
		o.SetValue("instead", instead)
	}
	return o
}

/* ---- the platform API ---- */

const (
	maxResultBytes  = 16 * 1024
	platformTimeout = 20 * time.Second
	accountTTL      = 5 * time.Minute
)

// Account is the platform account a key belongs to.
type Account struct{ Username, Organization string }

type accountCache struct {
	mu sync.Mutex
	m  map[string]struct {
		at time.Time
		v  *Account
	}
}

var accounts = &accountCache{m: map[string]struct {
	at time.Time
	v  *Account
}{}}

func keyID(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:24]
}

// viewerAccount is the account a key belongs to.
func (m *Module) viewerAccount(ctx context.Context, key string) *Account {
	id := keyID(key)
	accounts.mu.Lock()
	hit, ok := accounts.m[id]
	accounts.mu.Unlock()
	if ok && time.Since(hit.at) < accountTTL {
		return hit.v
	}
	origin := m.Plat.Origin()
	if origin == "" {
		return nil
	}
	var v *Account
	res, err := platformDo(ctx, "GET", origin+"/api/auth/session", key)
	if err == nil {
		defer res.Body.Close()
		if res.StatusCode < 300 {
			var body map[string]any
			if json.NewDecoder(res.Body).Decode(&body) == nil {
				if u, _ := body["username"].(string); u != "" {
					org, _ := body["organization"].(string)
					v = &Account{Username: u, Organization: org}
				}
			}
		}
	}
	if v != nil {
		accounts.mu.Lock()
		accounts.m[id] = struct {
			at time.Time
			v  *Account
		}{time.Now(), v}
		accounts.mu.Unlock()
	}
	return v
}

func platformDo(ctx context.Context, method, u, key string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, platformTimeout)
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	res.Body = &cancelBody{res.Body, cancel}
	return res, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel func()
}

func (c *cancelBody) Close() error { err := c.ReadCloser.Close(); c.cancel(); return err }

func isScalarJSON(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s == "" || (s[0] != '{' && s[0] != '[')
}

// projectResult is a result reduced to the tool's fields.
func projectResult(t *PlatformTool, value json.RawMessage) json.RawMessage {
	if jsval.IsObject(value) {
		if o, err := jsonobj.Parse(value); err == nil {
			o.Delete("$schema")
			value, _ = jsonobj.Marshal(o)
		}
	}
	if len(t.Fields) == 0 {
		return value
	}
	var items func(v json.RawMessage) json.RawMessage
	items = func(v json.RawMessage) json.RawMessage {
		if arr, ok := jsval.Array(v); ok {
			out := make([]json.RawMessage, len(arr))
			for i, x := range arr {
				out[i] = items(x)
			}
			b, _ := jsonobj.Marshal(out)
			return b
		}
		if jsval.IsObject(v) {
			o, err := jsonobj.Parse(v)
			if err != nil {
				return v
			}
			p := jsonobj.New()
			for _, f := range t.Fields {
				if o.Has(f) && !jsonobj.IsNullish(o.Get(f)) {
					p.Set(f, o.Get(f))
				}
			}
			b, _ := jsonobj.Marshal(p)
			return b
		}
		return v
	}
	if _, ok := jsval.Array(value); ok {
		return items(value)
	}
	if jsval.IsObject(value) {
		o, err := jsonobj.Parse(value)
		if err != nil {
			return value
		}
		listKey := ""
		for _, k := range o.Keys() {
			if _, ok := jsval.Array(o.Get(k)); ok {
				listKey = k
				break
			}
		}
		if listKey != "" {
			p := jsonobj.New()
			for _, k := range o.Keys() {
				switch {
				case k == listKey:
					p.Set(k, items(o.Get(k)))
				case isScalarJSON(o.Get(k)):
					p.Set(k, o.Get(k))
				}
			}
			b, _ := jsonobj.Marshal(p)
			return b
		}
		return items(value)
	}
	return value
}

func truncateResult(s string) string {
	if len(s) <= maxResultBytes {
		return s
	}
	cut := s[:maxResultBytes]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + "\n[truncated: the result was longer than the tool limit]"
}

// runPlatformTool runs one platform operation with the key.
func (m *Module) runPlatformTool(ctx context.Context, name string, args *jsonobj.Object, key string) (string, bool) {
	t := platformByName[name]
	if t == nil {
		return "Unknown tool " + name, true
	}
	origin := m.Plat.Origin()
	if origin == "" {
		return "No platform address is configured.", true
	}
	fill := map[string]string{}
	if slices.Contains(t.FromViewer, "organization") {
		acc := m.viewerAccount(ctx, key)
		if acc == nil || acc.Organization == "" {
			return "The organization for this call could not be found.", true
		}
		fill["organization"] = acc.Organization
	}
	p := t.Path
	var query []string
	for _, k := range args.Keys() {
		v := args.Get(k)
		if strings.Contains(p, "{"+k+"}") {
			p = strings.ReplaceAll(p, "{"+k+"}", encodeURIComponent(jsText(v)))
			continue
		}
		if arr, ok := jsval.Array(v); ok {
			for _, x := range arr {
				query = append(query, formEncode(k)+"="+formEncode(jsText(x)))
			}
			continue
		}
		query = append(query, formEncode(k)+"="+formEncode(jsText(v)))
	}
	for k, v := range fill {
		p = strings.ReplaceAll(p, "{"+k+"}", encodeURIComponent(v))
	}
	if mm := pathParam.FindStringSubmatch(p); mm != nil {
		return fmt.Sprintf("The %s argument is required.", mm[1]), true
	}
	u := origin + p
	if len(query) > 0 {
		u += "?" + strings.Join(query, "&")
	}
	res, err := platformDo(ctx, t.Method, u, key)
	if err != nil {
		msg := err.Error()
		return "The platform could not be reached: " + jsval.Slice(msg, 160), true
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	text := string(body)
	if res.StatusCode >= 400 {
		var problem map[string]any
		_ = json.Unmarshal(body, &problem)
		if s, _ := problem["message"].(string); s != "" {
			return s, true
		}
		if s, _ := problem["detail"].(string); s != "" {
			return s, true
		}
		if st := http.StatusText(res.StatusCode); st != "" {
			return nodeStatusText(res.StatusCode, st), true
		}
		return fmt.Sprintf("status %d", res.StatusCode), true
	}
	if jsTrim(text) == "" {
		return "Done.", false
	}
	if !json.Valid(body) {
		return "The lookup returned a result that could not be read.", true
	}
	return truncateResult(string(projectResult(t, body))), false
}

// nodeStatusText is node:http's STATUS_CODES where it differs from Go's.
func nodeStatusText(code int, goText string) string {
	switch code {
	case 413:
		return "Payload Too Large"
	case 414:
		return "URI Too Long"
	case 416:
		return "Range Not Satisfiable"
	case 418:
		return "I'm a Teapot"
	}
	return goText
}

// jsText is String(v) for a decoded JSON value.
func jsText(v json.RawMessage) string {
	if s, ok := jsonobj.String(v); ok {
		return s
	}
	return strings.TrimSpace(string(v))
}

// encodeURIComponent is JavaScript's.
func encodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// formEncode is URLSearchParams's serialization of one name or value.
func formEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '*' || c == '-' || c == '.' || c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
