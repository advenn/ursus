package ursus_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestExpressionOdds is step 324: Polars' limit, truediv, pipe, arctan2, to_physical,
// reinterpret, item, lower_bound, upper_bound and is_sorted. Every answer is Polars
// 1.44.1's.
func TestExpressionOdds(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	check := func(t *testing.T, lf *ursus.LazyFrame, want *ursus.LazyFrame) {
		t.Helper()
		got, err := lf.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		w, err := want.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, w)
	}
	ints := ursus.Frame(ursus.Values("a", []int64{3, 1, 2}), ursus.Values("b", []int64{2, 2, 2}))

	t.Run("limit, truediv, pipe", func(t *testing.T) {
		check(t, ints.Select(c("a").Limit(2)), ursus.Frame(ursus.Values("a", []int64{3, 1})))
		check(t, ints.Select(c("a").TrueDiv(c("b"))), ursus.Frame(ursus.Values("a", []float64{1.5, 0.5, 1})))
		triple := func(e ursus.Expr) ursus.Expr { return e.Mul(3) }
		check(t, ints.Select(c("a").Pipe(triple)), ursus.Frame(ursus.Values("a", []int64{9, 3, 6})))
	})

	t.Run("arctan2", func(t *testing.T) {
		yx := ursus.Frame(ursus.ValuesNullable("y", []int64{1, 0, 0, 0}, []bool{true, false, true, true}),
			ursus.Values("x", []int64{1, 1, 0, -1}))
		check(t, yx.Select(ursus.Arctan2(c("y"), c("x"))),
			ursus.Frame(ursus.ValuesNullable("y", []float64{math.Pi / 4, 0, 0, math.Pi}, []bool{true, false, true, true})))
		f32 := ursus.Frame(ursus.Values("y", []float32{1}), ursus.Values("x", []float32{1}))
		s, err := f32.Select(ursus.Arctan2(c("y"), c("x"))).CollectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if dt := s.Field(0).Type; dt != ursus.Float32 {
			t.Errorf("arctan2 of two Float32s is %s, want Float32", dt)
		}
	})

	t.Run("to_physical", func(t *testing.T) {
		// A Time's ticks are its unit's, nanoseconds here, Polars' only unit.
		at := time.Date(1970, 1, 1, 0, 0, 1, 0, time.UTC)
		lf := ursus.Frame(ursus.Values("d", []time.Time{at.AddDate(0, 0, 2)}), ursus.Values("t", []time.Time{at}),
			ursus.Values("u", []time.Duration{time.Second}), ursus.Values("e", []string{"b"}),
			ursus.Values("m", []string{"1.5"}), ursus.Values("k", []bool{true}), ursus.Values("s", []string{"x"})).
			Select(c("d").Cast(ursus.Date), c("t").Cast(ursus.Datetime(ursus.Micro, "")),
				c("u").Cast(ursus.Duration(ursus.Micro)), c("t").Cast(ursus.Datetime(ursus.Micro, "")).Dt().Time().Cast(ursus.TimeOf(ursus.Nano)).Alias("h"),
				c("e").Cast(ursus.Enum("a", "b")), c("m").Cast(ursus.Decimal(10, 2)), c("k"), c("s"))
		check(t, lf.Select(ursus.All().ToPhysical()), ursus.Frame(ursus.Values("d", []int32{2}),
			ursus.Values("t", []int64{1_000_000}), ursus.Values("u", []int64{1_000_000}),
			ursus.Values("h", []int64{1_000_000_000}), ursus.Values("e", []uint8{1}),
			ursus.Values("k", []bool{true}), ursus.Values("s", []string{"x"})).
			WithColumns(ursus.Lit(int64(150)).Cast(ursus.Int128).Alias("m")).
			Select(c("d"), c("t"), c("u"), c("h"), c("e"), c("m"), c("k"), c("s")))
	})

	t.Run("reinterpret", func(t *testing.T) {
		lf := ursus.Frame(ursus.Values("u", []uint64{math.MaxUint64, 1}), ursus.Values("i", []int64{-1, 2}),
			ursus.Values("i8", []int8{-1, 2}))
		check(t, lf.Select(c("u").Reinterpret(true), c("i").Reinterpret(false), c("i8").Reinterpret(false)),
			ursus.Frame(ursus.Values("u", []int64{-1, 1}), ursus.Values("i", []uint64{math.MaxUint64, 2}),
				ursus.Values("i8", []uint8{255, 2})))
		if _, err := ursus.Frame(ursus.Values("f", []float64{1.5})).Select(c("f").Reinterpret(true)).Collect(ctx); err == nil ||
			!strings.Contains(err.Error(), "reinterpret") {
			t.Errorf("a float reinterpreted: %v", err)
		}
	})

	t.Run("item", func(t *testing.T) {
		one := ursus.Frame(ursus.Values("a", []int64{7}), ursus.Values("b", []int64{1}))
		check(t, one.Select(c("a").Item(false), c("b")), ursus.Frame(ursus.Values("a", []int64{7}), ursus.Values("b", []int64{1})))
		null := ursus.Frame(ursus.ValuesNullable("a", []int64{0}, []bool{false}))
		check(t, null.Select(c("a").Item(false)), ursus.Frame(ursus.ValuesNullable("a", []int64{0}, []bool{false})))
		empty := ints.Head(0)
		check(t, empty.Select(c("a").Item(true)), ursus.Frame(ursus.ValuesNullable("a", []int64{0}, []bool{false})))
		for name, tc := range map[string]struct {
			lf   *ursus.LazyFrame
			want string
		}{
			"three values": {ints.Select(c("a").Item(false)), "expected a single value, got 3 values"},
			"none":         {empty.Select(c("a").Item(false)), "expected a single value, got none"},
			"a group of two": {ursus.Frame(ursus.Values("g", []int64{1, 1, 2}), ursus.Values("v", []int64{1, 2, 3})).
				GroupBy(c("g")).Agg(c("v").Item(false)), "got 2 values"},
		} {
			if _, err := tc.lf.Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want an error saying %q", name, err, tc.want)
			}
		}
	})

	t.Run("bounds", func(t *testing.T) {
		lf := ursus.Frame(ursus.Values("i8", []int8{1}), ursus.Values("u", []uint64{1}), ursus.Values("f", []float32{1}),
			ursus.Values("g", []float64{1}), ursus.Values("d", []int32{1}), ursus.Values("t", []int64{1})).
			WithColumns(c("d").Cast(ursus.Date), c("t").Cast(ursus.Datetime(ursus.Micro, "")))
		check(t, lf.Select(c("i8").LowerBound(), c("u").LowerBound(), c("f").LowerBound(), c("g").LowerBound(),
			c("d").LowerBound(), c("t").LowerBound()),
			ursus.Frame(ursus.Values("i8", []int8{math.MinInt8}), ursus.Values("u", []uint64{0}),
				ursus.Values("f", []float32{float32(math.Inf(-1))}), ursus.Values("g", []float64{math.Inf(-1)}),
				ursus.Values("d", []int32{math.MinInt32}), ursus.Values("t", []int64{math.MinInt64})))
		check(t, lf.Select(c("i8").UpperBound(), c("u").UpperBound(), c("f").UpperBound(), c("g").UpperBound(),
			c("d").UpperBound(), c("t").UpperBound()),
			ursus.Frame(ursus.Values("i8", []int8{math.MaxInt8}), ursus.Values("u", []uint64{math.MaxUint64}),
				ursus.Values("f", []float32{float32(math.Inf(1))}), ursus.Values("g", []float64{math.Inf(1)}),
				ursus.Values("d", []int32{math.MaxInt32}), ursus.Values("t", []int64{math.MaxInt64})))
		// A scalar, broadcast beside a column; and one a group.
		two := ursus.Frame(ursus.Values("a", []int16{1, 2}))
		check(t, two.Select(c("a").UpperBound().Alias("hi"), c("a")),
			ursus.Frame(ursus.Values("hi", []int16{32767, 32767}), ursus.Values("a", []int16{1, 2})))
		grouped := ursus.Frame(ursus.Values("g", []int64{1, 1, 2}), ursus.Values("a", []int64{1, 2, 3})).
			GroupBy(c("g")).MaintainOrder().Agg(c("a").LowerBound())
		check(t, grouped, ursus.Frame(ursus.Values("g", []int64{1, 2}), ursus.Values("a", []int64{math.MinInt64, math.MinInt64})))
		for _, col := range []*ursus.Column{ursus.Values("b", []bool{true}), ursus.Values("s", []string{"a"})} {
			if _, err := ursus.Frame(col).Select(c(col.Name()).LowerBound()).Collect(ctx); err == nil ||
				!strings.Contains(err.Error(), "bound") {
				t.Errorf("the bound of a %s: %v", col.DType(), err)
			}
		}
	})

	t.Run("is_sorted", func(t *testing.T) {
		null := func(vals []int64, ok ...bool) *ursus.Column { return ursus.ValuesNullable("a", vals, ok) }
		all := []bool{true, true, true, true}
		for _, tc := range []struct {
			name string
			col  *ursus.Column
			want [4]bool // ascending, descending, ascending nulls last, descending nulls last
		}{
			{"1 2 2 3", null([]int64{1, 2, 2, 3}, all...), [4]bool{true, false, true, false}},
			{"3 2 1", null([]int64{3, 2, 1}, all[:3]...), [4]bool{false, true, false, true}},
			{"null 1 2", null([]int64{0, 1, 2}, false, true, true), [4]bool{true, false, false, false}},
			{"1 2 null", null([]int64{1, 2, 0}, true, true, false), [4]bool{false, false, true, false}},
			{"null null", null([]int64{0, 0}, false, false), [4]bool{true, true, true, true}},
			{"none", null([]int64{}), [4]bool{true, true, true, true}},
			{"1.0 NaN", ursus.Values("a", []float64{1, math.NaN()}), [4]bool{true, false, true, false}},
			{"NaN 1.0", ursus.Values("a", []float64{math.NaN(), 1}), [4]bool{false, true, false, true}},
			{"a b B", ursus.Values("a", []string{"a", "b", "B"}), [4]bool{false, false, false, false}},
		} {
			lf := ursus.Frame(tc.col)
			got, err := lf.Select(c("a").IsSorted(false, false).Alias("asc"), c("a").IsSorted(true, false).Alias("desc"),
				c("a").IsSorted(false, true).Alias("asc_last"), c("a").IsSorted(true, true).Alias("desc_last")).Collect(ctx)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			for i, name := range []string{"asc", "desc", "asc_last", "desc_last"} {
				v, _, err := got.At[bool](0, name)
				if err != nil {
					t.Fatal(err)
				}
				if v != tc.want[i] {
					t.Errorf("%s: %s is %v, want %v", tc.name, name, v, tc.want[i])
				}
			}
		}
		// Across batches, and a group at a time.
		long := make([]int64, 1000)
		for i := range long {
			long[i] = int64(i / 3)
		}
		got, err := ursus.Frame(ursus.Values("a", long)).Select(c("a").IsSorted(false, false)).
			Collect(ctx, ursus.WithBatchSize(7))
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _ := got.At[bool](0, "a"); !v {
			t.Error("a sorted column read 7 rows at a time is not sorted")
		}
		long[500] = -1
		got, err = ursus.Frame(ursus.Values("a", long)).Select(c("a").IsSorted(false, false)).
			Collect(ctx, ursus.WithBatchSize(7))
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _ := got.At[bool](0, "a"); v {
			t.Error("a column with one value out of place, read 7 rows at a time, is sorted")
		}
		check(t, ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2}), ursus.Values("a", []int64{1, 2, 4, 3})).
			GroupBy(c("g")).MaintainOrder().Agg(c("a").IsSorted(false, false)),
			ursus.Frame(ursus.Values("g", []int64{1, 2}), ursus.Values("a", []bool{true, false})))
	})
}
