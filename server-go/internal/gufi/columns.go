package gufi

import (
	"regexp"
	"strings"
)

// The names of a raw SELECT's result columns, as SQLite names them: the
// alias after AS (or a bare alias after a column), else a column's own
// name, else the expression as written. query.ts's selectColumnNames is
// the same; both read ASCII only, so byte and UTF-16 offsets agree.

var (
	identRe    = `(?:[A-Za-z_][A-Za-z0-9_$]*|"(?:[^"]|"")*"|` + "`[^`]*`" + `|\[[^\]]*\])`
	columnRe   = regexp.MustCompile(`^(?:` + identRe + `\.)*(` + identRe + `)$`)
	bareAlias  = regexp.MustCompile(`^(?:` + identRe + `\.)*` + identRe + `[ \t\r\n\f\v]+(` + identRe + `)$`)
	singleName = regexp.MustCompile(`^` + identRe + `$`)
	stopWords  = map[string]bool{"from": true, "where": true, "group": true, "having": true, "order": true, "limit": true,
		"union": true, "intersect": true, "except": true, "window": true}
)

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func trimSQL(s string) string { return strings.Trim(s, " \t\r\n\f\v") }

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// unquoteIdent is an identifier without its quotes.
func unquoteIdent(s string) string {
	if len(s) >= 2 {
		switch {
		case s[0] == '"' && s[len(s)-1] == '"':
			return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
		case s[0] == '`' && s[len(s)-1] == '`', s[0] == '[' && s[len(s)-1] == ']':
			return s[1 : len(s)-1]
		}
	}
	return s
}

// withoutComments is the SQL with each comment made one space; quoted
// text is kept as it is.
func withoutComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`' || c == '[':
			closeC := c
			if c == '[' {
				closeC = ']'
			}
			j := i + 1
			for j < len(s) {
				if s[j] == closeC {
					if (closeC == '\'' || closeC == '"') && j+1 < len(s) && s[j+1] == closeC {
						j += 2
						continue
					}
					break
				}
				j++
			}
			j = min(j+1, len(s))
			b.WriteString(s[i:j])
			i = j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			if j := strings.Index(s[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(s)
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// SelectColumnNames is selectColumnNames: nil when the names cannot be
// told from the text, as for * or a statement that is not one SELECT.
func SelectColumnNames(sql string) []string {
	s := strings.TrimRight(trimSQL(withoutComments(sql)), "; \t\r\n")
	if len(s) < 7 || !strings.EqualFold(s[:6], "select") || !isSpaceByte(s[6]) {
		return nil
	}
	i := 6
	// DISTINCT or ALL, then the list.
	skipSpace := func() {
		for i < len(s) && isSpaceByte(s[i]) {
			i++
		}
	}
	skipSpace()
	for _, w := range []string{"distinct", "all"} {
		if len(s) >= i+len(w) && strings.EqualFold(s[i:i+len(w)], w) && (len(s) == i+len(w) || !isWordByte(s[i+len(w)])) {
			i += len(w)
			break
		}
	}
	type item struct {
		start, end int
		as         int // the last top-level AS, or -1
	}
	var items []item
	cur := item{start: i, as: -1}
	depth := 0
	end := len(s)
scan:
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`' || c == '[':
			closeC := c
			if c == '[' {
				closeC = ']'
			}
			i++
			for i < len(s) {
				if s[i] == closeC {
					if (closeC == '\'' || closeC == '"') && i+1 < len(s) && s[i+1] == closeC {
						i += 2
						continue
					}
					break
				}
				i++
			}
			i++
			continue
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			cur.end = i
			items = append(items, cur)
			cur = item{start: i + 1, as: -1}
		case depth == 0 && isWordByte(c) && (i == 0 || !isWordByte(s[i-1])):
			j := i
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			w := strings.ToLower(s[i:j])
			if stopWords[w] {
				end = i
				break scan
			}
			if w == "as" {
				cur.as = i
			}
			i = j
			continue
		}
		i++
	}
	if depth != 0 {
		return nil
	}
	cur.end = end
	items = append(items, cur)
	names := make([]string, 0, len(items))
	for _, it := range items {
		text := trimSQL(s[it.start:it.end])
		if text == "" {
			return nil
		}
		if text == "*" || strings.HasSuffix(text, ".*") {
			return nil
		}
		if it.as >= 0 {
			alias := trimSQL(s[it.as+2 : it.end])
			if singleName.MatchString(alias) && trimSQL(s[it.start:it.as]) != "" {
				names = append(names, unquoteIdent(alias))
				continue
			}
		}
		if m := bareAlias.FindStringSubmatch(text); m != nil {
			names = append(names, unquoteIdent(m[1]))
		} else if m := columnRe.FindStringSubmatch(text); m != nil {
			names = append(names, unquoteIdent(m[1]))
		} else {
			names = append(names, text)
		}
	}
	return names
}

// RawColumns is rawColumns: the names a raw SELECT gives its columns when
// they can be told and fit the rows (or there are no rows), else col1 to
// colN for the widest row.
func RawColumns(sql string, width int, anyRows bool) []string {
	if names := SelectColumnNames(sql); names != nil && (!anyRows || len(names) == width) {
		return names
	}
	cols := make([]string, max(width, 1))
	for i := range cols {
		cols[i] = "col" + itoa(i+1)
	}
	return cols
}
