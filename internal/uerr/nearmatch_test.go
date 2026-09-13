package uerr_test

// NearMatches decides what every "unknown column" error suggests, and its own doc
// says "Golden error files depend on this." It had no test.
//
// The thresholds are the fragile part: a budget that scales with length, three rank
// tiers, and a stable sort whose tie-break is source order. None of that fails
// loudly when broken — it just suggests slightly different things, and a golden file
// somewhere changes.

import (
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
)

// TestNearMatchesBudgetScalesWithLength pins the two tier flips.
//
// The budget is max(1, min(3, runes/3)), so it changes at exactly 6 and 9 runes.
// Those two lengths are the whole content of the formula; a test at length 5 and
// length 20 would pass against /2, /4 or a constant.
func TestNearMatchesBudgetScalesWithLength(t *testing.T) {
	for _, c := range []struct {
		name      string
		want      string
		candidate string
		suggested bool
		why       string
	}{
		// 5 runes -> budget 1. Distance 2 is out of reach.
		{"short, one edit", "prcie", "price", true, "a transposition inside budget 1"},
		{"short, two edits", "prxyz", "price", false, "distance 2 exceeds budget 1"},
		// 6 runes -> budget 2. This is the first flip.
		{"medium, two edits", "reveune", "revenue", true, "budget 2 at 7 runes"},
		// 9 runes -> budget 3. The second flip.
		{"long, three edits", "discountXYZ", "discount", true, "budget 3 at 11 runes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := uerr.NearMatches(c.want, []string{c.candidate}, 3)
			if slices.Contains(got, c.candidate) != c.suggested {
				t.Errorf("NearMatches(%q, [%q]) = %v, want suggested=%v (%s)",
					c.want, c.candidate, got, c.suggested, c.why)
			}
		})
	}
}

// TestNearMatchesFindsATransposition is the case the ten-line comment in
// nearmatch.go exists to justify: the OSA transposition arm is what makes "pirce"
// suggest "price" at budget 1, where plain Levenshtein needs 2.
//
// A "simplification" to plain Levenshtein leaves that comment reading perfectly
// correct and silently stops suggesting the most common typo there is. This test is
// the only thing that would notice.
func TestNearMatchesFindsATransposition(t *testing.T) {
	got := uerr.NearMatches("pirce", []string{"price", "qty"}, 3)
	if !slices.Contains(got, "price") {
		t.Errorf(`"pirce" must suggest "price" — a transposition is one OSA edit, `+
			`two Levenshtein ones. Got %v`, got)
	}
}

// TestNearMatchesSquashesSeparators is the second signal, and the package doc used
// to call it "prefix containment", which is not implemented and never was.
func TestNearMatchesSquashesSeparators(t *testing.T) {
	for _, c := range [][2]string{
		{"userid", "user_id"},
		{"user id", "user-id"},
		{"TS", "ts"},
	} {
		got := uerr.NearMatches(c[0], []string{c[1], "zzzzzzzzzz"}, 3)
		if !slices.Contains(got, c[1]) {
			t.Errorf("%q should match %q by squashing separators, got %v",
				c[0], c[1], got)
		}
	}
}

// TestNearMatchesRanksCloserFirst. The ordering is what the user reads first.
func TestNearMatchesRanksCloserFirst(t *testing.T) {
	// "price" differs from "prices" by one edit; "user_id" only matches by squash,
	// which ranks ahead of any edit distance.
	got := uerr.NearMatches("userid", []string{"prices", "user_id"}, 3)
	if len(got) == 0 || got[0] != "user_id" {
		t.Errorf("the separator match should rank first, got %v", got)
	}
}

// TestNearMatchesIsDeterministic. The sort is SliceStable on rank alone, so ties
// keep source order — and the doc says golden files depend on it. A sort.Slice
// would pass a single run and reorder on another.
func TestNearMatchesIsDeterministic(t *testing.T) {
	cands := []string{"aaa", "aab", "aac", "aad"}
	first := uerr.NearMatches("aaa", cands, 4)
	for range 32 {
		if got := uerr.NearMatches("aaa", cands, 4); !slices.Equal(got, first) {
			t.Fatalf("suggestions reordered between runs: %v then %v", first, got)
		}
	}
	// Equal-rank candidates must come back in the order they were given.
	if len(first) >= 2 && !slices.IsSorted(first) {
		t.Errorf("tied candidates should keep source order, got %v", first)
	}
}

// TestNearMatchesSkipsTheExactMatch, because a column that IS present is not the
// reason the lookup failed.
func TestNearMatchesSkipsTheExactMatch(t *testing.T) {
	got := uerr.NearMatches("price", []string{"price", "prices"}, 3)
	if slices.Contains(got, "price") {
		t.Errorf("the exact name must not be suggested back, got %v", got)
	}
}

// TestNearMatchesEdgeCases. The negative-n case used to PANIC on out[:n].
func TestNearMatchesEdgeCases(t *testing.T) {
	cands := []string{"price", "qty"}
	for _, c := range []struct {
		name string
		want string
		cand []string
		n    int
	}{
		{"empty want", "", cands, 3},
		{"no candidates", "price", nil, 3},
		{"zero n", "pirce", cands, 0},
		{"negative n", "pirce", cands, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := uerr.NearMatches(c.want, c.cand, c.n); len(got) != 0 {
				t.Errorf("want no suggestions, got %v", got)
			}
		})
	}
}

// TestNearMatchesTruncates, after ranking rather than before — otherwise the best
// suggestion can be cut before it is ordered.
func TestNearMatchesTruncates(t *testing.T) {
	cands := []string{"aaa", "aab", "aac", "aad", "aae"}
	got := uerr.NearMatches("aaa", cands, 2)
	if len(got) != 2 {
		t.Fatalf("got %d suggestions, want 2: %v", len(got), got)
	}
	full := uerr.NearMatches("aaa", cands, 5)
	if len(full) < 2 || !slices.Equal(got, full[:2]) {
		t.Errorf("truncation must take the top of the ranked list: %v vs %v", got, full)
	}
}

// TestNearMatchesHandlesMultiByte. editDistance walks runes while squash walks
// bytes, so a non-ASCII name is worth pinning.
func TestNearMatchesHandlesMultiByte(t *testing.T) {
	got := uerr.NearMatches("naïve", []string{"naive", "qty"}, 3)
	if len(got) == 0 {
		t.Errorf(`"naïve" should be close to "naive", got %v`, got)
	}
	// And it must not panic or mis-slice on a name that is all multi-byte.
	_ = uerr.NearMatches("日本語", []string{"日本", "日本語です"}, 3)
}

// TestNearMatchesDoesNotFloodShortNames is the reason the budget scales at all: a
// fixed budget of 3 would make every two-letter column match every other one.
func TestNearMatchesDoesNotFloodShortNames(t *testing.T) {
	// Every candidate differs in BOTH positions, so each is two edits away and the
	// budget of 1 excludes them all. The first draft of this test used "cd", which
	// shares the trailing d and is therefore one edit — the engine was right and the
	// fixture was wrong, which is the same mistake a fixed budget would hide.
	got := uerr.NearMatches("id", []string{"ab", "ef", "gh"}, 3)
	if len(got) > 0 {
		t.Errorf("nothing is within one edit of %q; got %v — a fixed budget of 3 "+
			"would suggest all of them", "id", got)
	}
}

// TestUnknownColumnQuotesSuggestions, since the names can contain punctuation and
// the suggestion line is what a user copies.
func TestUnknownColumnQuotesSuggestions(t *testing.T) {
	e := uerr.UnknownColumn("select", "my col", []string{"my_col"})
	if got := e.Error(); strings.Contains(got, "did you mean") &&
		!strings.Contains(got, `"my_col"`) {
		t.Errorf("suggestions should be quoted:\n%s", got)
	}
}
