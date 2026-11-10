package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// M4 of v0.6.6-scope.md (steps 281 and 282): rle and rle_id, append and
// extend_constant. Their answers are Polars 2.1.0's since step 500, recorded in the seq
// group of testdata/oracle and spelled in oracle_seq_test.go, which took this file's
// tables. What stays asserts the refusals, an appended column's nullability, and the
// properties of Sample and Shuffle, whose draws are ursus's own.

// seqFrame is Polars' frame: g [1, 1, 1, 2, 2, 2], a [1, 1, 2, None, None, 1], f
// [1.0, NaN, NaN, -0.0, 0.0, 2.0] and s ["x", "x", "y", "y", None, "x"].
func seqFrame() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}),
		ursus.ValuesNullable("a", []int64{1, 1, 2, 0, 0, 1}, []bool{true, true, true, false, false, true}),
		ursus.Values("f", []float64{1, math.NaN(), math.NaN(), math.Copysign(0, -1), 0, 2}),
		ursus.ValuesNullable("s", []string{"x", "x", "y", "y", "", "x"}, []bool{true, true, true, true, false, true}))
}

func TestAppendAndExtend(t *testing.T) {
	c := ursus.Col
	a := c("a")
	lf := seqFrame()
	// The appended side's nulls are the answer's, which a verified run holds it to.
	nulls := lf.Select(c("g").Append(a, true))
	if s, err := nulls.CollectSchema(t.Context()); err != nil || !s.Field(0).Nullable {
		t.Errorf("g appended by a nullable a: %v, %v, want nullable", s, err)
	}
	if _, err := nulls.Collect(t.Context(), ursus.WithVerify()); err != nil {
		t.Errorf("g appended by a nullable a, verified: %v", err)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "type i8 is incompatible with expected type i64".
		"without upcast": {lf.Select(a.Append(a.Cast(ursus.Int8), false)), "do not meet"},
		// Polars meets text with a number as text; ursus's Promote and Concat do not.
		"text and a number": {lf.Select(c("s").Append(a, true)), "do not meet"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestSampleAndShuffle: a seeded draw cannot be Polars', whose generator is its own,
// so its properties are held. Its sizes are Polars': 3 of 6, half of 6, 10 with
// replacement, and too many without refused as Polars refuses it; all 6 unshuffled are
// the column as it was.
func TestSampleAndShuffle(t *testing.T) {
	c := ursus.Col
	x := c("x")
	lf := ursus.Frame(ursus.Values("x", []int64{5, 3, 8, 1, 9, 2}), ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}))
	rows := func(q *ursus.LazyFrame) []string {
		return strings.Split(strings.TrimSpace(csvOf(t, q)), "\n")[1:]
	}
	member := map[string]bool{"5": true, "3": true, "8": true, "1": true, "9": true, "2": true}
	for name, tc := range map[string]struct {
		q *ursus.LazyFrame
		n int
	}{
		"3 of 6":              {lf.Select(x.SampleN(3, false, false, 7)), 3},
		"half":                {lf.Select(x.SampleFraction(0.5, false, false, 7)), 3},
		"0.4 of 6, truncated": {lf.Select(x.SampleFraction(0.4, false, false, 7)), 2},
		"10 with replacement": {lf.Select(x.SampleN(10, true, false, 7)), 10},
		"shuffled":            {lf.Select(x.Shuffle(7)), 6},
	} {
		got := rows(tc.q)
		if len(got) != tc.n {
			t.Errorf("%s: %d rows, want %d", name, len(got), tc.n)
		}
		for _, v := range got {
			if !member[v] {
				t.Errorf("%s drew %s, which the column has not", name, v)
			}
		}
		if again := rows(tc.q); strings.Join(again, ",") != strings.Join(got, ",") {
			t.Errorf("%s drew %v, then %v, from one seed", name, got, again)
		}
	}
	shuffled := rows(lf.Select(x.Shuffle(7)))
	seen := map[string]int{}
	for _, v := range shuffled {
		seen[v]++
	}
	if len(seen) != 6 {
		t.Errorf("a shuffle is each value once: %v", shuffled)
	}
	if got := strings.Join(rows(lf.Select(x.SampleN(6, false, false, 7))), ","); got != "5,3,8,1,9,2" {
		t.Errorf("all of them in order: %s", got)
	}
	if got := strings.Join(shuffled, ","); got == "5,3,8,1,9,2" {
		t.Errorf("a shuffle by seed 7 left the order as it was")
	}
	if strings.Join(rows(lf.Select(x.SampleN(3, false, false, 7))), ",") ==
		strings.Join(rows(lf.Select(x.SampleN(3, false, false, 8))), ",") {
		t.Errorf("seeds 7 and 8 drew alike")
	}
	fractions := lf.GroupBy(c("g")).Agg(x.SampleFraction(0.4, false, false, 7).List().Len().Alias("n")).Sort(ursus.Asc(c("g"))).Select(c("n"))
	if got := csvOf(t, fractions); got != "n\n1\n1\n" {
		t.Errorf("0.4 of a group of 3: %q", got)
	}
	groups := lf.GroupBy(c("g")).Agg(x.SampleN(2, false, false, 7).List().Len().Alias("n")).Sort(ursus.Asc(c("g"))).Select(c("n"))
	if got := csvOf(t, groups); got != "n\n2\n2\n" {
		t.Errorf("2 a group: %q", got)
	}
	if _, err := lf.Select(x.SampleN(10, false, false, 7)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "without replacement") {
		t.Errorf("too many without replacement: %v", err)
	}
}
