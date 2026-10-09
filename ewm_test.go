package ursus_test

// Step 153: the exponentially weighted functions and Interpolate. By hand against
// pandas' answers, then the adjusted ewm against its closed form, which weighs each
// value explicitly and shares no recurrence with the kernel.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus"
)

func TestEwmByHand(t *testing.T) {
	c := ursus.Col
	x := c("x")
	half := ursus.EwmAlpha(0.5)
	lf := ursus.Frame(ursus.Values("x", []float64{1, 2, 3}))
	gap := ursus.Frame(ursus.ValuesNullable("x", []float64{1, 0, 3}, []bool{true, false, true}))
	lead := ursus.Frame(ursus.ValuesNullable("x", []float64{0, 1, 2}, []bool{false, true, true}))
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		e    ursus.Expr
		want []string
	}{
		// pandas' Series([1, 2, 3]).ewm(alpha=0.5).mean() and its siblings.
		{"mean", lf, x.EwmMean(half), []string{"1", "1.667", "2.429"}},
		{"mean, unadjusted", lf, x.EwmMean(half, ursus.EwmAdjust(false)), []string{"1", "1.5", "2.25"}},
		{"var", lf, x.EwmVar(half), []string{"null", "0.5", "0.9286"}},
		{"std", lf, x.EwmStd(half), []string{"null", "0.7071", "0.9636"}},
		{"var, biased", lf, x.EwmVar(half, ursus.Biased()), []string{"0", "0.2222", "0.5306"}},
		{"mean from two values", lf, x.EwmMean(half, ursus.MinSamples(2)), []string{"null", "1.667", "2.429"}},
		// A null ages the weights before it, unless nulls are ignored; its row
		// answers the running value.
		{"a null", gap, x.EwmMean(half), []string{"1", "1", "2.6"}},
		{"a null, ignored", gap, x.EwmMean(half, ursus.IgnoreNulls()), []string{"1", "1", "2.333"}},
		{"a leading null", lead, x.EwmMean(half), []string{"null", "1", "1.667"}},
		// A NaN is a value, and the mean is NaN from there on.
		{"a NaN", ursus.Frame(ursus.Values("x", []float64{1, math.NaN(), 3})), x.EwmMean(half),
			[]string{"1", "NaN", "NaN"}},
		// Four spellings of one decay.
		{"span", lf, x.EwmMean(ursus.EwmSpan(3)), []string{"1", "1.667", "2.429"}},
		{"com", lf, x.EwmMean(ursus.EwmCom(1)), []string{"1", "1.667", "2.429"}},
		{"half-life", lf, x.EwmMean(ursus.EwmHalfLife(1)), []string{"1", "1.667", "2.429"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := numbers(t, collect(t, tc.lf.Select(tc.e.Alias("r"))), "r"); !slices.Equal(got, tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}

	t.Run("per group, in an order", func(t *testing.T) {
		lf := ursus.Frame(ursus.Values("g", []string{"a", "b", "a", "a", "b"}),
			ursus.Values("t", []int64{3, 1, 1, 2, 2}), ursus.Values("x", []float64{3, 10, 1, 2, 20}))
		spec := ursus.WindowSpec{PartitionBy: []ursus.Expr{c("g")}, OrderBy: []ursus.SortKey{ursus.Asc(c("t"))}}
		// a, in t's order, is 1, 2, 3; b is 10, 20.
		want := []string{"2.429", "10", "1", "1.667", "16.67"}
		if got := numbers(t, collect(t, lf.Select(x.EwmMean(half).OverWith(spec).Alias("r"))), "r"); !slices.Equal(got, want) {
			t.Errorf("got  %v\nwant %v", got, want)
		}
	})

	t.Run("each call's parameters are its own", func(t *testing.T) {
		df := collect(t, lf.Select(x.EwmMean(half).Alias("a"), x.EwmMean(ursus.EwmAlpha(0.25)).Alias("b"),
			x.EwmMean(half, ursus.EwmAdjust(false)).Alias("c"), x.EwmVar(half).Alias("d"),
			x.EwmVar(half, ursus.Biased()).Alias("e")))
		for name, want := range map[string][]string{
			"a": {"1", "1.667", "2.429"}, "b": {"1", "1.571", "2.189"}, "c": {"1", "1.5", "2.25"},
			"d": {"null", "0.5", "0.9286"}, "e": {"0", "0.2222", "0.5306"},
		} {
			if got := numbers(t, df, name); !slices.Equal(got, want) {
				t.Errorf("%s: %v, want %v", name, got, want)
			}
		}
	})

	t.Run("a Float32 stays one, an integer is a Float64", func(t *testing.T) {
		s, err := ursus.Frame(ursus.Values("f", []float32{1, 2}), ursus.Values("i", []int64{1, 2})).
			Select(c("f").EwmMean(half), c("i").EwmMean(half), c("i").Interpolate().Alias("j")).
			CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range []ursus.DataType{ursus.Float32, ursus.Float64, ursus.Float64} {
			if got := s.Field(i).Type; got != want {
				t.Errorf("column %d is %s, want %s", i, got, want)
			}
		}
	})

	t.Run("refused", func(t *testing.T) {
		for _, tc := range []struct {
			e    ursus.Expr
			want string
		}{
			{x.EwmMean(ursus.EwmAlpha(0)), "alpha"},
			{x.EwmMean(ursus.EwmAlpha(1.5)), "alpha"},
			{x.EwmMean(ursus.EwmSpan(0.5)), "span"},
			{x.EwmMean(ursus.EwmDecay{}), "no decay"},
			{c("s").EwmMean(half), "ewm_mean() is not defined for String"},
			{c("s").Interpolate(), "interpolate() is not defined for String"},
		} {
			assertUserError(t, ursus.Frame(ursus.Values("x", []float64{1}), ursus.Values("s", []string{"a"})).
				Select(tc.e), tc.want)
		}
	})
}

// ewmOracle is the adjusted ewm by its closed form: each row's mean is the weighted
// mean of the values up to it, the value i rows behind weighted (1-alpha)^i, with i
// counting rows, or with ignoreNulls values only; its unbiased variance is the
// weighted variance times (Σw)²/((Σw)²-Σw²).
func ewmOracle(alpha float64, ignoreNulls, variance bool, xs []float64, ok []bool) []string {
	out := make([]string, len(xs))
	for t := range xs {
		var sw, sw2, swx float64
		var ws, vs []float64
		seen := 0
		for i := t; i >= 0; i-- {
			if !ok[i] {
				continue
			}
			lag := t - i
			if ignoreNulls {
				lag = seen
			}
			w := math.Pow(1-alpha, float64(lag))
			sw, sw2, swx = sw+w, sw2+w*w, swx+w*xs[i]
			ws, vs = append(ws, w), append(vs, xs[i])
			seen++
		}
		out[t] = "null"
		if seen == 0 {
			continue
		}
		mean := swx / sw
		if !variance {
			out[t] = fmt.Sprint(mean)
			continue
		}
		var ss float64
		for j, w := range ws {
			ss += w * (vs[j] - mean) * (vs[j] - mean)
		}
		if den := sw*sw - sw2; den > 0 {
			out[t] = fmt.Sprint(ss / sw * sw * sw / den)
		}
	}
	return out
}

func TestEwmAgainstItsClosedForm(t *testing.T) {
	rng := rand.New(rand.NewPCG(153, 1))
	n := 120
	xs, ok := make([]float64, n), make([]bool, n)
	for i := range n {
		xs[i], ok[i] = math.Round(rng.NormFloat64()*1000)/10, rng.IntN(6) != 0
	}
	lf := ursus.Frame(ursus.ValuesNullable("x", xs, ok))
	var checked int
	for _, alpha := range []float64{0.1, 0.5, 0.9} {
		for _, ignore := range []bool{false, true} {
			opts := []ursus.WindowOption{}
			if ignore {
				opts = append(opts, ursus.IgnoreNulls())
			}
			df := collect(t, lf.Select(
				ursus.Col("x").EwmMean(ursus.EwmAlpha(alpha), opts...).Alias("m"),
				ursus.Col("x").EwmVar(ursus.EwmAlpha(alpha), opts...).Alias("v")))
			for name, variance := range map[string]bool{"m": false, "v": true} {
				want := ewmOracle(alpha, ignore, variance, xs, ok)
				col, err := df.Column[float64](name)
				if err != nil {
					t.Fatal(err)
				}
				for i := range n {
					checked++
					g := "null"
					if v, ok := col.Get(i); ok {
						g = fmt.Sprint(v)
					}
					if !near(g, want[i], 1e-9) {
						t.Fatalf("alpha %v, ignore nulls %v, %s, row %d: %s, the closed form %s",
							alpha, ignore, name, i, g, want[i])
					}
				}
			}
		}
	}
	if checked < 1_000 {
		t.Errorf("only %d answers checked", checked)
	}
}

func TestInterpolate(t *testing.T) {
	c := ursus.Col
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		e    ursus.Expr
		want []string
	}{
		{"a run between two values", ursus.Frame(ursus.ValuesNullable("x", []int64{1, 0, 0, 7}, []bool{true, false, false, true})),
			c("x").Interpolate(), []string{"1", "3", "5", "7"}},
		{"the ends stay null", ursus.Frame(ursus.ValuesNullable("x", []float64{0, 2, 0, 4, 0}, []bool{false, true, false, true, false})),
			c("x").Interpolate(), []string{"null", "2", "3", "4", "null"}},
		{"per group, in an order", ursus.Frame(ursus.Values("g", []string{"a", "b", "a", "a", "b"}),
			ursus.Values("t", []int64{3, 1, 2, 1, 2}),
			ursus.ValuesNullable("x", []float64{9, 5, 0, 1, 0}, []bool{true, true, false, true, false})),
			c("x").Interpolate().OverWith(ursus.WindowSpec{PartitionBy: []ursus.Expr{c("g")},
				OrderBy: []ursus.SortKey{ursus.Asc(c("t"))}}),
			[]string{"9", "5", "5", "1", "null"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := numbers(t, collect(t, tc.lf.Select(tc.e.Alias("r"))), "r"); !slices.Equal(got, tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
}
