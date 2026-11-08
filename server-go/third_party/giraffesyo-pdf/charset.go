package pdf

import (
	"strings"
	"unicode/utf16"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// The predefined CJK CMaps whose codes are a character set's bytes: the
// Shift-JIS, EUC, GBK, Big5 and UHC families, and the Unicode ones
// (UCS-2, UTF-16). Without the CMap files (and the CID-to-Unicode tables
// behind them) a code's text is the character set's decoding of its
// bytes, which is what the CMap encodes; pypdf reads these fonts the same
// way. Glyph widths then fall back to the font's default width, since the
// codes' CIDs are not known.

type charset struct {
	spaces []codeSpace
	dec    *encoding.Decoder // nil: UTF-16BE
}

var (
	lowASCII = codeSpace{low: 0x00, high: 0x80, bytes: 1}
	sjisSet  = []codeSpace{lowASCII, {low: 0xA0, high: 0xDF, bytes: 1}, {low: 0x8140, high: 0x9FFC, bytes: 2}, {low: 0xE040, high: 0xFCFC, bytes: 2}}
	eucjpSet = []codeSpace{lowASCII, {low: 0x8EA0, high: 0x8EDF, bytes: 2}, {low: 0xA1A1, high: 0xFEFE, bytes: 2}, {low: 0x8FA1A1, high: 0x8FFEFE, bytes: 3}}
	gbkSet   = []codeSpace{lowASCII, {low: 0x8140, high: 0xFEFE, bytes: 2}}
	gbSet    = []codeSpace{lowASCII, {low: 0xA1A1, high: 0xFEFE, bytes: 2}}
	big5Set  = []codeSpace{lowASCII, {low: 0xA140, high: 0xFEFE, bytes: 2}}
	uhcSet   = []codeSpace{lowASCII, {low: 0x8141, high: 0xFEFE, bytes: 2}}
	ucs2Set  = []codeSpace{{low: 0x0000, high: 0xFFFF, bytes: 2}}
	utf16Set = []codeSpace{{low: 0xD800DC00, high: 0xDBFFDFFF, bytes: 4}, {low: 0x0000, high: 0xFFFF, bytes: 2}}
)

// charsetCMap is the CMap a predefined name stands for, or nil.
func charsetCMap(name string) *cmapData {
	base, vertical := strings.CutSuffix(name, "-V")
	if !vertical {
		base = strings.TrimSuffix(name, "-H")
	}
	var cs *charset
	switch {
	case strings.Contains(base, "RKSJ"):
		cs = &charset{sjisSet, japanese.ShiftJIS.NewDecoder()}
	case base == "EUC" || base == "Ext-EUC" || base == "Add-EUC":
		cs = &charset{eucjpSet, japanese.EUCJP.NewDecoder()}
	case strings.HasPrefix(base, "GBK2K"):
		cs = &charset{gbkSet, simplifiedchinese.GB18030.NewDecoder()}
	case strings.HasPrefix(base, "GBK"):
		cs = &charset{gbkSet, simplifiedchinese.GBK.NewDecoder()}
	case base == "GB-EUC" || base == "GBpc-EUC" || base == "GBT-EUC":
		cs = &charset{gbSet, simplifiedchinese.GBK.NewDecoder()}
	case strings.HasPrefix(base, "B5") || strings.HasPrefix(base, "ETen") || strings.HasPrefix(base, "HKscs-B5") || base == "CNS-EUC":
		cs = &charset{big5Set, traditionalchinese.Big5.NewDecoder()}
	case strings.HasPrefix(base, "KSC"):
		cs = &charset{uhcSet, korean.EUCKR.NewDecoder()}
	case strings.HasPrefix(base, "Uni") && strings.HasSuffix(base, "-UCS2") || strings.HasPrefix(base, "Uni") && strings.HasSuffix(base, "-UCS2-HW"):
		cs = &charset{ucs2Set, nil}
	case strings.HasPrefix(base, "Uni") && strings.HasSuffix(base, "-UTF16"):
		cs = &charset{utf16Set, nil}
	default:
		return nil
	}
	c := &cmapData{spaces: cs.spaces, charset: cs}
	if vertical {
		c.wmode = 1
	}
	return c
}

// text is a code's characters.
func (cs *charset) text(key codeKey) string {
	b := make([]byte, key.bytes)
	for i := range b {
		b[len(b)-1-i] = byte(key.value >> (8 * i))
	}
	if cs.dec == nil {
		u := make([]uint16, 0, 2)
		for i := 0; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		s := string(utf16.Decode(u))
		if strings.ContainsRune(s, '�') {
			return ""
		}
		return s
	}
	out, err := cs.dec.Bytes(b)
	if err != nil || strings.ContainsRune(string(out), '�') {
		return ""
	}
	return string(out)
}
