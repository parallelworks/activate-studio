package chat

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// When the Studio adds itself to pw code on a machine it also allows its
// tools that only read in pw code's user settings there (permissions.allow
// in code.json). pw code asks for every call of another server's tool
// otherwise, whatever the tool's readOnlyHint says, and refuses it in
// read-only mode, so a model there goes back to searching files.
//
// The rules hold for turns a person types on the machine. pw code sets
// saved rules aside on a turn driven from elsewhere (the web app, a Studio
// chat through the platform), which keeps asking.

// machineRules are the tools of names the Studio allows on a machine, as
// pw code's rules, each tool by name (a wildcard is not matched), sorted.
func machineRules(names []string) []string {
	out := []string{}
	for _, n := range names {
		if chatReadTools[n] {
			out = append(out, studioToolPrefix+n)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// machineRulesAll are every rule the Studio may have written, which Remove
// takes back.
func machineRulesAll() []string {
	names := make([]string, 0, len(chatReadTools))
	for n := range chatReadTools {
		names = append(names, n)
	}
	return machineRules(names)
}

// bridgeAllows is the tools r's person would have allowed on a machine, for
// the card that asks, without pw code's prefix.
func (m *Module) bridgeAllows(r *http.Request) []string {
	rules := machineRules(specNames(m.personTools(r, m.personCtx(r))))
	for i, s := range rules {
		rules[i] = strings.TrimPrefix(s, studioToolPrefix)
	}
	return rules
}

// allowAwk adds rules to, or takes them out of, the array permissions.allow
// of the JSON object on standard input, and writes the file back with every
// other byte as it was. The last line it writes to standard error is the
// number of rules changed, or why it changed nothing. It reads the whole
// document first and refuses one it does not follow, as pw code does.
const allowAwk = `
function bad(w) { if (!no) no = w; return 0 }
function ws() { while (p <= n && index(" \t\r\n", substr(s, p, 1))) p++ }
function str(  a, c) {
  if (substr(s, p, 1) != "\"") return bad("is not valid JSON")
  a = ++p
  while (p <= n) {
    c = substr(s, p, 1)
    if (c == "\\") { p += 2; continue }
    if (c == "\"") { p++; return substr(s, a, p - 1 - a) }
    if (c == "\n") break
    p++
  }
  return bad("is not valid JSON")
}
function val(path,  c, a) {
  ws(); c = substr(s, p, 1)
  if (c == "{") return obj(path)
  if (c == "[") return arr(path)
  if (path == "/permissions" || path == "/permissions/allow") return bad("holds permissions in a form the Studio does not change")
  if (c == "\"") { str(); return }
  a = p
  while (p <= n && substr(s, p, 1) ~ /[-+.0-9A-Za-z]/) p++
  if (substr(s, a, p - a) !~ /^(true|false|null|-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][-+]?[0-9]+)?)$/) bad("is not valid JSON")
}
function obj(path,  k, first, watch) {
  watch = (path == "" || path == "/permissions")
  if (path == "/permissions/allow") return bad("holds permissions in a form the Studio does not change")
  p++; first = 1
  if (watch) { open[path] = p; ws(); empty[path] = (substr(s, p, 1) == "}") }
  while (!no) {
    ws()
    if (first && substr(s, p, 1) == "}") { p++; return }
    ws(); k = str(); ws()
    if (no) return
    if (watch && (index(k, "\\") || (path == "" && tolower(k) == "permissions" && k != "permissions") || (path != "" && tolower(k) == "allow" && k != "allow") || seen[path "/" k]++))
      return bad("holds permissions in a form the Studio does not change")
    if (substr(s, p, 1) != ":") return bad("is not valid JSON")
    p++; val(watch ? path "/" k : "x"); ws(); first = 0
    if (no) return
    if (substr(s, p, 1) == "}") { p++; return }
    if (substr(s, p, 1) != ",") return bad("is not valid JSON")
    p++
  }
}
function arr(path,  a, mine) {
  mine = (path == "/permissions/allow")
  if (path == "/permissions") return bad("holds permissions in a form the Studio does not change")
  p++
  if (mine) { have = 1; from = p }
  ws()
  if (substr(s, p, 1) == "]") { if (mine) { count = 0; to = p }; p++; return }
  if (mine) p = from
  while (!no) {
    if (mine) { a = p; ws(); count++; lead[count] = substr(s, a, p - a); a = p }
    val("x")
    if (no) return
    if (mine) elem[count] = substr(s, a, p - a)
    if (mine) { tail = p }
    ws()
    if (substr(s, p, 1) == "]") { if (mine) { to = p; rest = substr(s, tail, p - tail) }; p++; return }
    if (substr(s, p, 1) != ",") return bad("is not valid JSON")
    p++
  }
}
{ s = s (NR > 1 ? "\n" : "") $0 }
END { n = length(s); p = 1
  ws(); if (substr(s, p, 1) != "{") bad("is not a JSON object")
  else { val(""); ws(); if (!no && p <= n) bad("is not valid JSON") }
  if (no) { print no > "/dev/stderr"; exit 3 }
  want = split(rules, rule, " "); changed = 0
  for (i = 1; i <= want; i++) listed["\"" rule[i] "\""] = 1
  for (i = 1; i <= count; i++) is[elem[i]] = 1
  if (act == "remove") {
    out = ""
    for (i = 1; i <= count; i++) {
      if (listed[elem[i]]) { changed++; continue }
      out = out (kept++ ? "," : "") lead[i] elem[i]
    }
    if (changed) s = substr(s, 1, from - 1) out (kept ? rest : "") substr(s, to)
  } else {
    for (i = 1; i <= want; i++) if (!is["\"" rule[i] "\""]) { add = add (changed++ ? "," (count ? lead[count] : " ") : "") "\"" rule[i] "\"" }
    if (changed) {
      if (count) s = substr(s, 1, tail - 1) "," lead[count] add substr(s, tail)
      else if (have) s = substr(s, 1, from - 1) add substr(s, to)
      else if (seen["/permissions"]) s = substr(s, 1, open["/permissions"] - 1) " \"allow\": [" add "]" (empty["/permissions"] ? " " : ",") substr(s, open["/permissions"])
      else s = substr(s, 1, open[""] - 1) "\n  \"permissions\": { \"allow\": [" add "] }" (empty[""] ? "\n" : ",") substr(s, open[""])
    }
  }
  printf "%s\n", s
  print changed > "/dev/stderr"
}`

// bridgeRules runs on the machine: $1 is add or remove, $2 the rules and $3
// allowAwk. The settings file stays on the machine (it can hold other
// servers' keys) and its mode is kept. It is written as pw writes it, under
// pw's own lock where flock is there, and not at all when it changed
// meanwhile.
const bridgeRules = `LC_ALL=C; export LC_ALL; umask 077
no() { printf 'why=%s\n' "$1"; exit 0; }
f=${XDG_CONFIG_HOME:-$HOME/.config}/pw/code.json
[ -f "$f" ] || { [ "$1" = remove ] && { echo changed=0; exit 0; }; no "pw code has no settings file there"; }
l=$f.lock
if [ -L "$f" ]; then f=$(readlink -f "$f") && [ -f "$f" ] || no "its settings file is a link the Studio cannot follow"; fi
[ -r "$f" ] && [ -w "$f" ] && [ -O "$f" ] || no "its settings file is not yours to change"
command -v awk >/dev/null 2>&1 || no "awk is not installed there"
if [ ! -L "$l" ] && command -v flock >/dev/null 2>&1; then exec 9>>"$l" && { flock -w 20 9 || no "pw code is busy with its settings file"; }; fi
t=$(mktemp "$f.XXXXXX") || no "its settings file cannot be written"
e=$(mktemp "$f.XXXXXX") || { rm -f "$t"; no "its settings file cannot be written"; }
trap 'rm -f "$t" "$e"' EXIT
trap 'exit 1' HUP INT TERM
was=$(cksum <"$f")
cp -p "$f" "$t" && awk -v act="$1" -v rules="$2" "$3" <"$f" >"$t" 2>"$e"
c=$?
w=$(tail -n 1 "$e")
if [ $c -ne 0 ]; then case $w in is\ *|holds\ *) no "its settings file $w";; *) no "its settings file could not be read";; esac; fi
case $w in ''|*[!0-9]*) no "its settings file could not be read";; esac
if [ "$w" -gt 0 ]; then
  [ "$(awk -v act="$1" -v rules="$2" "$3" <"$t" 2>&1 >/dev/null | tail -n 1)" = 0 ] || no "its settings file could not be changed safely"
  [ "$was" = "$(cksum <"$f")" ] || no "its settings file changed meanwhile; try again"
  mv -f "$t" "$f" || no "its settings file cannot be written"
fi
printf 'changed=%s\n' "$w"
`

// bridgeRulesRun adds rules on the machine, or takes them back, and answers
// how many changed, or why none did.
func bridgeRulesRun(ctx context.Context, run bridgeRunner, site, key, act string, rules []string) (int, string) {
	if len(rules) == 0 {
		return 0, ""
	}
	res, err := run(ctx, site, key, []string{"sh", "-c", bridgeRules, "studio-rules", act, strings.Join(rules, " "), allowAwk}, 90*time.Second)
	if err != nil {
		return 0, firstLine(apiMessage(err), 300)
	}
	return parseBridgeRules(res)
}

var bridgeRulesChanged = regexp.MustCompile(`(?m)^changed=([0-9]{1,4})\r?$`)

func parseBridgeRules(res remote.Result) (int, string) {
	if m := bridgeRulesChanged.FindStringSubmatch(res.Stdout); m != nil && res.Code == 0 {
		n, _ := strconv.Atoi(m[1])
		return n, ""
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if why, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "why="); ok && why != "" {
			return 0, firstLine(why, 300)
		}
	}
	return 0, firstNonEmpty(firstLine(strings.TrimSpace(res.Stderr), 300), "the machine did not answer")
}

// bridgeRemove is POST /api/mcp/bridge/remove {conversationId | machineId}:
// the Studio takes itself out of pw code on the machine, as its owner: the
// rules it allows there, then the entry, when that is this Studio's. It is
// not offered there again until the person chooses it.
func (m *Module) bridgeRemove(w http.ResponseWriter, r *http.Request) error {
	if bridgeCrossSite(r) {
		return pwErr(403, "Change this from this Studio's own pages.")
	}
	rb, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	url, why := m.bridgeReach(r)
	if why != "" {
		return pwErr(409, why)
	}
	var mach bridgeMach
	if id := rb.Str("conversationId"); id != "" {
		_, mach, err = m.bridgeChat(r, id)
	} else {
		mach, err = m.bridgeListed(r, rb.Str("machineId"), "")
	}
	if err != nil {
		return err
	}
	site, key, err := m.bridgeSite(r, mach)
	if err != nil {
		return err
	}
	done, err := m.bridgeClaim(r)
	if err != nil {
		return err
	}
	defer done()
	run, ctx, who, machine := m.bridgeRun(), r.Context(), userID(r), firstNonEmpty(mach.Name, mach.ID)
	look, err := m.bridgeLookAt(r, site, key, machine)
	if err != nil {
		return err
	}
	entry := false
	if look.entry != nil {
		at, _, _ := look.entry.target()
		if at != url {
			return pwErr(409, "The studio entry in pw code on "+machine+" is for "+at+", not this Studio, so it was left as it is.")
		}
		entry = true
	}
	rules, rulesWhy := bridgeRulesRun(ctx, run, site, key, "remove", machineRulesAll())
	if rulesWhy != "" {
		return pwErr(502, "The Studio could not take its rules back on "+machine+": "+rulesWhy+". Nothing was removed.")
	}
	if entry {
		res, err := run(ctx, site, key, []string{look.pw, "code", "mcp", "remove", "--scope", "user", studioMCPName}, 90*time.Second)
		if err != nil {
			return bridgeUnreached(machine, site, err)
		}
		if res.Code != 0 {
			return pwErr(502, "pw code on "+machine+" did not remove the entry: "+firstLine(strings.TrimSpace(res.Stderr+" "+res.Stdout), 300))
		}
	}
	m.bridgePrefs().set(who, mach.ID, bridgePref{Declined: true})
	m.A.Log.Info("mcp setup: the Studio's tools removed from pw code", "user", who, "machine", machine, "entry", entry, "rules", rules)
	return reqbody.OK(w, reqbody.O("removed", entry, "rules", rules, "machine", machine))
}
