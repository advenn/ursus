package uerr

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// NearMatches returns up to n candidates that are plausible typos of want,
// best first.
//
// Two signals, because they catch different mistakes:
//
//   - Edit distance catches transpositions and single-character slips
//     ("reveune" → "revenue"), which are what people actually type.
//   - Case-insensitive equality and prefix containment catch the other common
//     failure, which is not a typo at all but a naming-convention mismatch
//     ("userid" → "user_id", "TS" → "ts").
//
// The distance budget scales with length: one edit for short names, up to three
// for long ones. A fixed budget either floods short-name suggestions or misses
// long-name typos.
func NearMatches(want string, candidates []string, n int) []string {
	if want == "" || len(candidates) == 0 {
		return nil
	}

	budget := max(1, min(3, utf8.RuneCountInString(want)/3))
	lowerWant := strings.ToLower(want)

	type scored struct {
		name string
		rank int // lower is better
	}
	var out []scored

	for _, c := range candidates {
		if c == want {
			continue // exact match is not a suggestion; the caller has a different bug
		}
		lowerC := strings.ToLower(c)

		switch {
		case lowerC == lowerWant:
			// Pure case difference — almost always the real answer.
			out = append(out, scored{c, 0})
		case squash(lowerC) == squash(lowerWant):
			// Differs only in separators: user_id vs userId vs userid.
			out = append(out, scored{c, 1})
		default:
			if d := editDistance(lowerWant, lowerC, budget); d >= 0 {
				out = append(out, scored{c, 2 + d})
			}
		}
	}

	// Stable: rank first, then source order, so suggestions do not reorder between
	// runs. Golden error files depend on this.
	sort.SliceStable(out, func(i, j int) bool { return out[i].rank < out[j].rank })

	if len(out) > n {
		out = out[:n]
	}
	names := make([]string, len(out))
	for i, s := range out {
		names[i] = s.name
	}
	return names
}

// squash removes separators so user_id, userId and userid compare equal.
func squash(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r != '_' && r != '-' && r != '.' && r != ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// editDistance returns the optimal-string-alignment (Damerau-Levenshtein)
// distance between a and b, or -1 if it exceeds budget.
//
// # Why transpositions get their own case
//
// Plain Levenshtein charges a transposition TWO edits, because it models it as a
// substitution in each direction. But swapping two adjacent characters is the most
// common typing error there is, and with a distance budget of 1 — which is all a
// short column name can afford before suggestions turn into noise — plain
// Levenshtein cannot suggest "price" for "pirce". Counting a transposition as one
// edit is what makes the suggestion appear.
//
// The early bail-out keeps this cheap when comparing against a wide schema: once
// every cell in a row exceeds the budget, no completion can come back under it.
func editDistance(a, b string, budget int) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	if len(rb)-len(ra) > budget {
		return -1
	}

	// Three rows: OSA needs the row before last to detect a transposition.
	prev2 := make([]int, len(ra)+1)
	prev := make([]int, len(ra)+1)
	curr := make([]int, len(ra)+1)
	for i := range prev {
		prev[i] = i
	}

	for j := 1; j <= len(rb); j++ {
		curr[0] = j
		rowMin := curr[0]
		for i := 1; i <= len(ra); i++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d := min(curr[i-1]+1, prev[i]+1, prev[i-1]+cost)

			// Transposition: the previous two characters are swapped.
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d = min(d, prev2[i-2]+1)
			}

			curr[i] = d
			rowMin = min(rowMin, d)
		}
		if rowMin > budget {
			return -1
		}
		prev2, prev, curr = prev, curr, prev2
	}

	if d := prev[len(ra)]; d <= budget {
		return d
	}
	return -1
}
