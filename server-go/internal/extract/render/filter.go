package render

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"io"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// decode is a stream's data through its filters, within the page's
// content budget, with what a damaged stream yields before the damage:
// pdftoppm draws a page's content up to the point a stream breaks off.
func (r *renderer) decode(sd *types.StreamDict) []byte {
	left := int64(maxContent - r.content)
	if left <= 0 {
		r.content = maxContent + 1
		return nil
	}
	var b []byte
	guard(func() error {
		b = decodeStream(sd, left)
		return nil
	})
	r.content += len(b)
	return b
}

func decodeStream(sd *types.StreamDict, limit int64) []byte {
	b := sd.Raw
	for _, f := range sd.FilterPipeline {
		parms := map[string]int{}
		for k, v := range f.DecodeParms {
			switch x := v.(type) {
			case types.Integer:
				parms[k] = x.Value()
			case types.Boolean:
				if x.Value() {
					parms[k] = 1
				} else {
					parms[k] = 0
				}
			}
		}
		switch f.Name {
		case filter.Flate:
			if p, ok := parms["Predictor"]; !ok || p <= 1 {
				b = inflate(b, limit)
				continue
			}
		case filter.DCT, filter.JPX, filter.CCITTFax, filter.JBIG2:
			return nil // image data, not content
		}
		fi, err := filter.NewFilter(f.Name, parms)
		if err != nil {
			return nil
		}
		out, err := fi.DecodeLength(bytes.NewReader(b), limit)
		if err != nil {
			return nil
		}
		b, _ = io.ReadAll(io.LimitReader(out, limit))
	}
	if int64(len(b)) > limit {
		b = b[:limit]
	}
	return b
}

// inflate decompresses zlib data, or raw deflate data when the zlib
// header is missing, keeping what comes before an error.
func inflate(b []byte, limit int64) []byte {
	var rd io.Reader
	if zr, err := zlib.NewReader(bytes.NewReader(b)); err == nil {
		rd = zr
	} else {
		rd = flate.NewReader(bytes.NewReader(b))
	}
	var out bytes.Buffer
	io.Copy(&out, io.LimitReader(rd, limit)) // an error keeps what was read
	return out.Bytes()
}
