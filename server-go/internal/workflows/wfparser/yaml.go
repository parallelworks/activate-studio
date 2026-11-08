// Package wfparser is the part of @parallelworks/workflow-parser the server
// uses: the input form conversion, the values a run is submitted with, step
// labels, job dependencies and the YAML dump.
package wfparser

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// DumpYAML is dumpYaml: js-yaml 5's dump(data, {lineWidth: -1}) with null
// scalars written empty. It reproduces js-yaml's block style for the values
// JSON and YAML decoding give (objects, arrays, strings, numbers, booleans,
// null); the golden test checks it against Node. Values shared between two
// places are written twice where js-yaml writes an anchor and an alias, as
// decoded documents never share them.
func DumpYAML(v any) string {
	if v == jsonv.Undefined {
		return ""
	}
	d := &dumper{}
	out := d.node(0, v, kindNone, true, true, false) + "\n"
	if d.openEnded {
		out += "...\n"
	}
	return out
}

type parentKind int

const (
	kindNone parentKind = iota
	kindMap
	kindSeq
)

type dumper struct {
	openEnded bool
}

const indent = 2

func nextLine(level int) string { return "\n" + strings.Repeat(" ", indent*level) }

// node is writeNode for a node of the AST jsToAst builds.
func (d *dumper) node(level int, v any, parent parentKind, block, compact, isKey bool) string {
	switch x := v.(type) {
	case *jsonv.Object:
		pairs := pairsOf(x)
		if block && len(pairs) > 0 {
			return d.blockMapping(level, pairs, compact)
		}
		d.openEnded = false
		return d.flowMapping(level, pairs)
	case []any:
		if block && len(x) > 0 {
			return d.blockSequence(level, x, compact)
		}
		d.openEnded = false
		return d.flowSequence(level, x)
	}
	s, tag := represent(v)
	return d.scalar(level, s, tag, block, isKey)
}

type pair struct {
	k string
	v any
}

func pairsOf(o *jsonv.Object) []pair {
	var out []pair
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		if v == jsonv.Undefined {
			continue
		}
		out = append(out, pair{k, v})
	}
	return out
}

func (d *dumper) flowSequence(level int, items []any) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.node(level, seqItem(it), kindSeq, false, false, false))
	}
	return "[" + b.String() + "]"
}

func (d *dumper) flowMapping(level int, pairs []pair) string {
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(", ")
		}
		k := d.node(level, p.k, kindMap, false, false, true)
		val := d.node(level, p.v, kindMap, false, false, false)
		sep := " "
		if val == "" {
			sep = ""
		}
		b.WriteString(k + ":" + sep + val)
	}
	return "{" + b.String() + "}"
}

// seqItem is build's rule for a hole in an array: undefined is null.
func seqItem(v any) any {
	if v == jsonv.Undefined {
		return nil
	}
	return v
}

func (d *dumper) blockSequence(level int, items []any, compact bool) string {
	var b strings.Builder
	for _, it := range items {
		item := d.node(level+1, seqItem(it), kindSeq, true, true, false)
		if !compact || b.Len() > 0 {
			b.WriteString(nextLine(level))
		}
		if item == "" || item[0] == '\n' {
			b.WriteString("-")
		} else {
			b.WriteString("- ")
		}
		b.WriteString(item)
	}
	return b.String()
}

func (d *dumper) blockMapping(level int, pairs []pair, compact bool) string {
	var b strings.Builder
	for _, p := range pairs {
		var pb strings.Builder
		if !compact || b.Len() > 0 {
			pb.WriteString(nextLine(level))
		}
		key := d.node(level+1, p.k, kindMap, true, true, true)
		explicit := strings.Contains(p.k, "\n") || utf8.RuneCountInString(key) > 1024
		if explicit {
			if key != "" && key[0] == '\n' {
				pb.WriteString("?")
			} else {
				pb.WriteString("? ")
			}
		}
		pb.WriteString(key)
		if explicit {
			pb.WriteString(nextLine(level))
		}
		val := d.node(level+1, p.v, kindMap, true, explicit, false)
		if val == "" || val[0] == '\n' {
			pb.WriteString(":")
		} else {
			pb.WriteString(": ")
		}
		pb.WriteString(val)
		b.WriteString(pb.String())
	}
	return b.String()
}

/* ---- scalars ---- */

type tag int

const (
	tagStr tag = iota
	tagNull
	tagBool
	tagInt
	tagFloat
	tagOther // timestamp, merge: what a string must not read as
)

// represent is the DUMP_SCHEMA tag's represent for a JSON value; null is
// written empty by dumpYaml's transform.
func represent(v any) (string, tag) {
	switch x := v.(type) {
	case nil:
		return "", tagNull
	case bool:
		if x {
			return "true", tagBool
		}
		return "false", tagBool
	case float64:
		return representNumber(x)
	case int:
		return strconv.Itoa(x), tagInt
	case string:
		return x, tagStr
	}
	return jsonv.String(v), tagStr
}

var expNoDot = regexp.MustCompile(`^[-+]?[0-9]+e`)

func representNumber(f float64) (string, tag) {
	switch {
	case math.IsNaN(f):
		return ".nan", tagFloat
	case math.IsInf(f, 1):
		return ".inf", tagFloat
	case math.IsInf(f, -1):
		return "-.inf", tagFloat
	case f == 0 && math.Signbit(f):
		return "-0.0", tagFloat
	}
	s := jsonv.FormatNumber(f)
	if f == math.Trunc(f) && !strings.Contains(s, "e") {
		return s, tagInt
	}
	if expNoDot.MatchString(s) {
		s = strings.Replace(s, "e", ".e", 1)
	}
	return s, tagFloat
}

type style int

const (
	plain style = iota
	singleQuoted
	doubleQuoted
	literal
)

var (
	invisibles  = regexp.MustCompile(`[\t\x{7F}-\x{A0}\x{2028}\x{2029}\x{FEFF}\x{FFFE}\x{FFFF}]`)
	wsOnly      = regexp.MustCompile(`^[\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]+$`)
	forbidFirst = regexp.MustCompile(`^(?:---|\.\.\.)(?:$|[ \t\n\r])`)
)

// scalar is the scalar branch of writeNode with the default style rules.
func (d *dumper) scalar(level int, s string, t tag, block, isKey bool) string {
	flowOnly := !block
	shiftOfParent := indent * (level - 1)
	if level == 0 {
		shiftOfParent = -1
	}
	shiftOfContent := indent * max(1, level)
	shiftOfFirstLine := indent * level

	canPlain := canUsePlain(s, t, isKey, shiftOfFirstLine)
	canSingle := canUseSingle(s, isKey)
	canBlock := !flowOnly && blockContent(s) && shiftOfContent-shiftOfParent >= 1

	st := plain
	if invisibles.MatchString(s) {
		st = doubleQuoted
	}
	if st == plain && wsOnly.MatchString(s) {
		st = doubleQuoted
	}
	multiline := strings.Contains(s, "\n")
	if st == plain && !isKey && multiline {
		if canBlock {
			st = literal
		} else {
			st = doubleQuoted
		}
	}
	if st == plain && !canPlain {
		if canSingle {
			st = singleQuoted
		} else {
			st = doubleQuoted
		}
	}
	if st == singleQuoted && !canSingle || st == literal && !canBlock {
		st = doubleQuoted
	}

	var body string
	switch st {
	case plain:
		body = s
	case singleQuoted:
		body = "'" + strings.ReplaceAll(s, "'", "''") + "'"
	case doubleQuoted:
		body = `"` + escapeString(s) + `"`
	case literal:
		body = "|" + blockHeader(s, shiftOfParent, shiftOfContent) + dropEndingNewline(indentString(s, shiftOfContent))
	}
	d.openEnded = st == literal && (s == "\n" || strings.HasSuffix(s, "\n\n"))
	if st != plain && t != tagStr {
		// A non-string written quoted carries its tag (never for JSON values).
		body = "!!" + tagName(t) + " " + body
	}
	return body
}

func tagName(t tag) string {
	switch t {
	case tagNull:
		return "null"
	case tagBool:
		return "bool"
	case tagInt:
		return "int"
	case tagFloat:
		return "float"
	}
	return "str"
}

func isWhite(r rune) bool { return r == ' ' || r == '\t' }

// isPrintable is c-printable.
func isPrintable(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D || r >= 0x20 && r <= 0x7E || r == 0x85 ||
		r >= 0xA0 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF
}

// isNBChar is nb-char: printable, not a break or the byte order mark.
func isNBChar(r rune) bool { return isPrintable(r) && r != '\n' && r != '\r' && r != 0xFEFF }

// isNSChar is ns-char.
func isNSChar(r rune) bool { return isNBChar(r) && !isWhite(r) }

func isIndicator(r rune) bool { return strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", r) }

// plainOneLine is NS_PLAIN_BLOCK_KEY (ns-plain-one-line in flow-out): the
// multi-line forms only matter for strings written as block scalars.
func plainOneLine(rs []rune) bool {
	n := len(rs)
	if n == 0 {
		return false
	}
	for _, r := range rs {
		if !isNSChar(r) && !isWhite(r) {
			return false
		}
	}
	first := rs[0]
	if !isNSChar(first) {
		return false
	}
	if isIndicator(first) {
		if !strings.ContainsRune("?:-", first) || n < 2 || !isNSChar(rs[1]) {
			return false
		}
	}
	if isWhite(rs[n-1]) {
		return false
	}
	for i := 1; i < n; i++ {
		switch rs[i] {
		case '#':
			if isWhite(rs[i-1]) {
				return false
			}
		case ':':
			if i+1 >= n || !isNSChar(rs[i+1]) {
				return false
			}
		}
	}
	return true
}

// plainMultiLine is NS_PLAIN_FLOW_OUT: one-line plains joined by line
// breaks, each later line starting with a plain char.
func plainMultiLine(s string) bool {
	lines := strings.Split(s, "\n")
	if !plainOneLine([]rune(lines[0])) {
		return false
	}
	for _, l := range lines[1:] {
		if l == "" {
			continue // \n+
		}
		rs := []rune(l)
		// The line continues the plain: its first char is a plain char.
		if rs[0] == '#' || isWhite(rs[0]) || !isNSChar(rs[0]) {
			return false
		}
		if rs[0] == ':' && (len(rs) < 2 || !isNSChar(rs[1])) {
			return false
		}
		if !plainOneLine(append([]rune{'a'}, rs[1:]...)) {
			return false
		}
	}
	return !strings.HasSuffix(s, "\n")
}

func canUsePlain(s string, t tag, isKey bool, shiftOfFirstLine int) bool {
	if s != "" {
		ok := false
		if isKey {
			ok = plainOneLine([]rune(s))
		} else {
			ok = plainMultiLine(s)
		}
		if !ok {
			return false
		}
		if shiftOfFirstLine == 0 && forbidFirst.MatchString(s) {
			return false
		}
	}
	r := resolve(s)
	if r != t {
		return false
	}
	if s == "=" && r == tagStr {
		return false
	}
	return true
}

func canUseSingle(s string, isKey bool) bool {
	for _, r := range s {
		if r == utf8.RuneError {
			return false
		}
		if r == '\n' {
			if isKey {
				return false
			}
			continue
		}
		if !(r == 0x09 || r >= 0x20 && r <= 0xD7FF || r >= 0xE000) {
			return false
		}
	}
	if strings.Contains(s, " \n") || strings.Contains(s, "\t\n") || strings.Contains(s, "\n ") || strings.Contains(s, "\n\t") {
		return false
	}
	return true
}

func blockContent(s string) bool {
	for _, r := range s {
		if r != '\n' && !isNBChar(r) {
			return false
		}
	}
	return true
}

func blockHeader(s string, shiftOfParent, shiftOfContent int) string {
	ind := ""
	if strings.HasPrefix(strings.TrimLeft(s, "\n"), " ") {
		ind = strconv.Itoa(shiftOfContent - shiftOfParent)
	}
	chomp := "-"
	if strings.HasSuffix(s, "\n") {
		chomp = ""
		if strings.HasSuffix(s, "\n\n") || s == "\n" {
			chomp = "+"
		}
	}
	return ind + chomp + "\n"
}

func dropEndingNewline(s string) string { return strings.TrimSuffix(s, "\n") }

func indentString(s string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	var b strings.Builder
	for len(s) > 0 {
		var line string
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line, s = s[:i+1], s[i+1:]
		} else {
			line, s = s, ""
		}
		if line != "" && line != "\n" {
			b.WriteString(pad)
		}
		b.WriteString(line)
	}
	return b.String()
}

func escapeString(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case 0:
			b.WriteString(`\0`)
		case 7:
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\v':
			b.WriteString(`\v`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		case 0x1B:
			b.WriteString(`\e`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case 0x85:
			b.WriteString(`\N`)
		case 0xA0:
			b.WriteString(`\_`)
		case 0x2028:
			b.WriteString(`\L`)
		case 0x2029:
			b.WriteString(`\P`)
		default:
			if r < 0x20 || r >= 0x7F && r <= 0xA0 || r == 0xFEFF || r == 0xFFFE || r == 0xFFFF || r == utf8.RuneError {
				h := strings.ToUpper(strconv.FormatInt(int64(r), 16))
				if r <= 0xFF {
					b.WriteString(`\x` + strings.Repeat("0", 2-len(h)) + h)
				} else {
					b.WriteString(`\u` + strings.Repeat("0", 4-len(h)) + h)
				}
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

/* ---- what a plain scalar reads back as (DUMP_SCHEMA's implicit tags) ---- */

var (
	nullValues  = map[string]bool{"": true, "~": true, "null": true, "Null": true, "NULL": true}
	boolValues  = map[string]bool{"true": true, "True": true, "TRUE": true, "y": true, "Y": true, "yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true, "false": true, "False": true, "FALSE": true, "n": true, "N": true, "no": true, "No": true, "NO": true, "off": true, "Off": true, "OFF": true}
	intYaml11   = regexp.MustCompile(`^(?:[-+]?0b[0-1_]+|[-+]?0[0-7_]+|[-+]?0x[0-9a-fA-F_]+|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+|[-+]?(?:0|[1-9][0-9_]*))$`)
	intCore     = regexp.MustCompile(`^(?:0o[0-7]+|0x[0-9a-fA-F]+|[-+]?[0-9]+)$`)
	floatYaml11 = regexp.MustCompile(`^(?:[-+]?(?:(?:[0-9][0-9_]*)?\.[0-9_]*)(?:[eE][-+][0-9]+)?|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	floatCore   = regexp.MustCompile(`^(?:[-+]?[0-9]+(?:\.[0-9]*)?(?:[eE][-+]?[0-9]+)?|[-+]?\.[0-9]+(?:[eE][-+]?[0-9]+)?|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	floatSpec   = regexp.MustCompile(`^(?:[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	dateRe      = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})$`)
	stampRe     = regexp.MustCompile(`^([0-9]{4})-([0-9][0-9]?)-([0-9][0-9]?)(?:[Tt]|[ \t]+)([0-9][0-9]?):([0-9]{2}):([0-9]{2})(?:\.([0-9]*))?(?:[ \t]*(Z|([-+])([0-9][0-9]?)(?::([0-9]{2}))?))?$`)
)

// resolve is resolveImplicitScalarTag's tag for a plain scalar's text.
func resolve(s string) tag {
	if nullValues[s] {
		return tagNull
	}
	if boolValues[s] {
		return tagBool
	}
	if s != "" && strings.ContainsRune("-+0123456789", rune(s[0])) {
		if intYaml11.MatchString(s) && finiteInt(s, true) || intCore.MatchString(s) && finiteInt(s, false) {
			return tagInt
		}
	}
	if s != "" && strings.ContainsRune("-+.0123456789", rune(s[0])) {
		if floatYaml11.MatchString(s) && (floatSpec.MatchString(s) || finiteFloat(strings.ReplaceAll(s, "_", ""))) ||
			floatCore.MatchString(s) && (floatSpec.MatchString(s) || finiteFloat(s)) {
			return tagFloat
		}
	}
	if s != "" && s[0] >= '0' && s[0] <= '9' && timestamp(s) {
		return tagOther
	}
	if s == "<<" {
		return tagOther
	}
	return tagStr
}

// finiteInt reports whether parseInt of the matched form is finite: only
// a decimal of more than about 308 digits is not.
func finiteInt(s string, yaml11 bool) bool {
	v := strings.TrimLeft(s, "+-")
	if yaml11 {
		v = strings.ReplaceAll(v, "_", "")
	}
	if strings.HasPrefix(v, "0x") || strings.HasPrefix(v, "0b") || strings.HasPrefix(v, "0o") || strings.Contains(v, ":") {
		v = v[2:]
		return len(v) < 250 || !strings.HasPrefix(s, "0x")
	}
	f, err := strconv.ParseFloat(v, 64)
	return err == nil && !math.IsInf(f, 0) || len(v) < 300
}

func finiteFloat(s string) bool {
	v := strings.ToLower(strings.TrimLeft(s, "+-"))
	if i := strings.IndexByte(v, ':'); i >= 0 {
		return true
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(v, "."), 64)
	if err != nil {
		// parseFloat reads the longest numeric prefix ("1.e5" reads 1).
		return true
	}
	return !math.IsInf(f, 0)
}

func timestamp(s string) bool {
	if m := dateRe.FindStringSubmatch(s); m != nil {
		return validDate(m[1], m[2], m[3])
	}
	m := stampRe.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	h, _ := strconv.Atoi(m[4])
	mi, _ := strconv.Atoi(m[5])
	se, _ := strconv.Atoi(m[6])
	if h > 23 || mi > 59 || se > 59 || !validDate(m[1], m[2], m[3]) {
		return false
	}
	if m[9] != "" {
		oh, _ := strconv.Atoi(m[10])
		om, _ := strconv.Atoi(m[11])
		if oh > 23 || om > 59 {
			return false
		}
	}
	return true
}

func validDate(ys, ms, ds string) bool {
	y, _ := strconv.Atoi(ys)
	m, _ := strconv.Atoi(ms)
	d, _ := strconv.Atoi(ds)
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && int(t.Month()) == m && t.Day() == d
}
