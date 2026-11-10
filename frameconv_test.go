package ursus_test

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestFrameConveniences is step 327: Polars' DataFrame corr, gather, row, item,
// get_column_index, replace_column, drop_in_place, the horizontal reductions, fold,
// to_series and to_struct, and a LazyFrame's columns, dtypes and width. The frames
// corr, gather and the reductions answer are the frameconv group's, Polars 2.1.0's
// (oracle_frameconv_test.go); here stay the refusals, the long parallel corr and
// gather, differentials, and Go's own accessors.
func TestFrameConveniences(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	df := dfOf(t, ursus.Values("a", []int64{1, 2, 3, 4}), ursus.Values("b", []float64{2, 4, 5, 9}),
		ursus.Values("s", []string{"x", "y", "z", "w"}))
	refused := func(t *testing.T, err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v, want an error saying %q", err, want)
		}
	}

	t.Run("corr", func(t *testing.T) {
		_, err := df.Corr(ctx)
		refused(t, err, `"s"`)

		// A Bool is its trues, as NumPy reads it; and a frame of three spans, summed
		// in parallel, agrees with Corr of two expressions, which skips no row here.
		n := 140_000
		x, y, z := make([]float64, n), make([]float64, n), make([]bool, n)
		for i := range n {
			x[i], y[i], z[i] = math.Sin(float64(i)), math.Cos(float64(i)*0.7)+0.3*math.Sin(float64(i)), i%3 == 0
		}
		long := dfOf(t, ursus.Values("x", x), ursus.Values("y", y), ursus.Values("z", z))
		got, err := long.Corr(ctx)
		if err != nil {
			t.Fatal(err)
		}
		pairs, err := long.Select(ctx, ursus.Corr(c("x"), c("y")).Alias("xy"),
			ursus.Corr(c("x"), c("z").Cast(ursus.Float64)).Alias("xz"),
			ursus.Corr(c("y"), c("z").Cast(ursus.Float64)).Alias("yz"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []struct {
			row  int
			col  string
			pair string
		}{{1, "x", "xy"}, {0, "y", "xy"}, {2, "x", "xz"}, {2, "y", "yz"}, {1, "z", "yz"}} {
			v, _, _ := got.At[float64](p.row, p.col)
			w, _, _ := pairs.At[float64](0, p.pair)
			if math.Abs(v-w) > 1e-12 || v == 0 {
				t.Errorf("corr row %d of %s: %v, want %v", p.row, p.col, v, w)
			}
		}
	})

	t.Run("gather", func(t *testing.T) {
		_, err := df.Gather(ctx, []int{9}, false)
		refused(t, err, "out of bounds")

		// A long gather, taken in chunks, every chunk of every column in parallel, and
		// each column's chunks joined in order.
		n := 150_000
		ints, texts, idx := make([]int64, n), make([]string, n), make([]int, n)
		back, backText := make([]int64, n), make([]string, n)
		for i := range n {
			ints[i], texts[i], idx[i] = int64(i), strconv.Itoa(i), n-1-i
			back[n-1-i], backText[n-1-i] = int64(i), strconv.Itoa(i)
		}
		long := dfOf(t, ursus.Values("i", ints), ursus.Values("t", texts), ursus.Values("j", ints))
		got, err := long.Gather(ctx, idx, false)
		if err != nil {
			t.Fatal(err)
		}
		// By the values, which AssertFrameEqual would render a cell at a time.
		for _, name := range []string{"i", "j"} {
			s, err := got.Column[int64](name)
			if err != nil || s.Len() != n || s.NullCount() != 0 || !slices.Equal(s.Values(), back) {
				t.Errorf("the long gather's %s: %v", name, err)
			}
		}
		if s, err := got.Column[string]("t"); err != nil || s.Len() != n || !slices.Equal(s.Values(), backText) {
			t.Errorf("the long gather's text: %v", err)
		}
		if strings.Join(got.Columns(), " ") != "i t j" {
			t.Errorf("the long gather's columns: %v", got.Columns())
		}
	})

	t.Run("row and item", func(t *testing.T) {
		row, err := df.Row(1)
		if err != nil || !slices.Equal(row, []any{int64(2), 4.0, "y"}) {
			t.Errorf("row 1: %v, %v", row, err)
		}
		if row, err := df.Row(-1); err != nil || !slices.Equal(row, []any{int64(4), 9.0, "w"}) {
			t.Errorf("the last row: %v, %v", row, err)
		}
		named, err := df.RowNamed(1)
		if err != nil || named["a"] != int64(2) || named["b"] != 4.0 || named["s"] != "y" || len(named) != 3 {
			t.Errorf("row 1, named: %v, %v", named, err)
		}
		_, err = df.Row(9)
		refused(t, err, "out of bounds")
		if row, err := df.RowWhere(ctx, c("s").Eq("z")); err != nil || !slices.Equal(row, []any{int64(3), 5.0, "z"}) {
			t.Errorf("the row where s is z: %v, %v", row, err)
		}
		_, err = df.RowWhere(ctx, c("a").Gt(1))
		refused(t, err, "returned 3 rows")
		_, err = df.RowWhere(ctx, c("a").Gt(9))
		refused(t, err, "returned no rows")

		sum, err := df.Select(ctx, c("a").Sum().Cast(ursus.Int64))
		if err != nil {
			t.Fatal(err)
		}
		if v, err := sum.Item(); err != nil || v != int64(10) {
			t.Errorf("the item: %v, %v", v, err)
		}
		_, err = df.Item()
		refused(t, err, "single element")
		if v, err := df.ItemAt(1, "b"); err != nil || v != 4.0 {
			t.Errorf("item at 1, b: %v, %v", v, err)
		}
		if v, err := df.ItemAt(-1, "a"); err != nil || v != int64(4) {
			t.Errorf("item at -1, a: %v, %v", v, err)
		}
	})

	t.Run("columns", func(t *testing.T) {
		if i, err := df.GetColumnIndex("b"); err != nil || i != 1 {
			t.Errorf("b's index: %d, %v", i, err)
		}
		_, err := df.GetColumnIndex("q")
		refused(t, err, `"q"`)
		got, err := df.ReplaceColumn(0, ursus.Values("z", []int64{9, 9, 9, 9}))
		if err != nil || strings.Join(got.Columns(), " ") != "z b s" {
			t.Errorf("replaced at 0: %v, %v", got, err)
		}
		got, err = df.ReplaceColumn(-1, ursus.Values("q", []int64{1, 1, 1, 1}))
		if err != nil || strings.Join(got.Columns(), " ") != "a b q" {
			t.Errorf("replaced at -1: %v, %v", got, err)
		}
		_, err = df.ReplaceColumn(0, ursus.Values("z", []int64{1}))
		refused(t, err, "doesn't match")
		col, rest, err := df.DropInPlace("b")
		if err != nil || col.Name() != "b" || col.Len() != 4 || strings.Join(rest.Columns(), " ") != "a s" ||
			rest.ColumnAt(1).Name() != "s" {
			t.Errorf("b dropped: %v, %v, %v", col, rest, err)
		}
		_, _, err = df.DropInPlace("q")
		refused(t, err, `"q"`)
		if s, err := df.ToSeries(0); err != nil || s.Name() != "a" {
			t.Errorf("the first series: %v, %v", s, err)
		}
		if s, err := df.ToSeries(-1); err != nil || s.Name() != "s" {
			t.Errorf("the last series: %v, %v", s, err)
		}
		_, err = df.ToSeries(5)
		refused(t, err, "out of bounds")
		st := df.ToStruct("")
		if st.Name() != "" || st.Len() != 4 || st.NullCount() != 0 || st.DType() != ursus.StructOf(
			ursus.Field{Name: "a", Type: ursus.Int64, Nullable: true}, ursus.Field{Name: "b", Type: ursus.Float64, Nullable: true},
			ursus.Field{Name: "s", Type: ursus.String, Nullable: true}) {
			t.Errorf("the frame as a struct: %v, %s", st, st.DType())
		}
	})

	t.Run("a lazy frame's schema", func(t *testing.T) {
		lf := df.Lazy()
		cols, err := lf.Columns(ctx)
		if err != nil || strings.Join(cols, " ") != "a b s" {
			t.Errorf("columns: %v, %v", cols, err)
		}
		types, err := lf.DTypes(ctx)
		if err != nil || !slices.Equal(types, []ursus.DataType{ursus.Int64, ursus.Float64, ursus.String}) {
			t.Errorf("dtypes: %v, %v", types, err)
		}
		if w, err := lf.Width(ctx); err != nil || w != 3 {
			t.Errorf("width: %d, %v", w, err)
		}
	})
}
