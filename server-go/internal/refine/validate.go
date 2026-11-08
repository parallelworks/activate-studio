// Package refine holds refine.ts's validateText: the check a search's text
// field gets before it runs, since a malformed fts5 expression otherwise
// fails in every directory and the search finds nothing without saying
// why. (refineOf, meaningPhrase and namePathMatches are internal/search's.)
package refine

import (
	"database/sql"
	"regexp"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"

	_ "modernc.org/sqlite"
)

var (
	once  sync.Once
	mu    sync.Mutex
	db    *sql.DB
	stmt  *sql.Stmt
	quote = regexp.MustCompile(`"[^"]*"`)
	ends  = regexp.MustCompile(`^\s*(AND|OR|NOT)\b|\b(AND|OR|NOT)\s*$`)
	// modernc's wrapping of SQLite's message: "SQL logic error: <msg> (1)"
	wrapped = regexp.MustCompile(`^(?:[A-Za-z ]+ error: )?(.*?)(?: \(\d+\))?$`)
	column  = regexp.MustCompile(`^no such column: (.+)$`)
)

// checker is fts5's own parser on an in-memory table; nil where it cannot
// be had, and the shape check is all there is.
func checker() func(string) string {
	once.Do(func() {
		d, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return
		}
		d.SetMaxOpenConns(1)
		if _, err := d.Exec("CREATE VIRTUAL TABLE t USING fts5(x)"); err != nil {
			d.Close()
			return
		}
		s, err := d.Prepare("SELECT count(*) FROM t WHERE t MATCH ?")
		if err != nil {
			d.Close()
			return
		}
		db, stmt = d, s
	})
	if stmt == nil {
		return nil
	}
	return func(expr string) string {
		mu.Lock()
		defer mu.Unlock()
		var n int
		if err := stmt.QueryRow(expr).Scan(&n); err != nil {
			return wrapped.FindStringSubmatch(err.Error())[1]
		}
		return ""
	}
}

// checkShape is checkShape(expr): an odd number of quotes, brackets that
// do not pair, or an operator at an end.
func checkShape(expr string) string {
	if strings.Count(expr, `"`)%2 == 1 {
		return "a quotation mark is not closed"
	}
	depth := 0
	for _, c := range quote.ReplaceAllString(expr, "") {
		if c == '(' {
			depth++
		}
		if c == ')' {
			depth--
			if depth < 0 {
				return "a closing bracket has no opening one"
			}
		}
	}
	if depth != 0 {
		return "a bracket is not closed"
	}
	if ends.MatchString(expr) {
		return "AND, OR and NOT need words on both sides"
	}
	return ""
}

// ValidateText is validateText(text): nil, or the 400 that says what is
// wrong with the expression and how to write it.
func ValidateText(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	why := checkShape(text)
	if why == "" {
		if c := checker(); c != nil {
			why = c(text)
		}
	}
	if why == "" {
		return nil
	}
	why = strings.TrimPrefix(why, "fts5: ")
	why = column.ReplaceAllString(why, `"$1" was read as a column name, so a word with a hyphen or a colon needs quotes`)
	return apierr.New(400, "%s", "The text search cannot be read ("+why+"). Put a word with punctuation in double quotes, such as \"covid-19\", and write AND, OR and NOT in capitals between words.")
}
