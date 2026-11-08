package pdf

import "testing"

func FuzzFontProgramParsers(f *testing.F) {
	f.Add(minimalTrueTypeCMap())
	f.Add(minimalCFF())
	f.Add([]byte("/Encoding 256 array dup 65 /A put"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = parseSFNTCMap(data)
		_, _ = parseCFFEncoding(data)
		_ = parseType1Encoding(data)
	})
}

func FuzzCMapParser(f *testing.F) {
	f.Add([]byte("1 begincodespacerange <00> <FF> endcodespacerange"))
	f.Add([]byte("1 beginbfchar <01> <0041> endbfchar"))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = parseCMap(data, nil, map[string]bool{})
	})
}
