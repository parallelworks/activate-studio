package libraries

import (
	"strings"
)

// All names every library a viewer can read.
const All = "all"

// ScopeIDs is scopeIds: nil for none (the main folder), ["all"] for every
// library, else the ids named, each once, in order.
func ScopeIDs(params []string) (all bool, ids []string) {
	seen := map[string]bool{}
	for _, p := range params {
		for _, s := range strings.Split(p, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if strings.ToLower(s) == All {
				all = true
			}
			if !seen[s] {
				seen[s] = true
				ids = append(ids, s)
			}
		}
	}
	if all {
		return true, nil
	}
	return false, ids
}

// SeveralLibraries is severalLibraries.
func SeveralLibraries(params []string) bool {
	all, ids := ScopeIDs(params)
	return all || len(ids) > 1
}

// Scope is scopeWithOmitted: the libraries a request means, and those left
// out for having no index to answer from. One library named alone is asked
// anyway; among several, such a library is named instead.
func (r *Registry) Scope(params []string, viewer Viewer, primaryLabel string) ([]Library, []Omission, error) {
	all, ids := ScopeIDs(params)
	if !all && len(ids) == 0 {
		l, err := r.Get("", primaryLabel, viewer)
		return []Library{l}, nil, err
	}
	if !all && len(ids) == 1 {
		l, err := r.Get(ids[0], primaryLabel, viewer)
		return []Library{l}, nil, err
	}
	var named []Library
	if all {
		named = r.List(primaryLabel, viewer)
	} else {
		for _, id := range ids {
			l, err := r.Get(id, primaryLabel, viewer)
			if err != nil {
				return nil, nil, err
			}
			named = append(named, l)
		}
	}
	var libs []Library
	var om Omissions
	for _, l := range named {
		if l.Caps.Index || l.Primary {
			libs = append(libs, l)
			continue
		}
		if g := r.Gap(l, false, false); g != nil {
			om.Add(*g)
		} else {
			om.Add(Omission{ID: l.ID, Label: l.Label, Reason: "index not found"})
		}
	}
	return libs, om.Items(), nil
}

// MeaningSearched is meaningSearched.
func (r *Registry) MeaningSearched(l Library) bool {
	if l.Meaning == "off" {
		return false
	}
	if l.Primary && l.Meaning != "" {
		return true
	}
	return !r.Lacks(l, "vectors")
}

// WordsSearched is wordsSearched.
func (r *Registry) WordsSearched(l Library) bool {
	if l.Caps.FullText {
		return true
	}
	if l.Primary {
		return l.Depth != "names"
	}
	if l.Depth == "names" {
		return l.Caps.FullText
	}
	return !r.Lacks(l, "fullText")
}
