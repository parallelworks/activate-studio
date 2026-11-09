package filter

import (
	"bytes"
	"compress/zlib"
	"io"
	"testing"
)

// TestFlateDamageEndsTheData: a bad checksum, or data cut short, ends the
// stream at what was decoded; damage from the first byte is an error.
func TestFlateDamageEndsTheData(t *testing.T) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write(bytes.Repeat([]byte("kept text "), 50))
	w.Close()
	good := z.Bytes()
	badSum := append([]byte(nil), good...)
	badSum[len(badSum)-1] ^= 0xFF
	for name, in := range map[string][]byte{"checksum": badSum, "cut": good[:len(good)-6]} {
		r, err := Apply(bytes.NewReader(in), "FlateDecode", Params{})
		if err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(r)
		if err != nil || !bytes.HasPrefix(out, []byte("kept text kept")) {
			t.Errorf("%s: %d bytes, %v", name, len(out), err)
		}
	}
	r, err := Apply(bytes.NewReader([]byte{0x78, 0x9c, 0xff, 0xff, 0xff, 0xff}), "FlateDecode", Params{})
	if err == nil {
		if _, err = io.ReadAll(r); err == nil {
			t.Error("no error for a stream damaged from the start")
		}
	}
}
