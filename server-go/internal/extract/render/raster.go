package render

import (
	"image"
	"math"
	"sort"
)

// pt is a point in device space: pixels, y down.
type pt struct{ x, y float64 }

// A polygon is a closed polyline in device space.
type polygon []pt

// subsamples is how many sample rows a pixel row is cut into. Spans are
// exact horizontally, so this is the vertical anti-aliasing.
const subsamples = 4

type edge struct {
	x0, y0, x1, y1 float64 // y0 < y1
	dir            int
}

// coverage rasterizes polygons under the nonzero or even-odd rule into a
// mask over the part of bounds they touch, 0 to 255 a pixel, and returns
// nil when nothing is covered.
func coverage(polys []polygon, evenOdd bool, bounds image.Rectangle) *image.Alpha {
	var edges []edge
	minx, miny, maxx, maxy := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range polys {
		n := len(p)
		if n < 3 {
			continue
		}
		for i := 0; i < n; i++ {
			a, b := p[i], p[(i+1)%n]
			minx, maxx = math.Min(minx, math.Min(a.x, b.x)), math.Max(maxx, math.Max(a.x, b.x))
			miny, maxy = math.Min(miny, math.Min(a.y, b.y)), math.Max(maxy, math.Max(a.y, b.y))
			switch {
			case a.y < b.y:
				edges = append(edges, edge{a.x, a.y, b.x, b.y, 1})
			case a.y > b.y:
				edges = append(edges, edge{b.x, b.y, a.x, a.y, -1})
			}
		}
	}
	if len(edges) == 0 {
		return nil
	}
	box := image.Rect(int(math.Floor(minx)), int(math.Floor(miny)), int(math.Ceil(maxx))+1, int(math.Ceil(maxy))+1).Intersect(bounds)
	if box.Empty() {
		return nil
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].y0 < edges[j].y0 })
	mask := image.NewAlpha(box)
	w := box.Dx()
	cover := make([]float32, w+1)
	diff := make([]float32, w+1)
	type cross struct {
		x   float64
		dir int
	}
	var active []int
	var xs []cross
	next := 0
	hit := false
	left := float64(box.Min.X)
	for py := box.Min.Y; py < box.Max.Y; py++ {
		rowHit := false
		for s := 0; s < subsamples; s++ {
			y := float64(py) + (float64(s)+0.5)/subsamples
			for next < len(edges) && edges[next].y0 <= y {
				active = append(active, next)
				next++
			}
			xs = xs[:0]
			k := 0
			for _, ei := range active {
				e := &edges[ei]
				if e.y1 <= y {
					continue // done with this edge
				}
				active[k] = ei
				k++
				t := (y - e.y0) / (e.y1 - e.y0)
				xs = append(xs, cross{e.x0 + t*(e.x1-e.x0), e.dir})
			}
			active = active[:k]
			if len(xs) < 2 {
				continue
			}
			sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
			wind := 0
			for i := 0; i+1 < len(xs); i++ {
				wind += xs[i].dir
				inside := wind != 0
				if evenOdd {
					inside = wind%2 != 0
				}
				if !inside {
					continue
				}
				a := math.Max(xs[i].x-left, 0)
				b := math.Min(xs[i+1].x-left, float64(w))
				if b > a {
					span(cover, diff, a, b, 1.0/subsamples)
					rowHit = true
				}
			}
		}
		if !rowHit {
			continue
		}
		var run float32
		off := (py - box.Min.Y) * mask.Stride
		for x := 0; x < w; x++ {
			run += diff[x]
			c := run + cover[x]
			cover[x], diff[x] = 0, 0
			if c > 1 {
				c = 1
			}
			if c > 0.002 {
				mask.Pix[off+x] = uint8(c*255 + 0.5)
				hit = true
			}
		}
		cover[w], diff[w] = 0, 0
	}
	if !hit {
		return nil
	}
	return mask
}

// span adds weight times the covered length of [a, b) to each pixel: the
// partial pixels at the ends directly, the whole ones between as a run
// in diff.
func span(cover, diff []float32, a, b float64, weight float32) {
	p0, p1 := int(a), int(b)
	if p0 == p1 {
		cover[p0] += float32(b-a) * weight
		return
	}
	cover[p0] += float32(float64(p0+1)-a) * weight
	diff[p0+1] += weight
	diff[p1] -= weight
	cover[p1] += float32(b-float64(p1)) * weight
}

// flatness is how far, in pixels, a flattened curve may stray.
const flatness = 0.2

// cubic appends the points of a Bézier curve from p0, flattened.
func cubic(out []pt, p0, p1, p2, p3 pt) []pt {
	d := math.Hypot(p1.x-p0.x, p1.y-p0.y) + math.Hypot(p2.x-p1.x, p2.y-p1.y) + math.Hypot(p3.x-p2.x, p3.y-p2.y)
	n := int(math.Ceil(math.Sqrt(d / flatness)))
	n = max(1, min(n, 256))
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		a, b, c, e := u*u*u, 3*u*u*t, 3*u*t*t, t*t*t
		out = append(out, pt{a*p0.x + b*p1.x + c*p2.x + e*p3.x, a*p0.y + b*p1.y + c*p2.y + e*p3.y})
	}
	return out
}

// stroke outlines a polyline of the given width (device pixels, along
// each axis of the user-space to device transform m) as polygons: a quad
// for each segment and a disc at each joint and end, which covers round
// joins and caps, and miter and bevel joins to within the line width.
func stroke(line []pt, closed bool, m mat, width float64) []polygon {
	if len(line) == 0 {
		return nil
	}
	// Half the width as a device-space offset: the line width is in user
	// space, so it scales with the transform.
	sx := math.Hypot(m[0], m[1])
	sy := math.Hypot(m[2], m[3])
	h := width * math.Sqrt(math.Abs(sx*sy)) / 2
	if h < 0.5 {
		h = 0.5 // the thinnest line a device can show
	}
	var out []polygon
	disc := func(c pt) {
		const sides = 8
		p := make(polygon, sides)
		for i := range p {
			// Turning the way the segment quads turn, so that under the
			// nonzero rule overlaps add rather than cancel.
			a := -2 * math.Pi * float64(i) / sides
			p[i] = pt{c.x + h*math.Cos(a), c.y + h*math.Sin(a)}
		}
		out = append(out, p)
	}
	n := len(line)
	segs := n - 1
	if closed && n > 2 {
		segs = n
	}
	if segs == 0 {
		disc(line[0])
		return out
	}
	for i := 0; i < segs; i++ {
		a, b := line[i], line[(i+1)%n]
		dx, dy := b.x-a.x, b.y-a.y
		l := math.Hypot(dx, dy)
		if l == 0 {
			continue
		}
		nx, ny := -dy/l*h, dx/l*h
		out = append(out, polygon{{a.x + nx, a.y + ny}, {b.x + nx, b.y + ny}, {b.x - nx, b.y - ny}, {a.x - nx, a.y - ny}})
	}
	if h > 1 {
		for _, p := range line {
			disc(p)
		}
	}
	return out
}
