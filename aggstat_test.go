package ursus_test

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

// stats is the fixture for the statistical aggregates.
//
// Group "a" has four values, "b" has two, and "c" has exactly one row whose value
// is NULL — the case that separates "no answer" from "zero", and the one step 4
// found Min broken on.
func stats() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a", "a", "b", "b", "c"}),
		ursus.ValuesNullable("v",
			[]float64{1, 2, 3, 4, 10, 20, 0},
			[]bool{true, true, true, true, true, true, false}),
		ursus.ValuesNullable("flag",
			[]bool{true, true, false, true, true, true, false},
			[]bool{true, true, true, true, true, true, false}),
	)
}

// f64 pulls one aggregate column out as (value, valid) triples in group order.
func f64(t *testing.T, df *ursus.DataFrame, name string) []struct {
	v  float64
	ok bool
} {
	t.Helper()
	s, err := df.Column[float64](name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]struct {
		v  float64
		ok bool
	}, df.Height())
	for i := range df.Height() {
		out[i].v, out[i].ok = s.Get(i)
	}
	return out
}

// TestQuantileParametersAreNotCollapsed is the reason Agg.String renders its
// parameters.
//
// extractAggs deduplicates inner aggregates by keying a map on String(). While that
// rendered "v.quantile()" regardless of q, these two became ONE temporary and both
// columns got the same number — no error, no warning, just the median reported as
// the p90.
func TestQuantileParametersAreNotCollapsed(t *testing.T) {
	df, err := stats().
		GroupBy(ursus.Col("g")).MaintainOrder().
		Agg(
			ursus.Col("v").Quantile(0.5, ursus.InterpLinear).Alias("p50"),
			ursus.Col("v").Quantile(0.9, ursus.InterpLinear).Alias("p90"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	p50, p90 := f64(t, df, "p50"), f64(t, df, "p90")
	// Group "a" is [1,2,3,4]: p50 = 2.5, p90 = 3.7.
	if p50[0].v == p90[0].v {
		t.Errorf("p50 and p90 are both %v — the two quantiles collapsed into one "+
			"temporary because String() hid the parameter", p50[0].v)
	}
	if got, want := p50[0].v, 2.5; math.Abs(got-want) > 1e-12 {
		t.Errorf("p50 = %v, want %v", got, want)
	}
	if got, want := p90[0].v, 3.7; math.Abs(got-want) > 1e-12 {
		t.Errorf("p90 = %v, want %v", got, want)
	}
}

// TestVarDdofIsNotCollapsed is the same hazard on the other parameterised family.
func TestVarDdofIsNotCollapsed(t *testing.T) {
	df, err := stats().
		GroupBy(ursus.Col("g")).MaintainOrder().
		Agg(
			ursus.Col("v").Var(0).Alias("pop"),
			ursus.Col("v").Var(1).Alias("samp"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	pop, samp := f64(t, df, "pop"), f64(t, df, "samp")
	// [1,2,3,4]: population 1.25, sample 5/3.
	if got, want := pop[0].v, 1.25; math.Abs(got-want) > 1e-12 {
		t.Errorf("var(ddof=0) = %v, want %v", got, want)
	}
	if got, want := samp[0].v, 5.0/3.0; math.Abs(got-want) > 1e-12 {
		t.Errorf("var(ddof=1) = %v, want %v — a collapsed key would repeat the other", got, want)
	}
}

// TestAggregatesMatchNaiveImplementation is the differential test that licenses the
// hand-written numerics, the same argument the CSV scanner and the .dt family make.
//
// The reference is deliberately the obvious O(n log n) implementation over a
// materialised slice: it is too slow to ship and too simple to be wrong.
func TestAggregatesMatchNaiveImplementation(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 19))
	const n = 400
	const nGroups = 5

	keys := make([]string, n)
	vals := make([]float64, n)
	valid := make([]bool, n)
	for i := range n {
		keys[i] = string(rune('a' + rng.IntN(nGroups)))
		vals[i] = rng.Float64()*200 - 100
		valid[i] = rng.IntN(5) != 0
	}

	df, err := ursus.Frame(
		ursus.Values("g", keys),
		ursus.ValuesNullable("v", vals, valid),
	).
		GroupBy(ursus.Col("g")).MaintainOrder().
		Agg(
			ursus.Col("v").Median().Alias("med"),
			ursus.Col("v").Quantile(0.75, ursus.InterpLinear).Alias("p75"),
			ursus.Col("v").Var(1).Alias("var"),
			ursus.Col("v").Std(1).Alias("std"),
			ursus.Col("v").Product().Alias("prod"),
			ursus.Col("v").ArgMin().Alias("amin"),
			ursus.Col("v").NullCount().Alias("nulls"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	g, err := df.Column[string]("g")
	if err != nil {
		t.Fatal(err)
	}
	med, p75 := f64(t, df, "med"), f64(t, df, "p75")
	variance, sd := f64(t, df, "var"), f64(t, df, "std")
	prod := f64(t, df, "prod")
	amin, err := df.Column[uint64]("amin")
	if err != nil {
		t.Fatal(err)
	}
	nulls, err := df.Column[uint64]("nulls")
	if err != nil {
		t.Fatal(err)
	}

	for row := range df.Height() {
		key, _ := g.Get(row)

		// The naive reference: collect the group's rows, then compute directly.
		var got []float64
		var nullN uint64
		bestPos, bestVal, haveBest := uint64(0), 0.0, false
		pos := uint64(0)
		for i := range n {
			if keys[i] != key {
				continue
			}
			if !valid[i] {
				nullN++
				pos++
				continue
			}
			if !haveBest || vals[i] < bestVal {
				bestVal, bestPos, haveBest = vals[i], pos, true
			}
			got = append(got, vals[i])
			pos++
		}
		slices.Sort(got)

		if v, _ := nulls.Get(row); v != nullN {
			t.Errorf("group %q null_count = %d, want %d", key, v, nullN)
		}
		if v, ok := amin.Get(row); ok != haveBest || (haveBest && v != bestPos) {
			t.Errorf("group %q arg_min = %d (valid %v), want %d (valid %v)",
				key, v, ok, bestPos, haveBest)
		}
		if len(got) == 0 {
			continue
		}

		closeTo(t, key, "median", med[row], naiveQuantile(got, 0.5))
		closeTo(t, key, "p75", p75[row], naiveQuantile(got, 0.75))
		closeTo(t, key, "product", prod[row], naiveProduct(got))

		if len(got) > 1 {
			wv := naiveVar(got, 1)
			closeTo(t, key, "var", variance[row], wv)
			closeTo(t, key, "std", sd[row], math.Sqrt(wv))
		}
	}
}

func closeTo(t *testing.T, key, what string, got struct {
	v  float64
	ok bool
}, want float64) {
	t.Helper()
	if !got.ok {
		t.Errorf("group %q %s is null, want %v", key, what, want)
		return
	}
	if math.Abs(got.v-want) > 1e-9*max(1, math.Abs(want)) {
		t.Errorf("group %q %s = %v, want %v", key, what, got.v, want)
	}
}

func naiveQuantile(sorted []float64, q float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (pos-float64(lo))*(sorted[hi]-sorted[lo])
}

func naiveProduct(v []float64) float64 {
	p := 1.0
	for _, x := range v {
		p *= x
	}
	return p
}

// naiveVar is the two-pass textbook formula — accurate, and too slow to ship
// because it reads the data twice.
func naiveVar(v []float64, ddof int) float64 {
	var sum float64
	for _, x := range v {
		sum += x
	}
	mean := sum / float64(len(v))
	var ss float64
	for _, x := range v {
		d := x - mean
		ss += d * d
	}
	return ss / float64(len(v)-ddof)
}

// TestStdVarPrecision is why Welford's algorithm is used instead of E[x²]−E[x]².
//
// These values differ by 1, 2 and 3 around a mean of a billion. The naive formula
// subtracts two nearly equal numbers around 1e18, where float64 spacing is about
// 256 — so the answer comes out as 0, or negative, or some multiple of 256. The
// true sample variance is exactly 1.
func TestStdVarPrecision(t *testing.T) {
	const base = 1e9
	df, err := ursus.Frame(
		ursus.Values("g", []string{"x", "x", "x"}),
		ursus.Values("v", []float64{base, base + 1, base + 2}),
	).
		GroupBy(ursus.Col("g")).
		Agg(
			ursus.Col("v").Var(1).Alias("var"),
			ursus.Col("v").Std(1).Alias("std"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	v := f64(t, df, "var")
	s := f64(t, df, "std")
	if got := v[0].v; math.Abs(got-1) > 1e-6 {
		t.Errorf("var = %v, want 1 — a sum-of-squares implementation returns 0 or "+
			"garbage on data with a large mean", got)
	}
	if got := s[0].v; math.Abs(got-1) > 1e-6 {
		t.Errorf("std = %v, want 1", got)
	}
}

// TestAggregatesOnAllNullGroup: every new aggregate against a group with no
// non-null value. Step 4 found Min broken on exactly this shape, and the reason it
// survived was that every existing test had at least one real value per group.
func TestAggregatesOnAllNullGroup(t *testing.T) {
	df, err := stats().
		GroupBy(ursus.Col("g")).MaintainOrder().
		Agg(
			ursus.Col("v").Median().Alias("med"),
			ursus.Col("v").Quantile(0.9, ursus.InterpLinear).Alias("p90"),
			ursus.Col("v").Var(0).Alias("var"),
			ursus.Col("v").Std(0).Alias("std"),
			ursus.Col("v").Product().Alias("prod"),
			ursus.Col("v").ArgMin().Alias("amin"),
			ursus.Col("v").ArgMax().Alias("amax"),
			ursus.Col("v").NullCount().Alias("nulls"),
			ursus.Col("flag").Any().Alias("any"),
			ursus.Col("flag").AllTrue().Alias("all"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("an all-null group must not fail: %v", err)
	}

	// Group "c" is the third row and holds one null.
	const c = 2
	for _, name := range []string{"med", "p90", "var", "std", "prod", "amin", "amax"} {
		if _, ok, err := df.At[float64](c, name); err == nil && ok {
			t.Errorf("%s over an all-null group must be null\n%s", name, df)
		}
		if _, ok, err := df.At[uint64](c, name); err == nil && ok {
			t.Errorf("%s over an all-null group must be null\n%s", name, df)
		}
	}
	// null_count is a counter, so it is never null — it is 1.
	nulls, err := df.Column[uint64]("nulls")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := nulls.Get(c); !ok || v != 1 {
		t.Errorf("null_count = %d (valid %v), want 1 and never null", v, ok)
	}
	// any/all_true over a group whose only value is null: null, following sum.
	for _, name := range []string{"any", "all"} {
		if _, ok, err := df.At[bool](c, name); err == nil && ok {
			t.Errorf("%s over an all-null group must be null, not the vacuous answer\n%s",
				name, df)
		}
	}
}

func TestAnyAllTrueAndNullCount(t *testing.T) {
	df, err := stats().
		GroupBy(ursus.Col("g")).MaintainOrder().
		Agg(
			ursus.Col("flag").Any().Alias("any"),
			ursus.Col("flag").AllTrue().Alias("all"),
			ursus.Col("v").Count().Alias("count"),
			ursus.Col("v").NullCount().Alias("nulls"),
			ursus.Len().Alias("rows"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	any, err := df.Column[bool]("any")
	if err != nil {
		t.Fatal(err)
	}
	all, err := df.Column[bool]("all")
	if err != nil {
		t.Fatal(err)
	}
	// Group "a" flags are [true,true,false,true]; group "b" is [true,true].
	if v, ok := any.Get(0); !ok || !v {
		t.Errorf("any(a) = %v (valid %v), want true", v, ok)
	}
	if v, ok := all.Get(0); !ok || v {
		t.Errorf("all_true(a) = %v (valid %v), want false", v, ok)
	}
	if v, ok := all.Get(1); !ok || !v {
		t.Errorf("all_true(b) = %v (valid %v), want true", v, ok)
	}

	// count + null_count == len, per group. The identity that makes null_count
	// worth having as its own aggregate rather than a subtraction.
	count, _ := df.Column[uint64]("count")
	nulls, _ := df.Column[uint64]("nulls")
	rows, _ := df.Column[uint64]("rows")
	for i := range df.Height() {
		c, _ := count.Get(i)
		n, _ := nulls.Get(i)
		r, _ := rows.Get(i)
		if c+n != r {
			t.Errorf("group %d: count %d + null_count %d != len %d", i, c, n, r)
		}
	}
}

// TestArgMinMaxPositions pins the three decisions: the index is within the group,
// nulls occupy a position but cannot win, and ties go to the earliest row.
func TestArgMinMaxPositions(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("g", []string{"t", "t", "t", "t"}),
		// positions:                0     1  2  3
		ursus.ValuesNullable("v", []float64{0, 5, 3, 3},
			[]bool{false, true, true, true}),
	).
		GroupBy(ursus.Col("g")).
		Agg(
			ursus.Col("v").ArgMin().Alias("amin"),
			ursus.Col("v").ArgMax().Alias("amax"),
		).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	amin, _ := df.Column[uint64]("amin")
	amax, _ := df.Column[uint64]("amax")

	// The minimum 3 appears at positions 2 and 3; the earliest wins. The null at
	// position 0 cannot win but still shifts everything after it.
	if v, ok := amin.Get(0); !ok || v != 2 {
		t.Errorf("arg_min = %d (valid %v), want 2 — nulls occupy a position, and a "+
			"tie goes to the earliest row", v, ok)
	}
	if v, ok := amax.Get(0); !ok || v != 1 {
		t.Errorf("arg_max = %d (valid %v), want 1", v, ok)
	}
}

func TestQuantileInterpolations(t *testing.T) {
	// [1,2,3,4] at q=0.4: rank 1.2, so lo=2, hi=3, frac=0.2.
	cases := []struct {
		interp ursus.Interpolation
		want   float64
	}{
		{ursus.InterpLinear, 2.2},
		{ursus.InterpLower, 2},
		{ursus.InterpHigher, 3},
		{ursus.InterpNearest, 2},
		{ursus.InterpMidpoint, 2.5},
	}
	for _, c := range cases {
		t.Run(c.interp.String(), func(t *testing.T) {
			df, err := ursus.Frame(
				ursus.Values("g", []string{"x", "x", "x", "x"}),
				ursus.Values("v", []float64{4, 1, 3, 2}), // deliberately unsorted
			).
				GroupBy(ursus.Col("g")).
				Agg(ursus.Col("v").Quantile(0.4, c.interp).Alias("q")).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			q := f64(t, df, "q")
			if math.Abs(q[0].v-c.want) > 1e-12 {
				t.Errorf("quantile(0.4, %s) = %v, want %v", c.interp, q[0].v, c.want)
			}
		})
	}
}

// TestProductReturnsFloat64 pins the compromise: product does not get sum's Int128
// treatment, because i128 has no multiplication.
func TestProductReturnsFloat64(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("g", []string{"x", "x", "x"}),
		ursus.Values("v", []int64{2, 3, 7}),
	).
		GroupBy(ursus.Col("g")).
		Agg(ursus.Col("v").Product().Alias("p")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := df.Schema().String(), "{g: String!, p: Float64}"; got != want {
		t.Errorf("schema = %s, want %s — an integer product is Float64, unlike sum", got, want)
	}
	p := f64(t, df, "p")
	if p[0].v != 42 {
		t.Errorf("product = %v, want 42", p[0].v)
	}
}

func TestStatAggregateRefusals(t *testing.T) {
	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		kind error
		want string
	}{
		{"any on a number", func() *ursus.LazyFrame {
			return stats().GroupBy(ursus.Col("g")).Agg(ursus.Col("v").Any())
		}, uerr.ErrType, "requires a Boolean"},
		{"var on a string", func() *ursus.LazyFrame {
			return stats().GroupBy(ursus.Col("g")).Agg(ursus.Col("g").Var(1))
		}, uerr.ErrType, "requires a numeric"},
		{"median on a string", func() *ursus.LazyFrame {
			return stats().GroupBy(ursus.Col("g")).Agg(ursus.Col("g").Median())
		}, uerr.ErrType, "requires a numeric"},
		{"q out of range", func() *ursus.LazyFrame {
			return stats().GroupBy(ursus.Col("g")).
				Agg(ursus.Col("v").Quantile(1.5, ursus.InterpLinear))
		}, uerr.ErrValue, "between 0 and 1"},
		{"q is NaN", func() *ursus.LazyFrame {
			return stats().GroupBy(ursus.Col("g")).
				Agg(ursus.Col("v").Quantile(math.NaN(), ursus.InterpLinear))
		}, uerr.ErrValue, "between 0 and 1"},
		{"negative ddof", func() *ursus.LazyFrame {
			return stats().GroupBy(ursus.Col("g")).Agg(ursus.Col("v").Std(-1))
		}, uerr.ErrValue, "ddof"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf().Collect(t.Context())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, c.kind) {
				t.Errorf("wrong kind: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should explain %q:\n%v", c.want, err)
			}
		})
	}
}

// TestStatAggregatesBatchSizeAndThreadsAreIndependent: batch size is a tuning knob,
// never a semantic one. For arg_min in particular this is the test that would catch
// a position counted per batch instead of per group.
func TestStatAggregatesBatchSizeAndThreadsAreIndependent(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return stats().
			GroupBy(ursus.Col("g")).MaintainOrder().
			Agg(
				ursus.Col("v").Median().Alias("med"),
				ursus.Col("v").Quantile(0.9, ursus.InterpNearest).Alias("p90"),
				ursus.Col("v").Std(1).Alias("std"),
				ursus.Col("v").Product().Alias("prod"),
				ursus.Col("v").ArgMin().Alias("amin"),
				ursus.Col("v").ArgMax().Alias("amax"),
				ursus.Col("v").NullCount().Alias("nulls"),
				ursus.Col("flag").Any().Alias("any"),
			)
	}
	ref, err := q().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := ref.String()

	for _, bs := range []int{1, 2, 3, 7, 8192} {
		for _, th := range []int{1, 2, 4} {
			got, err := q().Collect(t.Context(),
				ursus.WithBatchSize(bs), ursus.WithThreads(th), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d threads %d: %v", bs, th, err)
			}
			if got.String() != want {
				t.Errorf("batch %d threads %d changed the answer:\n%s\nwant:\n%s",
					bs, th, got, want)
			}
		}
	}
}
