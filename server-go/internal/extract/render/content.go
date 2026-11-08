package render

import (
	"bytes"
	"strconv"
)

// An operand on the content stream's stack. Only what painting needs is
// kept: numbers, names, and arrays of numbers.
type operand struct {
	kind byte // 'n' number, 'N' name, 'a' array, 's' string, 'o' other
	num  float64
	name string
	arr  []float64
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// lexer reads a content stream's tokens. It never fails: what it cannot
// read it skips.
type lexer struct {
	b []byte
	i int
}

// next returns the next operand, or an operator (op != ""), or done.
func (l *lexer) next() (o operand, op string, done bool) {
	b := l.b
	for l.i < len(b) {
		c := b[l.i]
		switch {
		case isSpace(c):
			l.i++
		case c == '%':
			for l.i < len(b) && b[l.i] != '\n' && b[l.i] != '\r' {
				l.i++
			}
		case c == '(':
			l.literal()
			return operand{kind: 's'}, "", false
		case c == '<' && l.i+1 < len(b) && b[l.i+1] == '<':
			l.dict()
			return operand{kind: 'o'}, "", false
		case c == '<':
			j := bytes.IndexByte(b[l.i:], '>')
			if j < 0 {
				l.i = len(b)
			} else {
				l.i += j + 1
			}
			return operand{kind: 's'}, "", false
		case c == '[':
			l.i++
			return l.array(), "", false
		case c == ']' || c == '>' || c == ')' || c == '{' || c == '}':
			l.i++
		case c == '/':
			j := l.i + 1
			for j < len(b) && !isSpace(b[j]) && !isDelim(b[j]) {
				j++
			}
			o := operand{kind: 'N', name: string(b[l.i+1 : j])}
			l.i = j
			return o, "", false
		default:
			j := l.i
			for j < len(b) && !isSpace(b[j]) && !isDelim(b[j]) {
				j++
			}
			w := b[l.i:j]
			l.i = j
			if f, ok := number(w); ok {
				return operand{kind: 'n', num: f}, "", false
			}
			return operand{}, string(w), false
		}
	}
	return operand{}, "", true
}

// number parses a PDF number: digits with an optional sign and point.
func number(w []byte) (float64, bool) {
	if len(w) == 0 || len(w) > 32 {
		return 0, false
	}
	c := w[0]
	if !(c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.') {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(w), 64)
	if err != nil {
		// Writers emit things like "--5" or "1.2.3"; PDF readers take the
		// leading number.
		end := 0
		for end < len(w) && (w[end] >= '0' && w[end] <= '9' || w[end] == '.' || end == 0 && (w[end] == '-' || w[end] == '+')) {
			end++
		}
		if f, err = strconv.ParseFloat(string(w[:end]), 64); err != nil {
			return 0, false
		}
	}
	return f, true
}

func (l *lexer) literal() {
	depth := 0
	for l.i < len(l.b) {
		switch l.b[l.i] {
		case '\\':
			l.i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				l.i++
				return
			}
		}
		l.i++
	}
}

// dict skips a dictionary, nested ones included.
func (l *lexer) dict() {
	depth := 0
	for l.i+1 < len(l.b) {
		switch {
		case l.b[l.i] == '(':
			l.literal()
			continue
		case l.b[l.i] == '<' && l.b[l.i+1] == '<':
			depth++
			l.i += 2
			continue
		case l.b[l.i] == '>' && l.b[l.i+1] == '>':
			depth--
			l.i += 2
			if depth == 0 {
				return
			}
			continue
		}
		l.i++
	}
	l.i = len(l.b)
}

// array reads an array's numbers; anything else in it is skipped.
func (l *lexer) array() operand {
	o := operand{kind: 'a'}
	for l.i < len(l.b) {
		c := l.b[l.i]
		if c == ']' {
			l.i++
			return o
		}
		v, op, done := l.next()
		if done {
			break
		}
		if op == "" && v.kind == 'n' && len(o.arr) < 1<<16 {
			o.arr = append(o.arr, v.num)
		}
	}
	return o
}

// inlineImage skips an inline image's dictionary and data, BI having
// been read: to the "EI" that follows whitespace and is followed by
// whitespace or the end, after the "ID" that ends the dictionary.
func (l *lexer) inlineImage() {
	b := l.b
	id := -1
	for j := l.i; j+2 <= len(b); j++ {
		if b[j] == 'I' && b[j+1] == 'D' && (j == 0 || isSpace(b[j-1])) && (j+2 == len(b) || isSpace(b[j+2])) {
			id = j + 3
			break
		}
	}
	if id < 0 {
		l.i = len(b)
		return
	}
	for j := id; j+2 <= len(b); j++ {
		if b[j] == 'E' && b[j+1] == 'I' && (j == 0 || isSpace(b[j-1])) && (j+2 == len(b) || isSpace(b[j+2])) {
			l.i = j + 2
			return
		}
	}
	l.i = len(b)
}
