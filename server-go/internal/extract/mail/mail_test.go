package mail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crlf writes a message with CRLF line ends, as mail is sent.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// emlx wraps a message in Apple Mail's format: the count line, the
// message, and a flags property list.
func emlx(msg string) string {
	return fmt.Sprintf("%d\n%s<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict><key>flags</key><integer>8590195713</integer></dict></plist>\n", len(msg), msg)
}

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, name, content string) string {
	t.Helper()
	out, err := ReadFile(write(t, name, content), 0)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return out
}

func has(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func hasNot(t *testing.T, out string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(out, b) {
			t.Errorf("unexpected %q in:\n%s", b, out)
		}
	}
}

const plain = `From: Ada Lovelace <ada@example.com>
To: Charles Babbage <cb@example.com>
Cc: team@example.com
Date: Mon, 2 Nov 2026 10:00:00 -0600
Subject: Engine notes
Message-ID: <1@example.com>
Content-Type: text/plain; charset=utf-8

The analytical engine weaves algebraic patterns.
`

func TestPlainEmlx(t *testing.T) {
	out := read(t, "123.emlx", emlx(crlf(plain)))
	want := "Subject: Engine notes\nFrom: Ada Lovelace <ada@example.com>\nTo: Charles Babbage <cb@example.com>\n" +
		"Cc: team@example.com\nDate: Mon, 2 Nov 2026 10:00:00 -0600\n\nThe analytical engine weaves algebraic patterns.\n"
	if out != want {
		t.Errorf("got\n%q\nwant\n%q", out, want)
	}
	hasNot(t, out, "plist", "Message-ID", "\r")
}

func TestEml(t *testing.T) {
	out := read(t, "note.eml", plain)
	has(t, out, "Subject: Engine notes", "algebraic patterns")
}

func TestHTMLOnly(t *testing.T) {
	msg := `From: a@example.com
Subject: Newsletter
Content-Type: text/html; charset=utf-8

<html><head><title>Ignored title</title><style>p { color: red }</style></head>
<body><p>First&nbsp;paragraph &amp; more.</p><div>Second <b>bold</b> line</div>
<script>alert("x")</script><table><tr><td>cell one</td><td>cell two</td></tr></table></body></html>
`
	out := read(t, "1.emlx", emlx(msg))
	has(t, out, "First paragraph & more.\nSecond bold line\ncell one cell two")
	hasNot(t, out, "<p>", "color: red", "alert", "Ignored title")
}

func TestAlternativePrefersPlain(t *testing.T) {
	msg := `From: a@example.com
Subject: Alt
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="XX"

--XX
Content-Type: text/plain; charset=utf-8

plain version wins
--XX
Content-Type: text/html; charset=utf-8

<p>html version loses</p>
--XX--
`
	out := read(t, "2.emlx", emlx(crlf(msg)))
	has(t, out, "plain version wins")
	hasNot(t, out, "html version loses")
}

func TestAlternativeRelatedHTML(t *testing.T) {
	// An alternative whose only text is HTML inside multipart/related.
	msg := `From: a@example.com
Subject: Rel
Content-Type: multipart/alternative; boundary="A"

--A
Content-Type: multipart/related; boundary="B"

--B
Content-Type: text/html; charset=utf-8

<p>related html body</p>
--B
Content-Type: image/png
Content-ID: <logo>
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--B--
--A--
`
	out := read(t, "3.emlx", emlx(msg))
	has(t, out, "related html body")
	hasNot(t, out, "Attachments:")
}

func TestAttachmentNames(t *testing.T) {
	msg := `From: a@example.com
Subject: Report attached
Content-Type: multipart/mixed; boundary="M"

--M
Content-Type: text/plain

See the quarterly figures.
--M
Content-Type: application/pdf; name="report.pdf"
Content-Disposition: attachment; filename="report.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQK
--M
Content-Type: application/octet-stream
Content-Disposition: attachment; filename*=UTF-8''r%C3%A9sum%C3%A9.txt

xyz
--M
Content-Type: image/jpeg; name="=?UTF-8?B?Y2Fmw6kuanBn?="

abc
--M--
`
	out := read(t, "4.emlx", emlx(msg))
	has(t, out, "See the quarterly figures.", "\n\nAttachments: report.pdf, résumé.txt, café.jpg\n")
	hasNot(t, out, "JVBER")
}

func TestQuotedPrintable(t *testing.T) {
	msg := `From: a@example.com
Subject: QP
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

Caf=C3=A9 meeting at noon, a long line that is softly=
 wrapped here.
`
	out := read(t, "5.emlx", emlx(msg))
	has(t, out, "Café meeting at noon, a long line that is softly wrapped here.")
}

func TestBase64(t *testing.T) {
	msg := `From: a@example.com
Subject: B64
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: base64

SGVsbG8gZnJvbSBiYXNlNjQgbGFuZCwg
d2l0aCB3cmFwcGVkIGxpbmVzLg==
`
	out := read(t, "6.emlx", emlx(crlf(msg)))
	has(t, out, "Hello from base64 land, with wrapped lines.")
}

func TestLatin1AndWindows1252(t *testing.T) {
	msg := "From: a@example.com\nSubject: Latin\nContent-Type: text/plain; charset=ISO-8859-1\n\nna\xefve fa\xe7ade\n"
	has(t, read(t, "7.emlx", emlx(msg)), "naïve façade")

	msg = "From: a@example.com\nSubject: Win\nContent-Type: text/plain; charset=windows-1252\n\n\x93quoted\x94 \x80 5\n"
	has(t, read(t, "8.emlx", emlx(msg)), "“quoted” € 5")

	// Unlabeled 8-bit text reads as Windows-1252.
	msg = "From: a@example.com\nSubject: Bare\n\ncr\xe8me br\xfbl\xe9e\n"
	has(t, read(t, "9.eml", msg), "crème brûlée")
}

func TestEncodedHeaders(t *testing.T) {
	msg := `From: =?ISO-8859-1?Q?Andr=E9?= <andre@example.com>
To: =?UTF-8?B?w4lsb2RpZQ==?= <e@example.com>
Subject: =?UTF-8?B?UsOpc3Vtw6kgZHUgcHJvamV0?=
 =?UTF-8?Q?_=E2=80=94_suite?=
Content-Type: text/plain

body
`
	out := read(t, "10.emlx", emlx(msg))
	has(t, out, "Subject: Résumé du projet — suite\n", "From: André <andre@example.com>\n", "To: Élodie <e@example.com>\n")
}

func TestPartialEmlx(t *testing.T) {
	// A .partial.emlx keeps the attachment's part header with its body
	// moved out to the Attachments folder.
	msg := `From: a@example.com
Subject: Big file
Content-Type: multipart/mixed; boundary="P"

--P
Content-Type: text/plain

The drawings are attached.
--P
Content-Type: application/zip; name=drawings.zip
Content-Disposition: attachment; filename=drawings.zip
X-Apple-Content-Length: 48211234

--P--
`
	out := read(t, "77.partial.emlx", emlx(crlf(msg)))
	has(t, out, "Subject: Big file", "The drawings are attached.", "Attachments: drawings.zip")
}

func TestForwardedMessage(t *testing.T) {
	msg := `From: a@example.com
Subject: Fwd: plans
Content-Type: multipart/mixed; boundary="F"

--F
Content-Type: text/plain

See below.
--F
Content-Type: message/rfc822

From: b@example.com
Subject: plans

inner message text
--F--
`
	out := read(t, "11.emlx", emlx(msg))
	has(t, out, "See below.", "Subject: plans", "inner message text")
}

func TestTruncatedAndCorrupt(t *testing.T) {
	// The count line promises more than the file holds.
	full := emlx(crlf(plain))
	cut := full[:len(full)/2+60]
	out := read(t, "12.emlx", cut)
	has(t, out, "Subject: Engine notes")

	// A multipart cut off inside a base64 part.
	msg := "From: a@example.com\nSubject: Cut\nContent-Type: multipart/mixed; boundary=\"Q\"\n\n--Q\nContent-Type: text/plain\nContent-Transfer-Encoding: base64\n\nSGVsbG8gd29y"
	has(t, read(t, "13.emlx", fmt.Sprintf("%d\n%s", len(msg)+500, msg)), "Hello wor")

	// Garbage, binary, empty, a bad boundary, a bad count line: no panic.
	for i, c := range []string{"", "\n", "999999999999999999999\nx", "12\n", "\x00\x01\x02\xff\xfe",
		"Content-Type: multipart/mixed; boundary=\"\x00\n\n--\x00\n",
		"Content-Type: multipart/mixed\n\nno boundary",
		"Content-Type: text/plain; charset=nonsense-9\n\n\xff\xfe text",
		"Content-Transfer-Encoding: base64\n\n!!!!====abc",
	} {
		ReadFile(write(t, fmt.Sprintf("bad%d.emlx", i), c), 0)
		ReadFile(write(t, fmt.Sprintf("bad%d.eml", i), c), 0)
	}
	if _, err := ReadFile(write(t, "empty.eml", ""), 0); err == nil {
		t.Error("an empty file read as a message")
	}
}

func TestDeepNesting(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("From: a@example.com\nSubject: deep\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "Content-Type: multipart/mixed; boundary=\"b%d\"\n\n--b%d\n", i, i)
	}
	sb.WriteString("Content-Type: text/plain\n\nbottom\n")
	out := read(t, "deep.eml", sb.String())
	has(t, out, "Subject: deep")
}

func TestMaxText(t *testing.T) {
	out, err := ReadFile(write(t, "m.emlx", emlx(plain)), 10)
	if err != nil || out != "Subject: E" {
		t.Errorf("got %q, %v", out, err)
	}
}

func TestTooLarge(t *testing.T) {
	p := filepath.Join(t.TempDir(), "huge.eml")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("Subject: huge\n\n")
	f.Truncate(MaxInputBytes + 1024)
	f.Close()
	if _, err := ReadFile(p, 0); err != ErrTooLarge {
		t.Errorf("got %v, want ErrTooLarge", err)
	}
}

func FuzzText(f *testing.F) {
	f.Add([]byte(plain), 0)
	f.Add([]byte("Content-Type: multipart/alternative; boundary=x\n\n--x\nContent-Type: text/html\n\n<p>a\n--x--"), 5)
	f.Fuzz(func(t *testing.T, b []byte, n int) {
		Text(b, n)
	})
}
