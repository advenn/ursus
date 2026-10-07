package ursus_test

// What the audit of 0.4's join work found (step 118).
//
// An as-of key checked on the left key's type alone, while the search runs on the
// type both keys meet at; a nearest as-of on a float key that chose NaN; and a
// build-side swap that changed what a caller had ordered, or a merged zero key's
// sign.

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

func TestAsOfKeysAreCheckedWhereTheyMeet(t *testing.T) {
	c := ursus.Col
	right := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("t", []int64{1, 5}), ursus.Values("v", []int64{10, 50}))
	}
	cases := []struct {
		name        string
		left, right *ursus.LazyFrame
	}{
		{"a Uint64 key meets an Int64 one at Int128",
			ursus.Frame(ursus.Values("t", []uint64{2, 6})), right()},
		{"an Int64 key meets a Decimal one",
			ursus.Frame(ursus.Values("t", []int64{2, 6})),
			right().Select(c("t").Cast(dtype.Decimal(10, 2)).Alias("t"), c("v"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lf := tc.left.JoinAsOf(tc.right, ursus.AsOfOn(c("t")))
			// Refused while planning, as a type error naming the meeting type. It
			// planned, and Collect failed as ursus's bug.
			_, err := lf.CollectSchema(t.Context())
			if !errors.Is(err, ursus.ErrType) {
				t.Fatalf("want a type error while planning, got %v", err)
			}
			if _, err := lf.Collect(t.Context()); errors.Is(err, ursus.ErrInternal) {
				t.Errorf("Collect fails as ursus's bug: %v", err)
			}
		})
	}
	t.Run("the cast the hint names works", func(t *testing.T) {
		got := runs(t, ursus.Frame(ursus.Values("t", []uint64{2, 6})).
			Select(c("t").Cast(ursus.Int64).Alias("t")).
			JoinAsOf(right(), ursus.AsOfOn(c("t"))), "v")
		if !slices.Equal(got, []string{"10", "50"}) {
			t.Errorf("%v", got)
		}
	})
}

func TestNearestAsOfOnAFloatKey(t *testing.T) {
	c := ursus.Col
	nearest := ursus.AsOfStrategyOpt(ursus.AsOfNearest)
	cases := []struct {
		name  string
		left  []float64
		right []float64
		want  []string
	}{
		// NaN sorts last, so it is the forward candidate of 5; it is no distance
		// from anything, and was chosen every time.
		{"a NaN forward candidate", []float64{5}, []float64{1, math.NaN()}, []string{"0"}},
		// An exact match on +Inf among duplicates: a tie, which goes backward to the
		// last of them, as on an integer key. Inf − Inf is NaN, and it went forward.
		{"an exact infinity among duplicates", []float64{math.Inf(1)},
			[]float64{1, math.Inf(1), math.Inf(1)}, []string{"2"}},
		{"the integer path agrees", []float64{5}, []float64{1, 5, 5}, []string{"2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := make([]int64, len(tc.right))
			for i := range idx {
				idx[i] = int64(i)
			}
			got := runs(t, ursus.Frame(ursus.Values("t", tc.left)).
				JoinAsOf(ursus.Frame(ursus.Values("t", tc.right), ursus.Values("i", idx)),
					ursus.AsOfOn(c("t")), nearest), "i")
			if !slices.Equal(got, tc.want) {
				t.Errorf("matched row %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildSideKeepsWhatTheCallerOrdered: the swap gives up the left input's row
// order, which an inner join does not promise — unless the caller sorted it, or an
// as-of join or MergeSorted above needs it.
func TestBuildSideKeepsWhatTheCallerOrdered(t *testing.T) {
	c := ursus.Col
	// ev has 6 rows in ts order; users has 60, so the rule would hash ev.
	ev := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("ts", []int64{1, 2, 3, 4, 5, 6}),
			ursus.Values("uid", []int64{5, 3, 1, 4, 2, 0}))
	}
	users := func() *ursus.LazyFrame {
		uid := make([]int64, 60)
		for i := range uid {
			uid[i] = int64(i)
		}
		return ursus.Frame(ursus.Values("uid", uid))
	}

	t.Run("an unordered join still swaps", func(t *testing.T) {
		if !swapped(t, ev().Join(users(), ursus.JoinOn(c("uid")))) {
			t.Error("the rule no longer fires")
		}
	})
	t.Run("a sorted left keeps its order", func(t *testing.T) {
		lf := ev().Sort(ursus.Desc(c("ts"))).Join(users(), ursus.JoinOn(c("uid"))).Head(3)
		if swapped(t, lf) {
			t.Error("a join over a sorted input was swapped")
		}
		if got := runs(t, lf, "ts"); !slices.Equal(got, []string{"6", "5", "4"}) {
			t.Errorf("the first three by ts descending are %v", got)
		}
	})
	t.Run("an as-of join above keeps its input sorted", func(t *testing.T) {
		q := ursus.Frame(ursus.Values("ts", []int64{0, 3}), ursus.Values("qv", []int64{10, 30}))
		lf := ev().Join(users(), ursus.JoinOn(c("uid"))).JoinAsOf(q, ursus.AsOfOn(c("ts")))
		if swapped(t, lf) {
			t.Error("the join under an as-of join was swapped")
		}
		if got := runs(t, lf, "qv"); !slices.Equal(got, []string{"10", "10", "30", "30", "30", "30"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("MergeSorted above keeps its inputs sorted", func(t *testing.T) {
		lf := ev().Join(users(), ursus.JoinOn(c("uid"))).
			MergeSorted(ursus.Frame(ursus.Values("ts", []int64{0}), ursus.Values("uid", []int64{9})), "ts")
		if got := runs(t, lf, "ts"); !slices.Equal(got, []string{"0", "1", "2", "3", "4", "5", "6"}) {
			t.Errorf("%v", got)
		}
	})
}

// TestBuildSideKeepsAMergedZeroKeysSign: the merged key comes from the side the
// table is built from, and the hash equates -0.0 with +0.0, so a swapped join
// returned the right input's +0.0 for the left's -0.0.
func TestBuildSideKeepsAMergedZeroKeysSign(t *testing.T) {
	c := ursus.Col
	negZero := math.Copysign(0, -1)
	left := ursus.Frame(ursus.Values("k", slices.Repeat([]float64{negZero}, 6)))
	right := ursus.Frame(ursus.Values("k", slices.Repeat([]float64{0}, 60)))
	lf := left.Join(right, ursus.JoinOn(c("k")))
	if swapped(t, lf) {
		t.Error("a join on a float key was swapped")
	}
	df, err := lf.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	col, err := df.Column[float64]("k")
	if err != nil {
		t.Fatal(err)
	}
	for i := range df.Height() {
		if v, _ := col.Get(i); !math.Signbit(v) {
			t.Fatalf("row %d's key is +0.0; the left input's is -0.0", i)
		}
	}
}
