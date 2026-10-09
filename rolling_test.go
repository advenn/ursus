package ursus_test

// Step 149: the fixed-size rolling functions. By hand first, then against a naive
// oracle that recomputes every window from its rows, over random partitions, orders,
// nulls, NaNs and infinities, and over one long partition, where a running sum that
// only added and subtracted would drift.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

func TestRollingByHand(t *testing.T) {
	c := ursus.Col
	x := c("x")
	lf := ursus.Frame(ursus.Values("x", []int64{1, 2, 3, 4, 5}))
	nulls := ursus.Frame(ursus.ValuesNullable("x", []int64{1, 0, 3, 4, 0, 6},
		[]bool{true, false, true, true, false, true}))
	floats := ursus.Frame(ursus.Values("x", []float64{1, math.NaN(), 3, 4, math.Inf(1), 1, 2}))
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		e    ursus.Expr
		want []string
	}{
		{"sum", lf, x.RollingSum(3), []string{"null", "null", "6", "9", "12"}},
		{"sum, from one value", lf, x.RollingSum(3, ursus.MinSamples(1)), []string{"1", "3", "6", "9", "12"}},
		{"mean", lf, x.RollingMean(2), []string{"null", "1.5", "2.5", "3.5", "4.5"}},
		{"min", lf, x.RollingMin(3), []string{"null", "null", "1", "2", "3"}},
		{"max", lf, x.RollingMax(3), []string{"null", "null", "3", "4", "5"}},
		{"var", lf, x.RollingVar(3), []string{"null", "null", "1", "1", "1"}},
		{"var, ddof 0", lf, x.RollingVar(2, ursus.Ddof(0)), []string{"null", "0.25", "0.25", "0.25", "0.25"}},
		{"std", lf, x.RollingStd(3), []string{"null", "null", "1", "1", "1"}},
		// A null makes a full window short of values, unless fewer will do.
		{"nulls", nulls, x.RollingSum(2), []string{"null", "null", "null", "7", "null", "null"}},
		{"nulls, from one value", nulls, x.RollingSum(2, ursus.MinSamples(1)),
			[]string{"1", "1", "3", "7", "4", "6"}},
		// A variance of one value with ddof 1 is null, as Var's is.
		{"var of one value", nulls, x.RollingVar(2, ursus.MinSamples(1)),
			[]string{"null", "null", "null", "0.5", "null", "null"}},
		// A NaN is in two windows, and an infinity leaves no NaN behind it.
		{"float sum", floats, x.RollingSum(2), []string{"null", "NaN", "NaN", "7", "+Inf", "+Inf", "3"}},
		{"float max", floats, x.RollingMax(2), []string{"null", "NaN", "NaN", "4", "+Inf", "+Inf", "2"}},
		{"float min", floats, x.RollingMin(2), []string{"null", "1", "3", "3", "4", "1", "1"}},
		{"float var", floats, x.RollingVar(2), []string{"null", "NaN", "NaN", "0.5", "NaN", "NaN", "0.5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := numbers(t, collect(t, tc.lf.Select(tc.e.Alias("r"))), "r"); !slices.Equal(got, tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}

	t.Run("a Float32 stays one", func(t *testing.T) {
		df := collect(t, ursus.Frame(ursus.Values("x", []float32{1, 2, 4})).
			Select(x.RollingMean(2).Alias("m"), x.RollingSum(2).Alias("s")))
		for _, name := range []string{"m", "s"} {
			col, err := df.Column[float32](name)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			want := map[string][]float32{"m": {1.5, 3}, "s": {3, 6}}[name]
			for i, w := range want {
				if v, _ := col.Get(i + 1); v != w {
					t.Errorf("%s row %d: %v, want %v", name, i+1, v, w)
				}
			}
		}
	})

	t.Run("an integer sum does not wrap", func(t *testing.T) {
		df := collect(t, ursus.Frame(ursus.Values("x", []int64{math.MaxInt64, math.MaxInt64})).
			Select(x.RollingSum(2).Cast(ursus.String)))
		if got := texts(t, df, "x"); !slices.Equal(got, []string{"null", "18446744073709551614"}) {
			t.Errorf("got %q", got)
		}
	})

	t.Run("min and max of text and dates", func(t *testing.T) {
		df := collect(t, ursus.Frame(
			ursus.Values("s", []string{"pear", "apple", "fig", "kiwi"}),
			ursus.Values("d", []time.Time{
				time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
				time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC),
			})).Select(c("s").RollingMin(2), c("d").Cast(ursus.Date).RollingMax(2).Cast(ursus.String)))
		if got := texts(t, df, "s"); !slices.Equal(got, []string{"null", "apple", "apple", "fig"}) {
			t.Errorf("s: %q", got)
		}
		if got := texts(t, df, "d"); !slices.Equal(got, []string{"null", "2024-03-01", "2024-02-01", "2024-04-01"}) {
			t.Errorf("d: %q", got)
		}
	})

	t.Run("each window's parameters are its own", func(t *testing.T) {
		// Rendered alike, two of these would share one computation (WinParams.args).
		df := collect(t, lf.Select(
			x.RollingSum(3).Alias("a"), x.RollingSum(3, ursus.MinSamples(1)).Alias("b"),
			x.RollingSum(2).Alias("c"), x.RollingVar(2).Alias("d"), x.RollingVar(2, ursus.Ddof(0)).Alias("e"),
			x.RollingMean(2).Alias("f")))
		for name, want := range map[string][]string{
			"a": {"null", "null", "6", "9", "12"}, "b": {"1", "3", "6", "9", "12"},
			"c": {"null", "3", "5", "7", "9"}, "d": {"null", "0.5", "0.5", "0.5", "0.5"},
			"e": {"null", "0.25", "0.25", "0.25", "0.25"}, "f": {"null", "1.5", "2.5", "3.5", "4.5"},
		} {
			if got := numbers(t, df, name); !slices.Equal(got, want) {
				t.Errorf("%s: %v, want %v", name, got, want)
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			e    ursus.Expr
			lf   *ursus.LazyFrame
			want string
		}{
			{x.RollingSum(0), lf, "at least one row"},
			{x.RollingSum(3, ursus.MinSamples(4)), lf, "min_samples"},
			{x.RollingVar(3, ursus.Ddof(-1)), lf, "ddof"},
			{c("s").RollingSum(2), ursus.Frame(ursus.Values("s", []string{"a"})), "rolling_sum() is not defined for String"},
			{c("b").RollingVar(2), ursus.Frame(ursus.Values("b", []bool{true})), "rolling_var() is not defined for Bool"},
			{c("d").RollingMean(2), ursus.Frame(ursus.Values("d", []time.Duration{time.Second})), "rolling_mean() is not defined"},
		} {
			assertUserError(t, tc.lf.Select(tc.e), tc.want)
		}
	})
}

// rollingOracle answers fn over each row's window by recomputing it from the window's
// rows: the rows of the row's group, ordered by t, up to and including it.
func rollingOracle(fn string, size, minN, ddof int, gs []int64, ts []int64, xs []float64, ok []bool) []string {
	n := len(xs)
	out := make([]string, n)
	byGroup := map[int64][]int{}
	for i, g := range gs {
		byGroup[g] = append(byGroup[g], i)
	}
	for _, rows := range byGroup {
		sort.Slice(rows, func(a, b int) bool { return ts[rows[a]] < ts[rows[b]] })
		for p, row := range rows {
			var vals []float64
			for _, r := range rows[max(0, p-size+1) : p+1] {
				if ok[r] {
					vals = append(vals, xs[r])
				}
			}
			out[row] = "null"
			if len(vals) < minN {
				continue
			}
			var v float64
			switch fn {
			case "sum", "mean":
				for _, x := range vals {
					v += x
				}
				if fn == "mean" {
					v /= float64(len(vals))
				}
			case "min", "max":
				v = vals[0]
				for _, x := range vals[1:] {
					switch {
					case math.IsNaN(x) || math.IsNaN(v):
						if fn == "max" {
							v = math.NaN() // NaN sorts above every number
						} else if math.IsNaN(v) {
							v = x
						}
					case fn == "min":
						v = math.Min(v, x)
					default:
						v = math.Max(v, x)
					}
				}
			case "var", "std":
				if len(vals) <= ddof {
					continue
				}
				var mean float64
				for _, x := range vals {
					mean += x
				}
				mean /= float64(len(vals))
				for _, x := range vals {
					v += (x - mean) * (x - mean)
				}
				v /= float64(len(vals) - ddof)
				if fn == "std" {
					v = math.Sqrt(v)
				}
			}
			out[row] = fmt.Sprint(v)
		}
	}
	return out
}

func near(a, b string, tol float64) bool {
	if a == b {
		return true
	}
	var x, y float64
	if _, err := fmt.Sscan(a, &x); err != nil {
		return false
	}
	if _, err := fmt.Sscan(b, &y); err != nil {
		return false
	}
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return math.IsNaN(x) == math.IsNaN(y) && x == y || (math.IsNaN(x) && math.IsNaN(y))
	}
	return math.Abs(x-y) <= tol*max(1, math.Abs(x), math.Abs(y))
}

func TestRollingAgainstTheOracle(t *testing.T) {
	c := ursus.Col
	rng := rand.New(rand.NewPCG(149, 1))
	n := 600
	gs, ts := make([]int64, n), make([]int64, n)
	xs, ok := make([]float64, n), make([]bool, n)
	for i := range n {
		gs[i] = int64(rng.IntN(5))
		ts[i] = int64(rng.Perm(1)[0]) + int64(i*7919%n) // unique
		ok[i] = rng.IntN(8) != 0
		switch rng.IntN(40) {
		case 0:
			xs[i] = math.NaN()
		case 1:
			xs[i] = math.Inf(1 - 2*rng.IntN(2))
		default:
			xs[i] = math.Round(rng.NormFloat64()*1e4) / 100
		}
	}
	lf := ursus.Frame(ursus.Values("g", gs), ursus.Values("t", ts), ursus.ValuesNullable("x", xs, ok))
	spec := ursus.WindowSpec{PartitionBy: []ursus.Expr{c("g")}, OrderBy: []ursus.SortKey{ursus.Asc(c("t"))}}

	type fnOf func(ursus.Expr, int, ...ursus.WindowOption) ursus.Expr
	fns := map[string]fnOf{
		"sum": ursus.Expr.RollingSum, "mean": ursus.Expr.RollingMean, "min": ursus.Expr.RollingMin,
		"max": ursus.Expr.RollingMax, "var": ursus.Expr.RollingVar, "std": ursus.Expr.RollingStd,
	}
	var checked int
	for _, size := range []int{1, 2, 3, 7} {
		for _, minN := range []int{0, 1, max(1, size-1)} {
			var exprs []ursus.Expr
			var names, fnNames []string
			for name, f := range fns {
				col := fmt.Sprintf("%s_%d_%d", name, size, minN)
				opts := []ursus.WindowOption{}
				if minN > 0 {
					opts = append(opts, ursus.MinSamples(minN))
				}
				exprs = append(exprs, f(c("x"), size, opts...).OverWith(spec).Alias(col))
				names, fnNames = append(names, col), append(fnNames, name)
			}
			df := collect(t, lf.Select(exprs...))
			needs := minN
			if needs == 0 {
				needs = size // MinSamples' default: the window's size
			}
			for j, col := range names {
				want := rollingOracle(fnNames[j], size, needs, 1, gs, ts, xs, ok)
				got, err := df.Column[float64](col)
				if err != nil {
					t.Fatal(err)
				}
				for i := range got.Len() {
					checked++
					g := "null"
					if v, ok := got.Get(i); ok {
						g = fmt.Sprint(v)
					}
					// A sum, mean or variance adds in another order than the oracle's,
					// and can differ from it in the last digits.
					if !near(g, want[i], 1e-9) {
						t.Fatalf("%s row %d: %s, the oracle %s", col, i, g, want[i])
					}
				}
			}
		}
	}
	if checked < 40_000 {
		t.Errorf("only %d answers checked", checked)
	}
}

// TestRollingDoesNotDrift: a mean over one long partition of large values, where a
// sum that only added and subtracted would build up its rounding.
func TestRollingDoesNotDrift(t *testing.T) {
	n := 50_000
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = 1e9 + float64(i%17)*0.1 + 1e-3*float64(i%5)
	}
	df := collect(t, ursus.Frame(ursus.Values("x", xs)).Select(ursus.Col("x").RollingMean(64).Alias("m"),
		ursus.Col("x").RollingStd(64).Alias("s")))
	m, _ := df.Column[float64]("m")
	s, _ := df.Column[float64]("s")
	for _, i := range []int{63, 1000, 25_000, n - 1} {
		var sum float64
		for _, x := range xs[i-63 : i+1] {
			sum += x - 1e9
		}
		want := 1e9 + sum/64
		if v, _ := m.Get(i); math.Abs(v-want) > 1e-6 {
			t.Errorf("row %d: mean %.9f, want %.9f", i, v, want)
		}
		var ss float64
		for _, x := range xs[i-63 : i+1] {
			ss += (x - want) * (x - want)
		}
		if v, _ := s.Get(i); math.Abs(v-math.Sqrt(ss/63)) > 1e-6 {
			t.Errorf("row %d: std %.9f, want %.9f", i, v, math.Sqrt(ss/63))
		}
	}
}
