// Package wordpiece is the BERT tokenizer of a HuggingFace tokenizer.json
// (the all-MiniLM-L6-v2 one the embedder uses), giving the token ids the
// tokenizers library gives: its added (special) tokens matched in the raw
// text, the BertNormalizer (clean text, spaces around CJK characters,
// accents stripped, lower case), the BertPreTokenizer (split on white
// space and around punctuation), greedy longest-match WordPiece with the
// ## prefix, and the [CLS] ... [SEP] template, cut to a token budget.
//
// The character classes (what the normalizer drops, what counts as
// punctuation, how a character lower-cases and loses its accents) follow
// the Rust library's Unicode tables, which are not Go's: where the two
// differ, the tables in tables.go, generated from the Python package by
// scripts/wordpiece_tables.py, say what the library does.
package wordpiece

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Tokenizer is one loaded tokenizer.json. It is safe for concurrent use.
type Tokenizer struct {
	vocab        map[string]int32
	unk          int32
	cls, sep     int32
	pad          int32
	prefix       string
	maxWordChars int
	lowercase    bool
	stripAccents bool
	cleanText    bool
	chinese      bool
	// special are the added tokens matched in the raw text, longest first.
	special []specialToken
}

type specialToken struct {
	text string
	id   int32
}

type file struct {
	AddedTokens []struct {
		ID         int32  `json:"id"`
		Content    string `json:"content"`
		SingleWord bool   `json:"single_word"`
		LStrip     bool   `json:"lstrip"`
		RStrip     bool   `json:"rstrip"`
		Normalized bool   `json:"normalized"`
	} `json:"added_tokens"`
	Normalizer *struct {
		Type               string `json:"type"`
		CleanText          bool   `json:"clean_text"`
		HandleChineseChars bool   `json:"handle_chinese_chars"`
		StripAccents       *bool  `json:"strip_accents"`
		Lowercase          bool   `json:"lowercase"`
	} `json:"normalizer"`
	PreTokenizer *struct {
		Type string `json:"type"`
	} `json:"pre_tokenizer"`
	PostProcessor *struct {
		Type   string            `json:"type"`
		Single []json.RawMessage `json:"single"`
	} `json:"post_processor"`
	Padding *struct {
		PadID int32 `json:"pad_id"`
	} `json:"padding"`
	Model struct {
		Type                    string           `json:"type"`
		UnkToken                string           `json:"unk_token"`
		ContinuingSubwordPrefix string           `json:"continuing_subword_prefix"`
		MaxInputCharsPerWord    int              `json:"max_input_chars_per_word"`
		Vocab                   map[string]int32 `json:"vocab"`
	} `json:"model"`
}

// Load reads a tokenizer.json. Anything but the BERT WordPiece pipeline
// is refused, so a different model is not tokenized wrongly in silence.
func Load(path string) (*Tokenizer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads the contents of a tokenizer.json.
func Parse(b []byte) (*Tokenizer, error) {
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("tokenizer.json: %w", err)
	}
	if f.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("tokenizer.json: model %q, not WordPiece", f.Model.Type)
	}
	if f.Normalizer == nil || f.Normalizer.Type != "BertNormalizer" {
		return nil, fmt.Errorf("tokenizer.json: not the BertNormalizer")
	}
	if f.PreTokenizer == nil || f.PreTokenizer.Type != "BertPreTokenizer" {
		return nil, fmt.Errorf("tokenizer.json: not the BertPreTokenizer")
	}
	t := &Tokenizer{
		vocab:        f.Model.Vocab,
		prefix:       f.Model.ContinuingSubwordPrefix,
		maxWordChars: f.Model.MaxInputCharsPerWord,
		lowercase:    f.Normalizer.Lowercase,
		cleanText:    f.Normalizer.CleanText,
		chinese:      f.Normalizer.HandleChineseChars,
	}
	t.stripAccents = t.lowercase
	if f.Normalizer.StripAccents != nil {
		t.stripAccents = *f.Normalizer.StripAccents
	}
	if t.maxWordChars <= 0 {
		t.maxWordChars = 100
	}
	var ok bool
	if t.unk, ok = t.vocab[f.Model.UnkToken]; !ok {
		return nil, fmt.Errorf("tokenizer.json: no %s in the vocabulary", f.Model.UnkToken)
	}
	if t.cls, ok = t.vocab["[CLS]"]; !ok {
		return nil, fmt.Errorf("tokenizer.json: no [CLS]")
	}
	if t.sep, ok = t.vocab["[SEP]"]; !ok {
		return nil, fmt.Errorf("tokenizer.json: no [SEP]")
	}
	if f.Padding != nil {
		t.pad = f.Padding.PadID
	}
	if pp := f.PostProcessor; pp != nil {
		if pp.Type != "TemplateProcessing" || len(pp.Single) != 3 {
			return nil, fmt.Errorf("tokenizer.json: post processor %s is not [CLS] $A [SEP]", pp.Type)
		}
	}
	for _, a := range f.AddedTokens {
		if a.SingleWord || a.LStrip || a.RStrip || a.Normalized {
			return nil, fmt.Errorf("tokenizer.json: added token %q has options this tokenizer does not implement", a.Content)
		}
		t.special = append(t.special, specialToken{a.Content, a.ID})
	}
	sort.SliceStable(t.special, func(i, j int) bool { return len(t.special[i].text) > len(t.special[j].text) })
	return t, nil
}

// PadID is the id of the padding token.
func (t *Tokenizer) PadID() int32 { return t.pad }

// Encode is the ids of text with [CLS] and [SEP] around them, at most
// maxLen ids in all (the tokens past the budget are cut, as the library's
// truncation does for a single sequence).
func (t *Tokenizer) Encode(text string, maxLen int) []int32 {
	ids := []int32{t.cls}
	budget := maxLen - 2
	for _, piece := range t.splitSpecial(text) {
		if len(ids)-1 >= budget {
			break
		}
		if piece.special {
			ids = append(ids, piece.id)
			continue
		}
		for _, w := range t.preTokenize(t.normalize(piece.text)) {
			ids = t.wordPiece(ids, w)
			if len(ids)-1 >= budget {
				break
			}
		}
	}
	if len(ids)-1 > budget {
		ids = ids[:budget+1]
	}
	return append(ids, t.sep)
}

type piece struct {
	text    string
	special bool
	id      int32
}

// splitSpecial cuts the raw text at the added tokens, leftmost and then
// longest first.
func (t *Tokenizer) splitSpecial(s string) []piece {
	if len(t.special) == 0 || !strings.Contains(s, "[") {
		return []piece{{text: s}}
	}
	var out []piece
	start := 0
	for i := 0; i < len(s); {
		matched := false
		if s[i] == '[' || !isASCIIStart(t.special) {
			for _, sp := range t.special {
				if strings.HasPrefix(s[i:], sp.text) {
					if start < i {
						out = append(out, piece{text: s[start:i]})
					}
					out = append(out, piece{special: true, id: sp.id})
					i += len(sp.text)
					start = i
					matched = true
					break
				}
			}
		}
		if !matched {
			i++
		}
	}
	if start < len(s) {
		out = append(out, piece{text: s[start:]})
	}
	return out
}

// isASCIIStart reports whether every added token starts with '[', so the
// scan can skip to those bytes.
func isASCIIStart(sp []specialToken) bool {
	for _, x := range sp {
		if !strings.HasPrefix(x.text, "[") {
			return false
		}
	}
	return true
}

// normalize is the BertNormalizer.
func (t *Tokenizer) normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, c := range s {
		if t.cleanText {
			if c == 0 || c == 0xFFFD || isControl(c) {
				continue
			}
			if isWhitespace(c) {
				c = ' '
			}
		}
		if t.chinese && isChinese(c) {
			b.WriteByte(' ')
			b.WriteRune(c)
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(c)
	}
	out := b.String()
	if t.stripAccents {
		out = stripAccents(out)
	}
	if t.lowercase {
		out = lower(out)
	}
	return out
}

func stripAccents(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	emit := func(seg string) {
		for _, c := range norm.NFD.String(seg) {
			if !isNonspacingMark(c) {
				b.WriteRune(c)
			}
		}
	}
	// A character the library's tables do not decompose is kept whole.
	start := 0
	for i, c := range s {
		if keepWhole[c] {
			emit(s[start:i])
			if !isNonspacingMark(c) {
				b.WriteRune(c)
			}
			start = i + utf8.RuneLen(c)
		}
	}
	emit(s[start:])
	return b.String()
}

func lower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if c < 0x80 {
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			b.WriteByte(byte(c))
			continue
		}
		if m, ok := lowerOverride[c]; ok {
			b.WriteString(m)
			continue
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}

// preTokenize splits on white space and isolates each punctuation mark.
func (t *Tokenizer) preTokenize(s string) []string {
	var out []string
	start := -1
	for i, c := range s {
		switch {
		case unicode.IsSpace(c):
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		case isPunct(c):
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			out = append(out, s[i:i+utf8.RuneLen(c)])
		default:
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

// wordPiece appends the ids of one word: the longest vocabulary entry from
// the start, then the longest ##-prefixed one from there, and so on; a
// word with a part no entry covers, or longer than the limit, is [UNK].
func (t *Tokenizer) wordPiece(ids []int32, w string) []int32 {
	if utf8.RuneCountInString(w) > t.maxWordChars {
		return append(ids, t.unk)
	}
	n := len(ids)
	var buf []byte
	for start := 0; start < len(w); {
		end := len(w)
		found := int32(-1)
		for end > start {
			sub := w[start:end]
			if start > 0 {
				buf = append(append(buf[:0], t.prefix...), sub...)
				sub = string(buf)
			}
			if id, ok := t.vocab[sub]; ok {
				found = id
				break
			}
			// Back one character.
			_, size := utf8.DecodeLastRuneInString(w[start:end])
			end -= size
		}
		if found < 0 {
			return append(ids[:n], t.unk)
		}
		ids = append(ids, found)
		start = end
	}
	return ids
}

func isWhitespace(c rune) bool {
	return c == '\t' || c == '\n' || c == '\r' || unicode.IsSpace(c)
}

func isControl(c rune) bool {
	if c == '\t' || c == '\n' || c == '\r' {
		return false
	}
	if v, ok := otherOverride[c]; ok {
		return v
	}
	return unicode.In(c, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs)
}

func isChinese(c rune) bool {
	switch {
	case 0x4E00 <= c && c <= 0x9FFF, 0x3400 <= c && c <= 0x4DBF, 0x20000 <= c && c <= 0x2A6DF,
		0x2A700 <= c && c <= 0x2B73F, 0x2B740 <= c && c <= 0x2B81F, 0x2B920 <= c && c <= 0x2CEAF,
		0xF900 <= c && c <= 0xFAFF, 0x2F800 <= c && c <= 0x2FA1F:
		return true
	}
	return false
}

func isPunct(c rune) bool {
	if c < 0x80 {
		return c > ' ' && c < 0x7F && !('0' <= c && c <= '9') && !('a' <= c && c <= 'z') && !('A' <= c && c <= 'Z')
	}
	if v, ok := punctOverride[c]; ok {
		return v
	}
	return unicode.IsPunct(c)
}

func isNonspacingMark(c rune) bool {
	if v, ok := markOverride[c]; ok {
		return v
	}
	return unicode.Is(unicode.Mn, c)
}
