package ursus_test

// Aggregations and windows, audit.md §7's cases: a silent wrong answer, a crash or a
// refusal that the audit measured, each answered under the rule the step chose for it.
// Their answers are Polars 2.1.0's since step 500, recorded in the aggwindow_byhand
// group of testdata/oracle and spelled in oracle_aggwindow_byhand_test.go, which took
// this file's hand answers. What stays here asserts structure and the refusals, and
// holds to math/big what Polars has no right answer for: an integer mean past 2^53,
// which Polars sums in float and rounds, and a median, a midpoint and a variance at
// the ends of a float's range, which Polars 2.1 overflows to +Inf or NaN.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

type aggCase struct {
	name   string
	got    *ursus.LazyFrame
	check  func(*ursus.DataFrame) string // a property of the answer, or nil with
	refuse []string                      // a value refusal naming these
}

func TestAggregationsAndWindowsByHand(t *testing.T) {
	c := ursus.Col
	cases := []aggCase{
		// --- A13: a global aggregate of nothing is one row, no columns ---
		{name: "A13 GroupBy().Agg() of nothing",
			got:   ursus.Frame(ursus.Values("v", []int64{1, 2, 3})).GroupBy().Agg(),
			check: oneEmptyRow},
		{name: "A13 GroupBy().Agg() of nothing, over no rows",
			got:   ursus.Frame(ursus.Values("v", []int64{1, 2, 3})).Filter(c("v").Gt(9)).GroupBy().Agg(),
			check: oneEmptyRow},

		// --- A14: an undeclared Closed is refused ---
		{name: "A14 GroupByDynamic with Closed(99)",
			got: hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:05:00").
				GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("1h"), Closed: ursus.Closed(99)}).
				Agg(ursus.Len()),
			refuse: []string{"Closed"}},
		{name: "A14 Rolling with Closed(99)",
			got: hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:05:00").
				Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("1h"), Closed: ursus.Closed(99)}).
				Agg(ursus.Len()),
			refuse: []string{"Closed"}},
		{name: "A14 IsBetween with Closed(99)",
			got:    ursus.Frame(ursus.Values("v", []int64{1, 2, 3})).Select(c("v").IsBetween(1, 3, ursus.Closed(99))),
			refuse: []string{"Closed"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if wrong := aggWrong(t, tc); wrong != "" {
				t.Error(wrong)
			}
		})
	}
}

// TestIntegerMeanIsExact is A4: an integer mean divides an exact Int128 sum, so the
// mean of [2^53+1, 2^53+2] is 2^53+1.5's nearest float, 2^53+2, where a float sum has
// 2^53+1 as 2^53 already and Polars 2.1 answers 2^53. Held to math/big: the exact
// rational mean, rounded once to float64, over the audit's pair and 200 rows of large
// integers, as a group's, a window's and a list's mean.
func TestIntegerMeanIsExact(t *testing.T) {
	c := ursus.Col
	const big53 = 1<<53 + 1
	rng := rand.New(rand.NewPCG(500, 4))
	n := 200
	g, v := make([]int64, n), make([]int64, n)
	for i := range n {
		g[i] = int64(i % 5)
		v[i] = rng.Int64N(1<<62) + 1<<53
		if i%7 == 0 {
			v[i] = -v[i]
		}
	}
	exact := func(vals []int64) float64 {
		sum := new(big.Int)
		for _, x := range vals {
			sum.Add(sum, big.NewInt(x))
		}
		f, _ := new(big.Rat).SetFrac(sum, big.NewInt(int64(len(vals)))).Float64()
		return f
	}
	byGroup := map[int64][]int64{}
	for i := range n {
		byGroup[g[i]] = append(byGroup[g[i]], v[i])
	}
	frame := ursus.Frame(ursus.Values("g", g), ursus.Values("v", v))
	pair := ursus.Frame(ursus.Values("g", []int64{0, 0}), ursus.Values("v", []int64{big53, big53 + 1}))
	for name, tc := range map[string]struct {
		q      *ursus.LazyFrame
		groups map[int64][]int64
	}{
		"the pair's mean": {pair.GroupBy(c("g")).Agg(c("v").Mean()), map[int64][]int64{0: {big53, big53 + 1}}},
		"a group's mean":  {frame.GroupBy(c("g")).Agg(c("v").Mean()).Sort(ursus.Asc(c("g"))), byGroup},
		"a window's mean": {frame.Select(c("g"), c("v").Mean().Over(c("g"))).
			GroupBy(c("g")).Agg(c("v").First()).Sort(ursus.Asc(c("g"))), byGroup},
		"a list's mean": {frame.GroupBy(c("g")).Agg(c("v").Implode()).Sort(ursus.Asc(c("g"))).Select(c("g"), c("v").List().Mean()), byGroup},
	} {
		df := collect(t, tc.q)
		keys, _ := df.Column[int64]("g")
		means, _ := df.Column[float64]("v")
		for i := range df.Height() {
			k, _ := keys.Get(i)
			got, _ := means.Get(i)
			if want := exact(tc.groups[k]); math.Float64bits(got) != math.Float64bits(want) {
				t.Errorf("%s, group %d: %v, want the exact %v", name, k, got, want)
			}
		}
	}
}

// TestAggregatesAtTheFloatsEnds is A2 and A5: a quantile at an exact rank is that
// value and nothing overflows, where Polars 2.1 answers +Inf for the median of
// [-1.7e308, 1.7e308] (lo + (hi - lo)/2) and the midpoint of [1e308, 1.5e308]
// ((lo + hi)/2), and NaN for the variance of [1e308, 1e308]. Held to math/big: the
// exact midpoint and variance of pairs drawn across the float's range, rounded once
// to float64, +Inf where the exact value is past it; a finite value within four ulps,
// lo + (hi - lo)/2 rounding twice, and 0 and +Inf to the bit.
func TestAggregatesAtTheFloatsEnds(t *testing.T) {
	near := func(got, want float64) bool {
		if want == 0 || math.IsInf(want, 0) {
			return math.Float64bits(got) == math.Float64bits(want)
		}
		return math.Abs(got-want) <= 1e-15*math.Abs(want)
	}
	c := ursus.Col
	rng := rand.New(rand.NewPCG(500, 5))
	pairs := [][2]float64{{-1.7e308, 1.7e308}, {1e308, 1.5e308}, {1.7e308, 1.7e308}, {1e308, 1e308}, {1e308, -1e308}}
	for range 60 {
		a, b := rng.Float64()*1.7e308, rng.Float64()*1.7e308
		if rng.IntN(2) == 0 {
			a = -a
		}
		if rng.IntN(2) == 0 {
			b = -b
		}
		pairs = append(pairs, [2]float64{a, b})
	}
	rat := func(f float64) *big.Rat { return new(big.Rat).SetFloat64(f) }
	for _, p := range pairs {
		df := collect(t, ursus.Frame(ursus.Values("v", []float64{p[0], p[1]})).GroupBy().Agg(
			c("v").Median().Alias("median"), c("v").Quantile(0.5, ursus.InterpMidpoint).Alias("midpoint"), c("v").Var(0).Alias("var")))
		mid, _ := new(big.Rat).Quo(new(big.Rat).Add(rat(p[0]), rat(p[1])), big.NewRat(2, 1)).Float64()
		// The variance about the exact mean: ((a-m)^2 + (b-m)^2)/2 = (a-b)^2/4.
		d := new(big.Rat).Sub(rat(p[0]), rat(p[1]))
		variance, _ := new(big.Rat).Quo(new(big.Rat).Mul(d, d), big.NewRat(4, 1)).Float64()
		for name, want := range map[string]float64{"median": mid, "midpoint": mid, "var": variance} {
			got, ok, err := df.At[float64](0, name)
			if err != nil || !ok {
				t.Fatalf("%s of %v: %v, %v", name, p, ok, err)
			}
			if !near(got, want) {
				t.Errorf("%s of %v: %v, want the exact %v", name, p, got, want)
			}
		}
	}
}

// oneEmptyRow is a global aggregate of nothing: one row, no columns.
func oneEmptyRow(df *ursus.DataFrame) string {
	if h, w := df.Shape(); h != 1 || w != 0 {
		return fmt.Sprintf("shape (%d, %d), want (1, 0): a global aggregate is one row", h, w)
	}
	return ""
}

func aggWrong(t *testing.T, tc aggCase) string {
	df, err := tc.got.Collect(t.Context())
	if tc.refuse != nil {
		switch {
		case err == nil:
			return fmt.Sprintf("answered %s; want a refusal", strings.Join(strings.Fields(df.String()), " "))
		case !errors.Is(err, ursus.ErrValue):
			return fmt.Sprintf("a %s error, want a value error: %v", kindOf(err), err)
		}
		for _, w := range tc.refuse {
			if !strings.Contains(err.Error(), w) {
				return fmt.Sprintf("the refusal does not say %q: %v", w, err)
			}
		}
		return ""
	}
	if err != nil {
		if errors.Is(err, ursus.ErrInternal) {
			return "ErrInternal: " + err.Error()
		}
		return err.Error()
	}
	return tc.check(df)
}
