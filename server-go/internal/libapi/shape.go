package libapi

import "regexp"

var quoted = regexp.MustCompile(`"[^"]*"`)
var opEnds = regexp.MustCompile(`^\s*(AND|OR|NOT)\b|\b(AND|OR|NOT)\s*$`)
