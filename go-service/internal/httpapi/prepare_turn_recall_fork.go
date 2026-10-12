package httpapi

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// prepareTurnSupportQuery is the query side of prepareTurnSupportRecallEligible:
// its phrase pairs and its distinctive terms without the anchors. Both are
// read only once built.
type prepareTurnSupportQuery struct {
	pairs map[string]bool
	terms []string
}

type prepareTurnSupportQueryKey struct {
	query, anchors string
}

// One request checks many texts against the same few queries; the most
// recently prepared queries are kept, bounded in number.
var prepareTurnSupportQueries = struct {
	sync.Mutex
	entries map[prepareTurnSupportQueryKey]*prepareTurnSupportQuery
	order   []prepareTurnSupportQueryKey
}{entries: map[prepareTurnSupportQueryKey]*prepareTurnSupportQuery{}}

const prepareTurnSupportQueryCacheSize = 32

func prepareTurnSupportQueryFor(query string, anchors []string) *prepareTurnSupportQuery {
	key := prepareTurnSupportQueryKey{query: query}
	if len(anchors) > 0 {
		// Length-prefixed, so different anchor lists never share a key.
		var b strings.Builder
		for _, anchor := range anchors {
			b.WriteString(strconv.Itoa(len(anchor)))
			b.WriteByte(':')
			b.WriteString(anchor)
		}
		key.anchors = b.String()
	}
	cache := &prepareTurnSupportQueries
	cache.Lock()
	prepared := cache.entries[key]
	cache.Unlock()
	if prepared != nil {
		return prepared
	}
	prepared = &prepareTurnSupportQuery{pairs: prepareTurnRecallPhrasePairs(query), terms: prepareTurnDistinctiveRecallTerms(query, anchors...)}
	cache.Lock()
	defer cache.Unlock()
	if existing := cache.entries[key]; existing != nil {
		return existing
	}
	if len(cache.order) >= prepareTurnSupportQueryCacheSize {
		delete(cache.entries, cache.order[0])
		cache.order = cache.order[1:]
	}
	cache.entries[key] = prepared
	cache.order = append(cache.order, key)
	return prepared
}

// prepareTurnDistinctiveQuery is the anchor-independent part of
// prepareTurnDistinctiveRecallTerms for one text, so the terms for many
// anchor lists come from one analysis. Anchors only exclude terms and forms:
// a term passes the frequency rules whatever the anchors, and each term
// value is excluded or not wherever it occurs.
type prepareTurnDistinctiveQuery struct {
	candidates []string   // first occurrences passing the frequency rules
	forms      [][]string // each candidate's term forms
	fallback   []string   // prepareTurnRecallTerms(text)
}

func newPrepareTurnDistinctiveQuery(text string) *prepareTurnDistinctiveQuery {
	all := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsNumber(r))
	})
	counts := map[string]int{}
	lengths := []int{}
	for _, term := range all {
		term = strings.TrimSpace(term)
		if term != "" {
			counts[term]++
			lengths = append(lengths, len([]rune(term)))
		}
	}
	sort.Ints(lengths)
	medianLength := 0
	minimumLength := 0
	if len(lengths) > 0 {
		medianLength = lengths[len(lengths)/2]
		minimumLength = lengths[0]
	}
	query := &prepareTurnDistinctiveQuery{fallback: prepareTurnRecallTerms(text)}
	seen := map[string]bool{}
	for _, term := range all {
		term = strings.TrimSpace(term)
		termLength := len([]rune(term))
		lowInformationRepeat := counts[term] > 1 && termLength < medianLength
		shortestTier := len(lengths) > 1 && termLength == minimumLength
		if term == "" || seen[term] || lowInformationRepeat || shortestTier {
			continue
		}
		seen[term] = true
		query.candidates = append(query.candidates, term)
		query.forms = append(query.forms, prepareTurnRecallTermForms(term))
	}
	return query
}

// terms is prepareTurnDistinctiveRecallTerms(text, anchors...).
func (query *prepareTurnDistinctiveQuery) terms(anchors ...string) []string {
	excluded := map[string]bool{}
	for _, anchor := range anchors {
		for _, term := range prepareTurnRecallTerms(anchor) {
			excluded[term] = true
		}
	}
	kept := []int{}
	for i, term := range query.candidates {
		if !excluded[term] {
			kept = append(kept, i)
		}
	}
	if len(kept) > 0 {
		forms := []string{}
		formSeen := map[string]bool{}
		for _, i := range kept {
			for _, form := range query.forms[i] {
				if !excluded[form] && !formSeen[form] {
					formSeen[form] = true
					forms = append(forms, form)
				}
			}
		}
		return forms
	}
	out := []string{}
	for _, term := range query.fallback {
		if !excluded[term] {
			out = append(out, term)
		}
	}
	return out
}

func prepareTurnRecallTermBreak(r rune) bool {
	return !(r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsNumber(r))
}
