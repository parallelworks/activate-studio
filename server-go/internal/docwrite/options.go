package docwrite

import (
	"fmt"
	"strconv"
	"strings"
)

// Options is how a PDF or Word document is set, from the front matter at
// the top of its Markdown:
//
//	---
//	align: justify
//	orientation: landscape
//	font: Times New Roman
//	size: 11
//	margin: 1
//	spacing: 1.15
//	header: Draft
//	footer: Quarterly report
//	---
//
// Anything left out keeps its default: left-aligned body text, portrait
// US Letter, a sans-serif font at 10.5 pt (11 pt in Word), one-inch
// margins, lines 1.15 apart, a page number alone at the foot of the page.
type Options struct {
	Justify   bool
	Landscape bool
	Serif     bool    // a serif face (Times New Roman or its metric twin)
	Font      string  // the Word font name; "" for Calibri, or Times New Roman when Serif
	Size      float64 // body size in points; 0 for the default
	Margin    float64 // inches; 0 for one inch
	Spacing   float64 // line spacing, 1 to 2; 0 for the default (1.15)
	Header    string  // a line centered at the top of every page
	Footer    string  // a line centered at the bottom of every page, above its number
}

// maxRunningText is the longest header or footer taken, in characters.
const maxRunningText = 120

// OptionKeys names the front matter keys, for the tool's description.
const OptionKeys = "align (left or justify), orientation (portrait or landscape), font (a family such as Times New Roman, Arial or Calibri, or serif or sans), size (body text in points, 8 to 16), margin (inches, 0.5 to 2), spacing (line spacing, 1 to 2), header and footer (a short line of text centered at the top and bottom of every page)"

// SplitFrontMatter takes the options from Markdown that opens with a ---
// block of key: value lines, and returns the rest. Markdown without one
// comes back as it was. Unknown keys and values out of range are listed
// as problems; their defaults stand.
func SplitFrontMatter(src string) (Options, string, []string) {
	var o Options
	body := strings.TrimLeft(src, "\ufeff")
	if !strings.HasPrefix(body, "---\n") && !strings.HasPrefix(body, "---\r\n") {
		return o, src, nil
	}
	rest := body[strings.IndexByte(body, '\n')+1:]
	end := -1
	lines := strings.SplitAfter(rest, "\n")
	off := 0
	var head []string
	for _, l := range lines {
		t := strings.TrimRight(l, "\r\n")
		if t == "---" || t == "..." {
			end = off + len(l)
			break
		}
		head = append(head, t)
		off += len(l)
	}
	if end < 0 {
		return o, src, nil
	}
	// A --- block of something other than key: value lines (a horizontal
	// rule after a line of text) is not front matter.
	for _, l := range head {
		if strings.TrimSpace(l) != "" && !strings.Contains(l, ":") {
			return o, src, nil
		}
	}
	var problems []string
	for _, l := range head {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		lv := strings.ToLower(v)
		switch k {
		case "align", "alignment", "text-align", "text_align":
			switch lv {
			case "justify", "justified", "both":
				o.Justify = true
			case "left", "":
			default:
				problems = append(problems, fmt.Sprintf("align %q is not left or justify", v))
			}
		case "orientation", "layout":
			switch lv {
			case "landscape":
				o.Landscape = true
			case "portrait", "":
			default:
				problems = append(problems, fmt.Sprintf("orientation %q is not portrait or landscape", v))
			}
		case "font", "font-family", "font_family", "fontfamily":
			o.Font, o.Serif = fontChoice(v)
		case "size", "font-size", "font_size", "fontsize":
			n, err := strconv.ParseFloat(strings.TrimSuffix(lv, "pt"), 64)
			if err != nil || n < 8 || n > 16 {
				problems = append(problems, fmt.Sprintf("size %q is not 8 to 16 points", v))
				continue
			}
			o.Size = n
		case "margin", "margins":
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(lv, "in"), "\"")), 64)
			if err != nil || n < 0.5 || n > 2 {
				problems = append(problems, fmt.Sprintf("margin %q is not 0.5 to 2 inches", v))
				continue
			}
			o.Margin = n
		case "spacing", "line-spacing", "line_spacing", "linespacing", "line-height", "line_height":
			n, ok := spacingValue(lv)
			if !ok {
				problems = append(problems, fmt.Sprintf("spacing %q is not 1 to 2", v))
				continue
			}
			o.Spacing = n
		case "header", "footer":
			t := strings.Join(strings.Fields(v), " ")
			if n := len([]rune(t)); n > maxRunningText {
				problems = append(problems, fmt.Sprintf("the %s is %d characters, over the %d a page's %s holds; it was left out", k, n, maxRunningText, k))
				continue
			}
			if k == "header" {
				o.Header = t
			} else {
				o.Footer = t
			}
		case "title", "author", "date", "subject":
			// Common in front matter; the title comes from the first heading.
		default:
			problems = append(problems, fmt.Sprintf("%q is not an option (%s)", k, OptionKeys))
		}
	}
	return o, body[strings.IndexByte(body, '\n')+1+end:], problems
}

// spacingValue reads a line spacing: a number from 1 to 2, or single,
// double or 1.5 by name.
func spacingValue(lv string) (float64, bool) {
	switch lv {
	case "single":
		return 1, true
	case "double":
		return 2, true
	case "one and a half", "one-and-a-half":
		return 1.5, true
	}
	n, err := strconv.ParseFloat(strings.TrimSuffix(lv, "x"), 64)
	if err != nil || n < 1 || n > 2 {
		return 0, false
	}
	return n, true
}

// fontChoice is the Word font name for a requested family, and whether
// the PDF draws it with a serif face.
func fontChoice(v string) (string, bool) {
	l := strings.ToLower(v)
	switch {
	case l == "" || l == "sans" || l == "sans-serif":
		return "", false
	case l == "serif" || strings.Contains(l, "times") || strings.Contains(l, "georgia") || strings.Contains(l, "garamond") || strings.Contains(l, "cambria") || strings.Contains(l, "serif") && !strings.Contains(l, "sans"):
		if l == "serif" || strings.Contains(l, "serif") {
			return "Times New Roman", true
		}
		return v, true
	}
	return v, false
}

// pdfSize is the PDF body size: the option or 10.5 pt.
func (o Options) pdfSize() float64 {
	if o.Size > 0 {
		return o.Size
	}
	return bodySize
}

// pdfLine is the PDF body line height at a text size: the spacing asked
// for, as a multiple of single spacing (1.2 times the size), or the
// default, which is 1.15.
func (o Options) pdfLine(size float64) float64 {
	if o.Spacing > 0 {
		return size * 1.2 * o.Spacing
	}
	return size * bodyLine / bodySize
}

func (o Options) marginPt() float64 {
	if o.Margin > 0 {
		return o.Margin * 72
	}
	return margin
}

// describe names the options set, for the note on what was made.
func (o Options) describe() string {
	var parts []string
	if o.Justify {
		parts = append(parts, "justified")
	}
	if o.Landscape {
		parts = append(parts, "landscape")
	}
	if o.Font != "" {
		parts = append(parts, o.Font)
	} else if o.Serif {
		parts = append(parts, "serif")
	}
	if o.Size > 0 {
		parts = append(parts, strconv.FormatFloat(o.Size, 'f', -1, 64)+" pt")
	}
	if o.Margin > 0 {
		parts = append(parts, strconv.FormatFloat(o.Margin, 'f', -1, 64)+" inch margins")
	}
	if o.Spacing > 0 {
		parts = append(parts, strconv.FormatFloat(o.Spacing, 'f', -1, 64)+" line spacing")
	}
	if o.Header != "" {
		parts = append(parts, "header "+strconv.Quote(o.Header))
	}
	if o.Footer != "" {
		parts = append(parts, "footer "+strconv.Quote(o.Footer))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
