package xls

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func textOf(t *testing.T, b []byte, maxText int) string {
	t.Helper()
	s, err := Text(bytes.NewReader(b), int64(len(b)), maxText)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	return s
}

func lines(ls ...string) string { return strings.Join(ls, "\n") }

func cp1251(s string) string {
	b, err := charmap.Windows1251.NewEncoder().String(s)
	if err != nil {
		panic(err)
	}
	return b
}

type synthCase struct {
	wb   workbookDef
	want string
}

// synthCases are workbooks built record by record, each with the text
// it must give. Run with XLS_SYNTH_OUT set, TestSynthetic also writes
// them out so the conformance run can check them against xlrd.
func synthCases() map[string]synthCase {
	long := strings.Repeat("abcdefghij", 12)
	return map[string]synthCase{
		// Shared strings split across CONTINUE records every 23 bytes,
		// with the width flag flipping at each split, rich-text runs and
		// extension blocks that are themselves split, and a surrogate
		// pair cut between two records.
		"sst_continue": {
			wb: workbookDef{
				biff8: true, codepage: 1200,
				sst: sstRecords([]sstString{
					{s: "plain ascii"},
					{s: long},
					{s: "日本語テキスト and ascii"},
					{s: "rich text here", runs: 3, ext: 10},
					{s: "  padded  "},
					{s: ""},
					{s: "rocket 🚀🚀🚀🚀 emoji"},
					{s: "after the rocket"},
				}, 23, true),
				sheets: []sheetDef{{name: "Strings", recs: cat(
					labelSST(0, 0, 15, 0), labelSST(0, 1, 15, 1),
					labelSST(1, 0, 15, 2), labelSST(1, 2, 15, 3),
					labelSST(2, 0, 15, 4), labelSST(2, 1, 15, 5), labelSST(2, 2, 15, 6),
					labelSST(3, 0, 15, 7),
				)}},
			},
			want: lines("Sheet: Strings",
				"plain ascii\t"+long,
				"日本語テキスト and ascii\t\trich text here",
				"padded\t\trocket 🚀🚀🚀🚀 emoji",
				"after the rocket"),
		},
		// Cached formula results: number, text from STRING (plain, split
		// over CONTINUE after a SHRFMLA, UTF-16 after an ARRAY, after a
		// TABLE), boolean, error, empty text, a text result whose STRING
		// is missing, and a number in a date format.
		"formulas": {
			wb: workbookDef{
				biff8: true, codepage: 1200,
				xfFmts: []int{0, 14},
				sheets: []sheetDef{{name: "F", recs: cat(
					formula(0, 0, cellXF(0), fNum(42)),
					formula(1, 0, cellXF(0), fSpecial(0, 0)), stringRecords("hello world", 100, false),
					formula(2, 0, cellXF(0), fSpecial(0, 0)), rec(recShrFmla, make([]byte, 10), []byte{0x1E, 1, 0}),
					stringRecords(long, 30, true),
					formula(3, 0, cellXF(0), fSpecial(0, 0)), rec(recArray, make([]byte, 14), []byte{0x1E, 1, 0}),
					stringRecords("数式の結果", 100, false),
					formula(4, 0, cellXF(0), fSpecial(0, 0)), rec(recTable, make([]byte, 16)),
					stringRecords("  after table  ", 100, false),
					formula(5, 0, cellXF(0), fSpecial(1, 1)),
					formula(5, 1, cellXF(0), fSpecial(1, 0)),
					formula(6, 0, cellXF(0), fSpecial(2, 0x07)),
					formula(6, 1, cellXF(0), fNum(7)),
					formula(7, 0, cellXF(0), fSpecial(3, 0)),
					formula(7, 1, cellXF(0), fNum(8)),
					formula(9, 0, cellXF(1), fNum(46333.5)),
					formula(10, 0, cellXF(0), fNum(0.30000000000000004)),
				)}},
			},
			want: lines("Sheet: F",
				"42", "hello world", long, "数式の結果", "after table",
				"TRUE\tFALSE", "\t7", "\t8", "2026-11-07 12:00:00", "0.3"),
		},
		// Damage xlrd rejects the whole workbook for: a text-result
		// FORMULA whose STRING record is missing (an empty cell here), a
		// shared-string index past the table (empty), and rich-text runs
		// cut by CONTINUE records.
		"formula_no_string": {
			wb: workbookDef{
				biff8: true,
				sst: sstRecords([]sstString{
					{s: "rich text here", runs: 3, ext: 10, splitRuns: true},
					{s: "next"},
				}, 11, true),
				sheets: []sheetDef{{name: "F", recs: cat(
					formula(0, 0, 15, fSpecial(0, 0)),
					number(0, 1, 15, 9),
					labelSST(1, 0, 15, 0), labelSST(1, 1, 15, 1), labelSST(1, 2, 15, 99),
				)}},
			},
			want: lines("Sheet: F", "\t9", "rich text here\tnext"),
		},
		// Date detection through XF and FORMAT, and the 1900 system's
		// serial 60.
		"dates_1900": {
			wb: workbookDef{
				biff8: true, codepage: 1200,
				formats: [][2]any{
					{15, "0.00"}, // a FORMAT record overrides built-in 15 (d-mmm-yy)
					{164, "yyyy-mm-dd"},
					{165, `"Date "0`},
					{166, "[Red]dd/mm/yyyy"},
					{167, `\d\a\y 0`},
					{168, "0.00E+00"},
					{169, "[$-409]h:mm AM/PM"},
					{170, "[h]:mm:ss"},
					{171, "mm:ss.0"},
					{172, "General"},
					{173, `yyyy"年"m"月"d"日"`},
					{174, "#,##0.00_);[Red](#,##0.00)"},
				},
				//         0   1   2   3   4   5   6    7    8    9    10   11   12   13   14   15   16   17   18
				xfFmts: []int{0, 14, 22, 45, 46, 47, 30, 200, 15, 164, 165, 166, 167, 168, 169, 170, 171, 172, 173, 174},
				sheets: []sheetDef{{name: "Dates", recs: cat(
					number(0, 0, cellXF(1), 46333), number(0, 1, cellXF(2), 46333.5734375),
					number(1, 0, cellXF(1), 59), number(1, 1, cellXF(1), 60), number(1, 2, cellXF(1), 61),
					number(2, 0, cellXF(2), 0.5), number(2, 1, cellXF(3), 0.000005787037037037037),
					number(2, 2, cellXF(4), 1.5), number(2, 3, cellXF(5), 0.25),
					number(3, 0, cellXF(6), 46333), number(3, 1, cellXF(7), 46333), number(3, 2, cellXF(8), 46333),
					number(4, 0, cellXF(9), 46333), number(4, 1, cellXF(10), 46333), number(4, 2, cellXF(11), 46333),
					number(4, 3, cellXF(12), 46333), number(4, 4, cellXF(13), 46333),
					number(5, 0, cellXF(14), 0.75), number(5, 1, cellXF(15), 2.25), number(5, 2, cellXF(16), 0.0001),
					number(5, 3, cellXF(17), 46333), number(5, 4, cellXF(18), 46333), number(5, 5, cellXF(19), 46333),
					number(6, 0, cellXF(1), 0), number(6, 1, cellXF(1), 1), number(6, 2, cellXF(1), 2958466),
					rkRec(6, 3, cellXF(1), 46333<<2|2),
				)}},
			},
			want: lines("Sheet: Dates",
				"2026-11-07\t2026-11-07 13:45:45",
				"1900-02-28\t1900-02-28\t1900-03-01",
				"12:00:00\t00:00:00.500000\t1900-01-01 12:00:00\t06:00:00",
				"2026-11-07\t46333\t46333",
				"2026-11-07\t46333\t2026-11-07\t46333\t46333",
				"18:00:00\t1900-01-02 06:00:00\t00:00:08.640000\t46333\t2026-11-07\t46333",
				"1899-12-31\t1900-01-01\t2958466.0\t2026-11-07"),
		},
		// The 1904 system (DATEMODE 1), as old Mac workbooks use.
		"dates_1904": {
			wb: workbookDef{
				biff8: true, codepage: 1200, date1904: true,
				xfFmts: []int{14, 22},
				sheets: []sheetDef{{name: "Mac", recs: cat(
					number(0, 0, cellXF(0), 0), number(0, 1, cellXF(0), 1), number(0, 2, cellXF(0), 60),
					number(1, 0, cellXF(0), 44871), number(1, 1, cellXF(1), 44871.75), number(1, 2, cellXF(1), 0.5),
				)}},
			},
			want: lines("Sheet: Mac",
				"1904-01-01\t1904-01-02\t1904-03-01",
				"2026-11-07\t2026-11-07 18:00:00\t12:00:00"),
		},
		// RK in its four forms, MULRK, NUMBER edge values, BOOLERR,
		// blanks, gaps, an empty row and a cell written twice.
		"numbers": {
			wb: workbookDef{
				biff8: true, codepage: 1200,
				sst: sstRecords([]sstString{{s: "second write"}}, 8224, false),
				sheets: []sheetDef{{name: "N", recs: cat(
					rkRec(0, 0, 15, 42<<2|2), rkRec(0, 1, 15, 1234<<2|3),
					rkRec(0, 2, 15, 0x3FF80000), rkRec(0, 3, 15, 0x3FF80001),
					rkRec(0, 4, 15, 0xFFFFFFEE), // -5 as a 30-bit integer
					rec(recMulRK, p16(1), p16(1), p16(15), p32(1<<2|2), p16(15), p32(2<<2|2), p16(15), p32(3<<2|2), p16(3)),
					number(2, 0, 15, 0.30000000000000004), number(2, 1, 15, 1e20), number(2, 2, 15, negZero()),
					number(2, 3, 15, 1.0/3), number(2, 4, 15, 1e-7), number(2, 5, 15, 123456.789),
					number(2, 6, 15, 1e15+0.5), number(2, 7, 15, 1e16), number(2, 8, 15, -2.5e-300),
					rec(recBlank, p16(3), p16(0), p16(15)),
					rec(recMulBlank, p16(3), p16(1), p16(15), p16(15), p16(2)),
					boolErr(4, 0, 1, 0), boolErr(4, 1, 0, 0), boolErr(4, 2, 0x07, 1), boolErr(4, 3, 1, 0),
					number(5, 5, 15, 5),
					number(6, 0, 15, 1), labelSST(6, 0, 15, 0),
				)}},
			},
			want: lines("Sheet: N",
				"42\t12.34\t1.5\t0.015\t-5",
				"\t1\t2\t3",
				"0.3\t100000000000000000000\t0\t0.333333333333333\t1e-07\t123456.789\t1e+15\t10000000000000000\t-2.5e-300",
				"TRUE\tFALSE\t\tTRUE",
				"\t\t\t\t\t5",
				"second write"),
		},
		// BIFF5 (Excel 5.0/95): a "Book" stream, 8-bit strings in the
		// CODEPAGE (Windows-1251 here), LABEL cells, and a formula's text
		// result in a BIFF5 STRING record split by CONTINUE.
		"biff5_cp1251": {
			wb: workbookDef{
				biff8: false, codepage: 1251,
				formats: [][2]any{{164, "dd.mm.yyyy"}},
				xfFmts:  []int{164},
				sheets: []sheetDef{{name: cp1251("Лист1"), recs: cat(
					rec(recLabel, p16(0), p16(0), p16(15), p16(len(cp1251("Привет, мир"))), []byte(cp1251("Привет, мир"))),
					rkRec(0, 1, 15, 7<<2|2),
					number(1, 0, cellXF(0), 46333),
					formula(2, 0, 15, fSpecial(0, 0)),
					rec(recString, p16(len(cp1251("Итого по разделу"))), []byte(cp1251("Итого"))),
					rec(recContinue, []byte(cp1251(" по разделу"))),
				)}},
			},
			want: lines("Sheet: Лист1", "Привет, мир\t7", "2026-11-07", "Итого по разделу"),
		},
		// Several sheets: a chart sheet is skipped, a chart embedded in a
		// worksheet (a nested BOF..EOF) is skipped, an empty worksheet
		// still gets its line.
		"sheets": {
			wb: workbookDef{
				biff8: true, codepage: 1200,
				sheets: []sheetDef{
					{name: "First", recs: cat(
						number(0, 0, 15, 1),
						bof(true, 0x0020), number(0, 0, 15, 999), number(5, 5, 15, 999), rec(recEOF),
						number(1, 0, 15, 2),
					)},
					{name: "Chart1", kind: 2, recs: number(0, 0, 15, 999)},
					{name: "Second ü", recs: number(0, 2, 15, 3)},
					{name: "Empty"},
				},
			},
			want: lines("Sheet: First", "1", "2", "Sheet: Second ü", "\t\t3", "Sheet: Empty"),
		},
	}
}

func negZero() float64 { z := 0.0; return -z }

func TestSynthetic(t *testing.T) {
	out := os.Getenv("XLS_SYNTH_OUT")
	for name, c := range synthCases() {
		t.Run(name, func(t *testing.T) {
			b := c.wb.file()
			if out != "" {
				if err := os.WriteFile(filepath.Join(out, "synth_"+name+".xls"), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := textOf(t, b, 0); got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

func TestBareStream(t *testing.T) {
	c := synthCases()["numbers"]
	s := c.wb.stream()
	if got := textOf(t, s, 0); got != c.want {
		t.Errorf("bare BIFF stream: got\n%s", got)
	}
}

func TestMaxText(t *testing.T) {
	var recs []byte
	for r := 0; r < 100; r++ {
		recs = append(recs, number(r, 0, 15, 12345)...) // 6 characters a line with its break
	}
	wb := workbookDef{biff8: true, sheets: []sheetDef{{name: "A", recs: recs}, {name: "B", recs: number(0, 0, 15, 1)}}}
	got := textOf(t, wb.file(), 30)
	// Lines are added until the count reaches 30: five of them.
	if want := lines("Sheet: A", "12345", "12345", "12345", "12345", "12345"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := textOf(t, wb.file(), 0); strings.Count(got, "\n") != 102 {
		t.Errorf("no cap: %d lines", strings.Count(got, "\n")+1)
	}
}

func TestErrors(t *testing.T) {
	enc := workbookDef{biff8: true, extra: rec(recFilepass, make([]byte, 54)), sheets: []sheetDef{{name: "S"}}}
	b := enc.file()
	if _, err := Text(bytes.NewReader(b), int64(len(b)), 0); !errors.Is(err, ErrEncrypted) {
		t.Errorf("FILEPASS: %v", err)
	}
	for _, in := range [][]byte{nil, []byte("hello"), []byte("PK\x03\x04 a zip, not a workbook"), oleMagic, append(append([]byte{}, oleMagic...), make([]byte, 600)...)} {
		if s, err := Text(bytes.NewReader(in), int64(len(in)), 0); err == nil || s != "" {
			t.Errorf("%q: got %q, %v", in, s, err)
		}
	}
	// An OLE file without a workbook stream.
	other := cfb(make([]byte, 100), "WordDocument")
	if _, err := Text(bytes.NewReader(other), int64(len(other)), 0); !errors.Is(err, ErrNotXLS) {
		t.Errorf("no Workbook stream: %v", err)
	}
}

// TestTruncated cuts every synthetic stream at every length and flips
// bytes through it, and reads it without Text's recover: the parser
// itself must not panic.
func TestTruncated(t *testing.T) {
	for name, c := range synthCases() {
		s := c.wb.stream()
		for n := 0; n <= len(s); n++ {
			mustNotPanic(t, name, s[:n])
		}
		for i := 0; i < len(s); i++ {
			m := append([]byte(nil), s...)
			m[i] ^= 0xFF
			mustNotPanic(t, name, m)
			m[i] = 0x7F
			mustNotPanic(t, name, m)
		}
	}
}

func mustNotPanic(t *testing.T, name string, stream []byte) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("%s: panic on %d-byte stream: %v", name, len(stream), p)
		}
	}()
	_, _ = streamText(stream, 0)
}

// FuzzStream feeds arbitrary BIFF streams to the parser, without Text's
// recover.
func FuzzStream(f *testing.F) {
	for _, c := range synthCases() {
		f.Add(c.wb.stream())
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = streamText(b, 1000)
	})
}

// FuzzFile feeds arbitrary files through the OLE layer as well, also
// without Text's recover, so a panic in mscfb shows too.
func FuzzFile(f *testing.F) {
	for _, c := range synthCases() {
		f.Add(c.wb.file())
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		stream, err := workbookStream(bytes.NewReader(b), int64(len(b)))
		if err == nil {
			_, _ = streamText(stream, 1000)
		}
	})
}
