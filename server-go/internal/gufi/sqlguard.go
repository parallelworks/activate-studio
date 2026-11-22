package gufi

import "strings"

// refusedNames are the SQL functions and virtual table modules a person's
// query may not name, because they start a program or read or write a file
// of the caller's choosing. A query runs as the Studio's own account.
//
// GUFI 0.8.1 and GUFI main register strop, intop and blobop (each runs
// its argument with popen) and lineop on main; create_spread and
// spread_to_external (run scripts and write files); the run_vt module
// (runs a command per row); load_extension (loads any shared library).
// The gufi_vt modules run gufi_query on any index path, and lembed's
// model loader opens any file. readfile, writefile, fsdir, lsdir,
// zipfile, sqlar and sqlite_dbpage are SQLite extensions GUFI does not
// build in today; they are named so a future build cannot open them up.
var refusedNames = map[string]bool{
	"strop": true, "intop": true, "blobop": true, "lineop": true,
	"create_spread": true, "spread_to_external": true,
	"run_vt":                 true,
	"load_extension":         true,
	"gufi_vt":                true,
	"lembed_model_from_file": true, "lembed_models": true,
	"readfile": true, "writefile": true, "fsdir": true, "lsdir": true,
	"zipfile": true, "sqlar_compress": true, "sqlar_uncompress": true,
	"sqlite_dbpage": true, "sqlite_dbdata": true, "sqlite_dbptr": true,
	"fts3_tokenizer": true,
}

// RefusedName is the first function or module of sql that a person's
// query may not name, or "". It reads sql as SQLite's tokenizer does:
// string literals and comments are passed by, quoted identifiers ("x",
// `x`, [x]) count as names, and names are compared without ASCII case,
// as SQLite compares them. A name is refused wherever it stands (a call,
// a table, a CTE, a subquery, a trigger or view body), so no spacing or
// comment between it and its parenthesis matters.
func RefusedName(sql string) string {
	for _, n := range sqlNames(sql) {
		if refusedNames[n] || strings.HasPrefix(n, "gufi_vt_") {
			return n
		}
	}
	return ""
}

// sqlNames are the bare words and quoted identifiers of sql, lower case
// in ASCII. Anything SQLite could take as a name is among them; so are
// some words it would not (parameters, the x of a blob literal), which
// only makes the check stricter.
func sqlNames(sql string) []string {
	var out []string
	s := sql
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return out
			}
			i += j + 1
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return out
			}
			i += 2 + j + 2
		case c == '\'':
			// SQLite takes a string where only a name may stand as the
			// name (FROM 'x'), so a literal is a name too when it is
			// one whole name; LIKE patterns and words within stay text.
			v, n := quoted(s[i:], '\'')
			if isWord(v) {
				out = append(out, asciiLower(v))
			}
			i += n
		case c == '"' || c == '`':
			v, n := quoted(s[i:], c)
			out = append(out, asciiLower(v))
			i += n
		case c == '[':
			j := strings.IndexByte(s[i+1:], ']')
			if j < 0 {
				out = append(out, asciiLower(s[i+1:]))
				return out
			}
			out = append(out, asciiLower(s[i+1:i+1+j]))
			i += j + 2
		case idChar(c):
			j := i
			for j < len(s) && idChar(s[j]) {
				j++
			}
			out = append(out, asciiLower(s[i:j]))
			i = j
		default:
			i++
		}
	}
	return out
}

// quoted reads a literal or identifier opening with q at s[0], where q
// doubled stands for itself: its value and the bytes it takes.
func quoted(s string, q byte) (string, int) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] == q {
			if i+1 < len(s) && s[i+1] == q {
				b.WriteByte(q)
				i++
				continue
			}
			return b.String(), i + 1
		}
		b.WriteByte(s[i])
	}
	return b.String(), len(s)
}

// idChar is SQLite's IdChar: a letter, digit, _ or $, or any byte of a
// multibyte character.
func idChar(c byte) bool {
	return c >= 0x80 || c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isWord is a string of name characters alone.
func isWord(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		if !idChar(v[i]) {
			return false
		}
	}
	return true
}

// asciiLower folds A-Z only, as SQLite does for names.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
