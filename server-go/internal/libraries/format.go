package libraries

import (
	"regexp"
	"strings"
)

// Index formats (indexFormat.ts): which GUFI wrote an index, by the columns
// of its top directory's summary table.
const (
	FormatCurrent = "current" // GUFI 0.8.0 or later (has dev)
	FormatEarlier = "earlier" // June to October 2026, reads as it did
	FormatOlder   = "older"   // before June 2026, with rollupscore
)

// FormatRecord is what one library's index is known to be. RolledUp is set
// for "older" only.
type FormatRecord struct {
	Format   string `json:"format"`
	RolledUp *bool  `json:"rolledUp,omitempty"`
}

// SummaryColumnsSQL lists the top directory's summary columns.
const SummaryColumnsSQL = "SELECT group_concat(name) FROM pragma_table_info('summary');"

// FormatOfColumns is formatOfColumns.
func FormatOfColumns(columns string) string {
	cols := map[string]bool{}
	for _, c := range strings.Split(columns, ",") {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			cols[c] = true
		}
	}
	if !cols["totfiles"] || !cols["isroot"] {
		return ""
	}
	switch {
	case cols["rollupscore"]:
		return FormatOlder
	case cols["isrolledup"] && cols["dev"]:
		return FormatCurrent
	case cols["isrolledup"]:
		return FormatEarlier
	}
	return ""
}

// FormatGap is formatGap: why a search covers an index only in part, or "".
func FormatGap(r *FormatRecord) string {
	if r != nil && r.Format == FormatOlder && r.RolledUp != nil && *r.RolledUp {
		return "written by a GUFI before 0.8.0 and rolled up, so the files of the folders it rolled up are listed more than once and counts and sizes over them are too high; rebuild it with GUFI 0.8.0 or later"
	}
	return ""
}

// FormatNote is formatNote: what Settings says of an index's format; nil
// when there is nothing to say.
func FormatNote(r *FormatRecord) *string {
	g := FormatGap(r)
	if g == "" {
		return nil
	}
	s := strings.ToUpper(g[:1]) + g[1:] + "."
	return &s
}

var (
	noDev    = regexp.MustCompile(`(?i)no such column:\s*dev\b`)
	noRollup = regexp.MustCompile(`(?i)no such column:\s*(canrollup|isrolledup)\b`)
)

// FormatFailure is formatFailure: a query's failure on an earlier format.
func FormatFailure(message string, r *FormatRecord) string {
	if r == nil || r.Format == FormatCurrent {
		return ""
	}
	if noDev.MatchString(message) {
		return "this index was written by a GUFI before 0.8.0 and has no dev column; rebuild it with GUFI 0.8.0 or later to add one"
	}
	if noRollup.MatchString(message) {
		return "this index was written by a GUFI before 0.8.0; rebuild it with GUFI 0.8.0 or later"
	}
	return ""
}

func validFormat(r *FormatRecord) *FormatRecord {
	if r == nil || (r.Format != FormatCurrent && r.Format != FormatEarlier && r.Format != FormatOlder) {
		return nil
	}
	return &FormatRecord{Format: r.Format, RolledUp: r.RolledUp}
}
