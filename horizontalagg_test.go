package ursus_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// TestHorizontalReductionsAnswerAsPolars: the six horizontal reductions (step 225),
// against Polars 1.44.1's answers, with the three cells where ursus answers as its
// column aggregates do instead, each marked: a max over NaN is NaN, where Polars
// skips it; a sum of nulls is null, where Polars gives 0; an integer sum is an
// Int128. Over an Int32, a Float64 and an Int64 with nulls, strings, and Booleans for
// Kleene's any and all; a literal operand repeats.
func TestHorizontalReductionsAnswerAsPolars(t *testing.T) {
	nan := math.NaN()
	frame := ursus.Frame(
		ursus.ValuesNullable("a", []int32{1, 0, 0, 5, 2}, []bool{true, false, false, true, true}),
		ursus.ValuesNullable("b", []float64{3.5, 2, 0, nan, -1}, []bool{true, true, false, true, true}),
		ursus.ValuesNullable("c", []int64{0, 7, 0, 1, 2}, []bool{false, true, false, true, true}),
		ursus.ValuesNullable("x", []string{"b", "", "a", "q", "q"}, []bool{true, false, true, true, true}),
		ursus.ValuesNullable("y", []string{"a", "c", "", "r", "p"}, []bool{true, true, false, true, true}),
	)
	c := ursus.Col
	got, err := frame.Select(
		ursus.MaxHorizontal(c("a"), c("b"), c("c")).Alias("max"),
		ursus.MinHorizontal(c("a"), c("b"), c("c")).Alias("min"),
		ursus.SumHorizontal(c("a"), c("b"), c("c")).Alias("sum"),
		ursus.MeanHorizontal(c("a"), c("b"), c("c")).Alias("mean"),
		ursus.SumHorizontal(c("a"), c("c")).Cast(ursus.Int64).Alias("sumi"),
		ursus.MaxHorizontal(c("a"), c("c")).Alias("maxi"),
		ursus.MaxHorizontal(c("x"), c("y")).Alias("smax"),
		ursus.MinHorizontal(c("x"), c("y")).Alias("smin"),
		ursus.SumHorizontal(c("c"), ursus.Lit(int64(10))).Cast(ursus.Int64).Alias("plus10"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(
		// Polars: [3.5, 7, null, 5, 2]; ursus's NaN is the largest.
		ursus.ValuesNullable("max", []float64{3.5, 7, 0, nan, 2}, []bool{true, true, false, true, true}),
		ursus.ValuesNullable("min", []float64{1, 2, 0, 1, -1}, []bool{true, true, false, true, true}),
		// Polars: [4.5, 9, 0, NaN, 3]; ursus's sum of nulls is null.
		ursus.ValuesNullable("sum", []float64{4.5, 9, 0, nan, 3}, []bool{true, true, false, true, true}),
		ursus.ValuesNullable("mean", []float64{2.25, 4.5, 0, nan, 1}, []bool{true, true, false, true, true}),
		// Polars: [1, 7, 0, 6, 4], an Int64.
		ursus.ValuesNullable("sumi", []int64{1, 7, 0, 6, 4}, []bool{true, true, false, true, true}),
		ursus.ValuesNullable("maxi", []int64{1, 7, 0, 5, 2}, []bool{true, true, false, true, true}),
		ursus.Values("smax", []string{"b", "c", "a", "r", "q"}),
		ursus.Values("smin", []string{"a", "c", "a", "q", "p"}),
		ursus.Values("plus10", []int64{10, 17, 10, 11, 12}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	sch, err := frame.Select(ursus.SumHorizontal(c("a"), c("c")), ursus.MeanHorizontal(c("a"), c("c")).Alias("m")).CollectSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sch.FieldSlice()[0].Type != dtype.Int128 || sch.FieldSlice()[1].Type != dtype.Float64 {
		t.Errorf("an integer sum and mean are %s and %s, want Int128 and Float64",
			sch.FieldSlice()[0].Type, sch.FieldSlice()[1].Type)
	}

	b := ursus.Frame(
		ursus.ValuesNullable("p", []bool{true, false, false, false, false, true}, []bool{true, true, false, false, true, true}),
		ursus.ValuesNullable("q", []bool{false, false, false, false, false, true}, []bool{false, false, false, true, true, true}),
	)
	gotB, err := b.Select(ursus.AnyHorizontal(c("p"), c("q")).Alias("any"),
		ursus.AllHorizontal(c("p"), c("q")).Alias("all")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantB, err := ursus.Frame(
		ursus.ValuesNullable("any", []bool{true, false, false, false, false, true}, []bool{true, false, false, false, true, true}),
		ursus.ValuesNullable("all", []bool{false, false, false, false, false, true}, []bool{false, true, false, true, true, true}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, gotB, wantB)

	for name, e := range map[string]ursus.Expr{
		"any over integers":     ursus.AnyHorizontal(c("a"), c("p")),
		"max of string and int": ursus.MaxHorizontal(c("x"), c("a")),
	} {
		_, err := b.WithColumns(ursus.Lit(int64(1)).Alias("a"), ursus.Lit("s").Alias("x")).
			Select(e).Collect(t.Context())
		if !errors.Is(err, uerr.ErrType) || !strings.Contains(err.Error(), "horizontal") {
			t.Errorf("%s should be refused, naming the function: %v", name, err)
		}
	}
	if _, err := ursus.Frame(ursus.Values("a", []int64{1})).Select(ursus.MaxHorizontal()).
		Collect(t.Context()); err == nil {
		t.Error("MaxHorizontal of nothing answered")
	}
}
