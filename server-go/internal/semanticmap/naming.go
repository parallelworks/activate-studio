package semanticmap

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// LinksPerCluster is how many clusters each cluster is linked to, by the
// cosine of their means.
const LinksPerCluster = 2

// labelClusters names each cluster for the folder most of its files live
// in; clusters that would share a name go a level deeper until they differ.
// A second folder keeps its parent when its own name is the same as the
// first's (pw/docs + gs_services/docs, not pw/docs + docs).
func labelClusters(paths []string, members [][]int, k int) []string {
	labelAt := func(c, depth int) string {
		type tally struct {
			seg string
			n   int
		}
		var order []tally
		at := map[string]int{}
		for _, i := range members[c] {
			parts := strings.Split(paths[i], "/")
			var seg string
			switch {
			case len(parts) > depth+1:
				seg = strings.Join(parts[:depth+1], "/")
			case len(parts) > 1:
				seg = strings.Join(parts[:len(parts)-1], "/")
			default:
				seg = "(root)"
			}
			if j, ok := at[seg]; ok {
				order[j].n++
			} else {
				at[seg] = len(order)
				order = append(order, tally{seg, 1})
			}
		}
		if len(order) == 0 {
			return ""
		}
		total := 0
		for _, t := range order {
			total += t.n
		}
		// Most files first; ties keep the order first seen, as Python's
		// stable sort of an insertion-ordered dict does.
		sort.SliceStable(order, func(a, b int) bool { return order[a].n > order[b].n })
		if float64(order[0].n)/float64(total) >= 0.6 || len(order) == 1 {
			return order[0].seg
		}
		first := order[0].seg
		second := strings.Split(order[1].seg, "/")
		firstParts := strings.Split(first, "/")
		tail := second[len(second)-1]
		if tail == firstParts[len(firstParts)-1] {
			from := len(second) - 2
			if from < 0 {
				from = 0
			}
			tail = strings.Join(second[from:], "/")
		}
		return first + " + " + tail
	}
	labels := make([]string, k)
	for c := 0; c < k; c++ {
		labels[c] = labelAt(c, 0)
	}
	for depth := 1; depth < 4; depth++ {
		seen := map[string]int{}
		for _, l := range labels {
			seen[l]++
		}
		collided := false
		for c := 0; c < k; c++ {
			if seen[labels[c]] > 1 {
				labels[c] = labelAt(c, depth)
				collided = true
			}
		}
		if !collided {
			break
		}
	}
	return labels
}

// clusterTerms returns each cluster's distinctive terms, best first: the
// share of its files (those with text) that use a term, times the term's
// inverse document frequency over every file of the map, so a word common
// to the whole map scores near nothing. A term must appear in at least 5%
// of the cluster's files with text (and in two, where the cluster has four
// or more). termsPath holds one line per file, its terms separated by
// spaces, in the order of assign.
func clusterTerms(termsPath string, assign []int32, k int, df map[string]int, nText, keep int) ([][]string, error) {
	cdf := make([]map[string]int, k)
	for c := range cdf {
		cdf[c] = map[string]int{}
	}
	withText := make([]int, k)
	f, err := os.Open(termsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	i := 0
	for sc.Scan() {
		line := sc.Text()
		if line != "" {
			c := assign[i]
			withText[c]++
			for _, t := range strings.Fields(line) {
				cdf[c][t]++
			}
		}
		i++
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([][]string, k)
	for c := 0; c < k; c++ {
		nc := withText[c]
		if nc == 0 {
			out[c] = []string{}
			continue
		}
		support := 1
		if nc >= 4 {
			support = max(2, int(math.Ceil(0.05*float64(nc))))
		}
		type scored struct {
			s float64
			t string
		}
		var list []scored
		for t, m := range cdf[c] {
			if m < support {
				continue
			}
			idf := math.Log(float64(nText+1) / float64(df[t]+1))
			s := float64(m) / float64(nc) * idf
			if s > 0.05 {
				list = append(list, scored{s, t})
			}
		}
		sort.Slice(list, func(a, b int) bool {
			if list[a].s != list[b].s {
				return list[a].s > list[b].s
			}
			return list[a].t < list[b].t
		})
		terms := []string{}
		for j := 0; j < len(list) && j < keep; j++ {
			terms = append(terms, list[j].t)
		}
		out[c] = terms
	}
	return out, nil
}

// nameClusters makes a name from each cluster's terms, largest cluster
// first: two terms, or three where they fit in maxChars. A term another
// cluster's name already uses goes behind the terms no name uses yet, and a
// name another cluster has takes the next term instead of its last, so no
// two names read alike. A cluster with fewer than two terms takes its folder
// name, which says more than one word would.
func nameClusters(candidates [][]string, folders []string, maxChars int) ([]string, [][]string) {
	names := make([]string, 0, len(candidates))
	chosen := make([][]string, 0, len(candidates))
	used := map[string]bool{}
	taken := map[string]bool{}
	for c, cands := range candidates {
		if len(cands) < 2 {
			name := folders[c]
			if name == "" {
				name = fmt.Sprintf("cluster %d", c+1)
			}
			base := folders[c]
			if base == "" {
				base = "cluster"
			}
			for n := 2; taken[name]; n++ {
				name = fmt.Sprintf("%s (%d)", base, n)
			}
			names = append(names, name)
			chosen = append(chosen, []string{})
			taken[name] = true
			continue
		}
		var ordered []string
		for _, t := range cands {
			if !used[t] {
				ordered = append(ordered, t)
			}
		}
		for _, t := range cands {
			if used[t] {
				ordered = append(ordered, t)
			}
		}
		pick := append([]string{}, ordered[:2]...)
		if len(ordered) > 2 && len(strings.Join(ordered[:3], ", ")) <= maxChars {
			pick = append([]string{}, ordered[:3]...)
		}
		name := strings.Join(pick, ", ")
		var spare []string
		inPick := map[string]bool{}
		for _, t := range pick {
			inPick[t] = true
		}
		for _, t := range ordered {
			if !inPick[t] {
				spare = append(spare, t)
			}
		}
		for taken[name] && len(spare) > 0 {
			pick = append(pick[:len(pick)-1:len(pick)-1], spare[0])
			spare = spare[1:]
			name = strings.Join(pick, ", ")
		}
		if taken[name] {
			parts := strings.Split(strings.Split(folders[c], " + ")[0], "/")
			tag := parts[len(parts)-1]
			if tag == "" {
				tag = fmt.Sprint(c + 1)
			}
			name = fmt.Sprintf("%s (%s)", name, tag)
		}
		names = append(names, name)
		chosen = append(chosen, pick)
		taken[name] = true
		for _, t := range pick {
			used[t] = true
		}
	}
	return names, chosen
}

// Link joins two clusters, a < b, with the cosine of their mean vectors.
type Link struct {
	A   int     `json:"a"`
	B   int     `json:"b"`
	Sim float64 `json:"sim"`
}

// clusterLinks links each cluster to the `per` clusters nearest it in the
// full embedding space: the cosine of the clusters' mean vectors, from the
// sums of their files' means. Pairs are listed once, nearest first.
func clusterLinks(sums [][]float64, k, per int) []Link {
	if k < 2 {
		return []Link{}
	}
	cents := make([][]float64, k)
	for c := 0; c < k; c++ {
		norm := 0.0
		for _, v := range sums[c] {
			norm += v * v
		}
		norm = math.Sqrt(norm)
		if norm == 0 {
			norm = 1
		}
		cents[c] = make([]float64, len(sums[c]))
		for j, v := range sums[c] {
			cents[c][j] = v / norm
		}
	}
	sim := func(a, b int) float64 {
		s := 0.0
		for j := range cents[a] {
			s += cents[a][j] * cents[b][j]
		}
		return s
	}
	type pair struct{ a, b int }
	pairs := map[pair]float64{}
	var order []pair
	for a := 0; a < k; a++ {
		others := make([]int, k)
		sims := make([]float64, k)
		for b := 0; b < k; b++ {
			others[b] = b
			sims[b] = sim(a, b)
		}
		sort.SliceStable(others, func(i, j int) bool { return sims[others[i]] > sims[others[j]] })
		n := 0
		for _, b := range others {
			if b == a {
				continue
			}
			if n == per {
				break
			}
			n++
			key := pair{min(a, b), max(a, b)}
			if _, ok := pairs[key]; !ok {
				order = append(order, key)
			}
			pairs[key] = round4(sims[b])
		}
	}
	out := make([]Link, 0, len(order))
	for _, p := range order {
		out = append(out, Link{p.a, p.b, pairs[p]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sim != out[j].Sim {
			return out[i].Sim > out[j].Sim
		}
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out
}

// round4 is Python's round(v, 4): the exact binary value rounded to four
// decimals.
func round4(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 4, 64), 64)
	return r
}
