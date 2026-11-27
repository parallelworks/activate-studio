package core

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
	"github.com/parallelworks/activate-studio/server-go/internal/jsval"
	"github.com/parallelworks/activate-studio/server-go/internal/mainlib"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

func (c *Core) getSettings(w http.ResponseWriter, r *http.Request) error {
	o := jsonobj.New()
	o.SetValue("effective", c.a.Settings.Effective())
	o.SetValue("saved", c.a.Settings.SavedObject())
	// Background work runs with no user present, so it can only use the
	// deployment credential or the host's pw CLI login.
	o.SetValue("deploymentCredential", c.a.Providers.GatewayConfigured(r))
	o.SetValue("gufiLogFile", c.a.Providers.GufiLogInfo())
	// Claude Code accounts as they stand for this person, and why they may
	// not change the setting (its administrators do, where people sign in).
	cc := jsonobj.New()
	cc.SetValue("on", pwcode.ClaudeCodeOff(r.Context()) == "")
	cc.SetValue("personal", c.a.Settings.PersonalInstall())
	locked := c.SettingsLocked(r)
	if locked != "" {
		cc.SetValue("locked", locked)
	}
	o.SetValue("claudeCode", cc)
	// Why the person may not change the Studio's settings; absent when
	// they may.
	if locked != "" {
		o.SetValue("locked", locked)
	}
	// Whether the saved custom tools may run (settings.CustomToolsApproved).
	o.SetValue("customToolsApproved", c.a.Settings.CustomToolsApproved(c.a.Settings.Effective()))
	if c.a.Settings.CustomToolsOff() {
		o.SetValue("customToolsOff", true)
	}
	// A configured icon that cannot be shown, and why (pageicons.go).
	ctx, stop := context.WithTimeout(r.Context(), 3*time.Second)
	defer stop()
	o.SetValue("iconProblems", c.IconProblems(ctx, true))
	return ok(w, o)
}

// SettingsAdminOnly is the refusal of a change to the Studio's settings to
// a signed-in person who does not administer it.
const SettingsAdminOnly = "On a Studio with sign-in, only its administrators change its settings."

// SettingsLocked is why the person in r may not change the Studio's
// settings, or "": on a Studio with sign-in they apply to everyone (and
// custom tools run commands as the Studio's own account), so only its
// administrators change them: the deployment's owner and STUDIO_ADMINS,
// as for the main library.
func (c *Core) SettingsLocked(r *http.Request) string {
	if ok, _ := mainlib.Request(c.a, r); ok {
		return ""
	}
	return SettingsAdminOnly
}

// savedMap is next as Settings.EffectiveOf takes it.
func savedMap(next *jsonobj.Object) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for _, k := range next.Keys() {
		m[k] = next.Get(k)
	}
	return m
}

// changedSettings is the settings whose effective value differs between a
// and b, by their names in the API.
func changedSettings(a, b settings.Effective) []string {
	var am, bm map[string]json.RawMessage
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	json.Unmarshal(ab, &am)
	json.Unmarshal(bb, &bm)
	var out []string
	keys, _ := jsonobj.Parse(ab)
	for _, k := range keys.Keys() {
		if string(am[k]) != string(bm[k]) {
			out = append(out, k)
		}
	}
	return out
}

var (
	accentRe    = regexp.MustCompile(`^([a-z][a-z0-9-]{0,23}|custom:#[0-9a-fA-F]{6})$`)
	surfaceRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	iconPathRe  = regexp.MustCompile(`(?i)^https?://`)
	httpRe      = regexp.MustCompile(`^https?://`)
	colorRe     = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	nameCharsRe = regexp.MustCompile(`[^a-z0-9_]`)
	edgeUnder   = regexp.MustCompile(`^_+|_+$`)
	slugRe      = regexp.MustCompile(`[^a-z0-9-]+`)
	edgeDash    = regexp.MustCompile(`^-+|-+$`)
	trailSlash  = regexp.MustCompile(`/+$`)
	oneSlash    = regexp.MustCompile(`/$`)

	workflowName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,120}$`)
	marketplace  = regexp.MustCompile(`^marketplace/([A-Za-z0-9_.-]{1,120})$`)
	githubEntry  = regexp.MustCompile(`^github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)((?:/[A-Za-z0-9_.-]+)*)(?:@([A-Za-z0-9_./-]{1,120}))?$`)
	schemeRe     = regexp.MustCompile(`^https?://`)
)

// ParseWorkflowEntry is workflowEntries.ts's parseWorkflowEntry(...).entry:
// a saved workflow's name, marketplace/<slug>, or github.com/<owner>/<repo>
// [/path][@ref]; "" for anything else.
func ParseWorkflowEntry(raw string) string {
	s := jsval.Trim(raw)
	if s == "" || jsval.Len(s) > 300 || strings.Contains(s, "..") {
		return ""
	}
	if workflowName.MatchString(s) || marketplace.MatchString(s) {
		return s
	}
	bare := schemeRe.ReplaceAllString(s, "")
	if githubEntry.MatchString(bare) {
		return bare
	}
	return ""
}

func slug(raw string, n int) string {
	s := nameCharsRe.ReplaceAllString(strings.ToLower(raw), "_")
	return jsval.Slice(edgeUnder.ReplaceAllString(s, ""), n)
}

// putSettings is settings.ts's PUT /api/settings, field for field.
func (c *Core) putSettings(w http.ResponseWriter, r *http.Request) error {
	b, err := body(r)
	if err != nil {
		return err
	}
	next := c.a.Settings.SavedObject()
	in := func(k string) (json.RawMessage, bool) { v := b[k]; return v, jsval.Present(v) }
	set := func(k string, v any) { next.SetValue(k, v) }
	str := func(k string) string { return jsval.String(b[k]) }
	orNil := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	bad := func(msg string) error { return apierr.New(400, "%s", msg) }

	if _, ok := in("appName"); ok {
		set("appName", orNil(jsval.Slice(str("appName"), 60)))
	}
	if _, ok := in("kbLabel"); ok {
		set("kbLabel", orNil(jsval.Slice(str("kbLabel"), 60)))
	}
	if v, ok := in("sections"); ok {
		if a, isArr := jsval.Array(v); isArr && len(a) > 0 {
			out := []string{}
			for i, e := range a {
				if i == 20 {
					break
				}
				out = append(out, jsval.String(e))
			}
			set("sections", out)
		} else {
			set("sections", nil)
		}
		set("sectionsGen", settings.SectionsGen)
	}
	if v, ok := in("workflowCollection"); ok {
		var list []string
		seen := map[string]bool{}
		if a, isArr := jsval.Array(v); isArr {
			for _, e := range a {
				if entry := ParseWorkflowEntry(jsval.String(e)); entry != "" && !seen[entry] {
					seen[entry] = true
					list = append(list, entry)
				}
			}
		}
		if len(list) > 60 {
			list = list[:60]
		}
		if len(list) > 0 {
			set("workflowCollection", list)
		} else {
			set("workflowCollection", nil)
		}
	}
	if _, ok := in("theme"); ok {
		if str("theme") == "dark" && isStringValue(b["theme"]) {
			set("theme", "dark")
		} else {
			set("theme", "light")
		}
	}
	if _, ok := in("accent"); ok {
		a := str("accent")
		if !accentRe.MatchString(a) {
			return bad("invalid accent")
		}
		set("accent", a)
	}
	for _, f := range []string{"iconUrl", "iconUrlDark"} {
		if _, ok := in(f); !ok {
			continue
		}
		v := jsval.Trim(str(f))
		if v != "" && !iconPathRe.MatchString(v) && !strings.HasPrefix(v, "/") {
			return bad("icon must be an http(s) URL or an absolute path")
		}
		set(f, v)
	}
	if v, ok := in("navWidth"); ok {
		n := jsval.Number(v)
		if !jsval.IsInteger(n) || n < 200 || n > 400 {
			return bad("navigation width must be a whole number of pixels from 200 to 400")
		}
		set("navWidth", n)
	}
	if _, ok := in("surface"); ok {
		a := str("surface")
		if !surfaceRe.MatchString(a) {
			return bad("invalid surface name")
		}
		set("surface", a)
	}
	if v, ok := in("searchEngine"); ok {
		s, _ := jsonobj.String(v)
		if s != "parallel" && s != "gufi_vt" {
			return bad("search engine must be parallel or gufi_vt")
		}
		set("searchEngine", s)
	}
	if v, ok := in("gufiLog"); ok {
		set("gufiLog", jsval.Truthy(v))
	}
	if v, ok := in("sweepIntervalSec"); ok {
		n := jsval.Number(v)
		if !jsval.IsFinite(n) || n < 0 || n > 86400 {
			return bad("sweep interval must be 0 to 86400 seconds")
		}
		set("sweepIntervalSec", n)
	}
	if v, ok := in("searchTimeLimitSec"); ok {
		n := jsval.Number(v)
		if !jsval.IsFinite(n) || n < 0 || n > 86400 {
			return bad("search time limit must be 0 to 86400 seconds (0 is no limit)")
		}
		set("searchTimeLimitSec", n)
	}
	if v, ok := in("suggestedPrompts"); ok {
		a, isArr := jsval.Array(v)
		if !isArr {
			return bad("suggestedPrompts must be an array")
		}
		list := []string{}
		for _, e := range a {
			if p := jsval.Slice(jsval.String(e), 300); p != "" {
				list = append(list, p)
			}
		}
		if len(list) > 12 {
			list = list[:12]
		}
		// The defaults, saved as they are, stay defaults.
		if len(list) > 0 && !sameList(list, c.a.Settings.EnvPrompts()) {
			set("suggestedPrompts", list)
		} else {
			set("suggestedPrompts", nil)
		}
	}
	if _, ok := in("visionModel"); ok {
		set("visionModel", orNil(jsval.Slice(str("visionModel"), 120)))
	}
	if v, ok := in("mcpEnabled"); ok {
		set("mcpEnabled", jsval.Truthy(v))
	}
	stopRag := false
	if v, ok := in("ragProxyEnabled"); ok {
		on := jsval.Truthy(v)
		set("ragProxyEnabled", on)
		// Off means off everywhere: the platform registration holds a
		// session that keeps the model listed.
		stopRag = !on
	}
	if v, ok := in("delegationEnabled"); ok {
		set("delegationEnabled", jsval.Truthy(v))
	}
	if v, ok := in("aiEnabled"); ok {
		set("aiEnabled", jsval.Truthy(v))
	}
	if v, ok := in("agentExecution"); ok {
		s, isStr := jsonobj.String(v)
		if !isStr || (s != "runner" && s != "sessions" && s != "both") {
			return bad("agent execution must be runner, sessions, or both")
		}
		set("agentExecution", s)
	}
	if v, ok := in("voiceEnabled"); ok {
		set("voiceEnabled", jsval.Truthy(v))
	}
	if _, ok := in("voiceUrl"); ok {
		u := trailSlash.ReplaceAllString(jsval.Trim(str("voiceUrl")), "")
		if u != "" && !httpRe.MatchString(u) {
			return bad("voice URL must be http(s)")
		}
		set("voiceUrl", orNil(u))
	}
	if v, ok := in("maxToolRounds"); ok {
		n := jsval.Number(v)
		if !jsval.IsInteger(n) || n < 5 || n > 200 {
			return bad("tool rounds must be 5 to 200")
		}
		set("maxToolRounds", n)
	}
	if v, ok := in("delegationMaxAgents"); ok {
		n := jsval.Number(v)
		if !jsval.IsFinite(n) || n < 1 || n > 24 {
			return bad("agents must be 1 to 24")
		}
		set("delegationMaxAgents", n)
	}
	if v, ok := in("delegationMaxDepth"); ok {
		n := jsval.Number(v)
		if !jsval.IsFinite(n) || n < 0 || n > 3 {
			return bad("depth must be 0 to 3")
		}
		set("delegationMaxDepth", n)
	}
	if v, ok := in("mcpServers"); ok {
		a, isArr := jsval.Array(v)
		if !isArr {
			return bad("mcpServers must be an array")
		}
		out := []*jsonobj.Object{}
		for i, e := range a {
			if i == 10 {
				break
			}
			m := objField(e)
			name := slug(fieldString(m, "name"), 24)
			url := jsval.Slice(jsval.Trim(fieldString(m, "url")), 500)
			if url != "" && !iconPathRe.MatchString(url) {
				return bad("MCP server url must be http(s)")
			}
			if name == "" || url == "" {
				continue
			}
			o := jsonobj.New()
			o.SetValue("name", name)
			o.SetValue("url", url)
			o.SetValue("header", orNil(jsval.Slice(fieldString(m, "header"), 300)))
			out = append(out, o)
		}
		set("mcpServers", out)
	}
	if v, ok := in("historyIntervalSec"); ok {
		n := jsval.Number(v)
		if !jsval.IsFinite(n) || n < 0 || n > 2592000 {
			return bad("history interval must be 0 to 2592000 seconds")
		}
		set("historyIntervalSec", n)
	}
	if v, ok := in("disabledTools"); ok {
		a, isArr := jsval.Array(v)
		if !isArr {
			return bad("disabledTools must be an array")
		}
		list := []string{}
		for _, e := range a {
			if t := jsval.Slice(jsval.String(e), 60); t != "" {
				list = append(list, t)
			}
		}
		if len(list) > 50 {
			list = list[:50]
		}
		set("disabledTools", list)
	}
	if v, ok := in("customTools"); ok {
		a, isArr := jsval.Array(v)
		if !isArr {
			return bad("customTools must be an array")
		}
		out := []*jsonobj.Object{}
		for i, e := range a {
			if i == 12 {
				break
			}
			m := objField(e)
			o := jsonobj.New()
			name := slug(fieldString(m, "name"), 40)
			command := jsval.Slice(fieldString(m, "command"), 500)
			o.SetValue("name", name)
			o.SetValue("description", jsval.Slice(fieldString(m, "description"), 300))
			o.SetValue("command", command)
			if name != "" && command != "" {
				out = append(out, o)
			}
		}
		set("customTools", out)
	}
	if _, ok := in("ragDefaultModel"); ok {
		set("ragDefaultModel", orNil(jsval.Slice(str("ragDefaultModel"), 120)))
	}
	if v, ok := in("ragTopK"); ok {
		n := jsval.Number(v)
		if !jsval.IsFinite(n) || n < 1 || n > 20 {
			return bad("ragTopK must be 1 to 20")
		}
		set("ragTopK", n)
	}
	for _, f := range []string{"ragAdvertiseRagModel", "ragAdvertiseAgentModel", "ragEndpointAutoStart", "requirePersonalKey"} {
		if v, ok := in(f); ok {
			set(f, jsval.Truthy(v))
		}
	}
	// The form sends back what it shows: only a change of it is a choice,
	// so a save of other settings does not pin the default.
	if v, ok := in("chatSharedHistory"); ok {
		if on := jsval.Truthy(v); on != c.a.Settings.Effective().ChatSharedHistory {
			set("chatSharedHistory", on)
			set(settings.SharedHistoryChosenKey, true)
		}
	}
	if _, ok := in("bannerText"); ok {
		set("bannerText", jsval.Slice(str("bannerText"), 200))
	}
	if _, ok := in("kbMission"); ok {
		// Blank, or the default as it is, keeps the default.
		m := jsval.Slice(str("kbMission"), 4000)
		if t := jsval.Trim(m); t != "" && t != jsval.Trim(c.a.Settings.DefaultMission()) {
			set("kbMission", m)
		} else {
			set("kbMission", nil)
		}
	}
	if _, ok := in("bannerColor"); ok {
		col := jsval.Trim(str("bannerColor"))
		if col != "" && !colorRe.MatchString(col) {
			return bad("banner color must be a #rrggbb value")
		}
		set("bannerColor", orNil(col))
	}
	for _, f := range []string{"bannerWhenEmbedded"} {
		if v, ok := in(f); ok {
			set(f, jsval.Truthy(v))
		}
	}
	if _, ok := in("hpcStatusUrl"); ok {
		v := oneSlash.ReplaceAllString(jsval.Trim(str("hpcStatusUrl")), "")
		if v != "" && !iconPathRe.MatchString(v) {
			return bad("hpcStatusUrl must be http(s)")
		}
		set("hpcStatusUrl", orNil(v))
	}
	if _, ok := in("hpcStatusWorkflow"); ok {
		set("hpcStatusWorkflow", orNil(jsval.Slice(str("hpcStatusWorkflow"), 80)))
	}
	for _, f := range []struct {
		key string
		max int
	}{{"chatModelWindows", 1000}, {"chatTemplateKwargs", 2000}, {"serveWorkflows", 4000}} {
		if _, ok := in(f.key); !ok {
			continue
		}
		raw := jsval.Slice(str(f.key), f.max)
		if !json.Valid([]byte(raw)) {
			return bad(f.key + " must be JSON")
		}
		set(f.key, raw)
	}
	if _, ok := in("chatEngine"); ok {
		v := jsval.Trim(str("chatEngine"))
		if !slices.Contains(settings.ChatEngines, v) {
			return bad("chatEngine must be auto, pwcode or studio")
		}
		set("chatEngine", v)
	}
	if _, ok := in("pwcodeModel"); ok {
		set("pwcodeModel", orNil(jsval.Slice(jsval.Trim(str("pwcodeModel")), 120)))
	}
	if _, ok := in("claudeCodeAccounts"); ok {
		v := jsval.Trim(str("claudeCodeAccounts"))
		if !slices.Contains(settings.ClaudeCodeModes, v) {
			return bad("claudeCodeAccounts must be auto, on or off")
		}
		set("claudeCodeAccounts", v)
	}
	if _, ok := in("pwcodePermissionMode"); ok {
		v := jsval.Trim(str("pwcodePermissionMode"))
		if !slices.Contains(settings.PWCodeModes, v) {
			return bad("pwcodePermissionMode must be read-only, accept-edits, auto, plan or empty")
		}
		set("pwcodePermissionMode", orNil(v))
	}
	if v, ok := in("updates"); ok {
		if !jsval.IsObject(v) {
			return bad("updates must be an object")
		}
		u := objField(v)
		if c, has := u["check"]; has && jsval.Present(c) && string(c) != "true" && string(c) != "false" {
			return bad("updates.check must be true or false")
		}
		if i, has := u["install"]; has && jsval.Present(i) {
			if s, _ := jsonobj.String(i); s != "manual" && s != "auto" {
				return bad("updates.install must be manual or auto")
			}
		}
		if ch, has := u["channel"]; has && jsval.Present(ch) {
			if s, _ := jsonobj.String(ch); s != "stable" && s != "preview" && s != "prerelease" {
				return bad("updates.channel must be stable or preview")
			}
		}
		set("updates", mergePolicy(next.Get("updates"), u))
	}
	if _, ok := in("ragEndpointName"); ok {
		n := slugRe.ReplaceAllString(strings.ToLower(str("ragEndpointName")), "-")
		set("ragEndpointName", orNil(jsval.Slice(edgeDash.ReplaceAllString(n, ""), 40)))
	}
	// Every setting applies to the whole Studio. The forms send back what
	// they show, so a value as it already stands is no change; any change
	// is its administrators', and none of the request is saved otherwise.
	after := c.a.Settings.EffectiveOf(savedMap(next))
	if why := c.SettingsLocked(r); why != "" {
		if changed := changedSettings(c.a.Settings.Effective(), after); len(changed) > 0 {
			return apierr.New(http.StatusForbidden, "%s Not saved: %s.", why, strings.Join(changed, ", "))
		}
		o := jsonobj.New()
		o.SetValue("effective", c.a.Settings.Effective())
		o.SetValue("saved", c.a.Settings.SavedObject())
		return ok(w, o)
	}
	// With STUDIO_CUSTOM_TOOLS=0 the saved custom tools are kept as they
	// are: none is added or changed, and a save, by someone Settings does
	// not show them to, approves none.
	off := c.a.Settings.CustomToolsOff()
	if off && !sameRaws(c.a.Settings.Effective().CustomTools, after.CustomTools) {
		return apierr.New(http.StatusForbidden, "%s", CustomToolsOff)
	}
	// Custom tools run as they were last saved here, by an administrator
	// (settings.CustomToolsApproved). With them off, the approval stays
	// as it was.
	switch {
	case off:
	case c.a.Cfg.AuthEnabled() && len(after.CustomTools) > 0:
		next.SetValue(settings.CustomToolsApprovedKey, settings.CustomToolsDigest(after.CustomTools))
	default:
		next.Delete(settings.CustomToolsApprovedKey)
	}
	if err := c.a.Settings.WriteObject(next); err != nil {
		return err
	}
	if stopRag {
		go c.a.Providers.StopRagEndpoint(r.Clone(contextWithoutCancel(r)))
	}
	c.a.Providers.InvalidateMCP()
	o := jsonobj.New()
	o.SetValue("effective", c.a.Settings.Effective())
	o.SetValue("saved", next)
	return ok(w, o)
}

// CustomToolsOff is the refusal of a change to custom tools where
// STUDIO_CUSTOM_TOOLS=0.
const CustomToolsOff = "Custom tools are off on this Studio (STUDIO_CUSTOM_TOOLS=0): the saved ones are kept, and none can be added or changed."

// sameRaws is whether two lists of JSON values are the same, ignoring
// spacing.
func sameRaws(a, b []json.RawMessage) bool {
	return settings.CustomToolsDigest(a) == settings.CustomToolsDigest(b)
}

func isStringValue(v json.RawMessage) bool { _, ok := jsonobj.String(v); return ok }

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func objField(v json.RawMessage) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if jsval.IsObject(v) {
		json.Unmarshal(v, &m)
	}
	return m
}

// fieldString is String(m?.k ?? ”): absent or null is "".
func fieldString(m map[string]json.RawMessage, k string) string {
	v := m[k]
	if jsonobj.IsNullish(v) {
		return ""
	}
	return jsval.String(v)
}

// policy is settings.ts's asPolicy over a saved or given value.
type policy struct {
	Check   bool   `json:"check"`
	Install string `json:"install"`
	Channel string `json:"channel"`
}

// MarshalJSON writes Preview as "prerelease", the value releases up to
// v1.188 store and read, so a rollback to one of them stays on the channel
// it was on; every reader here takes both spellings as preview.
func (p policy) MarshalJSON() ([]byte, error) {
	type plain policy
	if p.Channel == "preview" {
		p.Channel = "prerelease"
	}
	return json.Marshal(plain(p))
}

func asPolicy(m map[string]json.RawMessage) policy {
	p := policy{Check: true, Install: "manual", Channel: "stable"}
	if v, ok := m["check"]; ok && (string(v) == "true" || string(v) == "false") {
		p.Check = string(v) == "true"
	}
	if s, _ := jsonobj.String(m["install"]); s == "auto" {
		p.Install = "auto"
	}
	switch s, _ := jsonobj.String(m["channel"]); s {
	case "preview", "prerelease":
		p.Channel = "preview"
	}
	return p
}

// mergePolicy is asPolicy({ ...asPolicy(saved), ...given }).
func mergePolicy(saved json.RawMessage, given map[string]json.RawMessage) policy {
	base := asPolicy(objField(saved))
	m := map[string]json.RawMessage{}
	b, _ := json.Marshal(base)
	json.Unmarshal(b, &m)
	for k, v := range given {
		if jsval.Present(v) {
			m[k] = v
		}
	}
	return asPolicy(m)
}
