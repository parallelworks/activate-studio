// Package semanticmap makes the semantic map of an index: every file with
// embeddings placed by what it says, for the Stats page. It is a port of
// indexer/semantic_map.py and writes the same files (overview.json version
// 3 and cluster-<c>.json), so the server and the web app read either.
//
// Each file's passage vectors are averaged; a PCA fitted on at most Options.Fit
// files projects every file to three components for the map and sixteen for
// grouping; k-means makes 4 to 14 clusters, numbered largest first. Each
// cluster is named for its distinctive terms (TF-IDF of its members' passage
// text against the whole map) or, without two such terms, for its folders,
// and linked to the two clusters nearest it by the cosine of their mean
// vectors. The result is written to a scratch folder and swapped in whole,
// so a reader sees the old map or the new one.
package semanticmap

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GroupDims is how many components the clusters are made in.
const GroupDims = 16

// Options are the command line's: the overview's sample size, the files the
// projection is fitted on, and, for vectors fetched from another system, how
// many files have vectors there (Of, nil when not given).
type Options struct {
	Sample int
	Fit    int
	Of     *int
	// Seed for the random choices (fit rows, k-means starts, cluster order).
	Seed uint64
}

// MinSample and MinFit are the smallest Options.Sample and Options.Fit, the
// floors the command line puts on --sample and --fit.
const (
	MinSample = 100
	MinFit    = 1000
)

// validate rejects options the build cannot run with, such as the zero
// value, whose empty fit set has no projection.
func (o Options) validate() error {
	if o.Sample < MinSample || o.Fit < MinFit {
		return fmt.Errorf("semantic map: Sample must be at least %d and Fit at least %d (got %d and %d)", MinSample, MinFit, o.Sample, o.Fit)
	}
	if o.Of != nil && *o.Of < 0 {
		return fmt.Errorf("semantic map: Of must not be negative (got %d)", *o.Of)
	}
	return nil
}

// Num is a coordinate or similarity, written with at most four decimals.
type Num float64

// MarshalJSON writes the value as Python's round(v, 4) prints it.
func (n Num) MarshalJSON() ([]byte, error) {
	v := round4(float64(n))
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return []byte("0"), nil
	}
	return []byte(strconv.FormatFloat(v, 'f', -1, 64)), nil
}

// Cluster is one cluster in overview.json.
type Cluster struct {
	C      int      `json:"c"`
	Label  string   `json:"label"`
	Folder string   `json:"folder"`
	Terms  []string `json:"terms"`
	Count  int      `json:"count"`
	X      Num      `json:"x"`
	Y      Num      `json:"y"`
	Z      Num      `json:"z"`
}

// Point is a file in the overview's sample.
type Point struct {
	P string `json:"p"`
	X Num    `json:"x"`
	Y Num    `json:"y"`
	Z Num    `json:"z"`
	C int    `json:"c"`
}

type clusterPoint struct {
	P string `json:"p"`
	X Num    `json:"x"`
	Y Num    `json:"y"`
	Z Num    `json:"z"`
}

// Overview is overview.json.
type Overview struct {
	Version   int       `json:"version"`
	Available bool      `json:"available"`
	BuiltAt   string    `json:"builtAt"`
	Model     string    `json:"model"`
	Files     int       `json:"files"`
	Shown     int       `json:"shown"`
	Clusters  []Cluster `json:"clusters"`
	Links     []Link    `json:"links"`
	Points    []Point   `json:"points"`
	Ms        int64     `json:"ms"`
	ReadMs    int64     `json:"readMs"`
	Dbs       int       `json:"dbs"`
	Of        *int      `json:"of,omitempty"`
}

// Summary is the line printed when a build ends, as the Python script
// prints it; the server logs it.
type Summary struct {
	Available bool   `json:"available"`
	Files     int    `json:"files"`
	Shown     *int   `json:"shown,omitempty"`
	Ms        *int64 `json:"ms,omitempty"`
	ReadMs    *int64 `json:"readMs,omitempty"`
	Dbs       int    `json:"dbs"`
	Model     string `json:"model,omitempty"`
	Of        *int   `json:"of,omitempty"`
	Clusters  *int   `json:"clusters,omitempty"`
}

// Build makes the map of the files src gives into out, replacing it whole.
// With fewer than three files no map is made and out is left as it was.
func Build(src Source, out string, opt Options) (Summary, error) {
	if err := opt.validate(); err != nil {
		return Summary{}, err
	}
	t0 := time.Now()
	rng := rand.New(rand.NewPCG(opt.Seed, 0x5eed))
	parent := filepath.Dir(out)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Summary{}, err
	}
	scratch, err := os.MkdirTemp(parent, "semantic-map-*")
	if err != nil {
		return Summary{}, err
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(scratch)
		}
	}()

	// Each file's mean vector to a scratch file, and its terms one line per
	// file in the same order, so memory stays bounded on a large index.
	meansPath := filepath.Join(scratch, "means.f32")
	termsPath := filepath.Join(scratch, "terms.txt")
	meansF, err := os.Create(meansPath)
	if err != nil {
		return Summary{}, err
	}
	termsF, err := os.Create(termsPath)
	if err != nil {
		meansF.Close()
		return Summary{}, err
	}
	mw, tw := bufio.NewWriterSize(meansF, 1<<20), bufio.NewWriterSize(termsF, 1<<20)
	df := map[string]int{}
	nText := 0
	var paths []string
	models := map[string]int{}
	var modelOrder []string
	buf := make([]byte, vectorBytes)
	dbs, err := src(func(f File) error {
		if _, ok := models[f.Model]; !ok {
			modelOrder = append(modelOrder, f.Model)
		}
		models[f.Model]++
		paths = append(paths, f.Path)
		for i, v := range f.Vec {
			binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
		}
		if _, err := mw.Write(buf); err != nil {
			return err
		}
		ts := make([]string, len(f.Terms))
		for i, t := range f.Terms {
			ts[i] = t.Term
		}
		if _, err := tw.WriteString(strings.Join(ts, " ") + "\n"); err != nil {
			return err
		}
		if len(f.Terms) > 0 {
			nText++
			for _, t := range ts {
				df[t]++
			}
		}
		// A term seen in one file only cannot name a cluster; on a very
		// large map those are let go so the tally stays bounded.
		if len(df) > 1_500_000 {
			for t, v := range df {
				if v <= 1 {
					delete(df, t)
				}
			}
		}
		return nil
	})
	if err == nil {
		err = mw.Flush()
	}
	if err == nil {
		err = tw.Flush()
	}
	termsF.Close()
	if cerr := meansF.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Summary{}, err
	}
	readMs := time.Since(t0).Milliseconds()
	n := len(paths)
	if n < 3 {
		return Summary{Available: false, Files: n, Dbs: dbs}, nil
	}
	// An index holds one model; should a re-embedding be part way, the map
	// is labelled with the model most of its files use.
	model := ""
	for _, m := range modelOrder {
		if model == "" || models[m] > models[model] {
			model = m
		}
	}

	means, err := os.Open(meansPath)
	if err != nil {
		return Summary{}, err
	}
	defer means.Close()
	fitRows := sampleSorted(rng, n, min(n, opt.Fit))
	xf := make([]float64, len(fitRows)*Dims)
	row := make([]float32, Dims)
	for r, i := range fitRows {
		if err := readRow(means, i, row, buf); err != nil {
			return Summary{}, err
		}
		for j, v := range row {
			xf[r*Dims+j] = float64(v)
		}
	}
	mean, axes := pca(xf, len(fitRows), Dims, GroupDims)
	xf = nil
	nc := len(axes)
	proj := make([]float64, n*nc)
	// Sums of the means per file, block by block, are kept for the links
	// once the clusters are known; the projection reads the file once here.
	if _, err := means.Seek(0, io.SeekStart); err != nil {
		return Summary{}, err
	}
	br := bufio.NewReaderSize(means, 1<<20)
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(br, buf); err != nil {
			return Summary{}, err
		}
		decodeRow(buf, row)
		for c, ax := range axes {
			s := 0.0
			for j, v := range row {
				s += (float64(v) - mean[j]) * ax[j]
			}
			proj[i*nc+c] = s
		}
	}
	pos := make([]float64, n*3)
	for i := 0; i < n; i++ {
		for c := 0; c < 3 && c < nc; c++ {
			pos[i*3+c] = proj[i*nc+c]
		}
	}
	coords := unitAxes(pos, n, 3)
	pos = nil

	k := max(4, min(14, int(math.RoundToEven(math.Sqrt(float64(n)/2)))))
	fitPts := make([]float64, len(fitRows)*nc)
	for r, i := range fitRows {
		copy(fitPts[r*nc:(r+1)*nc], proj[i*nc:(i+1)*nc])
	}
	cents := kmeans(fitPts, len(fitRows), nc, k, rng, 30)
	fitPts = nil
	assign := make([]int32, n)
	nearest(proj, n, nc, cents, k, assign)
	proj = nil
	// Clusters numbered by size, largest first, so colors and the legend
	// follow from one build to the next.
	sizes := make([]int, k)
	for _, a := range assign {
		sizes[a]++
	}
	order := make([]int, k)
	for i := range order {
		order[i] = i
	}
	stableSortDesc(order, sizes)
	rank := make([]int32, k)
	for r, c := range order {
		rank[c] = int32(r)
	}
	for i, a := range assign {
		assign[i] = rank[a]
	}
	kept := 0
	for _, s := range sizes {
		if s > 0 {
			kept++
		}
	}
	k = kept
	members := make([][]int, k)
	for i, a := range assign {
		members[a] = append(members[a], i)
	}
	folders := labelClusters(paths, members, k)
	cands, err := clusterTerms(termsPath, assign, k, df, nText, 8)
	if err != nil {
		return Summary{}, err
	}
	labels, labelTerms := nameClusters(cands, folders, 26)

	// The links: each cluster's sum of its files' means, in the full space.
	sums := make([][]float64, k)
	for c := range sums {
		sums[c] = make([]float64, Dims)
	}
	if _, err := means.Seek(0, io.SeekStart); err != nil {
		return Summary{}, err
	}
	br.Reset(means)
	for i := 0; i < n; i++ {
		if _, err := io.ReadFull(br, buf); err != nil {
			return Summary{}, err
		}
		decodeRow(buf, row)
		s := sums[assign[i]]
		for j, v := range row {
			s[j] += float64(v)
		}
	}
	links := clusterLinks(sums, k, LinksPerCluster)

	// The sample: each cluster's share of opt.Sample, at least 25 files (or
	// all it has), so small clusters still show.
	quotas := make([]int, k)
	total := 0
	for c := 0; c < k; c++ {
		quotas[c] = min(len(members[c]), max(25, int(math.RoundToEven(float64(opt.Sample)*float64(len(members[c]))/float64(n)))))
		total += quotas[c]
	}
	for total > opt.Sample {
		big := 0
		for c := range quotas {
			if quotas[c] > quotas[big] {
				big = c
			}
		}
		if quotas[big] <= 25 {
			break
		}
		quotas[big]--
		total--
	}
	clusters := make([]Cluster, 0, k)
	points := []Point{}
	for c := 0; c < k; c++ {
		m := append([]int(nil), members[c]...)
		rng.Shuffle(len(m), func(i, j int) { m[i], m[j] = m[j], m[i] })
		var cx, cy, cz float64
		for _, i := range m {
			cx += coords[i*3]
			cy += coords[i*3+1]
			cz += coords[i*3+2]
		}
		cnt := float64(len(m))
		terms := labelTerms[c]
		if terms == nil {
			terms = []string{}
		}
		clusters = append(clusters, Cluster{C: c, Label: labels[c], Folder: folders[c], Terms: terms, Count: len(m),
			X: Num(cx / cnt), Y: Num(cy / cnt), Z: Num(cz / cnt)})
		cps := make([]clusterPoint, len(m))
		for j, i := range m {
			cps[j] = clusterPoint{paths[i], Num(coords[i*3]), Num(coords[i*3+1]), Num(coords[i*3+2])}
			if j < quotas[c] {
				points = append(points, Point{paths[i], Num(coords[i*3]), Num(coords[i*3+1]), Num(coords[i*3+2]), c})
			}
		}
		if err := writeJSON(filepath.Join(scratch, fmt.Sprintf("cluster-%d.json", c)), struct {
			C      int            `json:"c"`
			Label  string         `json:"label"`
			Count  int            `json:"count"`
			Points []clusterPoint `json:"points"`
		}{c, labels[c], len(m), cps}); err != nil {
			return Summary{}, err
		}
	}
	ov := Overview{Version: 3, Available: true, BuiltAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Model: model, Files: n, Shown: len(points), Clusters: clusters, Links: links, Points: points,
		Ms: time.Since(t0).Milliseconds(), ReadMs: readMs, Dbs: dbs}
	if opt.Of != nil {
		of := max(*opt.Of, n)
		ov.Of = &of
	}
	if err := writeJSON(filepath.Join(scratch, "overview.json"), ov); err != nil {
		return Summary{}, err
	}
	means.Close()
	os.Remove(meansPath)
	os.Remove(termsPath)
	// Swapped in whole: the old map stays readable until the new one is complete.
	old := out + ".old"
	os.RemoveAll(old)
	if _, err := os.Lstat(out); err == nil {
		if err := os.Rename(out, old); err != nil {
			return Summary{}, err
		}
	}
	if err := os.Rename(scratch, out); err != nil {
		return Summary{}, err
	}
	done = true
	os.RemoveAll(old)
	shown, kk := ov.Shown, k
	return Summary{Available: true, Files: n, Shown: &shown, Ms: &ov.Ms, ReadMs: &ov.ReadMs, Dbs: dbs, Model: model, Of: ov.Of, Clusters: &kk}, nil
}

func readRow(f *os.File, i int, row []float32, buf []byte) error {
	if _, err := f.ReadAt(buf, int64(i)*vectorBytes); err != nil {
		return err
	}
	decodeRow(buf, row)
	return nil
}

func decodeRow(buf []byte, row []float32) {
	for j := range row {
		row[j] = math.Float32frombits(binary.LittleEndian.Uint32(buf[j*4:]))
	}
}

// stableSortDesc orders idx by key, largest first, ties in index order.
func stableSortDesc(idx []int, key []int) {
	for i := 1; i < len(idx); i++ {
		for j := i; j > 0 && key[idx[j]] > key[idx[j-1]]; j-- {
			idx[j], idx[j-1] = idx[j-1], idx[j]
		}
	}
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
