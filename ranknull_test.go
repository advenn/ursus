package ursus_test

import (
	"cmp"
	"fmt"
	"slices"
	"testing"

	"github.com/advenn/ursus"
)

// TestRankLeavesNullsNull is step 335: a null's rank is null, and the values are
// ranked among themselves, as Polars ranks them, by every method, either way, over a
// partition and over the frame; in a list's body; and streamed past a memory limit
// as in memory. Every small answer is Polars 1.44.1's. Until now a null ranked as the
// least value, and took a position.
func TestRankLeavesNullsNull(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	lf := ursus.Frame(strs("g", "a", "a", "b", "a", "b", "b", "a", "b"), f64s("x", 5.0, nil, 4.0, 5.0, nil, nil, 1.0, 4.0))
	method := map[string]ursus.RankMethod{"average": ursus.RankAverage, "min": ursus.RankMin, "max": ursus.RankMax,
		"dense": ursus.RankDense, "ordinal": ursus.RankOrdinal}
	column := func(df *ursus.DataFrame, name string) string {
		rows, err := df.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(rows))
		for i, r := range rows {
			vals[i] = r[name]
		}
		return fmt.Sprint(vals)
	}
	for _, tc := range []struct {
		method      string
		descending  bool
		over, whole string
	}{
		{"average", false, "[2.5 <nil> 1.5 2.5 <nil> <nil> 1 1.5]", "[4.5 <nil> 2.5 4.5 <nil> <nil> 1 2.5]"},
		{"average", true, "[1.5 <nil> 1.5 1.5 <nil> <nil> 3 1.5]", "[1.5 <nil> 3.5 1.5 <nil> <nil> 5 3.5]"},
		{"min", false, "[2 <nil> 1 2 <nil> <nil> 1 1]", "[4 <nil> 2 4 <nil> <nil> 1 2]"},
		{"min", true, "[1 <nil> 1 1 <nil> <nil> 3 1]", "[1 <nil> 3 1 <nil> <nil> 5 3]"},
		{"max", false, "[3 <nil> 2 3 <nil> <nil> 1 2]", "[5 <nil> 3 5 <nil> <nil> 1 3]"},
		{"max", true, "[2 <nil> 2 2 <nil> <nil> 3 2]", "[2 <nil> 4 2 <nil> <nil> 5 4]"},
		{"dense", false, "[2 <nil> 1 2 <nil> <nil> 1 1]", "[3 <nil> 2 3 <nil> <nil> 1 2]"},
		{"dense", true, "[1 <nil> 1 1 <nil> <nil> 2 1]", "[1 <nil> 2 1 <nil> <nil> 3 2]"},
		{"ordinal", false, "[2 <nil> 1 3 <nil> <nil> 1 2]", "[4 <nil> 2 5 <nil> <nil> 1 3]"},
		{"ordinal", true, "[1 <nil> 1 2 <nil> <nil> 3 2]", "[1 <nil> 3 2 <nil> <nil> 5 4]"},
	} {
		m := method[tc.method]
		got, err := lf.Select(c("x").Rank(m, tc.descending).Over(c("g")).Alias("over"),
			c("x").Rank(m, tc.descending).Alias("whole")).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if o, w := column(got, "over"), column(got, "whole"); o != tc.over || w != tc.whole {
			t.Errorf("%s, descending %v: over g %s, want %s; the frame %s, want %s", tc.method, tc.descending,
				o, tc.over, w, tc.whole)
		}
		if f := got.Schema().Field(0); !f.Nullable {
			t.Errorf("%s: the rank's field is not nullable", tc.method)
		}
	}

	// Only nulls; text; a list's body.
	got := collect(t, ursus.Frame(ursus.ValuesNullable("x", []float64{0, 0}, []bool{false, false})).
		Select(c("x").Rank(ursus.RankAverage, false)))
	if s := column(got, "x"); s != "[<nil> <nil>]" {
		t.Errorf("only nulls: %s", s)
	}
	got = collect(t, ursus.Frame(strs("s", "b", nil, "a", "b")).Select(c("s").Rank(ursus.RankDense, false)))
	if s := column(got, "s"); s != "[2 <nil> 1 2]" {
		t.Errorf("text: %s", s)
	}
	got = collect(t, ursus.Frame(i64s("x", 5, nil, 4)).Select(c("x").Rank(ursus.RankOrdinal, false)))
	if s := column(got, "x"); s != "[2 <nil> 1]" {
		t.Errorf("ordinal: %s", s)
	}
	// Polars' l: [[3, 1, 2], None, [], [5, None, 4]].
	if s := listText(t, evalFixture(t), c("l").List().Eval(ursus.Element().Rank(ursus.RankMin, false))); s !=
		"n,e0,e1,e2\n3,3,1,2\n,,,\n0,,,\n3,2,,1\n" {
		t.Errorf("a list's body:\n%s", s)
	}

	// Streamed: one partition past the limit, ranked in a sorted pass by min both
	// ways, as worked out here.
	const n = 4000
	vals, valid := make([]float64, n), make([]bool, n)
	for i := range n {
		vals[i], valid[i] = float64(i%97), i%5 != 0
	}
	big := ursus.Frame(ursus.ValuesNullable("x", vals, valid))
	for _, m := range []ursus.RankMethod{ursus.RankAverage, ursus.RankMax, ursus.RankMin, ursus.RankDense, ursus.RankOrdinal} {
		for _, desc := range []bool{false, true} {
			lf := big.Select(c("x").Rank(m, desc).Over().Alias("r"))
			got, stats, err := spillCollect(t, lf, 16<<10, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if stats.Spills == 0 {
				t.Fatalf("%v: no spill", m)
			}
			if s, w := column(got, "r"), ranksOf(vals, valid, m, desc); s != w {
				t.Fatalf("%v, descending %v: streamed\n%.300s\nwant\n%.300s", m, desc, s, w)
			}
		}
	}
}

// ranksOf is the rank of each value as Polars ranks it, worked out the long way: a
// null's is null; the values sorted, ties in their order, and a run of equal ones
// ranked by method.
func ranksOf(vals []float64, valid []bool, m ursus.RankMethod, desc bool) string {
	var idx []int
	for i := range vals {
		if valid[i] {
			idx = append(idx, i)
		}
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		if desc {
			return cmp.Compare(vals[b], vals[a])
		}
		return cmp.Compare(vals[a], vals[b])
	})
	out := make([]any, len(vals))
	dense := 0
	for j := 0; j < len(idx); {
		k := j
		for k < len(idx) && vals[idx[k]] == vals[idx[j]] {
			k++
		}
		dense++
		for p := j; p < k; p++ {
			switch m {
			case ursus.RankAverage:
				out[idx[p]] = float64(j+1+k) / 2
			case ursus.RankMin:
				out[idx[p]] = uint32(j + 1)
			case ursus.RankMax:
				out[idx[p]] = uint32(k)
			case ursus.RankDense:
				out[idx[p]] = uint32(dense)
			default:
				out[idx[p]] = uint32(p + 1)
			}
		}
		j = k
	}
	return fmt.Sprint(out)
}
