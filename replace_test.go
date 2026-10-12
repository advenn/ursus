package ursus_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// TestReplaceAnswersAsPolars: Replace, ReplaceStrict and ReplaceOr (step 226),
// against Polars 1.44.1's replace and replace_strict, with the cell where ursus
// answers as a conditional instead marked: 2.5 into an Int64 widens it to Float64,
// where Polars truncates it to 2.
func TestReplaceAnswersAsPolars(t *testing.T) {
	frame := ursus.Frame(
		ursus.ValuesNullable("a", []int64{1, 2, 3, 0, 2}, []bool{true, true, true, false, true}),
		ursus.ValuesNullable("s", []string{"x", "y", "z", "", "y"}, []bool{true, true, true, false, true}),
		ursus.Values("i", []int32{1, 2, 3, 4, 2}),
		ursus.Values("f", []float64{math.NaN(), 1, math.Copysign(0, -1), 0, 7}),
	)
	c := ursus.Col
	got, err := frame.Select(
		c("a").Replace([]int64{2, 3}, []int64{20, 30}).Alias("r"),
		c("a").Replace([]int64{2}, []float64{2.5}).Alias("rf"),
		c("a").ReplaceStrict([]int64{1, 2, 3}, []string{"one", "two", "three"}).Alias("rs"),
		c("a").ReplaceOr([]int64{1, 2}, []string{"one", "two"}, "other").Alias("ro"),
		c("s").Replace([]string{"y"}, []string{"Y"}).Alias("sr"),
		c("i").Replace([]int{2}, []int{20}).Alias("ir"),
		c("f").Replace([]float64{math.NaN(), 0}, []float64{5, 6}).Alias("fr"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ok := []bool{true, true, true, false, true}
	want, err := ursus.Frame(
		ursus.ValuesNullable("r", []int64{1, 20, 30, 0, 20}, ok),
		// Polars: [1, 2, 3, null, 2], an Int64.
		ursus.ValuesNullable("rf", []float64{1, 2.5, 3, 0, 2.5}, ok),
		ursus.ValuesNullable("rs", []string{"one", "two", "three", "", "two"}, ok),
		ursus.Values("ro", []string{"one", "two", "other", "other", "two"}),
		ursus.ValuesNullable("sr", []string{"x", "Y", "z", "", "Y"}, ok),
		// 20 fits an Int32, so the column stays one, as in Polars.
		ursus.Values("ir", []int32{1, 20, 3, 4, 20}),
		// NaN matches NaN and −0 matches 0, as is_in has them.
		ursus.Values("fr", []float64{5, 1, 6, 6, 7}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	_, err = frame.Select(c("a").ReplaceStrict([]int64{1, 2}, []string{"one", "two"})).Collect(t.Context())
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "found 3 in row 2") {
		t.Errorf("an unmapped value should be refused, naming it and its row: %v", err)
	}
	for name, e := range map[string]ursus.Expr{
		"lengths differ":        c("a").Replace([]int64{1, 2}, []int64{1}),
		"an old value twice":    c("a").Replace([]int64{2, 2}, []int64{5, 6}),
		"a string against ints": c("a").Replace([]string{"x"}, []int64{1}),
		"strict with no values": c("a").ReplaceStrict([]int64{}, []string{}),
	} {
		if _, err := frame.Select(e).Collect(t.Context()); err == nil ||
			!strings.Contains(err.Error(), "replace") {
			t.Errorf("%s should be refused, naming replace: %v", name, err)
		}
	}
}

// TestAStrictReplaceIsNotPushedPastAJoin: replace_strict refuses a value its map
// does not hold, so, like a strict cast, it must not run on rows the join removes
// before it (internal/plan/fallible.go).
func TestAStrictReplaceIsNotPushedPastAJoin(t *testing.T) {
	left := ursus.Frame(ursus.Values("k", []int64{1, 2}), ursus.Values("v", []int64{10, 99}))
	right := ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("r", []int64{9}))
	c := ursus.Col
	df, err := left.Join(right, ursus.JoinOn(c("k"))).
		Filter(c("v").ReplaceStrict([]int64{10}, []string{"ten"}).Eq("ten")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Errorf("%d rows, want 1", df.Height())
	}
}
