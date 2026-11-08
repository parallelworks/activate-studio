package chat

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Citation links in replies, repaired as they stream (citations.ts). The
// prompt asks for each cited file as [path](#open=file:path); small models
// copy the prompt's placeholder into the path, leave out the target,
// percent-encode the text, or leave out the library. Each is repaired into
// the link the prompt asked for, on the server, which knows the files the
// tools returned. A bracket is held only until it closes, at most a line.

// CitationContext is what a repair knows: the files the tools returned in
// the conversation, by path (their library, "" for the main folder), and
// the library the conversation searches.
type CitationContext struct {
	Known   map[string]string
	Library string
}

var (
	placeholderRe = regexp.MustCompile(`(?i)^/?relative/path/`)
	libIDRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	pctRe         = regexp.MustCompile(`(?i)%[0-9a-f]{2}`)
	openTargetRe  = regexp.MustCompile(`^(.*?)(&q=[^&]*)?(?:&lib=([a-z0-9_-]+))?$`)
	schemeRe      = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:|^#|^//`)
	bracketsRe    = regexp.MustCompile(`([\[\]])`)
)

const maxHeld = 600

func decodeURI(s string) string {
	d, err := url.PathUnescape(s)
	if err != nil || !utf8.ValidString(d) {
		return s
	}
	return d
}

// EncodeURIComponent is encodeURIComponent.
func EncodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(hex2(c)))
	}
	return b.String()
}

func hex2(c byte) string {
	const h = "0123456789abcdef"
	return string([]byte{h[c>>4], h[c&15]})
}

func normalizedPath(p string) string {
	p = jsTrim(p)
	p = strings.TrimPrefix(p, "./")
	return strings.TrimLeft(p, "/")
}

func (ctx *CitationContext) libraryOf(p string) string {
	if lib, ok := ctx.Known[p]; ok {
		return lib
	}
	if l := ctx.Library; l != "" && l != "kb" && l != "all" {
		return l
	}
	return ""
}

// OpenTarget is openTarget(p, library, q).
func OpenTarget(p, library, q string) string {
	enc := EncodeURIComponent(p)
	enc = strings.NewReplacer("%2F", "/", "%2f", "/", "(", "%28", ")", "%29").Replace(enc)
	lib := ""
	if library != "" && library != "kb" && libIDRe.MatchString(library) {
		lib = "&lib=" + library
	}
	return "#open=file:" + enc + q + lib
}

func citeLink(text, p, library, q string) string {
	return "[" + bracketsRe.ReplaceAllString(text, `\$1`) + "](" + OpenTarget(p, library, q) + ")"
}

func shown(text string) string {
	t := placeholderRe.ReplaceAllString(text, "")
	if pctRe.MatchString(t) {
		return decodeURI(t)
	}
	return t
}

func (ctx *CitationContext) repairBare(inner string) (string, bool) {
	p := normalizedPath(decodeURI(inner))
	if p == "" || strings.Contains(p, "\n") {
		return "", false
	}
	if placeholderRe.MatchString(p) {
		rest := placeholderRe.ReplaceAllString(p, "")
		if rest == "" {
			return "", false
		}
		return citeLink(rest, rest, ctx.libraryOf(rest), ""), true
	}
	if _, ok := ctx.Known[p]; ok {
		return citeLink(p, p, ctx.libraryOf(p), ""), true
	}
	return "", false
}

func hasSpace(s string) bool { return strings.IndexFunc(s, isJSSpace) >= 0 }

func (ctx *CitationContext) repairLink(text, target string) (string, bool) {
	t := jsTrim(target)
	var raw, lib, q string
	isOpen := strings.HasPrefix(t, "#open=file:")
	if isOpen {
		m := openTargetRe.FindStringSubmatch(t[len("#open=file:"):])
		if m == nil {
			return "", false
		}
		raw, q, lib = m[1], m[2], m[3]
	} else if schemeRe.MatchString(t) {
		return "", false
	} else {
		raw = t
	}
	path0 := normalizedPath(decodeURI(raw))
	ph := placeholderRe.MatchString(path0)
	p := path0
	if ph {
		p = placeholderRe.ReplaceAllString(path0, "")
	}
	if p == "" {
		return "", false
	}
	if isOpen && !ph && !hasSpace(raw) && (lib != "" || ctx.libraryOf(p) == "") && !placeholderRe.MatchString(text) && !pctRe.MatchString(text) {
		return "", false
	}
	if _, known := ctx.Known[p]; !isOpen && !ph && !known {
		return "", false
	}
	s := shown(text)
	if s == "" {
		s = p
	}
	if lib == "" {
		lib = ctx.libraryOf(p)
	}
	return citeLink(s, p, lib, q), true
}

// u16 is the length of s in UTF-16 units, as JavaScript counts it.
func u16(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

type bracket struct {
	end  int
	kind string // text, bare, link
}

func bracketEnd(s string, at int, eof bool) *bracket {
	close := indexFrom(s, "]", at+1)
	nl := indexFrom(s, "\n", at+1)
	if close < 0 {
		if nl >= 0 || u16(s[at:]) > maxHeld || eof {
			return &bracket{at + 1, "text"}
		}
		return nil
	}
	if (nl >= 0 && nl < close) || u16(s[at:close]) > maxHeld {
		return &bracket{at + 1, "text"}
	}
	if close+1 >= len(s) {
		if eof {
			return &bracket{close + 1, "bare"}
		}
		return nil
	}
	if s[close+1] != '(' {
		return &bracket{close + 1, "bare"}
	}
	paren := indexFrom(s, ")", close+2)
	nl2 := indexFrom(s, "\n", close+2)
	if paren < 0 {
		if nl2 >= 0 || u16(s[at:]) > maxHeld || eof {
			return &bracket{close + 1, "bare"}
		}
		return nil
	}
	if nl2 >= 0 && nl2 < paren {
		return &bracket{close + 1, "bare"}
	}
	return &bracket{paren + 1, "link"}
}

func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	i := strings.Index(s[from:], sub)
	if i < 0 {
		return -1
	}
	return from + i
}

// RepairCitations is repairCitations(text, ctx).
func RepairCitations(text string, ctx *CitationContext) string {
	s := NewCitationStream(ctx)
	return s.Push(text) + s.Flush()
}

// CitationStream is the streaming form: Push each piece as it arrives and
// send what it returns; Flush at the end of a model turn.
type CitationStream struct {
	ctx  *CitationContext
	held string
	last string
}

// NewCitationStream is new CitationStream(ctx).
func NewCitationStream(ctx *CitationContext) *CitationStream { return &CitationStream{ctx: ctx} }

// Push takes the next piece of text.
func (c *CitationStream) Push(t string) string { return c.run(c.held+t, false) }

// Flush is everything still held, as the end of the text.
func (c *CitationStream) Flush() string { return c.run(c.held, true) }

// Drop forgets what is held: the turn it came from is asked again.
func (c *CitationStream) Drop() { c.held = "" }

func (c *CitationStream) run(s string, eof bool) string {
	out := c.scan(s, eof)
	if out != "" {
		r, _ := utf8.DecodeLastRuneInString(out)
		c.last = string(r)
		if r >= 0x10000 {
			// at(-1) is the low surrogate: never one of the marks compared
			_, lo := utf16.EncodeRune(r)
			c.last = string(lo)
		}
	}
	return out
}

func (c *CitationStream) scan(s string, eof bool) string {
	var out strings.Builder
	i := 0
	c.held = ""
	for {
		at := indexFrom(s, "[", i)
		if at < 0 {
			out.WriteString(s[i:])
			return out.String()
		}
		out.WriteString(s[i:at])
		b := bracketEnd(s, at, eof)
		if b == nil {
			c.held = s[at:]
			return out.String()
		}
		piece := s[at:b.end]
		prev := c.last
		if at > 0 {
			prev = s[at-1 : at]
		}
		next := ""
		if b.end < len(s) {
			next = s[b.end : b.end+1]
		}
		fixed, ok := "", false
		plain := prev != "!" && prev != `\` && prev != "`"
		if b.kind == "bare" && plain && prev != "]" && next != "[" && next != ":" {
			fixed, ok = c.ctx.repairBare(piece[1 : len(piece)-1])
		} else if b.kind == "link" && plain {
			cl := strings.Index(piece, "](")
			fixed, ok = c.ctx.repairLink(piece[1:cl], piece[cl+2:len(piece)-1])
		}
		if ok {
			out.WriteString(fixed)
		} else {
			out.WriteString(piece)
		}
		i = b.end
	}
}
