package semanticmap

import (
	"math"
	"math/rand/v2"
	"sort"

	"gonum.org/v1/gonum/mat"
)

// pca fits the principal axes of the rows of x (n by d, row-major) and
// returns the mean and the first `keep` axes, each a unit vector of length
// d, largest variance first. The axes are the eigenvectors of the scatter
// matrix (x - mean)ᵀ(x - mean), which are the right singular vectors numpy's
// SVD gives up to sign. Fewer rows than `keep` span fewer directions, and
// only those are kept, as numpy's thin SVD keeps min(n, d).
func pca(x []float64, n, d, keep int) (mean []float64, axes [][]float64) {
	mean = make([]float64, d)
	for i := 0; i < n; i++ {
		row := x[i*d : (i+1)*d]
		for j, v := range row {
			mean[j] += v
		}
	}
	for j := range mean {
		mean[j] /= float64(n)
	}
	centered := make([]float64, n*d)
	for i := 0; i < n; i++ {
		for j := 0; j < d; j++ {
			centered[i*d+j] = x[i*d+j] - mean[j]
		}
	}
	a := mat.NewDense(n, d, centered)
	var scatter mat.SymDense
	scatter.SymOuterK(1, a.T())
	var eig mat.EigenSym
	if !eig.Factorize(&scatter, true) {
		return mean, nil
	}
	vals := eig.Values(nil)
	var vecs mat.Dense
	eig.VectorsTo(&vecs)
	idx := make([]int, d)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return vals[idx[a]] > vals[idx[b]] })
	if keep > d {
		keep = d
	}
	if keep > n {
		keep = n
	}
	axes = make([][]float64, keep)
	for k := 0; k < keep; k++ {
		col := make([]float64, d)
		// A fixed sign: the component's largest coordinate positive, so a
		// rebuild of the same data draws the same map.
		big := 0.0
		for j := 0; j < d; j++ {
			col[j] = vecs.At(j, idx[k])
			if math.Abs(col[j]) > math.Abs(big) {
				big = col[j]
			}
		}
		if big < 0 {
			for j := range col {
				col[j] = -col[j]
			}
		}
		axes[k] = col
	}
	return mean, axes
}

// kmeans places k centroids by k-means++ and refines them with up to iters
// steps of Lloyd's algorithm, stopping when no centroid moves (numpy's
// allclose: rtol 1e-5, atol 1e-8). points is n by dim, row-major.
func kmeans(points []float64, n, dim, k int, rng *rand.Rand, iters int) []float64 {
	cents := make([]float64, 0, k*dim)
	first := rng.IntN(n)
	cents = append(cents, points[first*dim:(first+1)*dim]...)
	d2 := make([]float64, n)
	for i := 0; i < n; i++ {
		d2[i] = sqdist(points[i*dim:(i+1)*dim], cents[:dim])
	}
	for c := 1; c < k; c++ {
		total := 0.0
		for _, v := range d2 {
			total += v
		}
		var pick int
		if total > 0 {
			// numpy's choice with p: the cumulative sum searched for a uniform draw.
			u := rng.Float64() * total
			acc := 0.0
			pick = n - 1
			for i, v := range d2 {
				acc += v
				if u < acc {
					pick = i
					break
				}
			}
		} else {
			pick = rng.IntN(n)
		}
		cents = append(cents, points[pick*dim:(pick+1)*dim]...)
		last := cents[c*dim : (c+1)*dim]
		for i := 0; i < n; i++ {
			if v := sqdist(points[i*dim:(i+1)*dim], last); v < d2[i] {
				d2[i] = v
			}
		}
	}
	assign := make([]int32, n)
	for it := 0; it < iters; it++ {
		nearest(points, n, dim, cents, k, assign)
		moved := make([]float64, k*dim)
		count := make([]int, k)
		for i := 0; i < n; i++ {
			c := int(assign[i])
			count[c]++
			row := points[i*dim : (i+1)*dim]
			for j, v := range row {
				moved[c*dim+j] += v
			}
		}
		for c := 0; c < k; c++ {
			if count[c] == 0 {
				copy(moved[c*dim:(c+1)*dim], cents[c*dim:(c+1)*dim])
				continue
			}
			for j := 0; j < dim; j++ {
				moved[c*dim+j] /= float64(count[c])
			}
		}
		same := true
		for i := range moved {
			if math.Abs(moved[i]-cents[i]) > 1e-8+1e-5*math.Abs(cents[i]) {
				same = false
				break
			}
		}
		if same {
			break
		}
		cents = moved
	}
	return cents
}

// nearest writes the index of the nearest centroid of each point to out;
// ties go to the lower index, as numpy's argmin does.
func nearest(points []float64, n, dim int, cents []float64, k int, out []int32) {
	for i := 0; i < n; i++ {
		row := points[i*dim : (i+1)*dim]
		best, bestD := 0, math.Inf(1)
		for c := 0; c < k; c++ {
			if d := sqdist(row, cents[c*dim:(c+1)*dim]); d < bestD {
				best, bestD = c, d
			}
		}
		out[i] = int32(best)
	}
}

func sqdist(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		d := a[i] - b[i]
		s += d * d
	}
	return s
}

// percentile is numpy's default (linear) percentile of sorted values.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	pos := q / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo] + (sorted[hi]-sorted[lo])*frac
}

// unitAxes scales each of the dim columns of values (n by dim) to [0, 1],
// from its 0.5th to 99.5th percentile, so a few far files do not squeeze
// the rest into a corner. Columns past the data's own (fewer than three
// components) are left at 0.
func unitAxes(values []float64, n, dim int) []float64 {
	out := make([]float64, n*dim)
	col := make([]float64, n)
	for j := 0; j < dim; j++ {
		for i := 0; i < n; i++ {
			col[i] = values[i*dim+j]
		}
		sort.Float64s(col)
		lo, hi := percentile(col, 0.5), percentile(col, 99.5)
		span := hi - lo
		if !(span > 0) {
			span = 1
		}
		for i := 0; i < n; i++ {
			v := (values[i*dim+j] - lo) / span
			out[i*dim+j] = math.Min(1, math.Max(0, v))
		}
	}
	return out
}

// sampleSorted picks size distinct indices of [0, n) at random, ascending.
func sampleSorted(rng *rand.Rand, n, size int) []int {
	if size >= n {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all
	}
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	for i := 0; i < size; i++ {
		j := i + rng.IntN(n-i)
		perm[i], perm[j] = perm[j], perm[i]
	}
	out := perm[:size]
	sort.Ints(out)
	return out
}
