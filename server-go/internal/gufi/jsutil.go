package gufi

import (
	"strconv"
	"unicode/utf16"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

func utf16Units(s string) []uint16 { return utf16.Encode([]rune(s)) }

func utf16String(u []uint16) string { return string(utf16.Decode(u)) }

func itoa(i int) string { return strconv.Itoa(i) }

func newBadRequest(msg string) error { return apierr.New(400, "%s", msg) }
