package chat

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/llm"
)

// emuTools offers write_kb_file and search_kb, so a model on the text
// tool protocol is sent them in its prompt, and records what is run.
type emuTools struct {
	fakeTools
	mu  sync.Mutex
	ran []string
	// answer is what answering a held call gives, when set.
	answer *Answered
	// result is what a call returns, when set; results, when set, are
	// what write_kb_file calls return, in turn.
	result  string
	results []string
	// pause is how long a write takes; active and most count the writes
	// running at once.
	pause        time.Duration
	active, most int
}

func (e *emuTools) Answer(r *http.Request, in map[string]any, k *string) (*Answered, error) {
	if e.answer != nil {
		return e.answer, nil
	}
	return e.fakeTools.Answer(r, in, k)
}

func (e *emuTools) Specs(*http.Request, []string, bool, bool, bool, []string) (*Specs, error) {
	var out []Spec
	for _, n := range []string{"write_kb_file", "search_kb"} {
		b, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": n, "description": n,
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}})
		out = append(out, b)
	}
	return &Specs{Available: out, Selected: out}, nil
}

func (e *emuTools) Execute(_ *http.Request, name, args string, _ ToolCtx) (*Outcome, error) {
	e.mu.Lock()
	e.ran = append(e.ran, name+" "+args)
	result := e.result
	if name == "write_kb_file" && len(e.results) > 0 {
		result, e.results = e.results[0], e.results[1:]
	}
	if name == "write_kb_file" {
		e.active++
		e.most = max(e.most, e.active)
	}
	e.mu.Unlock()
	if name == "write_kb_file" {
		time.Sleep(e.pause)
		e.mu.Lock()
		e.active--
		e.mu.Unlock()
	}
	if result == "" {
		result = "Wrote it."
	}
	return &Outcome{Result: result, Summary: "wrote"}, nil
}

func (e *emuTools) runs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.ran...)
}

var promptNonceRe = regexp.MustCompile(`studio-tools-1\\\\?",\\\\?"nonce\\\\?":\\\\?"([0-9a-f]+)`)

// nonceOf is the nonce in the prompt a request carries.
func nonceOf(req []byte) string {
	m := promptNonceRe.FindSubmatch(req)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// said is a whole (unstreamed) answer from the stand-in model, as the
// emulated models give one, made from the prompt's nonce.
func said(make func(nonce string) string, why string) reply {
	return reply{fn: func(req []byte) reply {
		return reply{whole: true, body: map[string]any{"model": "genai-pro", "choices": []any{map[string]any{"index": 0,
			"message": map[string]any{"role": "assistant", "content": make(nonceOf(req))}, "finish_reason": why}}}}
	}}
}

// sheetRows is a spreadsheet's JSON of n rows of long text.
func sheetRows(n int) string {
	var rows []string
	for i := 0; i < n; i++ {
		rows = append(rows, `["Item `+strings.Repeat("x", 3)+`","A long description of the item, with words enough to fill a cell of the sheet and then some more words after that"]`)
	}
	return `{"sheets":[{"name":"Items","rows":[["Name","Description"],` + strings.Join(rows, ",") + `]}]}`
}

func writeEnvelope(nonce, content string) string {
	b, _ := json.Marshal(content)
	return `{"protocol":"studio-tools-1","nonce":"` + nonce + `","calls":[{"name":"write_kb_file","arguments":{"path":"reports/items.xlsx","overwrite":true,"content":` + string(b) + `}}]}`
}

func emuModule(t *testing.T) (*emuTools, string, *fakeGateway) {
	m, srv, gw, _ := newTestModule(t, nil)
	e := &emuTools{fakeTools: m.Tools.(fakeTools)}
	m.Tools = e
	return e, srv.URL, gw
}

func ask(t *testing.T, base string) string {
	return post(t, base, map[string]any{"model": "genai-pro", "conversationId": "c-env", "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "make the items spreadsheet"}}})
}

// answerOf is the text of a stream's content events: what the chat shows as
// the answer.
func answerOf(sse string) string {
	var b strings.Builder
	for _, blk := range strings.Split(sse, "\n\n") {
		if !strings.HasPrefix(blk, "event: content\n") {
			continue
		}
		var d struct{ Text string }
		json.Unmarshal([]byte(strings.TrimPrefix(strings.SplitN(blk, "\n", 2)[1], "data: ")), &d)
		b.WriteString(d.Text)
	}
	return b.String()
}

// requestOf is the gateway request n as a string.
func requestOf(gw *fakeGateway, n int) string {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	if n >= len(gw.requests) {
		return ""
	}
	return string(gw.requests[n])
}

func maxTokensOf(t *testing.T, req string) float64 {
	var o map[string]any
	if err := json.Unmarshal([]byte(req), &o); err != nil {
		t.Fatal(err)
	}
	f, _ := o["max_tokens"].(float64)
	return f
}

// countWrites is how many write_kb_file calls ran.
func countWrites(e *emuTools) int {
	n := 0
	for _, r := range e.runs() {
		if strings.HasPrefix(r, "write_kb_file ") {
			n++
		}
	}
	return n
}

// (a) A request cut off at the output limit is not shown: the chat says it
// is asked again in smaller parts, the model is told how (append=true) with
// a higher limit, and the request it sends then runs.
func TestCutOffRequestIsAskedAgainInParts(t *testing.T) {
	e, base, gw := emuModule(t)
	big := sheetRows(400)
	gw.load([]reply{
		said(func(n string) string { w := writeEnvelope(n, big); return w[:len(w)*2/3] }, "length"),
		said(func(n string) string { return writeEnvelope(n, sheetRows(5)) }, "stop"),
		said(func(string) string {
			return "The first part of the sheet is written; " + strings.Repeat("more follows. ", 20)
		}, "stop"),
	})
	sse := ask(t, base)
	if a := answerOf(sse); strings.Contains(a, "studio-tools-1") || strings.Contains(a, "A long description") {
		t.Fatalf("the envelope reached the chat: %.400s", sse)
	}
	if !strings.Contains(sse, "The model's request to write reports/items.xlsx was cut off; asking it again in smaller parts") {
		t.Errorf("no notice: %.600s", sse)
	}
	again := requestOf(gw, 1)
	if !strings.Contains(again, "append=true") || !strings.Contains(again, "cut off at the output limit") || !strings.Contains(again, "send the next part at once") || strings.Contains(again, "was a lookup request") {
		t.Errorf("the second request has no guidance: %.300s", again)
	}
	if first, second := maxTokensOf(t, requestOf(gw, 0)), maxTokensOf(t, again); second <= first {
		t.Errorf("max_tokens %v then %v", first, second)
	}
	if countWrites(e) != 1 {
		t.Errorf("ran %q", e.runs())
	}
}

// (a) Cut off again, it is withheld: the answer says so, with no envelope
// and no second answer made up in its place.
func TestCutOffTwiceIsWithheld(t *testing.T) {
	e, base, gw := emuModule(t)
	cut := said(func(n string) string {
		w := writeEnvelope(n, sheetRows(400))
		return "Here is the sheet.\n" + w[:len(w)/2]
	}, "length")
	gw.load([]reply{cut, cut, said(func(string) string { return "A made-up final answer." }, "stop")})
	sse := ask(t, base)
	if a := answerOf(sse); strings.Contains(a, "studio-tools-1") || strings.Contains(a, "A long description") {
		t.Fatalf("the envelope reached the chat: %.400s", sse)
	}
	if a := answerOf(sse); !strings.Contains(a, "Here is the sheet.") || !strings.Contains(a, "was cut off at the model's output limit, so nothing ran") {
		t.Errorf("no notice: %.800s", sse)
	}
	if strings.Contains(sse, "made-up") || len(gw.requests) != 2 || countWrites(e) != 0 {
		t.Errorf("%d requests, ran %q", len(gw.requests), e.runs())
	}
}

// (b) JSON written raw inside the content string, and a line break left
// raw in a string, are repaired and run.
func TestMalformedRequestIsRepaired(t *testing.T) {
	for name, mk := range map[string]func(n string) string{
		"raw nested JSON": func(n string) string {
			return `{"protocol":"studio-tools-1","nonce":"` + n + `","calls":[{"name":"write_kb_file","arguments":{"path":"reports/items.xlsx","overwrite":true,"content":"` + sheetRows(3) + `"}}]}`
		},
		"raw line break": func(n string) string {
			return `{"protocol":"studio-tools-1","nonce":"` + n + `","calls":[{"name":"write_kb_file","arguments":{"path":"reports/items.md","content":"line one` + "\n" + `line "two" here"}}]}`
		},
		"content as an object": func(n string) string {
			return `{"protocol":"studio-tools-1","nonce":"` + n + `","calls":[{"name":"write_kb_file","arguments":{"path":"reports/items.xlsx","content":` + sheetRows(2) + `}}]}`
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, base, gw := emuModule(t)
			gw.load([]reply{said(mk, "stop"), said(func(string) string { return "Written. " + strings.Repeat("Done. ", 40) }, "stop")})
			sse := ask(t, base)
			if strings.Contains(answerOf(sse), "studio-tools-1") || countWrites(e) != 1 {
				t.Fatalf("ran %q; stream %.300s", e.runs(), sse)
			}
			var w string
			for _, r := range e.runs() {
				if strings.HasPrefix(r, "write_kb_file ") {
					w = strings.TrimPrefix(r, "write_kb_file ")
				}
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(w), &args); err != nil {
				t.Fatalf("%v: %s", err, w)
			}
			content, _ := args["content"].(string)
			if strings.HasSuffix(fmtPath(args), ".xlsx") {
				var sheets map[string]any
				if err := json.Unmarshal([]byte(content), &sheets); err != nil || sheets["sheets"] == nil {
					t.Errorf("content %q: %v", content, err)
				}
			} else if content != "line one\nline \"two\" here" {
				t.Errorf("content %q", content)
			}
		})
	}
}

func fmtPath(args map[string]any) string { s, _ := args["path"].(string); return s }

// (c) A turn resumed after an approval keeps the nonce of its earlier
// round: an envelope after prose carrying it runs, and one carrying a
// nonce never issued in the turn (a quote from a file) stays text.
func TestEarlierNonceOfTheTurnIsAccepted(t *testing.T) {
	e, base, gw := emuModule(t)
	zero := 0
	e.answer = &Answered{OK: true, Tool: "write_kb_file", Summary: "wrote", Result: "Wrote it.", Rec: &HeldRec{
		ConversationID: "c-env", MessageID: "m1", ToolCallID: "em-1234abcd-1700000000000-0", Tool: "write_kb_file",
		Turn: []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{{Index: &zero, ID: "em-1234abcd-1700000000000-0", Type: "function",
			Function: llm.Function{Name: "write_kb_file", Arguments: `{"path":"reports/a.md","content":"a"}`}}}}}}}
	gw.load([]reply{
		said(func(string) string { return "Now the second sheet.\n" + writeEnvelope("1234abcd", sheetRows(2)) }, "stop"),
		said(func(string) string { return "Both are written. " + strings.Repeat("Done. ", 40) }, "stop"),
	})
	sse := post(t, base, map[string]any{"model": "genai-pro", "conversationId": "c-env", "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "make the items spreadsheet"}},
		"resume":   map[string]any{"messageId": "m1", "toolCallId": "em-1234abcd-1700000000000-0", "allowed": true}})
	if strings.Contains(answerOf(sse), "studio-tools-1") || countWrites(e) != 1 || !strings.Contains(answerOf(sse), "Now the second sheet.") {
		t.Fatalf("ran %q; stream %.500s", e.runs(), sse)
	}
	if got := turnNonces([]llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "em-0badc0de-1-0"}}}, {Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "em-1234abcd-2-0"}}}}, 1); len(got) != 1 || got[0] != "1234abcd" {
		t.Errorf("turnNonces %q: an earlier turn's nonce counts", got)
	}
}

// (d) An envelope after prose whose calls come before its protocol (the
// object opening well over 400 characters and 4 braces before the marker)
// runs, and the prose is kept.
func TestEnvelopeWithProtocolLastRuns(t *testing.T) {
	e, base, gw := emuModule(t)
	gw.load([]reply{
		said(func(n string) string {
			b, _ := json.Marshal(sheetRows(20))
			return "Writing it now.\n" + `{"calls":[{"name":"write_kb_file","arguments":{"path":"reports/items.xlsx","options":{"a":{"b":{"c":{}}}},"content":` + string(b) + `}}],"protocol":"studio-tools-1","nonce":"` + n + `"}`
		}, "stop"),
		said(func(string) string { return "Written. " + strings.Repeat("Done. ", 40) }, "stop"),
	})
	sse := ask(t, base)
	if strings.Contains(answerOf(sse), "studio-tools-1") || countWrites(e) != 1 || !strings.Contains(answerOf(sse), "Writing it now.") {
		t.Fatalf("ran %q; stream %.400s", e.runs(), sse)
	}
}

func TestBrokenEnvelope(t *testing.T) {
	n := []string{"ab12cd34"}
	if _, ok := brokenEnvelope(`The file says {"protocol":"studio-tools-1","nonce":"ffffffff","calls":[{"name":"search_kb"`, n); ok {
		t.Error("a quote from a file, with another nonce, was taken for the model's request")
	}
	b, ok := brokenEnvelope("Saving.\n```json\n"+`{"protocol":"studio-tools-1","nonce":"ab12cd34","calls":[{"name":"write_kb_file","arguments":{"path":"a/b.xlsx","content":"{\"sheets\":[`, n)
	if !ok || !b.cutOff || b.at != len("Saving.\n") || b.tool != "write_kb_file" || b.path != "a/b.xlsx" {
		t.Errorf("%+v %v", b, ok)
	}
	b, ok = brokenEnvelope(`{"protocol":"studio-tools-1","nonce":"99999999","calls":[{"name":"search_kb","arguments":{"query":"x" "y"}}]}`, n)
	if !ok || b.cutOff || b.at != 0 {
		t.Errorf("an envelope the reply opens with: %+v %v", b, ok)
	}
}

// A short turn that says what it will do, after lookups, with no request
// (F) is asked once for the request, with its tools: the file is written,
// not left to a final answer that cannot write it.
func TestAnnouncedActionIsAskedForOnce(t *testing.T) {
	e, base, gw := emuModule(t)
	gw.load([]reply{
		said(func(n string) string {
			return `{"protocol":"studio-tools-1","nonce":"` + n + `","calls":[{"name":"search_kb","arguments":{"query":"items"}}]}`
		}, "stop"),
		said(func(string) string { return "I'll write the spreadsheet now." }, "stop"),
		said(func(n string) string { return writeEnvelope(n, sheetRows(3)) }, "stop"),
		said(func(string) string { return "The sheet is written with 3 rows. " + strings.Repeat("Done. ", 40) }, "stop"),
	})
	sse := ask(t, base)
	if countWrites(e) != 1 || !strings.Contains(answerOf(sse), "The sheet is written") {
		t.Fatalf("ran %q; %d requests; answer %.300s", e.runs(), len(gw.requests), answerOf(sse))
	}
	if strings.Contains(requestOf(gw, 2), "No further tool calls are available") || !strings.Contains(requestOf(gw, 2), "made no tool request") {
		t.Errorf("the third request was not the nudge: %.300s", requestOf(gw, 2))
	}
}

// The Studio's own search before the first turn (G) is said as the
// Studio's, not as a search of the model's, and is not run when the user
// asked for no search.
func TestPresearchIsTheStudios(t *testing.T) {
	e, base, gw := emuModule(t)
	gw.load([]reply{said(func(string) string { return "An answer. " + strings.Repeat("More. ", 40) }, "stop")})
	sse := ask(t, base)
	if !strings.Contains(sse, "Studio searched your libraries first") || strings.Contains(sse, `"name":"search_kb"`) {
		t.Errorf("stream %.400s", sse)
	}
	if !strings.Contains(requestOf(gw, 0), "you did not ask for this search") {
		t.Errorf("the prompt does not say whose search it was")
	}
	if n := len(e.runs()); n != 1 {
		t.Fatalf("ran %q", e.runs())
	}

	e, base, gw = emuModule(t)
	gw.load([]reply{said(func(string) string { return "An answer. " + strings.Repeat("More. ", 40) }, "stop")})
	sse = post(t, base, map[string]any{"model": "genai-pro", "conversationId": "c-env2", "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "Make the items spreadsheet. Do not search first."}}})
	if len(e.runs()) != 0 || strings.Contains(sse, "Studio searched") {
		t.Errorf("searched anyway: %q", e.runs())
	}
}

// A workbook written in parts, with an empty reply after each part (as a
// model gave at 100 of 250 rows), is nudged again after each round that
// made progress, with what the last write said the file holds; the final
// answer after a write reports what was done, not what could not be
// verified.
func TestEmptyTurnsWhileWritingPartsAreNudged(t *testing.T) {
	e, base, gw := emuModule(t)
	e.result = "Added to reports/items.xlsx (25 rows; the workbook now holds: Items, 50 rows after the header)."
	empty := said(func(string) string { return "" }, "stop")
	part := func(rows int) reply {
		return said(func(n string) string { return writeEnvelope(n, sheetRows(rows)) }, "stop")
	}
	gw.load([]reply{part(25), empty, part(26), empty, part(27), empty, empty, said(func(string) string { return "The workbook holds 75 rows." }, "stop")})
	sse := ask(t, base)
	if countWrites(e) != 3 {
		t.Fatalf("ran %q; %d requests; answer %.300s", e.runs(), len(gw.requests), answerOf(sse))
	}
	nudge := requestOf(gw, 2)
	if !strings.Contains(nudge, "The last write") || !strings.Contains(nudge, "Items, 50 rows after the header") {
		t.Errorf("the nudge does not say what the file holds: %.400s", nudge)
	}
	// Three nudges in all, then the final answer, which reports what was
	// done.
	last := requestOf(gw, len(gw.requests)-1)
	if !strings.Contains(last, "Say what was done") || strings.Contains(last, "could not verify") {
		t.Errorf("final prompt %.600s", last)
	}
}

// An envelope that ended on its own but lacks its last closing brackets is
// read with them added; at the output limit it is not (that is a request
// cut off, asked again in parts).
func TestEnvelopeMissingItsLastBracketsRuns(t *testing.T) {
	short := func(n string) string { w := writeEnvelope(n, sheetRows(2)); return w[:len(w)-3] }
	e, base, gw := emuModule(t)
	gw.load([]reply{said(short, "stop"), said(func(string) string { return "Written. " + strings.Repeat("Done. ", 40) }, "stop")})
	sse := ask(t, base)
	if countWrites(e) != 1 || strings.Contains(answerOf(sse), "studio-tools-1") {
		t.Fatalf("ran %q; answer %.300s", e.runs(), answerOf(sse))
	}
	e, base, gw = emuModule(t)
	gw.load([]reply{said(short, "length"), said(func(string) string { return "Gave up." }, "stop")})
	ask(t, base)
	if countWrites(e) != 0 || !strings.Contains(requestOf(gw, 1), "cut off at the output limit") {
		t.Fatalf("a cut-off request was closed and run: %q", e.runs())
	}
}

func askFor(t *testing.T, base, text string) string {
	return post(t, base, map[string]any{"model": "genai-pro", "conversationId": "c-rows", "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": text}}})
}

// A workbook written with its header only, against an ask for 60 rows,
// is not the end even when the model answers at length: each write's
// result says how it stands against the ask, and the model is asked for
// the rest.
func TestWorkbookShortOfTheRowsAskedIsFinished(t *testing.T) {
	e, base, gw := emuModule(t)
	e.results = []string{
		"Wrote reports/items.xlsx (an Excel workbook of 1 sheet (Items, 0 rows after the header: the header only), 900 bytes) and re-indexed in 1 ms.",
		"Added to reports/items.xlsx (60 rows; the workbook now holds: Items, 60 rows after the header; 9000 bytes in all) and re-indexed in 1 ms.",
	}
	gw.load([]reply{
		said(func(n string) string {
			return writeEnvelope(n, `{"sheets":[{"name":"Items","rows":[["Name","Description"]]}]}`)
		}, "stop"),
		said(func(string) string {
			return "The workbook was created, but it contains 0 data rows after the header, so it does not meet your request for 60 rows. " + strings.Repeat("Sorry. ", 30)
		}, "stop"),
		said(func(n string) string { return writeEnvelope(n, sheetRows(60)) }, "stop"),
		said(func(string) string { return "The workbook holds all 60 rows. " + strings.Repeat("Done. ", 30) }, "stop"),
	})
	sse := askFor(t, base, "Make reports/items.xlsx with 60 rows of items. Do not search first.")
	if countWrites(e) != 2 || !strings.Contains(answerOf(sse), "all 60 rows") {
		t.Fatalf("ran %q; answer %.300s", e.runs(), answerOf(sse))
	}
	if r := requestOf(gw, 1); !strings.Contains(r, "The user asked for 60 rows; the workbook holds 0 after the header, so 60 are still to add") {
		t.Errorf("the write's result does not say how it stands: %.300s", r)
	}
	if r := requestOf(gw, 2); !strings.Contains(r, "holds 0 of the 60 rows the user asked for") {
		t.Errorf("no nudge for the rest: %.300s", r)
	}
	if r := requestOf(gw, 3); !strings.Contains(r, "the workbook holds 60 after the header, all of them") {
		t.Errorf("the last write's result: %.300s", r)
	}
}

// A reading call that asks what two before it asked, with only the count
// of results or the argument's name changed, is answered with their
// result and not run a third time.
func TestSameAskIsNotRunAThirdTime(t *testing.T) {
	e, base, gw := emuModule(t)
	search := func(args string) reply {
		return said(func(n string) string {
			return `{"protocol":"studio-tools-1","nonce":"` + n + `","calls":[{"name":"search_kb","arguments":` + args + `}]}`
		}, "stop")
	}
	gw.load([]reply{
		search(`{"name":"emu.xlsx"}`), search(`{"query":"emu.xlsx","limit":5}`), search(`{"query":"emu.xlsx","limit":50}`),
		said(func(string) string { return "Nothing was found. " + strings.Repeat("Done. ", 30) }, "stop"),
	})
	askFor(t, base, "Make emu.xlsx with 3 rows. Do not search first.")
	n := 0
	for _, r := range e.runs() {
		if strings.HasPrefix(r, "search_kb ") {
			n++
		}
	}
	if n != 2 || !strings.Contains(requestOf(gw, 3), "Not run: this asks the same as two calls before it") {
		t.Fatalf("searches run %d: %q", n, e.runs())
	}
}

func TestRowsHeldAndAsked(t *testing.T) {
	for in, want := range map[string]int{
		"Wrote a.xlsx (an Excel workbook of 2 sheets (A, 0 rows after the header: the header only; B, 5 rows after the header), 9 bytes), replacing the previous version, which held A, 20 rows after the header": 5,
		"Added to a.xlsx (4 rows; the workbook now holds: A, 3 rows after the header; B, 1 row after the header; 7 bytes in all)":                                                                                 4,
	} {
		if got, ok := rowsHeld(in); !ok || got != want {
			t.Errorf("%s: %d %v", in, got, ok)
		}
	}
	if _, ok := rowsHeld("Wrote notes/a.md (12 bytes)"); ok {
		t.Error("a text file counted")
	}
	for in, want := range map[string]int{"a sheet of 250 rows": 250, "600 data rows, 2 columns": 600, "1,000 records": 1000, "write it in 3 parts": 0, "1 row": 0} {
		if got := askedRows(in); got != want {
			t.Errorf("%q: %d", in, got)
		}
	}
}

// Quotes and brackets a model leaves after its request ("} after a
// complete envelope) are not shown.
func TestNoBracketsLeftAfterARequest(t *testing.T) {
	env := writeEnvelope("ab12cd34", "x")
	for in, want := range map[string]string{
		env + `"}`:                            "",
		"Saving.\n" + env + "\"}\nMore text.": "Saving.",
		"I'll add them before calling it complete." + env + "The workbook was not confirmed complete; no results came back.": "I'll add them before calling it complete.",
	} {
		prose, calls := splitEnvelope(in, []string{"ab12cd34"}, []string{"write_kb_file"}, true)
		if prose != want || len(calls) != 1 {
			t.Errorf("%q: prose %q, %d calls", in, prose, len(calls))
		}
	}
}

// A request withheld while the turn goes on (Studio asks for the rest of
// a workbook) is a progress line, not an answer telling the user to do
// what the Studio is doing; and the model's text in a round it is asked
// again is not shown.
func TestWithheldRequestMidTurnIsAProgressLine(t *testing.T) {
	e, base, gw := emuModule(t)
	e.results = []string{
		"Wrote reports/items.xlsx (an Excel workbook of 1 sheet (Items, 30 rows after the header), 900 bytes) and re-indexed in 1 ms.",
		"Added to reports/items.xlsx (30 rows; the workbook now holds: Items, 60 rows after the header; 9000 bytes in all) and re-indexed in 1 ms.",
	}
	cut := said(func(n string) string { w := writeEnvelope(n, sheetRows(400)); return w[:len(w)/2] }, "length")
	gw.load([]reply{
		said(func(n string) string { return writeEnvelope(n, sheetRows(30)) }, "stop"),
		cut, cut,
		said(func(n string) string { return writeEnvelope(n, sheetRows(31)) }, "stop"),
		said(func(string) string { return "The workbook holds all 60 rows. " + strings.Repeat("Done. ", 30) }, "stop"),
	})
	sse := askFor(t, base, "Make reports/items.xlsx with 60 rows of items. Do not search first.")
	a := answerOf(sse)
	if countWrites(e) != 2 || !strings.Contains(a, "all 60 rows") {
		t.Fatalf("ran %d writes; answer %.400s", countWrites(e), a)
	}
	if strings.Contains(a, "output limit") || strings.Contains(a, "Ask for it in smaller parts") || !strings.Contains(sse, "was cut off again; nothing ran") {
		t.Errorf("the notice is in the answer, or no progress line: %.600s", a)
	}
}

// A file the user asked for, not yet written, is asked for again when the
// model gives up without a request (here after searching for it), not
// left to a final answer without tools; its giving-up text is not shown.
func TestFileAskedForIsWritten(t *testing.T) {
	e, base, gw := emuModule(t)
	search := func(q string) reply {
		return said(func(n string) string {
			return `{"protocol":"studio-tools-1","nonce":"` + n + `","calls":[{"name":"search_kb","arguments":{"query":"` + q + `"}}]}`
		}, "stop")
	}
	gw.load([]reply{
		search("emu.xlsx"),
		said(func(string) string { return "I can't write the spreadsheet from the tools available in this chat." }, "stop"),
		said(func(n string) string { return writeEnvelope(n, sheetRows(3)) }, "stop"),
		said(func(string) string { return "Wrote emu.xlsx with 3 rows. " + strings.Repeat("Done. ", 30) }, "stop"),
	})
	sse := askFor(t, base, "Write a spreadsheet emu.xlsx of sales, three rows. Do not search first.")
	a := answerOf(sse)
	if countWrites(e) != 1 || strings.Contains(a, "I can't write") || !strings.Contains(a, "Wrote emu.xlsx") {
		t.Fatalf("ran %q; answer %.300s", e.runs(), a)
	}
	if !strings.Contains(requestOf(gw, 2), "nothing has been written yet") {
		t.Errorf("no nudge: %.300s", requestOf(gw, 2))
	}
	for in, want := range map[string]string{
		"Write a spreadsheet emu-250f.xlsx with 250 rows": "emu-250f.xlsx",
		"make reports/plan.md and a PDF":                  "reports/plan.md",
		"what does reports/plan.md say?":                  "",
		"write a short summary":                           "",
	} {
		if got := askedFile(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// A workbook short of the rows asked for, whose model keeps sending the
// rest in one request cut off at the output limit: each re-ask and nudge
// says how many rows to send in each request, a cut-off does not stop the
// nudges, and a turn that ends on one says what the workbook holds, once.
func TestCutOffRequestsForTheRestEndWithWhatTheWorkbookHolds(t *testing.T) {
	e, base, gw := emuModule(t)
	e.results = []string{"Wrote reports/items.xlsx (an Excel workbook of 1 sheet (Items, 30 rows after the header), 900 bytes) and re-indexed in 1 ms."}
	cut := said(func(n string) string { w := writeEnvelope(n, sheetRows(400)); return w[:len(w)/2] }, "length")
	gw.load([]reply{
		said(func(n string) string { return writeEnvelope(n, sheetRows(30)) }, "stop"),
		cut, cut, cut, cut, cut, cut, cut, cut,
	})
	sse := askFor(t, base, "Make reports/items.xlsx with 600 rows of items. Do not search first.")
	a := answerOf(sse)
	if strings.Count(a, "holds 30 of the 600 rows asked for") != 1 || strings.Count(a, "output limit") != 1 {
		t.Fatalf("answer %.600s", a)
	}
	if r := requestOf(gw, 2); !strings.Contains(r, "at most 50 rows in each request") {
		t.Errorf("the re-ask gives no size: %.400s", r)
	}
	nudges := 0
	for i := 3; i < 9; i++ {
		if r := requestOf(gw, i); strings.Contains(r, "holds 30 of the 600 rows the user asked for") && strings.Contains(r, "at most 50 rows in each request") {
			nudges++
		}
	}
	if nudges < 6 {
		t.Errorf("nudged %d times after cut-offs", nudges)
	}
	for in, want := range map[string]int{`[["a"],["b"]`: 10, "no rows": 50, strings.Repeat(`["a"],`, 40): 19, strings.Repeat(`["a"],`, 900): 50} {
		if got := rowsPerPart(in); got != want {
			t.Errorf("%.20q: %d", in, got)
		}
	}
}

// Parts sent in one round are written one at a time in the order asked,
// so the last result is what the workbook holds; text written before a
// round's calls is set apart from the next round's; and a turn whose
// answer does not give the count ends with what the workbook holds.
func TestPartsInOneRoundLandInOrderAndTheTurnSaysWhatTheWorkbookHolds(t *testing.T) {
	e, base, gw := emuModule(t)
	e.pause = 20 * time.Millisecond
	e.results = []string{
		"Wrote reports/items.xlsx (an Excel workbook of 1 sheet (Items, 20 rows after the header), 900 bytes) and re-indexed in 1 ms.",
		"Added to reports/items.xlsx (20 rows; the workbook now holds: Items, 40 rows after the header; 9000 bytes in all) and re-indexed in 1 ms.",
		"Added to reports/items.xlsx (20 rows; the workbook now holds: Items, 60 rows after the header; 9000 bytes in all) and re-indexed in 1 ms.",
	}
	part := func(i int) string {
		b, _ := json.Marshal(sheetRows(20))
		a := ""
		if i > 0 {
			a = `"append":true,`
		}
		return `{"name":"write_kb_file","arguments":{"path":"reports/items.xlsx",` + a + `"part":` + strconv.Itoa(i) + `,"content":` + string(b) + `}}`
	}
	gw.load([]reply{
		said(func(n string) string {
			return "I'll add the rows in parts and check the count before calling it complete.\n" +
				`{"protocol":"studio-tools-1","nonce":"` + n + `","calls":[` + part(0) + "," + part(1) + "," + part(2) + `]}`
		}, "stop"),
		said(func(string) string {
			return "The workbook was not confirmed complete. " + strings.Repeat("No results came back. ", 12)
		}, "stop"),
	})
	sse := askFor(t, base, "Make reports/items.xlsx with 60 rows of items. Do not search first.")
	a := answerOf(sse)
	var order []string
	for _, r := range e.runs() {
		if i := strings.Index(r, `"part":`); i >= 0 {
			order = append(order, r[i+7:i+8])
		}
	}
	if strings.Join(order, "") != "012" || e.most != 1 {
		t.Errorf("parts ran %v, %d at once", order, e.most)
	}
	if !strings.Contains(a, "complete.\n\nThe workbook") {
		t.Errorf("rounds run together: %.300s", a)
	}
	if !strings.HasSuffix(strings.TrimSpace(a), "reports/items.xlsx holds 60 of the 60 rows asked for.") {
		t.Errorf("no closing count: %.600s", a)
	}
}

// A reply giving up after a cut-off request is nudged again while the
// workbook is short, and the cut-off's notice is not left on a later reply.
func TestGivingUpAfterACutOffIsNudgedAndTheNoticeIsNotLeftOver(t *testing.T) {
	e, base, gw := emuModule(t)
	e.results = []string{
		"Wrote reports/items.xlsx (an Excel workbook of 1 sheet (Items, 30 rows after the header), 900 bytes) and re-indexed in 1 ms.",
		"Added to reports/items.xlsx (30 rows; the workbook now holds: Items, 60 rows after the header; 9000 bytes in all) and re-indexed in 1 ms.",
	}
	cut := said(func(n string) string { w := writeEnvelope(n, sheetRows(400)); return w[:len(w)/2] }, "length")
	gw.load([]reply{
		said(func(n string) string { return writeEnvelope(n, sheetRows(30)) }, "stop"),
		cut, cut,
		said(func(string) string {
			return "I can't complete the remaining rows because file-writing tools aren't available in this reply. " + strings.Repeat("Sorry. ", 30)
		}, "stop"),
		said(func(n string) string { return writeEnvelope(n, sheetRows(31)) }, "stop"),
		said(func(string) string { return "The workbook holds all 60 rows. " + strings.Repeat("Done. ", 30) }, "stop"),
	})
	sse := askFor(t, base, "Make reports/items.xlsx with 60 rows of items. Do not search first.")
	a := answerOf(sse)
	if countWrites(e) != 2 || strings.Contains(a, "cut off") || strings.Contains(a, "can't complete") || !strings.Contains(a, "all 60 rows") {
		t.Fatalf("ran %d writes; answer %.600s", countWrites(e), a)
	}
}

// A cut-off request's notice belongs to its round: a later reply that
// ends the turn is not followed by it.
func TestCutOffNoticeIsNotLeftOnALaterReply(t *testing.T) {
	_, base, gw := emuModule(t)
	cut := said(func(n string) string { w := writeEnvelope(n, sheetRows(400)); return w[:len(w)/2] }, "length")
	gw.load([]reply{
		cut, cut,
		said(func(string) string { return "Here is the plan in outline instead. " + strings.Repeat("A step. ", 40) }, "stop"),
	})
	a := answerOf(askFor(t, base, "Write a plan as notes/plan.md. Do not search first."))
	if !strings.Contains(a, "plan in outline") || strings.Contains(a, "cut off") {
		t.Fatalf("answer %.400s", a)
	}
}

// A workbook written with more rows than asked for is written again with
// the count asked, once, rather than only reported.
func TestWorkbookOverTheRowsAskedIsWrittenAgain(t *testing.T) {
	e, base, gw := emuModule(t)
	e.results = []string{
		"Wrote reports/items.xlsx (an Excel workbook of 1 sheet (Items, 73 rows after the header), 900 bytes) and re-indexed in 1 ms.",
		"Wrote reports/items.xlsx (an Excel workbook of 1 sheet (Items, 50 rows after the header), 900 bytes), replacing the previous version, which held Items, 73 rows after the header, and re-indexed in 1 ms.",
	}
	gw.load([]reply{
		said(func(n string) string { return writeEnvelope(n, sheetRows(73)) }, "stop"),
		said(func(string) string {
			return "It has 73 data rows, not the 50 you requested, so it does not match your requested row count. " + strings.Repeat("Sorry. ", 30)
		}, "stop"),
		said(func(n string) string { return writeEnvelope(n, sheetRows(50)) }, "stop"),
		said(func(string) string { return "The workbook holds 50 rows. " + strings.Repeat("Done. ", 30) }, "stop"),
	})
	a := answerOf(askFor(t, base, "Make reports/items.xlsx with 50 rows of items. Do not search first."))
	if countWrites(e) != 2 || !strings.Contains(a, "holds 50 rows") || strings.Contains(a, "73 data rows") {
		t.Fatalf("ran %d writes; answer %.400s", countWrites(e), a)
	}
	if r := requestOf(gw, 2); !strings.Contains(r, "holds 73 rows after the header; the user asked for 50. Write it again with exactly 50") {
		t.Errorf("no note to write it again: %.300s", r)
	}
}

// The same call made again in a reply is not run: it is answered with the
// first one's result and a note to go on with the task.
func TestTheSameCallIsAnsweredFromTheFirst(t *testing.T) {
	e, base, gw := emuModule(t)
	search := said(func(n string) string {
		return `{"protocol":"studio-tools-1","nonce":"` + n + `","calls":[{"name":"search_kb","arguments":{"limit":10,"name":"emu.xlsx"}}]}`
	}, "stop")
	gw.load([]reply{search, search, search,
		said(func(string) string { return "Nothing was found. " + strings.Repeat("Done. ", 30) }, "stop"),
	})
	askFor(t, base, "Make emu.xlsx with 3 rows. Do not search first.")
	n := 0
	for _, r := range e.runs() {
		if strings.HasPrefix(r, "search_kb ") {
			n++
		}
	}
	if n != 1 || !strings.Contains(requestOf(gw, 2), "a file the user asked you to make does not exist until you write it") {
		t.Fatalf("searches run %d: %q", n, e.runs())
	}
}
