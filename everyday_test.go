package ursus_test

// Step 147: Cut, QCut, ValueCounts and Describe, composed from what exists.

import (
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/advenn/ursus"
)

func TestCut(t *testing.T) {
	c := ursus.Col
	ages := ursus.Frame(
		ursus.ValuesNullable("age", []int64{1, 18, 19, 65, 66, 0}, []bool{true, true, true, true, true, false}),
		ursus.Values("x", []float64{2.5, math.NaN(), math.Inf(1), math.Inf(-1), 0, 3}),
	)
	breaks := []float64{18, 65}
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		col  string
		want []string
	}{
		// Closed on the right, as Polars' default: 18 is in (-inf, 18].
		{"right closed", c("age").Cut(breaks), "age",
			[]string{"(-inf, 18]", "(-inf, 18]", "(18, 65]", "(18, 65]", "(65, inf]", "null"}},
		{"left closed", c("age").Cut(breaks, ursus.CutLeftClosed()), "age",
			[]string{"[-inf, 18)", "[18, 65)", "[18, 65)", "[65, inf)", "[65, inf)", "null"}},
		{"labelled", c("age").Cut(breaks, ursus.CutLabels("minor", "adult", "senior")), "age",
			[]string{"minor", "minor", "adult", "adult", "senior", "null"}},
		// NaN has no bin; the infinities have the outer ones.
		{"floats", c("x").Cut([]float64{0, 2.5}), "x",
			[]string{"(0, 2.5]", "null", "(2.5, inf]", "(-inf, 0]", "(-inf, 0]", "(2.5, inf]"}},
		{"no breaks", c("x").Cut(nil), "x",
			[]string{"(-inf, inf]", "null", "(-inf, inf]", "(-inf, inf]", "(-inf, inf]", "(-inf, inf]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df := collect(t, ages.Select(tc.e))
			if got := texts(t, df, tc.col); !slices.Equal(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}

	t.Run("over a selection of several columns", func(t *testing.T) {
		df := collect(t, ages.Select(c("age", "x").Cut([]float64{20})))
		if got := df.Columns(); !slices.Equal(got, []string{"age", "x"}) {
			t.Errorf("columns %v", got)
		}
	})

	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"breaks that do not increase", c("age").Cut([]float64{65, 18}), "must increase"},
		{"a NaN break", c("age").Cut([]float64{math.NaN()}), "finite"},
		{"labels of the wrong count", c("age").Cut(breaks, ursus.CutLabels("a", "b")), "3 bins"},
		{"a String", ursus.Lit("a").Cut(breaks), "requires a numeric operand"},
	} {
		t.Run(tc.name, func(t *testing.T) { assertUserError(t, ages.Select(tc.e), tc.want) })
	}
}

func TestQCut(t *testing.T) {
	c := ursus.Col
	one := ursus.Frame(ursus.Values("v", []int64{5, 1, 8, 3, 2, 7, 4, 6}))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want []string
	}{
		// The median of 1..8 is 4.5.
		{"the median", c("v").QCut([]float64{0.5}),
			[]string{"(4.5, inf]", "(-inf, 4.5]", "(4.5, inf]", "(-inf, 4.5]", "(-inf, 4.5]", "(4.5, inf]", "(-inf, 4.5]", "(4.5, inf]"}},
		// Quartiles, linear: 2.75, 4.5 and 6.25.
		{"quartiles", c("v").QCutN(4),
			[]string{"(4.5, 6.25]", "(-inf, 2.75]", "(6.25, inf]", "(2.75, 4.5]", "(-inf, 2.75]", "(6.25, inf]", "(2.75, 4.5]", "(4.5, 6.25]"}},
		{"labelled", c("v").QCutN(2, ursus.CutLabels("low", "high")),
			[]string{"high", "low", "high", "low", "low", "high", "low", "high"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := texts(t, collect(t, one.Select(tc.e)), "v"); !slices.Equal(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}

	t.Run("equal quantiles leave an empty bin", func(t *testing.T) {
		df := collect(t, ursus.Frame(ursus.Values("v", []int64{1, 1, 1, 1, 2})).
			Select(c("v").QCut([]float64{0.25, 0.5})))
		want := []string{"(-inf, 1]", "(-inf, 1]", "(-inf, 1]", "(-inf, 1]", "(1, inf]"}
		if got := texts(t, df, "v"); !slices.Equal(got, want) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("a column of nulls", func(t *testing.T) {
		df := collect(t, ursus.Frame(ursus.ValuesNullable("v", []float64{0, 0}, []bool{false, false})).
			Select(c("v").QCutN(2)))
		if got := texts(t, df, "v"); !slices.Equal(got, []string{"null", "null"}) {
			t.Errorf("got %q", got)
		}
		// An aggregate over a group of nulls, binned after the aggregation.
		df = collect(t, ursus.Frame(ursus.Values("g", []string{"a", "a", "b"}),
			ursus.ValuesNullable("v", []int64{0, 0, 2}, []bool{false, false, true})).
			GroupBy(c("g")).Agg(c("v").Min().Cut([]float64{1}).Alias("bin"), c("v").Min().BitwiseCountOnes().Alias("ones")).
			Sort(ursus.Asc(c("g"))))
		if got := texts(t, df, "bin"); !slices.Equal(got, []string{"null", "(1, inf]"}) {
			t.Errorf("an aggregate of nulls: %q", got)
		}
		// A typed null literal has no values to read at all.
		df = collect(t, one.Select(ursus.Null(ursus.Float64).Alias("v").Cut([]float64{1})))
		if got := texts(t, df, "v"); !slices.Equal(got, slices.Repeat([]string{"null"}, 8)) {
			t.Errorf("a null literal: %q", got)
		}
	})

	t.Run("the quantiles are the whole frame's, across batches", func(t *testing.T) {
		vals := make([]int64, 10_000)
		for i := range vals {
			vals[i] = int64((i * 7919) % 10_000) // 0..9999, shuffled
		}
		df, err := ursus.Frame(ursus.Values("v", vals)).
			WithColumns(c("v").QCutN(2, ursus.CutLabels("low", "high")).Alias("half")).
			ValueCounts(c("half")).Sort(ursus.Asc(c("half"))).
			Collect(t.Context(), ursus.WithBatchSize(512))
		if err != nil {
			t.Fatal(err)
		}
		n, _ := df.Column[uint64]("count")
		for i, half := range texts(t, df, "half") {
			if v, _ := n.Get(i); v != 5_000 {
				t.Errorf("%s holds %d rows, want 5000", half, v)
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		assertUserError(t, one.Select(c("v").QCut([]float64{0.5, 0.25})), "must increase")
		assertUserError(t, one.Select(c("v").QCut([]float64{1.5})), "between 0 and 1")
		assertUserError(t, one.Select(c("v").QCutN(0)), "at least one bin")
	})
}

func TestValueCounts(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(
		ursus.ValuesNullable("city", []string{"b", "a", "b", "c", "a", "b", ""},
			[]bool{true, true, true, true, true, true, false}),
		ursus.Values("year", []int64{1, 1, 2, 1, 1, 2, 1}),
	)
	df := collect(t, lf.ValueCounts(c("city")))
	// c and the null tie at 1, in the order they first appear.
	if got := texts(t, df, "city"); !slices.Equal(got, []string{"b", "a", "c", "null"}) {
		t.Errorf("cities %q", got)
	}
	counts, err := df.Column[uint64]("count")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []uint64{3, 2, 1, 1} {
		if v, _ := counts.Get(i); v != want {
			t.Errorf("row %d: %d, want %d", i, v, want)
		}
	}

	t.Run("ties in first appearance, across batches", func(t *testing.T) {
		// 2,000 keys seen once each, in a shuffled order, over batches the group-by
		// folds in parallel: without MaintainOrder, its groups come out in no
		// promised order.
		keys := make([]int64, 2_000)
		for i := range keys {
			keys[i] = int64((i * 7919) % 2_000)
		}
		df, err := ursus.Frame(ursus.Values("k", keys)).ValueCounts(c("k")).
			Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := df.Column[int64]("k")
		for i, want := range keys {
			if v, _ := got.Get(i); v != want {
				t.Fatalf("row %d is %d, want %d, the %dth key to appear", i, v, want, i)
			}
		}
	})

	t.Run("each combination", func(t *testing.T) {
		df := collect(t, lf.ValueCounts(c("city"), c("year")))
		if got := texts(t, df, "city"); !slices.Equal(got, []string{"a", "b", "b", "c", "null"}) {
			t.Errorf("cities %q", got)
		}
	})
	t.Run("eager", func(t *testing.T) {
		df, err := collect(t, lf).ValueCounts(t.Context(), c("year"))
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 2 {
			t.Errorf("%d rows", df.Height())
		}
	})
	t.Run("refused", func(t *testing.T) {
		assertUserError(t, lf.ValueCounts(), "at least one")
		assertUserError(t, lf.ValueCounts(c("year").Alias("count")), "count")
	})
}

func TestDescribe(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(
		ursus.ValuesNullable("n", []int64{1, 2, 3, 4, 0}, []bool{true, true, true, true, false}),
		ursus.ValuesNullable("s", []string{"b", "a", "", "c", "a"}, []bool{true, true, false, true, true}),
		ursus.ValuesNullable("ok", []bool{true, false, true, true, false}, []bool{true, true, true, true, false}),
	).WithColumns(c("n").Cast(ursus.Decimal(10, 2)).Alias("d"))
	df, err := lf.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Columns(); !slices.Equal(got, []string{"statistic", "n", "s", "ok", "d"}) {
		t.Fatalf("columns %v", got)
	}
	stats := []string{"count", "null_count", "mean", "std", "min", "25%", "50%", "75%", "max"}
	if got := texts(t, df, "statistic"); !slices.Equal(got, stats) {
		t.Fatalf("statistics %q", got)
	}
	floats := func(name string) []string {
		col, err := df.Column[float64](name)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, col.Len())
		for i := range out {
			v, ok := col.Get(i)
			out[i] = "null"
			if ok {
				out[i] = strconv.FormatFloat(v, 'g', 4, 64)
			}
		}
		return out
	}
	// 1, 2, 3, 4 and a null. The percentiles are the nearest values, as Polars'
	// describe takes them: ranks 0.75, 1.5 and 2.25 are values 2, 3 and 3.
	nums := []string{"4", "1", "2.5", "1.291", "1", "2", "3", "3", "4"}
	if got := floats("n"); !slices.Equal(got, nums) {
		t.Errorf("n: %q", got)
	}
	if got := floats("d"); !slices.Equal(got, nums) {
		t.Errorf("a Decimal: %q", got)
	}
	// The share of trues, and whether there is a false and a true.
	if got := floats("ok"); !slices.Equal(got, []string{"4", "1", "0.75", "null", "0", "null", "null", "null", "1"}) {
		t.Errorf("ok: %q", got)
	}
	if got := texts(t, df, "s"); !slices.Equal(got, []string{"4", "1", "null", "null", "a", "null", "null", "null", "c"}) {
		t.Errorf("s: %q", got)
	}

	t.Run("other percentiles", func(t *testing.T) {
		df, err := collect(t, lf).Describe(t.Context(), 0.9, 0.1)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"count", "null_count", "mean", "std", "min", "10%", "90%", "max"}
		if got := texts(t, df, "statistic"); !slices.Equal(got, want) {
			t.Errorf("statistics %q", got)
		}
	})
	t.Run("refused", func(t *testing.T) {
		if _, err := lf.Describe(t.Context(), 1.5); err == nil {
			t.Error("a percentile of 1.5 described")
		}
		if _, err := lf.WithColumns(c("n").Alias("statistic")).Describe(t.Context()); err == nil {
			t.Error("a column named statistic described")
		}
	})
}
