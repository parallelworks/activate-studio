package xls

import (
	"math"
	"testing"
)

// The expected values below were printed by xlrd 2.0.2's
// is_date_format_string and by Python's str(int(v)) / '%.15g' / repr.

func TestIsDateFormat(t *testing.T) {
	cases := []struct {
		f    string
		date bool
	}{
		{"yyyy-mm-dd", true},
		{"0.00", false},
		{"General", false},
		{"@", false},
		{"0.00E+00", false},
		{"##0.0E+0", false},
		{"[h]:mm:ss", true},
		{"mm:ss.0", true},
		{"\"Date \"0", false},
		{"0\\d", false},
		{"[Red]dd/mm/yyyy", true},
		{"[$-409]h:mm AM/PM", true},
		{"#,##0.00_);[Red](#,##0.00)", false},
		{"yyyy\"年\"m\"月\"d\"日\"", true},
		{"0.00\" days\"", false},
		{"d", true},
		{"h:mm;@", true},
		{"dd/mm/yyyy;dd/mm/yyyy;_-* \"\"??_-;_-@_-", true},
		{"[$-F800]dddd, mmmm dd, yyyy", true},
		{"# ?/?", false},
		{"0%", false},
		{"$#,##0", false},
		{"\"yes\";\"yes\";\"no\"", false},
		{"[=1]\"one\";0", false},
		{"mmm-yy", true},
		{"[ss]", false},
		{"_(* #,##0_)", false},
		{"\\h0", false},
		{"*m0", false},
		{"[unclosed m", true},
	}
	for _, c := range cases {
		if got := isDateFormat(c.f); got != c.date {
			t.Errorf("isDateFormat(%q) = %v, want %v", c.f, got, c.date)
		}
	}
	for id := uint16(0); id < 200; id++ {
		want := id >= 14 && id <= 22 || id >= 27 && id <= 36 || id >= 45 && id <= 47 || id >= 50 && id <= 58 || id >= 71 && id <= 81
		if builtinDate(id) != want {
			t.Errorf("builtinDate(%d) = %v", id, !want)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	cases := []struct {
		v          float64
		num, repr_ string
	}{
		{0.3, "0.3", "0.3"},
		{1e+20, "100000000000000000000", "1e+20"},
		{math.Copysign(0, -1), "0", "-0.0"},
		{1.0 / 3, "0.333333333333333", "0.3333333333333333"},
		{1e-07, "1e-07", "1e-07"},
		{123456.789, "123456.789", "123456.789"},
		{1000000000000000.5, "1e+15", "1000000000000000.5"},
		{1e+16, "10000000000000000", "1e+16"},
		{-2.5e-300, "-2.5e-300", "-2.5e-300"},
		{0.30000000000000004, "0.3", "0.30000000000000004"},
		{2.5, "2.5", "2.5"},
		{-1.5, "-1.5", "-1.5"},
		{1e-05, "1e-05", "1e-05"},
		{1.2345678901234568e+17, "123456789012345680", "1.2345678901234568e+17"},
		{5e-324, "4.94065645841247e-324", "5e-324"},
		{12345678901234.5, "12345678901234.5", "12345678901234.5"},
		{0.00012345678901234567, "0.000123456789012346", "0.00012345678901234567"},
		{42, "42", "42.0"},
	}
	for _, c := range cases {
		if got := formatNumber(c.v); got != c.num {
			t.Errorf("formatNumber(%v) = %q, want %q", c.v, got, c.num)
		}
		if got := pyRepr(c.v); got != c.repr_ {
			t.Errorf("pyRepr(%v) = %q, want %q", c.v, got, c.repr_)
		}
	}
	if got := formatNumber(math.MaxFloat64); len(got) != 309 || got[:17] != "17976931348623157" {
		t.Errorf("formatNumber(MaxFloat64) = %q", got)
	}
}

func TestFormatDate(t *testing.T) {
	cases := []struct {
		v    float64
		d    bool
		want string
	}{
		{46333, false, "2026-11-07"},
		{46333.5734375, false, "2026-11-07 13:45:45"},
		{59, false, "1900-02-28"},
		{60, false, "1900-02-28"}, // Excel's 1900-02-29
		{61, false, "1900-03-01"},
		{1, false, "1900-01-01"},
		{0, false, "1899-12-31"},
		{0.5, false, "12:00:00"},
		{0.75, true, "18:00:00"},
		{1.5, false, "1900-01-01 12:00:00"},
		{0, true, "1904-01-01"},
		{1, true, "1904-01-02"},
		{44871, true, "2026-11-07"},
		{-1401, true, "1900-03-01"},
		{36585.0000057870370, false, "2000-02-29 00:00:00.500000"},
		{0.99999999999, false, "1900-01-01"}, // rounds up to the next day
		{2958465, false, "9999-12-31"},
		{2958466, false, "2958466.0"}, // past datetime's range
		{1e300, false, "1e+300"},
	}
	for _, c := range cases {
		if got := formatDate(c.v, c.d); got != c.want {
			t.Errorf("formatDate(%v, 1904=%v) = %q, want %q", c.v, c.d, got, c.want)
		}
	}
}

func TestPyStrip(t *testing.T) {
	if got := pyStrip("\x1c  text　\x1f\n"); got != "text" {
		t.Errorf("pyStrip = %q", got)
	}
}
