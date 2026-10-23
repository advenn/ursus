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

	got, err := df.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}})
	if err != nil {
		t.Fatal(err)
	}
	check(got, k, ursus.ValuesNullable("y", []int64{1, 4, 0}, []bool{true, true, false}),
		ursus.Values("x", []int64{3, 2, 5}))

	got, err = df.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v", "w"}})
	if err != nil {
		t.Fatal(err)
	}
	check(got, k, ursus.ValuesNullable("v_y", []int64{1, 4, 0}, []bool{true, true, false}),
		ursus.Values("v_x", []int64{3, 2, 5}),
		ursus.ValuesNullable("w_y", []float64{1.5, 4.5, 0}, []bool{true, true, false}),
		ursus.ValuesNullable("w_x", []float64{0, 2.5, 5.5}, []bool{false, true, true}))

	got, err = df.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"},
		SortColumns: true})
	if err != nil {
		t.Fatal(err)
	}
	if cols := strings.Join(got.Columns(), ","); cols != "k,x,y" {
		t.Errorf("sorted, the columns are %s, want k,x,y", cols)
	}

	// Values left out are every other column: here w.
	got, err = df.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k", "v"}})
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
	_, err = dup.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}})
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "Agg") {
		t.Errorf("a cell of two values should be refused, asking for Agg: %v", err)
	}
	got, err = dup.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"},
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
	got, err = nullOn.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}})
	if err != nil {
		t.Fatal(err)
	}
	check(got, ursus.Values("k", []int64{1, 2}), ursus.ValuesNullable("10", []int64{1, 0}, []bool{true, false}),
		ursus.ValuesNullable("null", []int64{0, 2}, []bool{false, true}))

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

// pivotFrame is the frame of TestPivotSeveralOn and TestPivotOnColumns, the one
// Polars was asked about.
func pivotFrame() *ursus.LazyFrame {
	return ursus.Frame(
		strs("k", "b", "a", "b", "a", nil),
		strs("on", "y", "x", "x", "y", "x"),
		strs("p", "p", "q", "p", "q", "p"),
		i64s("v", 1, 2, 3, 4, 5),
		f64s("w", 1.5, 2.5, nil, 4.5, 5.5),
	)
}

// assertPivot holds got to the columns want, each Polars 1.44.1's.
func assertPivot(t *testing.T, name string, got *ursus.DataFrame, err error, want ...*ursus.Column) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	w, err := ursus.Frame(want...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Run(name, func(t *testing.T) { ursustest.AssertFrameEqual(t, got, w) })
}

// TestPivotSeveralOn is P1 of v0.6.7-scope.md (step 303): several On columns, each
// new column named by their values together as Polars names it, {"y","p"}, text
// quoted, and v_{"y","p"} for several value columns. Every answer is Polars 1.44.1's.
func TestPivotSeveralOn(t *testing.T) {
	ctx := t.Context()
	df, err := pivotFrame().Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	k := strs("k", "b", "a", nil)
	on := []string{"on", "p"}

	got, err := df.Pivot(ctx, ursus.PivotOptions{On: on, Index: []string{"k"}, Values: []string{"v"}})
	assertPivot(t, "one value", got, err, k, i64s(`{"y","p"}`, 1, nil, nil), i64s(`{"x","q"}`, nil, 2, nil),
		i64s(`{"x","p"}`, 3, nil, 5), i64s(`{"y","q"}`, nil, 4, nil))

	got, err = df.Pivot(ctx, ursus.PivotOptions{On: on, Index: []string{"k"}, Values: []string{"v", "w"}})
	assertPivot(t, "two values", got, err, k,
		i64s(`v_{"y","p"}`, 1, nil, nil), i64s(`v_{"x","q"}`, nil, 2, nil), i64s(`v_{"x","p"}`, 3, nil, 5),
		i64s(`v_{"y","q"}`, nil, 4, nil),
		f64s(`w_{"y","p"}`, 1.5, nil, nil), f64s(`w_{"x","q"}`, nil, 2.5, nil), f64s(`w_{"x","p"}`, nil, nil, 5.5),
		f64s(`w_{"y","q"}`, nil, 4.5, nil))

	got, err = df.Pivot(ctx, ursus.PivotOptions{On: on, Index: []string{"k"}, Values: []string{"v"}, SortColumns: true})
	assertPivot(t, "sorted", got, err, k, i64s(`{"x","p"}`, 3, nil, 5), i64s(`{"x","q"}`, nil, 2, nil),
		i64s(`{"y","p"}`, 1, nil, nil), i64s(`{"y","q"}`, nil, 4, nil))

	// Lazily, the combinations given: a row of OnColumns a new column, its columns
	// in any order.
	got, err = pivotFrame().Pivot(ursus.PivotOptions{On: on, Index: []string{"k"}, Values: []string{"v"},
		OnColumns: []*ursus.Column{strs("p", "p", "q"), strs("on", "x", "y")}}).Collect(ctx)
	assertPivot(t, "lazy", got, err, k, i64s(`{"x","p"}`, 3, nil, 5), i64s(`{"y","q"}`, nil, 4, nil))

	// A null among them is written null, where Polars names the whole column "null"
	// and then refuses a second (feature-gaps.md §9). Numbers are not quoted.
	nulls, err := ursus.Frame(i64s("k", 1, 1, 1), i64s("i", 1, nil, 2), strs("s", "a", "c", nil),
		i64s("v", 1, 2, 3)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err = nulls.Pivot(ctx, ursus.PivotOptions{On: []string{"i", "s"}, Index: []string{"k"}, Values: []string{"v"}})
	assertPivot(t, "nulls", got, err, i64s("k", 1), i64s(`{1,"a"}`, 1), i64s(`{null,"c"}`, 2), i64s(`{2,null}`, 3))
}

// TestPivotOnColumns is P1 of v0.6.7-scope.md (step 303): the new columns given, as
// Polars' on_columns, lazily and eagerly. A given value the frame never holds is a
// column of empty cells, one it holds and OnColumns does not is left out, its index
// still a row; a null, NaN and -0 match as a group-by keys them. Every answer is
// Polars 1.44.1's.
func TestPivotOnColumns(t *testing.T) {
	ctx := t.Context()
	df, err := pivotFrame().Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	k := strs("k", "b", "a", nil)
	of := func(vs ...any) []*ursus.Column { return []*ursus.Column{strs("on", vs...)} }
	opts := func(cols []*ursus.Column) ursus.PivotOptions {
		return ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}, OnColumns: cols}
	}

	got, err := df.Pivot(ctx, opts(of("x")))
	assertPivot(t, "a subset", got, err, k, i64s("x", 3, 2, 5))
	// Polars ignores sort_columns beside on_columns too.
	o := opts(of("z", "y", "x"))
	o.SortColumns = true
	got, err = df.Pivot(ctx, o)
	assertPivot(t, "reordered, z never held", got, err, k, i64s("z", nil, nil, nil), i64s("y", 1, 4, nil),
		i64s("x", 3, 2, 5))
	got, err = pivotFrame().Pivot(opts(of("x", "y"))).Collect(ctx)
	assertPivot(t, "lazy", got, err, k, i64s("x", 3, 2, 5), i64s("y", 1, 4, nil))
	got, err = pivotFrame().Pivot(opts(of("x"))).Collect(ctx)
	assertPivot(t, "lazy, a subset", got, err, k, i64s("x", 3, 2, 5))
	got, err = ursus.Frame(strs("k", "a", "b"), strs("on", "x", "y"), i64s("v", 1, 2)).Pivot(opts(of("x"))).Collect(ctx)
	assertPivot(t, "an index none of whose values is given", got, err, strs("k", "a", "b"), i64s("x", 1, nil))
	got, err = pivotFrame().WithColumns(ursus.When(ursus.Col("on").Eq(ursus.Lit("y"))).Then(ursus.Null(ursus.String)).
		Otherwise(ursus.Col("on")).Alias("on")).Pivot(opts(of("x", nil))).Collect(ctx)
	assertPivot(t, "a null given", got, err, k, i64s("x", 3, 2, 5), i64s("null", 1, 4, nil))
	got, err = pivotFrame().Pivot(opts(of())).Collect(ctx)
	assertPivot(t, "none given", got, err, k)

	// NaN matches NaN and 0 matches -0, as Polars' ==v does; a column is named by
	// the value given, as Polars writes a float, 0.0 (since step 325).
	floats := ursus.Frame(i64s("k", 1, 1, 1, 1, 1, 1), f64s("f", 1.5, 2.0, 1.5, math.NaN(), math.Copysign(0, -1), 1.5),
		i64s("v", 1, 2, 3, 4, 5, 6))
	got, err = floats.Pivot(ursus.PivotOptions{On: []string{"f"}, Index: []string{"k"}, Values: []string{"v"},
		Agg: ursus.Expr.First, OnColumns: []*ursus.Column{f64s("f", math.NaN(), 0.0, 1.5)}}).Collect(ctx)
	assertPivot(t, "NaN and the zeros", got, err, i64s("k", 1), i64s("NaN", 4), i64s("0.0", 5), i64s("1.5", 1))
	// Both zeros given: each a column, each of both zeros' rows, as Polars' are.
	got, err = ursus.Frame(i64s("k", 1, 1, 2), f64s("f", 0.0, math.Copysign(0, -1), 0.0), i64s("v", 1, 2, 3)).
		Pivot(ursus.PivotOptions{On: []string{"f"}, Index: []string{"k"}, Values: []string{"v"},
			Agg:       func(e ursus.Expr) ursus.Expr { return e.Sum().Cast(ursus.Int64) },
			OnColumns: []*ursus.Column{f64s("f", 0.0, math.Copysign(0, -1))}}).Collect(ctx)
	assertPivot(t, "0 and -0 both given", got, err, i64s("k", 1, 2), i64s("0.0", 3, 3), i64s("-0.0", 3, 3))

	// OnColumns of another integer width are cast to the On column's.
	got, err = pivotFrame().WithColumns(ursus.Col("v").Cast(ursus.Int32).Alias("n")).
		Pivot(ursus.PivotOptions{On: []string{"n"}, Index: []string{"k"}, Values: []string{"w"},
			OnColumns: []*ursus.Column{i64s("n", 2, 5)}}).Collect(ctx)
	assertPivot(t, "an Int64 given for an Int32", got, err, k, f64s("2", nil, 2.5, nil), f64s("5", nil, nil, 5.5))
}

// TestPivotCellsOfNone (step 303): a cell is Agg of the values that fall in it, so
// one none fall in is Agg of none, as Polars 1.44.1 answers it: a count's 0, a
// first's null. A sum of none is null here, Polars' 0 (feature-gaps.md §9), so
// Sum().FillNull(0) is Polars' sum. Without Agg a cell of none is null. Until step
// 303 every such cell was null.
func TestPivotCellsOfNone(t *testing.T) {
	ctx := t.Context()
	df, err := ursus.Frame(i64s("k", 1, 1, 2), strs("on", "x", "x", "y"), i64s("v", 1, 2, 5)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	k := i64s("k", 1, 2)
	for _, tc := range []struct {
		name string
		agg  func(ursus.Expr) ursus.Expr
		x, y *ursus.Column
	}{
		{"len", ursus.Expr.Len, nil, nil},
		{"n_unique", ursus.Expr.NUnique, nil, nil},
		{"sum", ursus.Expr.Sum, i64s("x", 3, nil), i64s("y", nil, 5)},
		{"sum, nulls 0", func(e ursus.Expr) ursus.Expr { return e.Sum().FillNull(0) }, i64s("x", 3, 0), i64s("y", 0, 5)},
		{"first", ursus.Expr.First, i64s("x", 1, nil), i64s("y", nil, 5)},
		{"last", ursus.Expr.Last, i64s("x", 2, nil), i64s("y", nil, 5)},
		{"min", ursus.Expr.Min, i64s("x", 1, nil), i64s("y", nil, 5)},
		{"max", ursus.Expr.Max, i64s("x", 2, nil), i64s("y", nil, 5)},
		{"mean", ursus.Expr.Mean, f64s("x", 1.5, nil), f64s("y", nil, 5.0)},
		{"median", ursus.Expr.Median, f64s("x", 1.5, nil), f64s("y", nil, 5.0)},
	} {
		got, err := df.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}, Agg: tc.agg})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.x == nil {
			// A count is a Uint64 here, Polars' UInt32 (§9): its values, 2 and 0, 0
			// and 1.
			if got, err = got.Select(ctx, ursus.Col("k"), ursus.Col("x").Cast(ursus.Int64), ursus.Col("y").Cast(ursus.Int64)); err != nil {
				t.Fatal(err)
			}
			tc.x, tc.y = i64s("x", 2, 0), i64s("y", 0, 1)
		} else if tc.name == "sum" || tc.name == "sum, nulls 0" {
			// An integer sum is an Int128 here (§9).
			if got, err = got.Select(ctx, ursus.Col("k"), ursus.Col("x").Cast(ursus.Int64), ursus.Col("y").Cast(ursus.Int64)); err != nil {
				t.Fatal(err)
			}
		}
		assertPivot(t, tc.name, got, nil, k, tc.x, tc.y)
	}
	got, err := df.Filter(ctx, ursus.Col("v").Ne(2))
	if err != nil {
		t.Fatal(err)
	}
	got, err = got.Pivot(ctx, ursus.PivotOptions{On: []string{"on"}, Index: []string{"k"}, Values: []string{"v"}})
	assertPivot(t, "no Agg", got, err, k, i64s("x", 1, nil), i64s("y", nil, 5))
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

// TestTransposeAnswersAsPolars: DataFrame.Transpose (step 229) against Polars
// 1.44.1: column j holds row j, every column cast to the type they meet at, text
// beside a number text since step 320; a header column of the names; given column
// names, and names from a column (step 320).
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

	// Text beside a number or a Boolean is text, as Polars casts them; a Boolean
	// beside a number is the number; a List beside one meets nothing.
	mixed, err := ursus.Frame(ursus.Values("a", []int64{1, 3}), ursus.Values("s", []string{"x", "y"}),
		ursus.Values("b", []bool{true, false})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err = mixed.Transpose(ctx, ursus.TransposeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want, err = ursus.Frame(ursus.Values("column_0", []string{"1", "x", "true"}),
		ursus.Values("column_1", []string{"3", "y", "false"})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
	got, err = mixed.Lazy().Select(ursus.Col("b"), ursus.Col("a")).Head(1).Collect(ctx)
	if err == nil {
		got, err = got.Transpose(ctx, ursus.TransposeOptions{})
	}
	if err != nil {
		t.Fatal(err)
	}
	want, err = ursus.Frame(ursus.Values("column_0", []int64{1, 1})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
	listed, err := ursus.Frame(ursus.Values("a", []int64{1})).
		WithColumns(ursus.ConcatList(ursus.Col("a")).Alias("l")).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listed.Transpose(ctx, ursus.TransposeOptions{}); !errors.Is(err, uerr.ErrType) {
		t.Errorf("a List beside an Int64 should be refused: %v", err)
	}

	// Names from a column, which is left out.
	named, err := ursus.Frame(ursus.Values("id", []string{"a", "b", "c"}), ursus.Values("col1", []int64{1, 3, 2}),
		ursus.Values("col2", []int64{3, 4, 6})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err = named.Transpose(ctx, ursus.TransposeOptions{IncludeHeader: true, ColumnNamesFrom: "id"})
	if err != nil {
		t.Fatal(err)
	}
	want, err = ursus.Frame(ursus.Values("column", []string{"col1", "col2"}), ursus.Values("a", []int64{1, 3}),
		ursus.Values("b", []int64{3, 4}), ursus.Values("c", []int64{2, 6})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
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
