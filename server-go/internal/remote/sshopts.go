// Package remote runs commands on other hosts for the Studio, as
// server/src/remoteExec.ts does: over `pw ssh <site>` to a cluster on the
// platform, acting with the viewer's own platform key, or over plain ssh
// to a site written ssh://[user@]host[:port][#options], with the keys,
// agent and ~/.ssh/config of the account the Studio runs as.
//
// Commands go over a few long-lived sessions per host (Pool): a small bash
// loop on the remote end reads one line per request, the command's argv
// with every argument base64-encoded, runs it without a shell, and answers
// one line with the exit status and the base64 of stdout and of stderr's
// tail. Over plain ssh the sessions share one connection (ControlMaster).
// A host whose login runs only gufi_query is marked direct, and each
// command there is its own ssh call.
package remote

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// SSHScheme starts a site reached over plain ssh.
const SSHScheme = "ssh://"

// SSHDestination is user@host, or an alias from ~/.ssh/config: nothing ssh
// would take as an option, and nothing a shell would interpret.
var SSHDestination = regexp.MustCompile(`^(?:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}@)?[A-Za-z0-9_][A-Za-z0-9_.-]{0,252}$`)

// SSHHost is a site over plain ssh, parsed.
type SSHHost struct {
	Destination string
	Port        int // 0: ssh's default
	Options     []string
}

var sshOptionKeys = map[string]string{
	"addressfamily": "word", "canonicaldomains": "list", "canonicalizefallbacklocal": "word", "canonicalizehostname": "word",
	"canonicalizemaxdots": "number", "certificatefile": "path", "checkhostip": "word", "ciphers": "list", "compression": "word",
	"connectionattempts": "number", "connecttimeout": "number", "gssapiauthentication": "word", "gssapidelegatecredentials": "word",
	"hostkeyalgorithms": "list", "hostkeyalias": "host", "hostname": "host", "identitiesonly": "word", "identityagent": "path",
	"identityfile": "path", "ipqos": "list", "kexalgorithms": "list", "loglevel": "word", "macs": "list", "port": "port",
	"preferredauthentications": "list", "proxyjump": "jump", "pubkeyacceptedalgorithms": "list", "pubkeyacceptedkeytypes": "list",
	"pubkeyauthentication": "word", "rekeylimit": "list", "serveralivecountmax": "number", "serveraliveinterval": "number",
	"stricthostkeychecking": "word", "tcpkeepalive": "word", "updatehostkeys": "word", "user": "login",
	"userknownhostsfile": "path", "verifyhostkeydns": "word",
}

var sshOptionRefused = map[string]string{
	"proxycommand":       "ProxyCommand runs a local command: use -J (ProxyJump), or put it in a Host entry in ~/.ssh/config and give that alias as the host",
	"localcommand":       "LocalCommand runs a local command",
	"permitlocalcommand": "PermitLocalCommand runs a local command",
	"knownhostscommand":  "KnownHostsCommand runs a local command",
	"remotecommand":      "RemoteCommand replaces the command the Studio runs",
	"batchmode":          "the Studio always runs ssh without prompts (BatchMode=yes)",
	"controlmaster":      "the Studio shares connections itself",
	"controlpath":        "the Studio shares connections itself",
	"controlpersist":     "the Studio shares connections itself",
}

const maxOptionsText = 500

var (
	loginRe     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)
	hostnameRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,252}$`)
	localPathRe = regexp.MustCompile("^(?:/|~/)[^\\s'\"`$\\\\;&|<>(){}*?!\\x00-\\x1f\\x7f]*$")
	jumpRe      = regexp.MustCompile(`^(?:([^@]+)@)?([^:@]+)(?::(\d{1,5}))?$`)
	portDigits  = regexp.MustCompile(`^\d{1,5}$`)
	quoteChars  = regexp.MustCompile("['\"`\\\\]")
	flagRe      = regexp.MustCompile(`^-([JpilFo])(.*)$`)
	kvRe        = regexp.MustCompile(`^([A-Za-z0-9]+)=(.*)$`)
	numberRe    = regexp.MustCompile(`^\d{1,6}$`)
	wordRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	listRe      = regexp.MustCompile(`^[A-Za-z0-9_@.,:+^/=%-]{1,300}$`)
	pathSpecial = regexp.MustCompile(`^(?:none|SSH_AUTH_SOCK)$`)
	jsSpaces    = regexp.MustCompile(`[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+`)
)

// localPath is LOCAL_PATH: at most 1,024 characters (Go's regexp caps a
// repeat count at 1,000).
func localPath(s string) bool { return len([]rune(s)) <= 1024 && localPathRe.MatchString(s) }

func isPort(s string) bool {
	if !portDigits.MatchString(s) {
		return false
	}
	n, _ := strconv.Atoi(s)
	return n >= 1 && n <= 65535
}

func jumpHost(s string) bool {
	m := jumpRe.FindStringSubmatch(s)
	return m != nil && (m[1] == "" || loginRe.MatchString(m[1])) && hostnameRe.MatchString(m[2]) && (m[3] == "" || isPort(m[3]))
}

func jumpList(s string) bool {
	for _, p := range strings.Split(s, ",") {
		if !jumpHost(p) {
			return false
		}
	}
	return true
}

func optionValue(kind, value string) bool {
	switch kind {
	case "jump":
		return jumpList(value)
	case "host":
		return hostnameRe.MatchString(value)
	case "port":
		return isPort(value)
	case "login":
		return loginRe.MatchString(value)
	case "path":
		return localPath(value) || pathSpecial.MatchString(value)
	case "number":
		return numberRe.MatchString(value)
	case "word":
		return wordRe.MatchString(value)
	}
	return listRe.MatchString(value)
}

func bad400(msg string) error { return apierr.New(400, "%s", msg) }

// ParseSSHOptions is parseSshOptions: the options a library's ssh runs
// with, as argv, from what the person typed. A 400 says what was refused.
func ParseSSHOptions(text string) ([]string, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return []string{}, nil
	}
	if len([]rune(s)) > maxOptionsText {
		return nil, bad400(fmt.Sprintf("ssh options: at most %d characters", maxOptionsText))
	}
	if quoteChars.MatchString(s) {
		return nil, bad400("ssh options: no quotes or backslashes; a value with a space belongs in a Host entry in ~/.ssh/config")
	}
	return CheckSSHOptions(jsSpaces.Split(s, -1))
}

// CheckSSHOptions is checkSshOptions: the same check on options already split.
func CheckSSHOptions(words []string) ([]string, error) {
	out := []string{}
	bad := func(msg string) ([]string, error) { return nil, bad400("ssh options: " + msg) }
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "-4" || w == "-6" || w == "-C" {
			out = append(out, w)
			continue
		}
		m := flagRe.FindStringSubmatch(w)
		if m == nil {
			return bad(w + " is not one of -J, -p, -i, -l, -F, -o, -4, -6 or -C")
		}
		flag, value := m[1], m[2]
		if value == "" {
			i++
			if i < len(words) {
				value = words[i]
			}
		}
		if value == "" {
			return bad("-" + flag + " needs a value")
		}
		switch flag {
		case "J":
			if !jumpList(value) {
				return bad("-J " + value + ": give [user@]host[:port], several separated by commas")
			}
		case "p":
			if !isPort(value) {
				return bad("-p " + value + ": a port from 1 to 65535")
			}
		case "i", "F":
			if !localPath(value) {
				return bad("-" + flag + " " + value + ": an absolute path on this computer, or one under ~/")
			}
		case "l":
			if !loginRe.MatchString(value) {
				return bad("-l " + value + ": a login name")
			}
		case "o":
			kv := kvRe.FindStringSubmatch(value)
			if kv == nil {
				return bad("-o " + value + ": write it as Keyword=value")
			}
			key := strings.ToLower(kv[1])
			if why, ok := sshOptionRefused[key]; ok {
				return bad(kv[1] + " is not allowed here: " + why)
			}
			kind, ok := sshOptionKeys[key]
			if !ok {
				return bad(kv[1] + " is not allowed here; a Host entry in ~/.ssh/config can set it")
			}
			if !optionValue(kind, kv[2]) {
				return bad("-o " + value + ": not a valid value for " + kv[1])
			}
		}
		out = append(out, "-"+flag, value)
	}
	return out, nil
}

// OptionsText is sshOptionsText: argv joined with spaces.
func OptionsText(argv []string) string { return strings.Join(argv, " ") }

var proxyJumpKey = regexp.MustCompile(`(?i)^proxyjump=`)
var portKey = regexp.MustCompile(`(?i)^port=`)

// JumpOf is jumpOf: the jump hosts the options name, or "".
func JumpOf(options []string) string {
	for i := 0; i < len(options); i++ {
		if options[i] == "-J" && i+1 < len(options) {
			return options[i+1]
		}
		if options[i] == "-o" && i+1 < len(options) && proxyJumpKey.MatchString(options[i+1]) {
			return options[i+1][strings.Index(options[i+1], "=")+1:]
		}
	}
	return ""
}

func portIn(options []string) bool {
	for i, o := range options {
		if o == "-p" || (i > 0 && options[i-1] == "-o" && portKey.MatchString(o)) {
			return true
		}
	}
	return false
}

// SSHSite is sshSite: ssh://<destination>[:<port>], with the options after
// a #, base64url of their argv as JSON.
func SSHSite(destination string, port int, options []string) (string, error) {
	if !SSHDestination.MatchString(destination) {
		return "", bad400("invalid ssh destination")
	}
	if port != 0 && (port < 1 || port > 65535) {
		return "", bad400("invalid ssh port")
	}
	opts, err := CheckSSHOptions(options)
	if err != nil {
		return "", err
	}
	if port != 0 && portIn(opts) {
		return "", bad400("ssh options: give the port once, in the port field or in the options")
	}
	frag := ""
	if len(opts) > 0 {
		j, _ := json.Marshal(opts)
		frag = "#" + base64.RawURLEncoding.EncodeToString(jsonCompat(j))
	}
	p := ""
	if port != 0 {
		p = ":" + strconv.Itoa(port)
	}
	return SSHScheme + destination + p + frag, nil
}

// jsonCompat undoes Go's HTML escaping, so the JSON is JavaScript's.
func jsonCompat(b []byte) []byte {
	s := string(b)
	s = strings.ReplaceAll(s, `<`, "<")
	s = strings.ReplaceAll(s, `>`, ">")
	s = strings.ReplaceAll(s, `&`, "&")
	return []byte(s)
}

var hostPortRe = regexp.MustCompile(`^(.*?)(?::(\d{1,5}))?$`)

// ParseSSHSite is parseSshSite: the host a site names, or nil for a cluster.
func ParseSSHSite(site string) (*SSHHost, error) {
	if !strings.HasPrefix(site, SSHScheme) {
		return nil, nil
	}
	rest := site[len(SSHScheme):]
	hash := strings.Index(rest, "#")
	hostPart := rest
	if hash >= 0 {
		hostPart = rest[:hash]
	}
	m := hostPortRe.FindStringSubmatch(hostPart)
	port := 0
	if m[2] != "" {
		port, _ = strconv.Atoi(m[2])
	}
	if !SSHDestination.MatchString(m[1]) || (m[2] != "" && (port < 1 || port > 65535)) {
		return nil, bad400("invalid ssh site")
	}
	options := []string{}
	if hash >= 0 {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(rest[hash+1:], "="))
		var parsed []string
		if err != nil || json.Unmarshal(raw, &parsed) != nil {
			return nil, bad400("invalid ssh site")
		}
		if options, err = CheckSSHOptions(parsed); err != nil {
			return nil, err
		}
	}
	return &SSHHost{Destination: m[1], Port: port, Options: options}, nil
}

// SiteName is siteName: the destination (and port) of a site over ssh,
// without its options; a cluster's name as it is.
func SiteName(site string) string {
	if !strings.HasPrefix(site, SSHScheme) {
		return site
	}
	rest := site[len(SSHScheme):]
	if i := strings.Index(rest, "#"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// IsSSH reports whether a site is reached over plain ssh.
func IsSSH(site string) bool { return strings.HasPrefix(site, SSHScheme) }

// ShellFor is shellFor: how a person reaches a site from a terminal.
func ShellFor(site string) []string {
	h, err := ParseSSHSite(site)
	if err != nil || h == nil {
		return []string{"pw", "ssh", site}
	}
	out := append([]string{"ssh"}, h.Options...)
	if h.Port != 0 {
		out = append(out, "-p", strconv.Itoa(h.Port))
	}
	return append(out, h.Destination)
}

// OptionsTag is optionsTag: a library's own options as a short hash, ""
// without any. Each -o keyword is put in lower case first.
func OptionsTag(options []string) string {
	if len(options) == 0 {
		return ""
	}
	norm := make([]string, len(options))
	for i, o := range options {
		if i > 0 && options[i-1] == "-o" {
			k := strings.Index(o, "=")
			if k < 0 {
				k = len(o)
			}
			o = strings.ToLower(o[:k]) + o[k:]
		}
		norm[i] = o
	}
	sum := sha1.Sum([]byte(strings.Join(norm, "\x00")))
	return hex.EncodeToString(sum[:])[:optionsTagLen]
}

// ShellWord is shellWord: one argument as a POSIX shell reads it back.
func ShellWord(a string) string {
	if shellPlain.MatchString(a) {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

var shellPlain = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// DirectCommand is directCommand: the command line a direct call sends.
func DirectCommand(argv []string) string {
	w := make([]string, len(argv))
	for i, a := range argv {
		w[i] = ShellWord(a)
	}
	return strings.Join(w, " ")
}

var quietWarn = regexp.MustCompile(`^\*\* `)

// QuietStderr is quietStderr: ssh's notices that are not errors removed.
func QuietStderr(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" || quietWarn.MatchString(l) || strings.HasPrefix(l, "Warning: Permanently added ") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}
