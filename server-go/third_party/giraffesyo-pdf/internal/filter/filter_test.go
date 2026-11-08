package filter

import (
	"bytes"
	"compress/flate"
	"compress/lzw"
	"compress/zlib"
	"encoding/ascii85"
	"io"
	"math/rand"
	"strings"
	"testing"
)

func decode(t *testing.T, data []byte, name string, p Params) []byte {
	t.Helper()
	r, err := Apply(bytes.NewReader(data), name, p)
	if err != nil {
		t.Fatalf("Apply(%s): %v", name, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return out
}

// compress writes data through w (a zlib/flate/lzw writer) and closes it.
func compress(t *testing.T, w io.WriteCloser, data []byte) {
	t.Helper()
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFlateRoundTrip(t *testing.T) {
	orig := []byte(strings.Repeat("stream content with repetition ", 50))
	var b bytes.Buffer
	compress(t, zlib.NewWriter(&b), orig)
	if got := decode(t, b.Bytes(), "FlateDecode", Params{}); !bytes.Equal(got, orig) {
		t.Errorf("zlib round trip: got %d bytes, want %d", len(got), len(orig))
	}
}

func TestFlateRawDeflateFallback(t *testing.T) {
	orig := []byte("headerless deflate stream from a sloppy generator")
	var b bytes.Buffer
	w, _ := flate.NewWriter(&b, flate.DefaultCompression)
	compress(t, w, orig)
	if got := decode(t, b.Bytes(), "FlateDecode", Params{}); !bytes.Equal(got, orig) {
		t.Errorf("raw deflate fallback: got %q", got)
	}
}

// lzwEncode is a minimal LZW encoder (MSB, 9-12 bit codes) with
// selectable EarlyChange, used to exercise the decoder. The early=false
// output is cross-checked against stdlib compress/lzw in TestLZWAgainstStdlib.
func lzwEncode(data []byte, early bool) []byte {
	e := 0
	if early {
		e = 1
	}
	var out bytes.Buffer
	var acc uint32
	var nbits uint
	width := uint(9)
	emit := func(code int) {
		acc = acc<<width | uint32(code&0xFFF) // codes are ≤ 12 bits
		nbits += width
		for nbits >= 8 {
			nbits -= 8
			out.WriteByte(byte(acc >> nbits & 0xFF))
			acc &= 1<<nbits - 1
		}
	}
	table := map[string]int{}
	next := 258
	reset := func() {
		table = map[string]int{}
		for i := range 256 {
			table[string([]byte{byte(i)})] = i
		}
		next = 258
		width = 9
	}
	reset()
	emit(256) // initial clear, as most encoders write
	var w []byte
	for _, c := range data {
		wc := append(append([]byte{}, w...), c)
		if _, ok := table[string(wc)]; ok {
			w = wc
			continue
		}
		emit(table[string(w)])
		if next < 4096 {
			table[string(wc)] = next
			next++
			// The decoder's table lags the encoder's by one entry, so
			// the encoder bumps at strictly-greater (calibrated against
			// compress/lzw byte-for-byte in TestLZWAgainstStdlib).
			if next+e > 1<<width && width < 12 {
				width++
			}
		}
		w = []byte{c}
	}
	if len(w) > 0 {
		emit(table[string(w)])
	}
	emit(257) // EOD
	if nbits > 0 {
		out.WriteByte(byte(acc << (8 - nbits) & 0xFF))
	}
	return out.Bytes()
}

func lzwTestData() []byte {
	// Enough repetitive-but-varied data to push past the 9->10->11 bit
	// width transitions, where EarlyChange actually matters.
	rng := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test data
	var b bytes.Buffer
	words := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	for b.Len() < 20000 {
		b.WriteString(words[rng.Intn(len(words))])
		b.WriteByte(byte(rng.Intn(16) & 0xFF))
	}
	return b.Bytes()
}

func TestLZWAgainstStdlib(t *testing.T) {
	// stdlib compress/lzw implements the EarlyChange=0 variant; our test
	// encoder must agree with it byte for byte, which validates the
	// encoder used for the EarlyChange=1 cases below.
	data := lzwTestData()
	var b bytes.Buffer
	compress(t, lzw.NewWriter(&b, lzw.MSB, 8), data)
	got := decode(t, b.Bytes(), "LZWDecode", Params{NoEarlyChange: true})
	if !bytes.Equal(got, data) {
		t.Fatalf("stdlib-encoded LZW: got %d bytes, want %d", len(got), len(data))
	}
	// Calibration: the test encoder's EarlyChange=0 output must be
	// byte-identical to stdlib's (both emit a leading clear code), which
	// anchors its EarlyChange=1 output one code earlier per spec.
	if mine := lzwEncode(data, false); !bytes.Equal(mine, b.Bytes()) {
		t.Error("test encoder (early=0) does not match compress/lzw output")
	}
}

func TestLZWEarlyChange(t *testing.T) {
	data := lzwTestData()
	for _, early := range []bool{true, false} {
		enc := lzwEncode(data, early)
		got := decode(t, enc, "LZWDecode", Params{NoEarlyChange: !early})
		if !bytes.Equal(got, data) {
			t.Errorf("early=%v: got %d bytes, want %d", early, len(got), len(data))
		}
	}
}

func TestLZWWrongEarlyChangeFails(t *testing.T) {
	// Decoding EarlyChange=1 data in EarlyChange=0 mode must not decode
	// cleanly to the same bytes (it desynchronizes at the width bump).
	data := lzwTestData()
	enc := lzwEncode(data, true)
	r, err := Apply(bytes.NewReader(enc), "LZWDecode", Params{NoEarlyChange: true})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	if bytes.Equal(got, data) {
		t.Error("mismatched EarlyChange decoded identically; test data too small?")
	}
}

func TestASCIIHex(t *testing.T) {
	cases := []struct{ in, want string }{
		{"48656C6C6F3E", "Hello>"},
		{"48 65 6c 6C\n6F>", "Hello"},
		{"48657>", "Hep"}, // odd digit padded with 0
		{"4865>trailing garbage ignored", "He"},
	}
	for _, c := range cases {
		if got := decode(t, []byte(c.in), "ASCIIHexDecode", Params{}); string(got) != c.want {
			t.Errorf("ASCIIHex(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := io.ReadAll(must(Apply(bytes.NewReader([]byte("4G>")), "ASCIIHexDecode", Params{}))); err == nil {
		t.Error("invalid hex byte must error")
	}
}

func TestASCII85(t *testing.T) {
	orig := []byte("ASCII85 round trip \x00\x00\x00\x00 with zeros and a longer tail")
	enc := make([]byte, ascii85.MaxEncodedLen(len(orig)))
	n := ascii85.Encode(enc, orig)
	for _, in := range []string{
		string(enc[:n]) + "~>",
		"<~" + string(enc[:n]) + "~>",
		strings.Join(strings.Split(string(enc[:n]), ""), "") + "~>\nmore bytes after eod",
	} {
		if got := decode(t, []byte(in), "ASCII85Decode", Params{}); !bytes.Equal(got, orig) {
			t.Errorf("ASCII85(%.20q...) = %q", in, got)
		}
	}
	if got := decode(t, []byte("z~>"), "ASCII85Decode", Params{}); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
		t.Errorf("z shortcut = %v", got)
	}
}

func TestRunLength(t *testing.T) {
	in := []byte{2, 'a', 'b', 'c', 254, 'x', 128}
	if got := decode(t, in, "RunLengthDecode", Params{}); string(got) != "abcxxx" {
		t.Errorf("RunLength = %q, want abcxxx", got)
	}
	// Truncated run yields the decoded prefix, no error.
	if got := decode(t, []byte{2, 'a'}, "RunLengthDecode", Params{}); string(got) != "a" {
		t.Errorf("truncated RunLength = %q, want a", got)
	}
}

func TestPNGPredictors(t *testing.T) {
	p := Params{Predictor: 12, Columns: 4} // family selector; tags rule per row
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"up", []byte{2, 0x61, 0x62, 0x63, 0x64, 2, 4, 4, 4, 4}, "abcdefgh"},
		{"sub", []byte{1, 0x61, 1, 1, 1}, "abcd"},
		{"none", []byte{0, 0x61, 0x62, 0x63, 0x64}, "abcd"},
		{"paeth", []byte{4, 0x61, 1, 1, 1}, "abcd"},
		{"average", []byte{3, 0x61, 0x32, 0x32, 0x33}, "abcd"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		compress(t, zlib.NewWriter(&b), c.in)
		if got := decode(t, b.Bytes(), "FlateDecode", p); string(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTIFFPredictor(t *testing.T) {
	var b bytes.Buffer
	compress(t, zlib.NewWriter(&b), []byte{0x61, 1, 1, 1})
	got := decode(t, b.Bytes(), "FlateDecode", Params{Predictor: 2, Columns: 4})
	if string(got) != "abcd" {
		t.Errorf("TIFF predictor = %q, want abcd", got)
	}
}

func TestPredictorHostileColumns(t *testing.T) {
	var b bytes.Buffer
	compress(t, zlib.NewWriter(&b), []byte{0})
	if _, err := Apply(bytes.NewReader(b.Bytes()), "FlateDecode", Params{Predictor: 12, Columns: 1 << 30}); err == nil {
		t.Error("hostile Columns must be rejected")
	}
}

func TestUnsupportedFilters(t *testing.T) {
	for _, name := range []string{"DCTDecode", "JPXDecode", "CCITTFaxDecode", "JBIG2Decode", "Bogus"} {
		if _, err := Apply(bytes.NewReader(nil), name, Params{}); err == nil {
			t.Errorf("filter %s must be rejected", name)
		}
	}
}

func must(r io.Reader, err error) io.Reader {
	if err != nil {
		panic(err)
	}
	return r
}

// TestFlateReleaseReturnsToPool: a released FlateDecode reader hands its
// decompressor back, refuses further reads, and tolerates a second
// Release; the next stream decodes correctly on the recycled state.
func TestFlateReleaseReturnsToPool(t *testing.T) {
	for i := range 3 {
		orig := bytes.Repeat([]byte{byte('a' + i)}, 70000) // wider than one window
		var b bytes.Buffer
		compress(t, zlib.NewWriter(&b), orig)
		r := must(Apply(bytes.NewReader(b.Bytes()), "FlateDecode", Params{}))
		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, orig) {
			t.Fatalf("round %d: got %d bytes, err %v", i, len(got), err)
		}
		rel, ok := r.(Releaser)
		if !ok {
			t.Fatalf("FlateDecode reader is %T, want Releaser", r)
		}
		rel.Release()
		rel.Release()
		if _, err := r.Read(make([]byte, 1)); err == nil {
			t.Fatal("read after Release succeeded")
		}
	}
}

func TestFlatePredictorForwardsRelease(t *testing.T) {
	var b bytes.Buffer
	compress(t, zlib.NewWriter(&b), []byte{2, 1, 1, 1})
	r := must(Apply(bytes.NewReader(b.Bytes()), "FlateDecode", Params{Predictor: 12, Columns: 3}))
	if _, ok := r.(Releaser); !ok {
		t.Fatalf("predictor-wrapped reader is %T, want Releaser", r)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatal(err)
	}
	r.(Releaser).Release()
	if _, err := r.Read(make([]byte, 1)); err == nil {
		t.Fatal("read after Release succeeded")
	}
}
