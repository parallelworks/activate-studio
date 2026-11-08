package chat

import "github.com/parallelworks/activate-studio/server-go/internal/jsval"

func jsTrim(s string) string { return jsval.Trim(s) }

// isJSSpace is \s in a JavaScript regular expression.
func isJSSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
