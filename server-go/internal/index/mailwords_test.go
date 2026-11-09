package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/embed"
	"github.com/parallelworks/activate-studio/server-go/internal/extract"
)

// An Apple Mail message in a library is found by a word in its body: the
// extraction pass reads it into the cache, and the words step writes that
// text to the folder's words table. It counts as a document for meaning.
func TestMailFoundByBodyWord(t *testing.T) {
	kb, index, cache := t.TempDir(), t.TempDir(), t.TempDir()
	msg := "From: a@example.com\r\nSubject: Quarterly\r\nContent-Type: multipart/alternative; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/html; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"<p>The xylophone recital moved to the caf=E9.</p>\r\n--b--\r\n"
	rel := "Mail/V10/acct/INBOX.mbox/Data/Messages"
	for _, f := range []struct{ name, body string }{
		{"1234.emlx", fmt.Sprintf("%d\n%s<?xml version=\"1.0\"?><plist version=\"1.0\"><dict/></plist>\n", len(msg), msg)},
		{"note.eml", msg},
	} {
		p := filepath.Join(kb, filepath.FromSlash(rel), f.name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	x, err := extract.New(extract.Options{Tools: &extract.Tools{}, Jobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	s, err := x.Run(context.Background(), extract.PassOptions{KBRoot: kb, CacheRoot: cache})
	if err != nil || s.Written != 2 || s.Kinds[".emlx"].Written != 1 || s.Kinds[".eml"].Written != 1 {
		t.Fatalf("extract: %+v, %v", s, err)
	}
	// The folders of a GUFI index, each with its db.db.
	dbDir := filepath.Join(index, filepath.FromSlash(rel))
	os.MkdirAll(dbDir, 0o755)
	db := filepath.Join(dbDir, "db.db")
	os.WriteFile(db, nil, 0o644)
	res, err := WriteWords(context.Background(), WordsOptions{KBRoot: kb, Index: index, Cache: cache, Jobs: 1})
	if err != nil || res.Files != 2 || res.Documents[".emlx"] == nil || res.Documents[".emlx"].NoText != 0 {
		t.Fatalf("words: %+v, %v", res, err)
	}
	h, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	rows, err := h.Query("SELECT fname FROM words WHERE words MATCH 'xylophone' ORDER BY fname")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		got = append(got, n)
	}
	rows.Close()
	if fmt.Sprint(got) != "[1234.emlx note.eml]" {
		t.Fatalf("found %v", got)
	}
	var subject int
	h.QueryRow("SELECT count(*) FROM words WHERE words MATCH 'quarterly AND café'").Scan(&subject)
	if subject != 2 {
		t.Fatalf("header and decoded body: %d", subject)
	}
	// Meaning on documents takes them as documents.
	for _, n := range []string{"1234.emlx", "77.partial.emlx", "note.eml"} {
		if !embed.EmbedsFile(n, true) {
			t.Errorf("%s not embedded with documents only", n)
		}
	}
	// With documents off they index by name only.
	nd, _ := extract.New(extract.Options{Tools: &extract.Tools{}, NoDocuments: true})
	if k := nd.KindOf("1.emlx"); k != extract.KindNone {
		t.Errorf("no-documents kind %v", k)
	}
}
