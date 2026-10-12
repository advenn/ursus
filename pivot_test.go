package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// TestPivotAnswersAsPolars: DataFrame.Pivot (step 229) against Polars 1.44.1's
// DataFrame.pivot: rows in the order each index first appears, a null index a row;
// columns in the order each On value first appears, or sorted; two value columns
// named value_on; a null On value a column "null"; a cell of two values refused
// without Agg and summed with it.
func TestPivotAnswersAsPolars(t *testing.T) {
	ctx := t.Context()
	df, err := ursus.Frame(
		ursus.ValuesNullable("k", []string{"b", "a", "b", "a", ""}, []bool{true, true, true, true, false}),
		ursus.Values("on", []string{"y", "x", "x", "y", "x"}),
		ursus.Values("v", []int64{1, 2, 3, 4, 5}),
		ursus.ValuesNullable("w", []float64{1.5, 2.5, 0, 4.5, 5.5}, []bool{true, true, false, true, true}),
	).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check := func(got *ursus.DataFrame, want ...*ursus.Column) {
		t.Helper()
		w, err := ursus.Frame(want...).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, w)
	}
	k := ursus.ValuesNullable("k", []string{"b", "a", ""}, []bool{true, true, false})

	got, err := df.Pivot(ctx, ursus.PivotOptions{On: "on", Index: []string{"k"}, Values: []string{"v"}})
	if err != nil {
		t.Fatal(err)
	}
	check(got, k, ursus.ValuesNullable("y", []int64{1, 4, 0}, []bool{true, true, false}),
		ursus.Values("x", []int64{3, 2, 5}))

	got, err = df.Pivot(ctx, ursus.PivotOptions{On: "on", Index: []string{"k"}, Values: []string{"v", "w"}})
	if err != nil {
		t.Fatal(err)
	}
	check(got, k, ursus.ValuesNullable("v_y", []int64{1, 4, 0}, []bool{true, true, false}),
		ursus.Values("v_x", []int64{3, 2, 5}),
		ursus.ValuesNullable("w_y", []float64{1.5, 4.5, 0}, []bool{true, true, false}),
		ursus.ValuesNullable("w_x", []float64{0, 2.5, 5.5}, []bool{false, true, true}))

	got, err = df.Pivot(ctx, ursus.PivotOptions{On: "on", Index: []string{"k"}, Values: []string{"v"},
		SortColumns: true})
	if err != nil {
		t.Fatal(err)
	}
	if cols := strings.Join(got.Columns(), ","); cols != "k,x,y" {
		t.Errorf("sorted, the columns are %s, want k,x,y", cols)
	}

	// Values left out are every other column: here w.
	got, err = df.Pivot(ctx, ursus.PivotOptions{On: "on", Index: []string{"k", "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if cols := strings.Join(got.Columns(), ","); cols != "k,v,y,x" {
		t.Errorf("with Values left out, the columns are %s, want k,v,y,x", cols)
	}

	dup, err := ursus.Frame(ursus.Values("k", []int64{1, 1}), ursus.Values("on", []string{"x", "x"}),
		ursus.Values("v", []int64{1, 2})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dup.Pivot(ctx, ursus.PivotOptions{On: "on", Index: []string{"k"}, Values: []string{"v"}})
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "Agg") {
		t.Errorf("a cell of two values should be refused, asking for Agg: %v", err)
	}
	got, err = dup.Pivot(ctx, ursus.PivotOptions{On: "on", Index: []string{"k"}, Values: []string{"v"},
		Agg: ursus.Expr.Sum})
	if err != nil {
		t.Fatal(err)
	}
	if got, err = got.Select(ctx, ursus.Col("k"), ursus.Col("x").Cast(ursus.Int64)); err != nil {
		t.Fatal(err)
	}
	check(got, ursus.Values("k", []int64{1}), ursus.Values("x", []int64{3}))

	nullOn, err := ursus.Frame(ursus.Values("k", []int64{1, 2}),
		ursus.ValuesNullable("on", []int64{10, 0}, []bool{true, false}), ursus.Values("v", []int64{1, 2})).
		Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err = nullOn.Pivot(ctx, ursus.PivotOptions{On: "on", Index: []string{"k"}, Values: []string{"v"}})
	if err != nil {
		t.Fatal(err)
	}
	check(got, ursus.Values("k", []int64{1, 2}), ursus.ValuesNullable("10", []int64{1, 0}, []bool{true, false}),
		ursus.ValuesNullable("null", []int64{0, 2}, []bool{false, true}))

	for name, o := range map[string]ursus.PivotOptions{
		"no On column":       {On: "nope", Index: []string{"k"}},
		"neither":            {On: "on"},
		"a column twice":     {On: "on", Index: []string{"k"}, Values: []string{"k"}},
		"a name taken twice": {On: "on", Index: []string{"y"}, Values: []string{"v"}},
	} {
		frame := df
		if name == "a name taken twice" {
			frame, err = df.WithColumns(ctx, ursus.Col("k").Alias("y"))
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err := frame.Pivot(ctx, o)
		if !errors.Is(err, uerr.ErrSchema) || !strings.Contains(err.Error(), "pivot") {
			t.Errorf("%s should be refused as a schema error naming pivot: %v", name, err)
		}
	}
}

// TestTransposeAnswersAsPolars: DataFrame.Transpose (step 229) against Polars
// 1.44.1: column j holds row j, every column cast to the type they meet at; a header
// column of the names; given column names; and a String beside a number refused,
// where Polars casts both to String.
func TestTransposeAnswersAsPolars(t *testing.T) {
	ctx := t.Context()
	df, err := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("b", []float64{3.5, 4.5}),
		ursus.ValuesNullable("c", []int64{0, 6}, []bool{false, true})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := df.Transpose(ctx, ursus.TransposeOptions{IncludeHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(ursus.Values("column", []string{"a", "b", "c"}),
		ursus.ValuesNullable("column_0", []float64{1, 3.5, 0}, []bool{true, true, false}),
		ursus.Values("column_1", []float64{2, 4.5, 6})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	got, err = df.Transpose(ctx, ursus.TransposeOptions{ColumnNames: []string{"p", "q"}})
	if err != nil {
		t.Fatal(err)
	}
	if cols := strings.Join(got.Columns(), ","); cols != "p,q" || got.Height() != 3 {
		t.Errorf("named, the columns are %s over %d rows, want p,q over 3", cols, got.Height())
	}
	if _, err := df.Transpose(ctx, ursus.TransposeOptions{ColumnNames: []string{"p"}}); !errors.Is(err, uerr.ErrSchema) {
		t.Errorf("one name for two rows should be refused: %v", err)
	}

	mixed, err := ursus.Frame(ursus.Values("a", []int64{1}), ursus.Values("s", []string{"x"})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mixed.Transpose(ctx, ursus.TransposeOptions{}); !errors.Is(err, uerr.ErrType) {
		t.Errorf("a String beside an Int64 should be refused: %v", err)
	}
}
