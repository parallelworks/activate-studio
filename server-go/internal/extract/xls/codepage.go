package xls

import (
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// decoder returns the decoder for a BIFF5 CODEPAGE value, or nil for
// Latin-1. BIFF8 strings are UTF-16 and do not use it. The special values
// are xlrd's: 32768 and 10000 are Mac Roman, 32769 is Windows-1252, 367
// is ASCII (read as Latin-1, which agrees on ASCII). A workbook without a
// CODEPAGE record, or with one Go has no table for, reads as Latin-1, as
// xlrd does without a codepage.
func decoder(cp uint16) *encoding.Decoder {
	var e encoding.Encoding
	switch cp {
	case 437:
		e = charmap.CodePage437
	case 850:
		e = charmap.CodePage850
	case 852:
		e = charmap.CodePage852
	case 855:
		e = charmap.CodePage855
	case 858:
		e = charmap.CodePage858
	case 860:
		e = charmap.CodePage860
	case 862:
		e = charmap.CodePage862
	case 863:
		e = charmap.CodePage863
	case 865:
		e = charmap.CodePage865
	case 866:
		e = charmap.CodePage866
	case 874:
		e = charmap.Windows874
	case 932:
		e = japanese.ShiftJIS
	case 936:
		e = simplifiedchinese.GBK
	case 949:
		e = korean.EUCKR
	case 950:
		e = traditionalchinese.Big5
	case 1250:
		e = charmap.Windows1250
	case 1251:
		e = charmap.Windows1251
	case 1252, 32769:
		e = charmap.Windows1252
	case 1253:
		e = charmap.Windows1253
	case 1254:
		e = charmap.Windows1254
	case 1255:
		e = charmap.Windows1255
	case 1256:
		e = charmap.Windows1256
	case 1257:
		e = charmap.Windows1257
	case 1258:
		e = charmap.Windows1258
	case 10000, 32768:
		e = charmap.Macintosh
	case 10007:
		e = charmap.MacintoshCyrillic
	default:
		return nil
	}
	return e.NewDecoder()
}

// decode8 turns 8-bit BIFF5 text into a string through the workbook's
// codepage.
func (b *book) decode8(p []byte) string {
	if b.dec != nil {
		if s, err := b.dec.Bytes(p); err == nil {
			return string(s)
		}
	}
	return latin1(p)
}

func latin1(p []byte) string {
	var sb strings.Builder
	sb.Grow(len(p))
	for _, c := range p {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}
