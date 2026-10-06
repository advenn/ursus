package ursus_test

// An as-of join and MergeSorted run on every integer, float, temporal and string key
// (step 109).
//
// audit.md J7: both read their key as int64 ticks, which only the temporal types and
// Int64 are. An as-of join on a Float, Int8 or Uint64 key planned and then failed at
// Collect with "cannot be read as Int64"; MergeSorted refused Float64, Int8, Uint64
// and String, with a hint that the key "must be numeric or temporal".

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/advenn/ursus"
)

// asOfOracle is the as-of match for every left key, by brute force: the index of
// the right key it takes under strategy s, or -1.
func asOfOracle[T cmp.Ordered](left, right []T, s ursus.AsOfStrategy, dist func(a, b T) float64) []int {
	out := make([]int, len(left))
	for i, l := range left {
		back, fwd := -1, -1
		for j, r := range right {
			if r <= l {
				back = j // the last one
			}
			if r >= l && fwd < 0 {
				fwd = j // the first one
			}
		}
		switch s {
		case ursus.AsOfBackward:
			out[i] = back
		case ursus.AsOfForward:
			out[i] = fwd
		default:
			switch {
			case back < 0:
				out[i] = fwd
			case fwd < 0:
				out[i] = back
			case dist(l, right[back]) <= dist(right[fwd], l):
				out[i] = back
			default:
				out[i] = fwd
			}
		}
	}
	return out
}

// checkAsOf joins left to right on k for every strategy and checks each left row's
// match against the oracle. Right row j carries payload "w" = j.
func checkAsOf[T interface {
	cmp.Ordered
	ursus.Literal
}](t *testing.T, left, right []T, dist func(a, b T) float64) {
	t.Helper()
	w := make([]int64, len(right))
	for j := range w {
		w[j] = int64(j)
	}
	l := ursus.Frame(ursus.Values("k", left))
	r := ursus.Frame(ursus.Values("k", right), ursus.Values("w", w))
	for _, s := range []ursus.AsOfStrategy{ursus.AsOfBackward, ursus.AsOfForward, ursus.AsOfNearest} {
		df, err := l.JoinAsOf(r, ursus.AsOfOn(ursus.Col("k")), ursus.AsOfStrategyOpt(s)).Collect(t.Context())
		if err != nil {
			t.Fatalf("strategy %d: %v", s, err)
		}
		_, got := textOf(t, df, "w")
		for i, want := range asOfOracle(left, right, s, dist) {
			ws := "∅"
			if want >= 0 {
				ws = strconv.Itoa(want)
			}
			if got[i] != ws {
				t.Fatalf("strategy %d, left row %d (%v): matched %s, want %s", s, i, left[i], got[i], ws)
			}
		}
	}
}

func intDist[T int8 | int16 | uint32](a, b T) float64 { return math.Abs(float64(a) - float64(b)) }

func TestAsOfJoinOnEveryKeyType(t *testing.T) {
	t.Run("Int8", func(t *testing.T) {
		checkAsOf(t, []int8{-128, -5, 0, 3, 3, 9, 127}, []int8{-100, -5, 1, 3, 10, 120}, intDist[int8])
	})
	t.Run("Int16", func(t *testing.T) {
		checkAsOf(t, []int16{-300, 0, 299, 300, 1000}, []int16{-299, 1, 300, 301}, intDist[int16])
	})
	t.Run("Uint32", func(t *testing.T) {
		checkAsOf(t, []uint32{0, 7, 4_000_000_000}, []uint32{1, 6, 8, 3_999_999_999}, intDist[uint32])
	})
	t.Run("Uint64, past Int64's range", func(t *testing.T) {
		const e = 1_000_000_000_000_000
		checkAsOf(t, []uint64{5, 9_000 * e, 9_300 * e, 18_000 * e},
			[]uint64{1, 9_100 * e, 9_400 * e, 18_446 * e},
			func(a, b uint64) float64 { return math.Abs(float64(a) - float64(b)) })
	})
	t.Run("Float64, negatives and fractions", func(t *testing.T) {
		checkAsOf(t, []float64{-3.5, -0.25, 0, 0.5, 2.75, 1e300}, []float64{-4, -0.5, 0.25, 0.75, 2.5, 3},
			func(a, b float64) float64 { return math.Abs(a - b) })
	})
	t.Run("Float32", func(t *testing.T) {
		checkAsOf(t, []float32{-1.5, 0.125, 7}, []float32{-2, 0, 0.25, 6.5},
			func(a, b float32) float64 { return math.Abs(float64(a) - float64(b)) })
	})
	t.Run("a Decimal key is refused while planning", func(t *testing.T) {
		f := ursus.Frame(ursus.Values("k", []int64{1})).Select(ursus.Col("k").Cast(ursus.Decimal(10, 2)))
		_, err := f.JoinAsOf(f, ursus.AsOfOn(ursus.Col("k"))).CollectSchema(t.Context())
		if !errors.Is(err, ursus.ErrType) {
			t.Errorf("want a type error, got %v", err)
		}
	})
}

// checkMerge merges two sorted frames and checks the rows against a stable sort of
// the two concatenated, which is merge_sorted's answer: ties take the left row first.
func checkMerge[T ursus.Literal](t *testing.T, left, right []T) {
	t.Helper()
	tag := func(side string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s%d", side, i)
		}
		return out
	}
	l := ursus.Frame(ursus.Values("k", left), ursus.Values("row", tag("l", len(left))))
	r := ursus.Frame(ursus.Values("k", right), ursus.Values("row", tag("r", len(right))))
	got, err := l.MergeSorted(r, "k").Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Concat([]*ursus.LazyFrame{l, r}).Sort(ursus.Asc(ursus.Col("k"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, g := textOf(t, got, "row")
	_, w := textOf(t, want, "row")
	if !slices.Equal(g, w) {
		t.Errorf("merged %v, want %v", g, w)
	}
}

func TestMergeSortedOnEveryKeyType(t *testing.T) {
	t.Run("Float64", func(t *testing.T) { checkMerge(t, []float64{-2.5, 0, 0, 3.25}, []float64{-3, 0, 1e10}) })
	t.Run("Int8", func(t *testing.T) { checkMerge(t, []int8{-128, 0, 5}, []int8{-1, 5, 127}) })
	t.Run("Uint64", func(t *testing.T) {
		checkMerge(t, []uint64{0, 1 << 63, math.MaxUint64}, []uint64{5, 1<<63 - 1, 1<<63 + 1})
	})
	t.Run("String", func(t *testing.T) { checkMerge(t, []string{"", "apple", "pear"}, []string{"a", "apple", "zz"}) })
	t.Run("a Boolean key is refused while planning", func(t *testing.T) {
		f := ursus.Frame(ursus.Values("k", []bool{false, true}))
		_, err := f.MergeSorted(f, "k").CollectSchema(t.Context())
		if !errors.Is(err, ursus.ErrType) {
			t.Errorf("want a type error, got %v", err)
		}
	})
}
