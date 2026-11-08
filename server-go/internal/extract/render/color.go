package render

import (
	"math"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// A colorSpace turns colour operands into grey, the only colour OCR needs.
type colorSpace struct {
	family string // Gray, RGB, CMYK, Lab, Indexed, Tint, Pattern
	n      int    // operands a colour takes
	base   *colorSpace
	hival  int
	lookup []byte
}

var (
	deviceGray = &colorSpace{family: "Gray", n: 1}
	deviceRGB  = &colorSpace{family: "RGB", n: 3}
	deviceCMYK = &colorSpace{family: "CMYK", n: 4}
	patternCS  = &colorSpace{family: "Pattern"}
)

// gray is the grey of components c, 0 black to 1 white.
func (cs *colorSpace) gray(c []float64) float64 {
	at := func(i int) float64 {
		if i < len(c) {
			return clamp01(c[i])
		}
		return 0
	}
	switch cs.family {
	case "Gray":
		return at(0)
	case "RGB":
		return 0.3*at(0) + 0.59*at(1) + 0.11*at(2)
	case "CMYK":
		return 1 - math.Min(1, 0.3*at(0)+0.59*at(1)+0.11*at(2)+at(3))
	case "Lab":
		if len(c) > 0 {
			return clamp01(c[0] / 100)
		}
		return 0
	case "Tint":
		// A separation or DeviceN colour: no tint is white, full tint
		// black, as an ink on paper prints. The tint transform to the
		// alternate space is not run.
		t := 0.0
		for _, v := range c {
			t = math.Max(t, clamp01(v))
		}
		return 1 - t
	case "Indexed":
		i := 0
		if len(c) > 0 && !math.IsNaN(c[0]) {
			i = int(math.Max(0, math.Min(float64(cs.hival), math.Round(c[0]))))
		}
		n := cs.base.n
		if n <= 0 || (i+1)*n > len(cs.lookup) {
			return 0
		}
		comp := make([]float64, n)
		for k := range comp {
			comp[k] = float64(cs.lookup[i*n+k]) / 255
		}
		if cs.base.family == "Lab" {
			comp[0] *= 100
		}
		return cs.base.gray(comp)
	}
	return 0
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}

func toPaint(g, alpha float64) paint {
	return paint{gray: uint8(clamp01(g)*255 + 0.5), alpha: alpha}
}

// initial is the colour a space starts with when selected: black, or
// nothing for a pattern.
func (cs *colorSpace) initial(alpha float64) paint {
	switch cs.family {
	case "Pattern":
		return paint{none: true, alpha: alpha}
	case "Tint":
		return toPaint(cs.gray([]float64{1}), alpha)
	case "CMYK":
		return toPaint(0, alpha)
	}
	return toPaint(cs.gray(nil), alpha)
}

// paint is the colour sc or scn sets from the operands. A pattern, the
// last operand a name, paints nothing: OCR misses little by skipping
// shadings and tiles, and drawing them as a solid would hide what is
// over them.
func (cs *colorSpace) paint(ops []operand, alpha float64) paint {
	if cs.family == "Pattern" || len(ops) > 0 && ops[len(ops)-1].kind == 'N' {
		return paint{none: true, alpha: alpha}
	}
	var c []float64
	for _, o := range ops {
		if o.kind == 'n' {
			c = append(c, o.num)
		}
	}
	return toPaint(cs.gray(c), alpha)
}

// deviceColor runs g, G, rg, RG, k and K, which also select the space.
func (r *renderer) deviceColor(op string, ops []operand) {
	var cs *colorSpace
	switch op {
	case "g", "G":
		cs = deviceGray
	case "rg", "RG":
		cs = deviceRGB
	default:
		cs = deviceCMYK
	}
	if op == "g" || op == "rg" || op == "k" {
		r.st.fillCS, r.st.fill = cs, cs.paint(ops, r.st.fill.alpha)
	} else {
		r.st.strCS, r.st.stroke = cs, cs.paint(ops, r.st.stroke.alpha)
	}
}

// colorSpace resolves the space cs or CS names: a device space, or one in
// the resources.
func (r *renderer) colorSpace(res types.Dict, name string) *colorSpace {
	if cs := familyByName(name); cs != nil {
		return cs
	}
	if d := r.dict(res, "ColorSpace"); d != nil {
		return r.parseColorSpace(d[name], 0)
	}
	return deviceGray
}

func familyByName(name string) *colorSpace {
	switch name {
	case "DeviceGray", "G", "CalGray":
		return deviceGray
	case "DeviceRGB", "RGB", "CalRGB":
		return deviceRGB
	case "DeviceCMYK", "CMYK":
		return deviceCMYK
	case "Pattern":
		return patternCS
	case "Lab":
		return &colorSpace{family: "Lab", n: 3}
	}
	return nil
}

func (r *renderer) parseColorSpace(o types.Object, depth int) *colorSpace {
	if depth > 4 {
		return deviceGray
	}
	o, err := r.pc.Dereference(o)
	if err != nil || o == nil {
		return deviceGray
	}
	switch v := o.(type) {
	case types.Name:
		if cs := familyByName(v.Value()); cs != nil {
			return cs
		}
	case types.Array:
		if len(v) == 0 {
			break
		}
		fam, _ := r.pc.Dereference(v[0])
		nm, ok := fam.(types.Name)
		if !ok {
			break
		}
		switch nm.Value() {
		case "ICCBased":
			if len(v) > 1 {
				if sd, _, err := r.pc.DereferenceStreamDict(v[1]); err == nil && sd != nil {
					if alt := sd.Dict["Alternate"]; alt != nil {
						return r.parseColorSpace(alt, depth+1)
					}
					if n := sd.Dict.IntEntry("N"); n != nil {
						switch *n {
						case 3:
							return deviceRGB
						case 4:
							return deviceCMYK
						}
					}
				}
			}
			return deviceGray
		case "Indexed", "I":
			if len(v) < 4 {
				break
			}
			base := r.parseColorSpace(v[1], depth+1)
			hival, _ := r.number(v[2])
			return &colorSpace{family: "Indexed", n: 1, base: base, hival: int(math.Max(0, math.Min(255, hival))), lookup: r.lookup(v[3])}
		case "Separation":
			return &colorSpace{family: "Tint", n: 1}
		case "DeviceN":
			n := 1
			if len(v) > 1 {
				if a, err := r.pc.DereferenceArray(v[1]); err == nil {
					n = max(1, len(a))
				}
			}
			return &colorSpace{family: "Tint", n: n}
		case "Pattern":
			return patternCS
		default:
			if cs := familyByName(nm.Value()); cs != nil {
				return cs
			}
		}
	}
	return deviceGray
}

// lookup is an indexed space's table: a string or a stream.
func (r *renderer) lookup(o types.Object) []byte {
	o, err := r.pc.Dereference(o)
	if err != nil || o == nil {
		return nil
	}
	switch v := o.(type) {
	case types.StringLiteral:
		b, err := types.Unescape(v.Value())
		if err == nil {
			return b
		}
	case types.HexLiteral:
		b, err := v.Bytes()
		if err == nil {
			return b
		}
	case types.StreamDict:
		return r.decode(&v)
	}
	return nil
}
