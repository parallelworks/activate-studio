package chat

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

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
	// result is what a call returns, when set.
	result string
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
	e.mu.Unlock()
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
