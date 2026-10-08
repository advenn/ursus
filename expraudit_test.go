package ursus_test

// What the audit of 0.4's expression work found (step 121).

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/i128"
)

// TestAGuardedBranchFailsOnlyOnItsOwnRows: both branches of a conditional are
// evaluated over every row, and a branch whose fault is an error — Int128 past its
// range since step 100, a strict cast — failed the query on rows its condition sent
// to the other branch. A guard is how a caller keeps those rows out.
func TestAGuardedBranchFailsOnlyOnItsOwnRows(t *testing.T) {
	c := ursus.Col
	all := []bool{true, true, true, true}
	lf := int128FrameValid(t, []i128.Int128{i128.Max, i128.FromInt64(1), i128.FromInt64(-3), i128.Min}, all)

	t.Run("Then, guarded", func(t *testing.T) {
		y := ursus.When(c("v").Lt(10).And(c("v").Gt(-10))).Then(c("v").Mul(2)).Otherwise(0)
		if got := runs(t, lf.Select(y.Alias("y")), "y"); !slices.Equal(got, []string{"0", "2", "-6", "0"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("Otherwise, guarded", func(t *testing.T) {
		y := ursus.When(c("v").Gt(10).Or(c("v").Lt(-10))).Then(0).Otherwise(c("v").Mul(2))
		if got := runs(t, lf.Select(y.Alias("y")), "y"); !slices.Equal(got, []string{"0", "2", "-6", "0"}) {
			t.Errorf("%v", got)
		}
	})
	t.Run("a row the branch is taken for still fails", func(t *testing.T) {
		y := ursus.When(c("v").Gt(0)).Then(c("v").Mul(2)).Otherwise(0)
		if _, err := lf.Select(y.Alias("y")).Collect(t.Context()); !errors.Is(err, ursus.ErrValue) {
			t.Errorf("Max * 2 under its own branch: want a value error, got %v", err)
		}
	})
	t.Run("a strict cast, guarded", func(t *testing.T) {
		s := ursus.Frame(ursus.Values("s", []string{"12", "x", "-4"}))
		y := ursus.When(c("s").Ne("x")).Then(c("s").Cast(ursus.Int64)).Otherwise(-1)
		if got := runs(t, s.Select(y.Alias("y")), "y"); !slices.Equal(got, []string{"12", "-1", "-4"}) {
			t.Errorf("%v", got)
		}
	})
}

// TestInt128NegAndAbsAtTheMinimum: -2^127 has no positive counterpart. Neg returned
// it unchanged and Abs returned it negative, where Lit(0).Sub(x) refused it: Int128
// is exact or refused since step 100.
func TestInt128NegAndAbsAtTheMinimum(t *testing.T) {
	c := ursus.Col
	lf := int128FrameValid(t, []i128.Int128{i128.FromInt64(-5), i128.Min}, []bool{true, true})
	for name, e := range map[string]ursus.Expr{"neg": c("v").Neg(), "abs": c("v").Abs()} {
		if _, err := lf.Select(e.Alias("y")).Collect(t.Context()); !errors.Is(err, ursus.ErrValue) {
			t.Errorf("%s of the minimum: want a value error, got %v", name, err)
		}
	}
	// A null minimum is not judged, and the rest negate.
	nullMin := int128FrameValid(t, []i128.Int128{i128.FromInt64(-5), i128.Min}, []bool{true, false})
	if got := runs(t, nullMin.Select(c("v").Abs().Alias("y")), "y"); !slices.Equal(got, []string{"5", "∅"}) {
		t.Errorf("%v", got)
	}
}

// TestTopKOrdersSignedZeros: the total order calls -0.0 and +0.0 equal, and TopK
// kept the first of two equals, so its answer depended on arrival order, which a
// parallel group-by decides. +0.0 now ranks above -0.0.
func TestTopKOrdersSignedZeros(t *testing.T) {
	c := ursus.Col
	negZero := math.Copysign(0, -1)
	for _, order := range [][]float64{{negZero, 0}, {0, negZero}} {
		df, err := ursus.Frame(ursus.Values("g", []int64{1, 1}), ursus.Values("x", order)).
			GroupBy(c("g")).Agg(c("x").TopK(1).List().First().Alias("top"), c("x").BottomK(1).List().First().Alias("bottom")).
			Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		top, _, _ := df.At[float64](0, "top")
		bottom, _, _ := df.At[float64](0, "bottom")
		if math.Signbit(top) || !math.Signbit(bottom) {
			t.Errorf("arriving as %v: top %v, bottom %v; want +0 and -0", order, top, bottom)
		}
	}
}
