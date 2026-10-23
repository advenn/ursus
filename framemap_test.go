package ursus_test

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestFrameMaps is step 328: Polars' map_rows, map_columns, show and cache. Every
// answer is Polars 1.44.1's.
func TestFrameMaps(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	df := dfOf(t, ursus.Values("a", []int64{1, 2, 3}), ursus.Values("b", []float64{2, 4, 5}),
		ursus.ValuesNullable("s", []string{"x", "y", ""}, []bool{true, true, false}))
	refused := func(t *testing.T, err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v, want an error saying %q", err, want)
		}
	}

	t.Run("map_rows", func(t *testing.T) {
		type in struct {
			A int64
			B float64
			S *string
		}
		type pair struct {
			C0 int64   `ursus:"column_0"`
			C1 float64 `ursus:"column_1"`
		}
		// A struct's fields are columns, as Polars' tuple's are.
		got, err := df.MapRows(ctx, func(r in) (pair, error) { return pair{r.A * 2, r.B + 1}, nil })
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("column_0", []int64{2, 4, 6}),
			ursus.Values("column_1", []float64{3, 5, 6})))
		// Anything else is one column, "map".
		got, err = df.MapRows(ctx, func(r in) (int64, error) { return r.A * 10, nil })
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("map", []int64{10, 20, 30})))
		got, err = df.MapRows(ctx, func(r in) (*string, error) { return r.S, nil })
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, strs("map", "x", "y", nil)))
		// A row as []any, in the columns' order, as Row gives it.
		got, err = df.MapRows(ctx, func(r []any) (string, error) { return fmt.Sprintf("%v %v %v", r...), nil })
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("map", []string{"1 2 x", "2 4 y", "3 5 <nil>"})))
		// No rows: a frame of none, typed by R, where Polars cannot say.
		empty, err := df.Head(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err = empty.MapRows(ctx, func(r in) (float64, error) { return r.B, nil })
		if err != nil || got.Height() != 0 || got.Schema().Field(0).Type != ursus.Float64 {
			t.Errorf("no rows: %v, %v", got, err)
		}
		bad := errors.New("bad row")
		_, err = df.MapRows(ctx, func(r in) (int64, error) {
			if r.A == 2 {
				return 0, bad
			}
			return r.A, nil
		})
		if !errors.Is(err, bad) || !strings.Contains(err.Error(), "row 1") {
			t.Errorf("a failed row: %v", err)
		}
		_, err = df.MapRows(ctx, func(r in) (any, error) { return r.A, nil })
		refused(t, err, "concrete")
		_, err = df.MapRows(ctx, func(r int64) (int64, error) { return r, nil })
		refused(t, err, "a struct or a []any")
		_, err = df.MapRows(ctx, func(r time.Time) (int64, error) { return 0, nil })
		refused(t, err, "a struct or a []any")
	})

	t.Run("map_columns", func(t *testing.T) {
		times10 := func(col *ursus.Column) (*ursus.Column, error) {
			out, err := dfOf(t, col).Select(ctx, c(col.Name()).Mul(10))
			if err != nil {
				return nil, err
			}
			return out.ColumnAt(0), nil
		}
		got, err := df.MapColumns(ctx, []string{"a"}, times10)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("a", []int64{10, 20, 30}), ursus.Values("b", []float64{2, 4, 5}),
			ursus.ValuesNullable("s", []string{"x", "y", ""}, []bool{true, true, false})))
		// A renamed answer keeps its column's name and place; two columns, each.
		got, err = df.MapColumns(ctx, []string{"b", "a"}, func(col *ursus.Column) (*ursus.Column, error) {
			out, err := dfOf(t, col).Select(ctx, c(col.Name()).Cast(ursus.Float32).Alias("z"))
			if err != nil {
				return nil, err
			}
			return out.ColumnAt(0), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if s := got.Schema(); strings.Join(got.Columns(), " ") != "a b s" || s.Field(0).Type != ursus.Float32 ||
			s.Field(1).Type != ursus.Float32 {
			t.Errorf("two columns cast: %s", s)
		}
		// One value is every row's, as Polars broadcasts a sum.
		got, err = df.MapColumns(ctx, []string{"a"}, func(col *ursus.Column) (*ursus.Column, error) {
			out, err := dfOf(t, col).Select(ctx, c("a").Sum().Cast(ursus.Int64))
			if err != nil {
				return nil, err
			}
			return out.ColumnAt(0), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if s, err := got.Column[int64]("a"); err != nil || fmt.Sprint(s.Values()) != "[6 6 6]" {
			t.Errorf("a sum broadcast: %v, %v", s, err)
		}
		_, err = df.MapColumns(ctx, []string{"a"}, func(col *ursus.Column) (*ursus.Column, error) {
			return ursus.Values("a", []int64{1, 2}), nil
		})
		refused(t, err, "2 rows")
		_, err = df.MapColumns(ctx, []string{"q"}, times10)
		refused(t, err, `"q"`)
	})

	t.Run("show", func(t *testing.T) {
		head, err := df.Head(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if got := df.Show(2); got != head.String() {
			t.Errorf("show 2:\n%s\nwant:\n%s", got, head.String())
		}
		if got := df.Show(-1); got != head.String() {
			t.Errorf("show -1:\n%s\nwant:\n%s", got, head.String())
		}
		if got, err := df.Lazy().Show(ctx, 2); err != nil || got != head.String() {
			t.Errorf("a query's show 2: %v\n%s", err, got)
		}
		// Every row asked for is drawn, where String draws ten.
		long := make([]int64, 30)
		for i := range long {
			long[i] = int64(i)
		}
		thirty := dfOf(t, ursus.Values("i", long))
		if got := thirty.Show(20); strings.Count(got, "\n") != 20+6 || strings.Contains(got, "more rows") ||
			!strings.Contains(got, "shape: (20, 1)") || !strings.Contains(got, "│ 19 ") {
			t.Errorf("show 20 of 30:\n%s", got)
		}
		if got := thirty.Show(100); !strings.Contains(got, "shape: (30, 1)") || !strings.Contains(got, "│ 29 ") {
			t.Errorf("show 100 of 30:\n%s", got)
		}
		if got := thirty.Show(-40); !strings.Contains(got, "shape: (0, 1)") {
			t.Errorf("show -40 of 30:\n%s", got)
		}
	})

	t.Run("cache", func(t *testing.T) {
		var calls atomic.Int64
		counted := ursus.Frame(ursus.Values("a", []int64{1, 2, 3})).Select(
			c("a").MapBatches[int64, int64]("count", ursus.Int64, func(v []int64, ok []bool) ([]int64, []bool, error) {
				calls.Add(1)
				return append([]int64(nil), v...), ok, nil
			}))
		both := func(lf *ursus.LazyFrame) *ursus.LazyFrame { return ursus.Concat([]*ursus.LazyFrame{lf, lf}) }
		want := dfOf(t, ursus.Values("a", []int64{1, 2, 3, 1, 2, 3}))
		for _, tc := range []struct {
			name  string
			lf    *ursus.LazyFrame
			calls int64
		}{{"uncached, a Select runs twice", both(counted), 2}, {"cached, once", both(counted.Cache()), 1}} {
			calls.Store(0)
			got, err := tc.lf.Collect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, want)
			if calls.Load() != tc.calls {
				t.Errorf("%s: the function ran %d times", tc.name, calls.Load())
			}
		}
		plan, err := both(counted.Cache()).Explain(ctx)
		if err != nil || strings.Count(plan, "CACHE #1") != 2 {
			t.Errorf("a cached query used twice:\n%s", plan)
		}
		// Used once, at the root or under another step, it is no cache, as Polars drops
		// one.
		for _, once := range []*ursus.LazyFrame{counted.Cache().Cache(), counted.Cache().Select(c("a").Mul(2))} {
			plan, err = once.Explain(ctx)
			if err != nil || strings.Contains(plan, "CACHE") {
				t.Errorf("a cached query used once:\n%s", plan)
			}
		}
		// Asked for, it runs once with sharing off, a query as written; and two queries
		// cached, each used twice, are two caches, each its own rows.
		other := ursus.Frame(ursus.Values("a", []int64{7, 8}))
		for _, flags := range []ursus.OptFlags{ursus.DefaultOptFlags(), ursus.NoOptFlags()} {
			calls.Store(0)
			a, b := counted.Cache(), other.Cache()
			got, err := ursus.Concat([]*ursus.LazyFrame{a, b, a, b}).Collect(ctx, ursus.WithOptFlags(flags))
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("a", []int64{1, 2, 3, 7, 8, 1, 2, 3, 7, 8})))
			if calls.Load() != 1 {
				t.Errorf("two caches, flags %+v: the function ran %d times", flags, calls.Load())
			}
		}
	})
}
