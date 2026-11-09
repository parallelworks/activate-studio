// Package mail reads an email message, an Apple Mail .emlx file or a plain
// .eml file, as text: a short header block (Subject, From, To, Cc, Date),
// then the body, then the names of its attachments.
//
// An .emlx file is a first line with the decimal byte count of the RFC 822
// message, that many bytes of message, then an XML property list of Mail's
// flags, which is not read. A .partial.emlx file is the same, with its
// large attachments kept apart in an Attachments folder beside it; those
// are ordinary files to the library, and only their names show here (the
// message still lists the parts).
//
// The body is the text/plain part where the message has one, else its
// HTML converted to text. Quoted-printable and base64 transfer encodings,
// any charset golang.org/x/text knows, RFC 2047 encoded headers and RFC
// 2231 file names are decoded. Malformed input gives what could be read,
// never a panic.
package mail

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
)

// MaxInputBytes is the largest message read. A message with its
// attachments inline can be tens of megabytes; past this it is refused.
const MaxInputBytes = 64 << 20

const (
	maxDepth = 16   // nesting of multiparts and attached messages
	maxParts = 1000 // parts read in one message
)

// Suffixes are the file suffixes this package reads (".partial.emlx"
// ends in ".emlx").
var Suffixes = map[string]bool{".emlx": true, ".eml": true}

// ErrTooLarge is a message above MaxInputBytes.
var ErrTooLarge = errors.New("mail: message too large")

// ReadFile reads the message in the file at path as text, at most maxText
// characters (none when maxText <= 0). A name ending in .emlx is read as
// Apple Mail's format, anything else as a bare message.
func ReadFile(path string, maxText int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxInputBytes+64))
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.ToLower(filepath.Base(path)), ".emlx") {
		b = Unwrap(b)
	}
	if len(b) > MaxInputBytes {
		return "", ErrTooLarge
	}
	return Text(b, maxText)
}

// Unwrap is the RFC 822 message inside .emlx content: the bytes the count
// line names, or what there is of them in a truncated file. Content that
// does not open with a count line is returned as it is.
func Unwrap(b []byte) []byte {
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 || nl > 20 {
		return b
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b[:nl])))
	if err != nil || n < 0 {
		return b
	}
	rest := b[nl+1:]
	if n < len(rest) {
		rest = rest[:n]
	}
	return rest
}

// Text reads the message msg as text, at most maxText characters (none
// when maxText <= 0). It fails only when not even a header could be read.
func Text(msg []byte, maxText int) (out string, err error) {
	defer func() {
		// The readers here are bounded and checked, but a hostile message
		// must not take an index pass down whatever happens.
		if p := recover(); p != nil {
			out, err = "", fmt.Errorf("mail: %v", p)
		}
	}()
	r := &reader{}
	text, ok := r.message(msg, 0)
	if !ok {
		return "", errors.New("mail: no message header")
	}
	if len(r.attachments) > 0 {
		text = strings.TrimRight(text, "\n") + "\n\nAttachments: " + strings.Join(r.attachments, ", ") + "\n"
	}
	return head(text, maxText), nil
}

type reader struct {
	attachments []string
	parts       int
}

// message reads one message (the top one or an attached one): its header
// block, then its body. ok is false when there was no header at all.
func (r *reader) message(msg []byte, depth int) (string, bool) {
	// An mbox "From " separator line before the header.
	if bytes.HasPrefix(msg, []byte("From ")) {
		if nl := bytes.IndexByte(msg, '\n'); nl >= 0 {
			msg = msg[nl+1:]
		}
	}
	br := bufio.NewReader(bytes.NewReader(msg))
	hdr, err := textproto.NewReader(br).ReadMIMEHeader()
	if len(hdr) == 0 {
		return "", false
	}
	var body []byte
	if err == nil || errors.Is(err, io.EOF) {
		body, _ = io.ReadAll(br)
	}
	var sb strings.Builder
	for _, k := range []string{"Subject", "From", "To", "Cc", "Date"} {
		if v := headerValue(hdr, k); v != "" {
			sb.WriteString(k + ": " + v + "\n")
		}
	}
	if text := strings.TrimSpace(r.entity(hdr, body, depth)); text != "" {
		sb.WriteString("\n" + text + "\n")
	}
	return sb.String(), true
}

// candidate is the text of a part, with whether it is still HTML.
type candidate struct {
	text string
	html bool
}

func (c candidate) String() string {
	if c.html {
		return htmlText(c.text)
	}
	return c.text
}

// entity is the readable text of one MIME entity.
func (r *reader) entity(hdr textproto.MIMEHeader, body []byte, depth int) string {
	return r.part(hdr, body, depth).String()
}

func (r *reader) part(hdr textproto.MIMEHeader, body []byte, depth int) candidate {
	r.parts++
	if depth > maxDepth || r.parts > maxParts {
		return candidate{}
	}
	ctype := hdr.Get("Content-Type")
	media, params, err := mime.ParseMediaType(ctype)
	if err != nil || media == "" {
		media, params = "text/plain", map[string]string{}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ctype)), "multipart/") {
			// A multipart whose parameters do not parse: no boundary to
			// split it by, so it reads as nothing.
			return candidate{}
		}
	}
	disp, dparams, _ := mime.ParseMediaType(hdr.Get("Content-Disposition"))
	name := dparams["filename"]
	if name == "" {
		name = params["name"]
	}
	name = decodeWords(name)

	if strings.HasPrefix(media, "multipart/") {
		return r.multipart(media, params["boundary"], body, depth)
	}
	attachment := disp == "attachment" ||
		(name != "" && media != "text/plain" && media != "text/html" && media != "message/rfc822")
	if attachment {
		if name == "" {
			name = media
		}
		r.attachments = append(r.attachments, name)
		return candidate{}
	}
	data := decodeTransfer(hdr.Get("Content-Transfer-Encoding"), body)
	switch media {
	case "text/plain":
		return candidate{text: strings.TrimSpace(normalize(decodeCharset(data, params["charset"])))}
	case "text/html":
		return candidate{text: decodeCharset(data, params["charset"]), html: true}
	case "message/rfc822", "message/global":
		text, _ := r.message(data, depth+1)
		return candidate{text: strings.TrimSpace(text)}
	}
	return candidate{}
}

func (r *reader) multipart(media, boundary string, body []byte, depth int) candidate {
	if boundary == "" {
		return candidate{}
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var kids []candidate
	for r.parts <= maxParts {
		p, err := mr.NextRawPart()
		if err != nil {
			break
		}
		// What there is of a truncated part is kept.
		data, _ := io.ReadAll(p)
		kids = append(kids, r.part(p.Header, data, depth+1))
	}
	if media == "multipart/alternative" {
		// The plain text where there is one, else the richest other
		// (alternatives run from plainest to richest).
		for _, k := range kids {
			if !k.html && strings.TrimSpace(k.text) != "" {
				return k
			}
		}
		for i := len(kids) - 1; i >= 0; i-- {
			if strings.TrimSpace(kids[i].text) != "" {
				return kids[i]
			}
		}
		return candidate{}
	}
	var texts []string
	for _, k := range kids {
		if t := strings.TrimSpace(k.String()); t != "" {
			texts = append(texts, t)
		}
	}
	return candidate{text: strings.Join(texts, "\n\n")}
}

// decodeTransfer undoes a Content-Transfer-Encoding; what decodes before
// an error is kept.
func decodeTransfer(cte string, body []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		out, _ := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		return out
	case "base64":
		clean := bytes.Map(func(c rune) rune {
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				return -1
			}
			return c
		}, body)
		// Stop at the first byte outside the alphabet (a corrupt or
		// truncated tail), and drop a lone sextet that cannot decode.
		n := 0
		for n < len(clean) && isBase64(clean[n]) {
			n++
		}
		clean = bytes.TrimRight(clean[:n], "=")
		if len(clean)%4 == 1 {
			clean = clean[:len(clean)-1]
		}
		out, _ := io.ReadAll(base64.NewDecoder(base64.RawStdEncoding, bytes.NewReader(clean)))
		return out
	}
	return body
}

func isBase64(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '='
}

// lookup is the encoding a charset label names, nil for UTF-8 and for an
// unknown label.
func lookup(label string) encoding.Encoding {
	label = strings.ToLower(strings.Trim(strings.TrimSpace(label), `"`))
	switch label {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return nil
	}
	e, err := htmlindex.Get(label)
	if err != nil {
		return nil
	}
	if n, _ := htmlindex.Name(e); n == "utf-8" {
		return nil
	}
	return e
}

// decodeCharset is data in charset as UTF-8. Data that is not valid
// UTF-8 under a UTF-8, missing or unknown label reads as Windows-1252,
// the usual charset of unlabeled mail.
func decodeCharset(data []byte, charset string) string {
	if e := lookup(charset); e != nil {
		if out, err := e.NewDecoder().Bytes(data); err == nil {
			return strings.ToValidUTF8(string(out), "�")
		}
	}
	if utf8.Valid(data) {
		return string(data)
	}
	out, _ := charmap.Windows1252.NewDecoder().Bytes(data)
	return string(out)
}

var wordDecoder = &mime.WordDecoder{CharsetReader: func(charset string, in io.Reader) (io.Reader, error) {
	e := lookup(charset)
	if e == nil {
		return in, nil
	}
	return e.NewDecoder().Reader(in), nil
}}

// decodeWords decodes RFC 2047 encoded words; raw 8-bit text that is not
// UTF-8 reads as Windows-1252.
func decodeWords(s string) string {
	if s == "" {
		return ""
	}
	if !utf8.ValidString(s) {
		s = decodeCharset([]byte(s), "")
	}
	if out, err := wordDecoder.DecodeHeader(s); err == nil {
		s = out
	}
	return strings.ToValidUTF8(s, "�")
}

func headerValue(h textproto.MIMEHeader, key string) string {
	vs := h.Values(key)
	if len(vs) == 0 {
		return ""
	}
	v := decodeWords(strings.Join(vs, ", "))
	return strings.Join(strings.Fields(v), " ")
}

// normalize turns CRLF and lone CR line ends into LF.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// head is the first n characters of s (all of it when n <= 0).
func head(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	i := 0
	for k := 0; k < n && i < len(s); k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

// htmlText is the readable text of an HTML body: the document's text
// with scripts, styles and the head left out, a line break at each block,
// and runs of spaces collapsed.
func htmlText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return ""
	}
	var sb strings.Builder
	newline := func() {
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteString("\n")
		}
	}
	pre := 0 // inside <pre>, where line breaks in the text count
	var walk func(n *html.Node, depth int)
	walk = func(n *html.Node, depth int) {
		if depth > 512 {
			return
		}
		block := false
		switch n.Type {
		case html.TextNode:
			if pre > 0 {
				sb.WriteString(n.Data)
			} else {
				// Line breaks in HTML source are spaces.
				sb.WriteString(strings.Map(func(r rune) rune {
					if r == '\n' || r == '\r' || r == '\t' {
						return ' '
					}
					return r
				}, n.Data))
			}
			return
		case html.ElementNode:
			switch n.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Title, atom.Noscript, atom.Template:
				return
			case atom.Br, atom.Hr:
				newline()
				return
			case atom.P, atom.Div, atom.Tr, atom.Li, atom.Table, atom.Blockquote, atom.Pre,
				atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Ul, atom.Ol,
				atom.Section, atom.Article, atom.Header, atom.Footer, atom.Center:
				block = true
				newline()
			case atom.Td, atom.Th:
				sb.WriteString(" ")
			}
		}
		if n.DataAtom == atom.Pre {
			pre++
			defer func() { pre-- }()
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
		if block {
			newline()
		}
	}
	walk(doc, 0)
	return tidy(sb.String())
}

// tidy collapses spaces within lines and drops empty lines.
func tidy(s string) string {
	s = strings.ReplaceAll(s, "\u00a0", " ")
	var out []string
	for _, l := range strings.Split(normalize(s), "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
