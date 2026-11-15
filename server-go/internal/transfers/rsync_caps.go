package transfers

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// rsyncCaps is what one rsync program offers, read from its --version and
// --help output and from one probe (--trust-sender --version). The two ends
// of a transfer often run different rsync releases: this computer may have
// macOS's rsync 2.6.9 or openrsync while a cluster has 3.1.3, or the other
// way round. rsync negotiates its protocol, but not its options, so each
// option Studio passes is chosen from the capabilities of the end or ends
// that must understand it (see rsyncDecide).
type rsyncCaps struct {
	ver       [3]int
	protocol  int
	openrsync bool
	long      map[string]bool
	short     map[byte]bool
	// trust: the program accepts --trust-sender, though its --help may not
	// list it (RHEL 8's backport adds the option without the help line).
	trust bool
}

var (
	rsyncProtoRe   = regexp.MustCompile(`(?i)protocol\s+version\s+(\d+)`)
	rsyncLongRe    = regexp.MustCompile(`(?:^|[\s,\[|(])--([a-z0-9][a-z0-9-]*)`)
	rsyncShortRe   = regexp.MustCompile(`(?:^|[\s,\[|(])-([0-9A-Za-z])(?:[\s,=\]|)]|$)`)
	rsyncClusterRe = regexp.MustCompile(`\[-([0-9A-Za-z]{2,})\]`)
	rsyncSpecEnd   = regexp.MustCompile(`\s{2,}|\t`)
)

// parseRsyncVersionText is the version in rsync --version's output;
// openrsync says it is compatible with 2.6.9 and is taken as that.
func parseRsyncVersionText(text string) ([3]int, bool, bool) {
	if strings.Contains(strings.ToLower(text), "openrsync") {
		return [3]int{2, 6, 9}, true, true
	}
	v, ok := parseRsyncVersion(text)
	return v, false, ok
}

// parseRsyncCaps reads a program's --version and --help output. help may
// be GNU rsync's option list (one option per line, then its description
// after two or more spaces) or openrsync's usage synopsis (options in
// brackets, short ones run together as [-0468B...]). Only the option
// column is read, so an option named in another's description does not
// count.
func parseRsyncCaps(version, help string, trust bool) (rsyncCaps, bool) {
	v, open, ok := parseRsyncVersionText(version)
	if !ok {
		return rsyncCaps{}, false
	}
	c := rsyncCaps{ver: v, openrsync: open, long: map[string]bool{}, short: map[byte]bool{}, trust: trust}
	if x := rsyncProtoRe.FindStringSubmatch(version); x != nil {
		c.protocol, _ = strconv.Atoi(x[1])
	}
	for _, line := range strings.Split(help, "\n") {
		spec := strings.TrimSpace(line)
		if spec == "" {
			continue
		}
		// A synopsis line is read whole; an option line up to its
		// description.
		if !strings.HasPrefix(spec, "[") && !strings.Contains(strings.ToLower(spec), "usage") {
			if loc := rsyncSpecEnd.FindStringIndex(spec); loc != nil {
				spec = spec[:loc[0]]
			}
		}
		for _, m := range rsyncLongRe.FindAllStringSubmatch(spec, -1) {
			c.long[m[1]] = true
		}
		for _, m := range rsyncShortRe.FindAllStringSubmatch(spec, -1) {
			c.short[m[1][0]] = true
		}
		for _, m := range rsyncClusterRe.FindAllStringSubmatch(spec, -1) {
			for i := 0; i < len(m[1]); i++ {
				c.short[m[1][i]] = true
			}
		}
	}
	return c, true
}

func (c rsyncCaps) has(opt string) bool { return c.long[opt] }

// protectArgs: -s sends the arguments over the protocol, past the remote
// shell. GNU rsync 3.0 calls it --protect-args, 3.3 --secluded-args; both
// take -s. openrsync has neither.
func (c rsyncCaps) protectArgs() bool { return c.has("protect-args") || c.has("secluded-args") }

// incRecursive: the program has incremental recursion and so
// --no-inc-recursive (GNU rsync 3.0, protocol 30). --help does not list
// that option, so it is read from the protocol.
func (c rsyncCaps) incRecursive() bool { return !c.openrsync && c.protocol >= 30 }

// from0 is how to ask for a NUL separated list: --from0, or -0 where only
// the short form is listed; "" when there is neither.
func (c rsyncCaps) from0() string {
	switch {
	case c.has("from0"):
		return "--from0"
	case c.short['0']:
		return "-0"
	}
	return ""
}

// escapesArgs: GNU rsync 3.2.4 and later backslash-escapes the remote
// file arguments it hands the remote shell itself (and offers --old-args
// to turn that off).
func (c rsyncCaps) escapesArgs() bool { return c.has("old-args") }

// checksSender: as a client receiving from a host, the program refuses
// names the sender adds to the list it was asked for (CVE-2022-29154,
// fixed upstream in 3.2.5 together with --trust-sender). A distribution
// build of an older release that carries the fix is known by that option;
// one whose backport left the option out is refused.
func (c rsyncCaps) checksSender() bool {
	return (!c.openrsync && atLeast(c.ver, [3]int{3, 2, 5})) || c.trust || c.has("trust-sender")
}

// lacks is the first of the options Studio needs that the program does not
// list, as rsync spells it; "" when it has them all.
func (c rsyncCaps) lacks(opts ...string) string {
	for _, o := range opts {
		switch o {
		case "from0":
			if c.from0() == "" {
				return "--from0"
			}
		default:
			if !c.has(o) {
				return "--" + o
			}
		}
	}
	return ""
}

// name is the program for messages: "rsync 3.1.3", or "openrsync (rsync
// 2.6.9 compatible)".
func (c rsyncCaps) name() string {
	v := fmt.Sprintf("%d.%d.%d", c.ver[0], c.ver[1], c.ver[2])
	if c.openrsync {
		return "openrsync (rsync " + v + " compatible)"
	}
	return "rsync " + v
}

func (c rsyncCaps) version() string {
	return fmt.Sprintf("%d.%d.%d", c.ver[0], c.ver[1], c.ver[2])
}

// better says whether c is the better rsync to run here than b: one that
// can pull safely, then one with protect args, then the newer.
func (c rsyncCaps) better(b rsyncCaps) bool {
	if x, y := c.lacks(rsyncNeeds...) == "", b.lacks(rsyncNeeds...) == ""; x != y {
		return x
	}
	if c.checksSender() != b.checksSender() {
		return c.checksSender()
	}
	if c.protectArgs() != b.protectArgs() {
		return c.protectArgs()
	}
	if c.ver != b.ver {
		return atLeast(c.ver, b.ver)
	}
	return false
}

// rsyncNeeds are the options the rsync that runs a transfer (the client)
// must have: the file list, read NUL separated, and --chmod, which keeps
// the staging folder writable by Studio.
var rsyncNeeds = []string{"files-from", "from0", "chmod"}

// pullNeeds are the options the sending host's rsync must have in a pull:
// the client passes it --files-from=- and --from0 and sends the list over
// the connection.
var pullNeeds = []string{"files-from", "from0"}

// Messages for what the rsync found is used for.
const pullNeedsFix = "needs rsync 3.2.5 or a build with the CVE-2022-29154 fix"

// localUse says what this computer's rsync is used for.
func localUse(c rsyncCaps, path string) string {
	head := c.name() + " at " + path + ": "
	if o := c.lacks(rsyncNeeds...); o != "" {
		return head + "not used, it has no " + o + "; Studio copy carries transfers"
	}
	if c.checksSender() {
		return head + "used on this computer, for sends to hosts and for pulls from hosts"
	}
	return head + "used on this computer and for sends to hosts; pulls use Studio copy (" + pullNeedsFix + ")"
}

// hostUse says what a host's rsync is used for.
func hostUse(c rsyncCaps) string {
	head := c.name() + " there: "
	if o := c.lacks(pullNeeds...); o != "" {
		return head + "not used, it has no " + o + "; Studio copy carries transfers"
	}
	if o := c.lacks(rsyncNeeds...); o != "" {
		return head + "used for sends and pulls with this computer; copies on that host use Studio copy, it has no " + o
	}
	return head + "used for sends and pulls with this computer and for copies on that host"
}
