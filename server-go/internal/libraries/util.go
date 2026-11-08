package libraries

import "unicode/utf16"

func utf16Units(s string) []uint16   { return utf16.Encode([]rune(s)) }
func utf16String(u []uint16) string { return string(utf16.Decode(u)) }
