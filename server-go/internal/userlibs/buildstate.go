package userlibs

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// What remoteBuild.ts reads of a build's output.

var kvKey = regexp.MustCompile(`^[a-z_]+$`)

// keyValues is key=value lines as a record; later keys win.
func keyValues(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(line, "=")
		if i > 0 && kvKey.MatchString(line[:i]) {
			out[line[:i]] = jsTrim(line[i+1:])
		}
	}
	return out
}

var digits = regexp.MustCompile(`^\d+$`)

// buildStatus is parseStatus's answer.
type buildStatus struct {
	// State is missing (no such build), gone (it stopped without
	// finishing), running or finished.
	State string
	Phase string
	Exit  *float64
	Error *string
	// Dirs are the folders indexed so far, while gufi_dir2index runs.
	Dirs     *float64
	Manifest *jsonv.Object
	// manifestSet: the manifest parsed, whatever it was (JSON.parse's value).
	manifestSet bool
	manifestAny any
}

func parseStatus(text string) buildStatus {
	parts := strings.Split(text, "\n__MANIFEST__\n")
	kv := keyValues(parts[0])
	st := buildStatus{Phase: kv["phase"]}
	if len(parts) > 1 && parts[1] != "" {
		if v, err := jsonv.DecodeString(parts[1]); err == nil {
			st.manifestSet, st.manifestAny = true, v
			st.Manifest = jsonv.Obj(v)
		}
	}
	if e, ok := kv["exit"]; ok && digits.MatchString(e) {
		f, _ := strconv.ParseFloat(e, 64)
		st.Exit = &f
	}
	switch {
	case kv["state"] == "missing":
		st.State = "missing"
	case kv["state"] == "gone":
		st.State = "gone"
	case st.Exit != nil:
		st.State = "finished"
	default:
		st.State = "running"
	}
	if e := kv["error"]; e != "" {
		st.Error = &e
	} else if kv["state"] == "gone" {
		msg := "the build stopped without finishing"
		if kv["log"] != "" {
			msg += ": " + kv["log"]
		}
		st.Error = &msg
	}
	if d, ok := kv["dirs"]; ok && digits.MatchString(d) {
		f, _ := strconv.ParseFloat(d, 64)
		st.Dirs = &f
	}
	return st
}

// json is the status as Node's object reads.
func (st buildStatus) json() *jsonv.Object {
	var manifest any
	if st.manifestSet {
		manifest = st.manifestAny
	}
	return jsonv.NewObject("state", st.State, "phase", st.Phase, "exit", fptr(st.Exit), "error", sptr(st.Error),
		"dirs", fptr(st.Dirs), "manifest", manifest)
}

func fptr(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func sptr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func get(o *jsonv.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}

// orZero is Number(v) || 0.
func orZero(v any) float64 {
	f := number(v)
	if math.IsNaN(f) {
		return 0
	}
	return f
}

// vectorsMade is whether a build's vectors are there: embed.py goes on
// past a table it could not write, so the index is asked, through vectorRows.
func vectorsMade(m *jsonv.Object) bool {
	chunks := orZero(get(jsonv.Obj(get(m, "embed")), "chunks"))
	rows, isNum := get(m, "vectorRows").(float64)
	return get(m, "vectors") == true && !(chunks > 0 && isNum && rows == 0)
}

// localeNumber is n.toLocaleString('en-US'): grouped, at most three
// decimals.
func localeNumber(n float64) string {
	if math.IsNaN(n) {
		return "NaN"
	}
	if math.IsInf(n, 0) {
		if n > 0 {
			return "∞"
		}
		return "-∞"
	}
	r := math.Round(n*1000) / 1000
	if r == math.Trunc(r) && math.Abs(r) < 1e15 {
		return thousands(int64(r))
	}
	s := strconv.FormatFloat(math.Abs(r), 'f', 3, 64)
	whole, frac, _ := strings.Cut(s, ".")
	frac = strings.TrimRight(frac, "0")
	w, _ := strconv.ParseInt(whole, 10, 64)
	out := thousands(w)
	if frac != "" {
		out += "." + frac
	}
	if r < 0 {
		out = "-" + out
	}
	return out
}

func plural(n float64, one string) string {
	if n == 1 {
		return localeNumber(n) + " " + one + " has"
	}
	return localeNumber(n) + " " + one + "s have"
}

func isStr(v any) (string, bool) { s, ok := v.(string); return s, ok }

// buildGaps is what a finished build could not read or embed, in words for
// the library's row, from its manifest. A manifest from an earlier version
// says nothing of this, and gives no lines.
func buildGaps(m *jsonv.Object) []string {
	out := []string{}
	depth, _ := isStr(get(m, "depth"))
	text := jsonv.Obj(get(m, "text"))
	if why, ok := isStr(get(m, "textWhy")); ok && why != "" {
		out = append(out, "No text in this index: "+why)
	}
	if docs := jsonv.Obj(get(text, "documents")); docs != nil {
		for _, suffix := range docs.Keys() {
			n := jsonv.Obj(get(docs, suffix))
			empty := orZero(get(n, "noText"))
			if empty == 0 {
				continue
			}
			if suffix != ".pdf" {
				out = append(out, plural(empty, suffix+" file")+" no text")
				continue
			}
			reader := get(text, "pdfReader")
			var why string
			switch {
			case !jsonv.Truthy(reader):
				why = "this system has no PDF reader"
			case jsonv.Truthy(get(text, "ocr")):
				why = jsonv.String(reader) + " and OCR found none, as with a damaged or protected file"
			case depth == "text":
				why = jsonv.String(reader) + " found none, as with scanned pages, and this depth runs no OCR"
			default:
				why = jsonv.String(reader) + " found none, as with scanned pages, and this system has no tesseract"
			}
			out = append(out, plural(empty, "PDF")+" no text: "+why)
		}
	}
	if strings.HasPrefix(depth, "meaning-") {
		embed := jsonv.Obj(get(m, "embed"))
		missing := orZero(get(m, "pdfWords")) - orZero(get(m, "pdfVectors"))
		firstError := get(embed, "firstError")
		if why, ok := isStr(get(m, "vectorsWhy")); ok && why != "" {
			out = append(out, "No search by meaning: "+why)
		} else if !vectorsMade(m) {
			line := "No search by meaning: no vectors were written"
			if jsonv.Truthy(firstError) {
				line += ": " + jsonv.String(firstError)
			}
			out = append(out, line)
		} else {
			if missing > 0 {
				out = append(out, plural(missing, "PDF")+" text but no vectors: too little of it reads as prose")
			}
			if errs := get(embed, "errors"); jsonv.Truthy(errs) {
				why := "the embedding pass could not write them"
				if firstError != nil {
					why = jsonv.String(firstError)
				}
				out = append(out, plural(number(errs), "passage or folder")+" no vectors: "+why)
			}
		}
	}
	return out
}

// phaseLabel is what a phase reads as, for the progress line.
var phaseLabel = map[string]string{
	"prepare": "Preparing", "index": "Indexing folders", "extract": "Extracting text",
	"embed": "Computing search by meaning", "count": "Counting", "done": "Done",
}
