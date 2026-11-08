package stepmesh

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// Encode is model.ts's encodeModel: the bytes of a model-cache file,
// little endian throughout:
//
//	uint32   header length in bytes
//	bytes    that many bytes of UTF-8 JSON, padded with spaces to a multiple of 4
//	bytes    the arrays, back to back, in the order the header names them
//
// The header is JSON.stringify({format, triangles, meshes}), each mesh
// {name, color, positions, normals, indices} with the keys that are
// undefined left out and each array a span {o: byte offset, n: length}.
// triangles is the sum of the index counts over three, or, with no index
// arrays (STL), the positions over nine as the caller gives it.
func Encode(format string, triangles float64, meshes []Mesh) []byte {
	var blobs bytes.Buffer
	span := func(n int) string {
		return `{"o":` + strconv.Itoa(blobs.Len()) + `,"n":` + strconv.Itoa(n) + `}`
	}
	var h bytes.Buffer
	h.WriteString(`{"format":`)
	f, _ := jsonobj.Marshal(format)
	h.Write(f)
	h.WriteString(`,"triangles":` + jsonNumber(triangles) + `,"meshes":[`)
	for i, m := range meshes {
		if i > 0 {
			h.WriteByte(',')
		}
		h.WriteByte('{')
		if m.Name != "" {
			n, _ := jsonobj.Marshal(m.Name)
			h.WriteString(`"name":`)
			h.Write(n)
			h.WriteByte(',')
		}
		if m.Color != nil {
			h.WriteString(`"color":` + jsonArray(m.Color) + `,`)
		}
		h.WriteString(`"positions":` + span(len(m.Positions)))
		putFloats(&blobs, m.Positions)
		if m.Normals != nil {
			h.WriteString(`,"normals":` + span(len(m.Normals)))
			putFloats(&blobs, m.Normals)
		}
		if m.Indices != nil {
			h.WriteString(`,"indices":` + span(len(m.Indices)))
			for _, x := range m.Indices {
				binary.Write(&blobs, binary.LittleEndian, x)
			}
		}
		h.WriteByte('}')
	}
	h.WriteString(`]}`)
	pad := (4 - h.Len()%4) % 4
	var out bytes.Buffer
	out.Grow(4 + h.Len() + pad + blobs.Len())
	binary.Write(&out, binary.LittleEndian, uint32(h.Len()+pad))
	out.Write(h.Bytes())
	out.Write(bytes.Repeat([]byte{' '}, pad))
	out.Write(blobs.Bytes())
	return out.Bytes()
}

// Triangles is parseStep's count: the index arrays' lengths over three.
func Triangles(meshes []Mesh) float64 {
	n := 0
	for _, m := range meshes {
		n += len(m.Indices)
	}
	return float64(n) / 3
}

func putFloats(b *bytes.Buffer, fs []float32) {
	var w [4]byte
	for _, f := range fs {
		binary.LittleEndian.PutUint32(w[:], math.Float32bits(f))
		b.Write(w[:])
	}
}

// jsonNumber is a number as JSON.stringify writes it (null when not finite).
func jsonNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	return jsNumber(f)
}

// jsonArray is JSON.stringify of an array of the values emval holds.
func jsonArray(items []any) string {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, x := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(jsonValue(x))
	}
	b.WriteByte(']')
	return b.String()
}

func jsonValue(x any) string {
	switch v := x.(type) {
	case nil, undefinedT:
		// A hole or undefined in an array is null.
		return "null"
	case float64:
		return jsonNumber(v)
	case bool:
		return strconv.FormatBool(v)
	case string:
		s, _ := jsonobj.Marshal(v)
		return string(s)
	case *jsArray:
		items := make([]any, len(v.items))
		for i, it := range v.items {
			items[i] = deref(it)
		}
		return jsonArray(items)
	case *jsObject:
		var b bytes.Buffer
		b.WriteByte('{')
		first := true
		for _, k := range v.keys {
			if _, u := v.vals[k].(undefinedT); u {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			kb, _ := jsonobj.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			b.WriteString(jsonValue(v.vals[k]))
		}
		b.WriteByte('}')
		return b.String()
	}
	return "null"
}
