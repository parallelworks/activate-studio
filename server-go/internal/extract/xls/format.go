package xls

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// builtinDate reports whether a built-in number format id is a date or
// time format. The ranges are xlrd's (formatting.py fmt_code_ranges):
// 14-22 and 45-47 everywhere, 27-36 and 50-58 for the CJK locales and
// 71-81 for Thai. A FORMAT record for the same id takes precedence.
func builtinDate(id uint16) bool {
	switch {
	case id >= 14 && id <= 22, id >= 27 && id <= 36, id >= 45 && id <= 47,
		id >= 50 && id <= 58, id >= 71 && id <= 81:
		return true
	}
	return false
}

// isDateFormat follows xlrd's is_date_format_string: drop quoted text,
// characters escaped by \, _ or *, the characters $-+/(): and space, and
// then any [bracketed] section (colors, locales, elapsed-time markers).
// What remains is a date format when it has more of y m d h s (either
// case) than of 0 # ?, each counted with the same weight.
func isDateFormat(f string) bool {
	var b strings.Builder
	state := 0
	for _, c := range f {
		switch state {
		case 0:
			switch {
			case c == '"':
				state = 1
			case c == '\\' || c == '_' || c == '*':
				state = 2
			case strings.ContainsRune("$-+/(): ", c):
			default:
				b.WriteRune(c)
			}
		case 1:
			if c == '"' {
				state = 0
			}
		case 2:
			state = 0
		}
	}
	s := stripBrackets(b.String())
	switch s {
	case "0.00E+00", "##0.0E+0", "General", "GENERAL", "general", "@":
		return false
	}
	dates, nums := 0, 0
	for _, c := range s {
		switch c {
		case 'y', 'm', 'd', 'h', 's', 'Y', 'M', 'D', 'H', 'S':
			dates++
		case '0', '#', '?':
			nums++
		}
	}
	return dates > nums
}

// stripBrackets removes every [...] run, as re.sub(r'\[[^]]*\]', ”, s)
// does: an unclosed [ is kept.
func stripBrackets(s string) string {
	if !strings.Contains(s, "[") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '[')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i+1:], ']')
		if j < 0 {
			break
		}
		b.WriteString(s[:i])
		s = s[i+1+j+1:]
	}
	b.WriteString(s)
	return b.String()
}

var (
	epoch1900      = time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	epoch1900Minus = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	epoch1904      = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
)

// formatDate renders a date serial the way enrich.py prints
// xlrd.xldate_as_datetime: 'YYYY-MM-DD' at midnight, else
// 'YYYY-MM-DD HH:MM:SS' with '.ffffff' when there are milliseconds.
// The serial is split as xlrd splits it (whole days toward zero, the
// fraction rounded half to even to the millisecond). In the 1900 system
// serials from 60 on count from 1899-12-30, which absorbs Excel's
// phantom 1900-02-29, so 60 and 59 both print 1900-02-28 as in xlrd.
//
// One deliberate difference: a pure time (0 < serial < 1) prints as
// 'HH:MM:SS', where xlrd prefixes the epoch day (1899-12-31 or
// 1904-01-01), which is not a date the sheet shows.
//
// A serial outside datetime's range (years 1 to 9999) prints as Python's
// str(float), as enrich.py's fallback does.
func formatDate(v float64, date1904 bool) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 4e6 {
		return pyRepr(v)
	}
	epoch := epoch1900
	switch {
	case date1904:
		epoch = epoch1904
	case v >= 60:
		epoch = epoch1900Minus
	}
	days := math.Trunc(v)
	ms := math.RoundToEven((v - days) * 86400000.0)
	t := epoch.AddDate(0, 0, int(days)).Add(time.Duration(ms) * time.Millisecond)
	if t.Year() < 1 || t.Year() > 9999 {
		return pyRepr(v)
	}
	clock := t.Format("15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		clock += "." + pad6(us)
	}
	if v > 0 && v < 1 && ms < 86400000 {
		return clock
	}
	date := pad4(t.Year()) + t.Format("-01-02")
	if clock == "00:00:00" {
		return date
	}
	return date + " " + clock
}

func pad4(y int) string {
	s := strconv.Itoa(y)
	return strings.Repeat("0", max(0, 4-len(s))) + s
}

func pad6(n int) string {
	s := strconv.Itoa(n)
	return strings.Repeat("0", max(0, 6-len(s))) + s
}

// formatNumber renders a number as enrich.py does: str(int(v)) when it
// is integral, else '%.15g', which is what Excel and LibreOffice show.
func formatNumber(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	case v == math.Trunc(v):
		if v == 0 {
			return "0" // int(-0.0) is 0
		}
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return pyG15(v)
}

// pyG15 is Python's '%.15g'. Go's 'g' with precision 15 switches to an
// exponent at the same points (exponent < -4 or >= 15), drops trailing
// zeros and writes at least two exponent digits, as Python does.
func pyG15(v float64) string {
	return strconv.FormatFloat(v, 'g', 15, 64)
}

// pyRepr is Python's repr(float): the shortest round-trip digits, in
// positional notation for exponents from -4 to 15 and with '.0' on
// integral values, else d.ddde+XX.
func pyRepr(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(v, 'e', -1, 64)
	mant, exps, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(exps)
	if exp < -4 || exp >= 16 {
		sign := "+"
		if exp < 0 {
			sign, exp = "-", -exp
		}
		e := strconv.Itoa(exp)
		if len(e) < 2 {
			e = "0" + e
		}
		return mant + "e" + sign + e
	}
	s = strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// pyStrip is Python's str.strip(): it trims the characters str.isspace
// accepts, which include the C0 separators 0x1C-0x1F that Go's
// unicode.IsSpace leaves alone.
func pyStrip(s string) string {
	return strings.TrimFunc(s, pySpace)
}

func pySpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0,
		0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
