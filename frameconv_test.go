package ursus_test

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFrameConveniences is step 327: Polars' DataFrame corr, gather, row, item,
// get_column_index, replace_column, drop_in_place, the horizontal reductions, fold,
// to_series and to_struct, and a LazyFrame's columns, dtypes and width. Every answer
// is Polars 1.44.1's.
func TestFrameConveniences(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	df := dfOf(t, ursus.Values("a", []int64{1, 2, 3, 4}), ursus.Values("b", []float64{2, 4, 5, 9}),
		ursus.Values("s", []string{"x", "y", "z", "w"}))
	num := dfOf(t, ursus.Values("a", []int64{1, 2, 3, 4}), ursus.Values("b", []float64{2, 4, 5, 9}))
	refused := func(t *testing.T, err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v, want an error saying %q", err, want)
		}
	}

	t.Run("corr", func(t *testing.T) {
		got, err := num.Corr(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// NumPy's own, to the bit, its sums here exact: divided by the row's deviation
		// and then the column's, so the two sides of the diagonal differ.
		exact := func(t *testing.T, got *ursus.DataFrame, names []string, want [][]float64) {
			t.Helper()
			for j, name := range names {
				for i := range want {
					v, _, err := got.At[float64](i, name)
					if err != nil {
						t.Fatal(err)
					}
					if v != want[i][j] {
						t.Errorf("corr row %d of %s: %v, want %v", i, name, v, want[i][j])
					}
				}
			}
		}
		exact(t, got, []string{"a", "b"}, [][]float64{{1, 0.9647638212377321}, {0.9647638212377322, 1}})
		ten, err := dfOf(t, ursus.Values("x", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}),
			ursus.Values("y", []float64{2, 3, 1, 5, 4, 8, 7, 6, 10, 9})).Corr(ctx)
		if err != nil {
			t.Fatal(err)
		}
		exact(t, ten, []string{"x", "y"}, [][]float64{{0.9999999999999999, 0.8909090909090909},
			{0.8909090909090909, 0.9999999999999999}})
		withNull, err := dfOf(t, ursus.ValuesNullable("a", []int64{1, 2, 0, 4}, []bool{true, true, false, true}),
			ursus.Values("b", []float64{2, 4, 5, 9}), ursus.Values("c", []float64{1, 1, 1, 1})).Corr(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, cell := range []struct {
			row  int
			col  string
			want float64
		}{{0, "a", math.NaN()}, {1, "a", math.NaN()}, {1, "b", 1}, {2, "c", math.NaN()}, {1, "c", math.NaN()}} {
			v, _, err := withNull.At[float64](cell.row, cell.col)
			if err != nil {
				t.Fatal(err)
			}
			if !(math.IsNaN(cell.want) && math.IsNaN(v)) && v != cell.want {
				t.Errorf("corr row %d of %s: %v, want %v", cell.row, cell.col, v, cell.want)
			}
		}
		_, err = df.Corr(ctx)
		refused(t, err, `"s"`)

		// No rows, or one, is NaN throughout, as NumPy's is.
		for _, rows := range []int{0, 1} {
			few, err := num.Head(ctx, rows)
			if err != nil {
				t.Fatal(err)
			}
			got, err := few.Corr(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"a", "b"} {
				for i := range 2 {
					if v, _, _ := got.At[float64](i, name); !math.IsNaN(v) {
						t.Errorf("%d rows: corr row %d of %s is %v", rows, i, name, v)
					}
				}
			}
		}

		// Clipped as NumPy clips: a's own correlation comes to 1.0000000000000002.
		clip, err := dfOf(t, ursus.Values("a", []float64{0.1, 0.3, 5.6}), ursus.Values("b", []float64{1, 2, 4})).Corr(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _ := clip.At[float64](0, "a"); v != 1 {
			t.Errorf("a's own correlation is %v, want 1", v)
		}
		if v, _, _ := clip.At[float64](1, "a"); math.Abs(v-0.954919108405064) > 1e-15 {
			t.Errorf("a with b: %v, want 0.954919108405064", v)
		}

		// A Decimal is read, which Polars' to_numpy cannot hand NumPy; the answer is
		// the floats'.
		dec, err := ursus.Frame(ursus.Values("a", []float64{1, 2, 4}), ursus.Values("b", []int64{1, 2, 5})).
			Select(c("a").Cast(ursus.Decimal(10, 2)), c("b")).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := dec.Corr(ctx); err != nil {
			t.Error(err)
		} else if v, _, _ := got.At[float64](1, "a"); math.Abs(v-0.9958705948858223) > 1e-15 {
			t.Errorf("a Decimal with b: %v, want 0.9958705948858223", v)
		}

		// A Bool is its trues, as NumPy reads it; and a frame of three spans, summed
		// in parallel, agrees with Corr of two expressions, which skips no row here.
		n := 140_000
		x, y, z := make([]float64, n), make([]float64, n), make([]bool, n)
		for i := range n {
			x[i], y[i], z[i] = math.Sin(float64(i)), math.Cos(float64(i)*0.7)+0.3*math.Sin(float64(i)), i%3 == 0
		}
		long := dfOf(t, ursus.Values("x", x), ursus.Values("y", y), ursus.Values("z", z))
		got, err = long.Corr(ctx)
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
		got, err := df.Gather(ctx, []int{2, 0, -1}, false)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("a", []int64{3, 1, 4}),
			ursus.Values("b", []float64{5, 2, 9}), ursus.Values("s", []string{"z", "x", "w"})))
		_, err = df.Gather(ctx, []int{9}, false)
		refused(t, err, "out of bounds")
		got, err = df.Gather(ctx, []int{9, 1}, true)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.ValuesNullable("a", []int64{0, 2}, []bool{false, true}),
			ursus.ValuesNullable("b", []float64{0, 4}, []bool{false, true}),
			ursus.ValuesNullable("s", []string{"", "y"}, []bool{false, true})))

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
		got, err = long.Gather(ctx, idx, false)
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

	t.Run("horizontal", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			got  func() (*ursus.Column, error)
			want *ursus.Column
		}{
			{"sum", func() (*ursus.Column, error) { return num.SumHorizontal(ctx, true) },
				ursus.Values("sum", []float64{3, 6, 8, 13})},
			{"min", func() (*ursus.Column, error) { return num.MinHorizontal(ctx) },
				ursus.Values("min", []float64{1, 2, 3, 4})},
			{"max", func() (*ursus.Column, error) { return num.MaxHorizontal(ctx) },
				ursus.Values("max", []float64{2, 4, 5, 9})},
			// A null skipped, and with ignoreNulls false a row holding one null.
			{"mean", func() (*ursus.Column, error) {
				return dfOf(t, f64s("a", 1.0, nil), f64s("b", 3.0, nil)).MeanHorizontal(ctx, true)
			}, f64s("mean", 2.0, nil)},
			{"sum of a null, not ignored", func() (*ursus.Column, error) {
				return dfOf(t, f64s("a", 1.0, 1.0), f64s("b", 2.0, nil)).SumHorizontal(ctx, false)
			}, f64s("sum", 3.0, nil)},
			{"fold", func() (*ursus.Column, error) {
				return num.Fold(ctx, func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) })
			}, ursus.Values("a", []float64{3, 6, 8, 13})},
		} {
			col, err := tc.got()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			ursustest.AssertFrameEqual(t, dfOf(t, col), dfOf(t, tc.want))
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
