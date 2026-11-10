package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// TestPivotAnswersAsPolars: DataFrame.Pivot (step 229) against Polars' pivot. Its
// rows, columns and cells are the pivot group's, Polars 2.1.0's
// (oracle_pivot_test.go); here stay the refusals: a cell of two values without Agg,
// and the options a schema error names.
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

	dup, err := ursus.Frame(ursus.Values("k", []int64{1, 1}), ursus.Values("on", []string{"x", "x"}),
		ursus.Values("v", []int64{1, 2})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dup.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}})
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "Agg") {
		t.Errorf("a cell of two values should be refused, asking for Agg: %v", err)
	}

	for name, o := range map[string]ursus.PivotOptions{
		"no On column":       {On: []string{"nope"}, Index: []string{"k"}},
		"neither":            {On: []string{"on"}},
		"a column twice":     {On: []string{"on"}, Index: []string{"k"}, Values: []string{"k"}},
		"a name taken twice": {On: []string{"on"}, Index: []string{"y"}, Values: []string{"v"}},
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

// i64s, f64s and strs are nullable columns of their values, nil a null.
func i64s(name string, vs ...any) *ursus.Column {
	vals, ok := make([]int64, len(vs)), make([]bool, len(vs))
	for i, v := range vs {
		if v != nil {
			vals[i], ok[i] = int64(v.(int)), true
		}
	}
	return ursus.ValuesNullable(name, vals, ok)
}

func f64s(name string, vs ...any) *ursus.Column {
	vals, ok := make([]float64, len(vs)), make([]bool, len(vs))
	for i, v := range vs {
		if v != nil {
			vals[i], ok[i] = v.(float64), true
		}
	}
	return ursus.ValuesNullable(name, vals, ok)
}

func strs(name string, vs ...any) *ursus.Column {
	vals, ok := make([]string, len(vs)), make([]bool, len(vs))
	for i, v := range vs {
		if v != nil {
			vals[i], ok[i] = v.(string), true
		}
	}
	return ursus.ValuesNullable(name, vals, ok)
}

// pivotFrame is the frame of TestPivotRefusals, and of the pivot group's cases.
func pivotFrame() *ursus.LazyFrame {
	return ursus.Frame(
		strs("k", "b", "a", "b", "a", nil),
		strs("on", "y", "x", "x", "y", "x"),
		strs("p", "p", "q", "p", "q", "p"),
		i64s("v", 1, 2, 3, 4, 5),
		f64s("w", 1.5, 2.5, nil, 4.5, 5.5),
	)
}

// TestPivotSeveralOnNamesANull is P1 of v0.6.7-scope.md (step 303): several On
// columns name each new column by their values together, {"y","p"}, as Polars names
// it; the pivot group records those. A combination holding a null is written null,
// {null,"c"}, where Polars names the whole column "null" and then refuses a second
// (feature-gaps.md §9): Polars has no answer, so the documented naming is pinned here.
// Numbers are not quoted.
func TestPivotSeveralOnNamesANull(t *testing.T) {
	ctx := t.Context()
	nulls, err := ursus.Frame(i64s("k", 1, 1, 1), i64s("i", 1, nil, 2), strs("s", "a", "c", nil),
		i64s("v", 1, 2, 3)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := nulls.Pivot(ctx, ursus.PivotOptions{On: []string{"i", "s"}, Index: []string{"k"}, Values: []string{"v"}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(i64s("k", 1), i64s(`{1,"a"}`, 1), i64s(`{null,"c"}`, 2), i64s(`{2,null}`, 3)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// TestPivotRefusals (step 303): what a pivot refuses, by its message, and the
// columns of a lazy one known before it runs.
func TestPivotRefusals(t *testing.T) {
	ctx := t.Context()
	lf := pivotFrame()
	of := func(cols ...*ursus.Column) ursus.PivotOptions {
		return ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}, OnColumns: cols}
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"no OnColumns, lazily", lf.Pivot(of()), "a lazy pivot needs OnColumns"},
		{"no On", lf.Pivot(ursus.PivotOptions{Index: []string{"k"}}), "pivot needs On"},
		{"OnColumns of another name", lf.Pivot(of(strs("o", "x"))), `OnColumns has a column "o", which is not among On`},
		{"OnColumns without one of On", lf.Pivot(ursus.PivotOptions{On: []string{"on", "p"}, Index: []string{"k"},
			OnColumns: []*ursus.Column{strs("on", "x")}}), `OnColumns has no column "p"`},
		{"OnColumns twice", lf.Pivot(of(strs("on", "x"), strs("on", "y"))), `OnColumns has "on" twice`},
		{"OnColumns of two lengths", lf.Pivot(ursus.PivotOptions{On: []string{"on", "p"}, Index: []string{"k"},
			OnColumns: []*ursus.Column{strs("on", "x"), strs("p", "p", "q")}}), `"on" is 1 long and "p" 2`},
		{"a nil column", lf.Pivot(of(nil)), "OnColumns holds a nil column"},
		// Polars: cannot compare string with numeric type (i64).
		{"numbers for text", lf.Pivot(of(i64s("on", 1))), `OnColumns' "on" is Int64, and the frame's is String, which cannot be compared`},
		{"text for numbers", lf.Pivot(ursus.PivotOptions{On: []string{"v"}, Index: []string{"k"}, Values: []string{"w"},
			OnColumns: []*ursus.Column{strs("v", "1")}}), "which cannot be compared"},
		{"a value the column cannot hold", lf.WithColumns(ursus.Col("v").Cast(ursus.Int8)).Pivot(ursus.PivotOptions{On: []string{"v"},
			Index: []string{"k"}, Values: []string{"w"}, OnColumns: []*ursus.Column{i64s("v", 300)}}), `OnColumns' "v" as the frame's Int8`},
		// Polars: column with name 'x' has more than one occurrence.
		{"a value twice", lf.Pivot(of(strs("on", "x", "x"))), `two columns of the pivot would be named "x"`},
		{"an Agg reading another column", lf.Pivot(ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"},
			OnColumns: []*ursus.Column{strs("on", "x")}, Agg: func(e ursus.Expr) ursus.Expr { return e.Sum().Add(ursus.Col("w").Sum()) }}),
			`Agg reads "w"`},
		// Polars: aggregation 'item' expected no or a single value, got 2 values.
		{"two values in a cell", ursus.Frame(i64s("k", 1, 1), strs("on", "x", "x"), i64s("v", 1, 2)).Pivot(of(strs("on", "x"))),
			"2 rows fall where k=1 and on=x meet, and Agg is not set"},
	} {
		_, err := tc.q.Collect(ctx)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
		}
	}

	// The columns of a lazy pivot are known before it runs: a count's type, and the
	// names of a value each.
	q := lf.Pivot(ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v", "w"},
		Agg: ursus.Expr.Len, OnColumns: []*ursus.Column{strs("on", "x", "y")}})
	s, err := q.CollectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var fields []string
	for _, f := range s.All() {
		fields = append(fields, f.Name+":"+f.Type.String())
	}
	if got := strings.Join(fields, " "); got != "k:String v_x:Uint64 v_y:Uint64 w_x:Uint64 w_y:Uint64" {
		t.Errorf("the schema %s", got)
	}
	plan, err := q.Explain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "PIVOT on index k values v, w -> [v_x, v_y, w_x, w_y]") ||
		!strings.Contains(plan, "AGGREGATE") || !strings.Contains(plan, "(ordered)") {
		t.Errorf("the plan:\n%s", plan)
	}
}

// TestPivotUnderAMemoryLimit (step 303): a lazy pivot streams its input through
// the group-by beneath it, and holds only the pairs: 20,000 rows in batches of 64,
// under a limit far below them, answer as without one. Pairs past the limit are
// refused, naming the pivot.
func TestPivotUnderAMemoryLimit(t *testing.T) {
	ctx := t.Context()
	const n = 20_000
	pivot := func(groups int) *ursus.LazyFrame {
		ks, ons, vs := make([]int64, n), make([]string, n), make([]float64, n)
		for i := range n {
			ks[i], ons[i], vs[i] = int64(i%groups), string(rune('a'+i%4)), float64(i)
		}
		return ursus.Frame(ursus.Values("k", ks), ursus.Values("on", ons), ursus.Values("v", vs)).
			Pivot(ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}, Agg: ursus.Expr.Sum,
				OnColumns: []*ursus.Column{ursus.Values("on", []string{"a", "b", "c", "d", "e"})}})
	}
	q := pivot(50)
	want, err := q.Collect(ctx, ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	var st ursus.MemoryStats
	got, err := q.Collect(ctx, ursus.WithBatchSize(64), ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(t.TempDir()),
		ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatalf("under 64 KiB: %v", err)
	}
	ursustest.AssertFrameEqual(t, got, want)
	if got.Height() != 50 || got.Width() != 6 {
		t.Errorf("a pivot of %d rows and %d columns", got.Height(), got.Width())
	}

	// 20,000 pairs, which the group-by spills, and the pivot then cannot hold.
	_, err = pivot(n).Collect(ctx, ursus.WithBatchSize(64), ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(t.TempDir()))
	if !errors.Is(err, uerr.ErrResource) || !strings.Contains(err.Error(), "pivot holds a row for each index") {
		t.Errorf("pairs past the limit: %v", err)
	}
}

// TestTransposeAnswersAsPolars: DataFrame.Transpose (step 229) against Polars'
// transpose; its cells and types are the pivot group's, Polars 2.1.0's. Here stay the
// refusals: one name for two rows, a List beside a number, and the namings.
func TestTransposeAnswersAsPolars(t *testing.T) {
	ctx := t.Context()
	df, err := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("b", []float64{3.5, 4.5}),
		ursus.ValuesNullable("c", []int64{0, 6}, []bool{false, true})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := df.Transpose(ctx, ursus.TransposeOptions{ColumnNames: []string{"p"}}); !errors.Is(err, uerr.ErrSchema) {
		t.Errorf("one name for two rows should be refused: %v", err)
	}
	listed, err := ursus.Frame(ursus.Values("a", []int64{1})).
		WithColumns(ursus.ConcatList(ursus.Col("a")).Alias("l")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listed.Transpose(ctx, ursus.TransposeOptions{}); !errors.Is(err, uerr.ErrType) {
		t.Errorf("a List beside an Int64 should be refused: %v", err)
	}

	named, err := ursus.Frame(ursus.Values("id", []string{"a", "b", "c"}), ursus.Values("col1", []int64{1, 3, 2}),
		ursus.Values("col2", []int64{3, 4, 6})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		df   *ursus.DataFrame
		o    ursus.TransposeOptions
		want string
	}{
		"both namings":   {named, ursus.TransposeOptions{ColumnNamesFrom: "id", ColumnNames: []string{"p", "q", "r"}}, "give one"},
		"an unknown one": {named, ursus.TransposeOptions{ColumnNamesFrom: "nope"}, "nope"},
		"numbers":        {named, ursus.TransposeOptions{ColumnNamesFrom: "col1"}, "text is needed"},
		"a null": {collect(t, ursus.Frame(ursus.ValuesNullable("id", []string{"a", ""}, []bool{true, false}),
			ursus.Values("v", []int64{1, 2}))), ursus.TransposeOptions{ColumnNamesFrom: "id"}, "null at row 1"},
		"a name twice": {collect(t, ursus.Frame(ursus.Values("id", []string{"a", "a"}), ursus.Values("v", []int64{1, 2}))),
			ursus.TransposeOptions{ColumnNamesFrom: "id"}, `"a"`},
	} {
		if _, err := tc.df.Transpose(ctx, tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %s", name, err, tc.want)
		}
	}
}
