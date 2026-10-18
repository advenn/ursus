package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// statsFrame is Polars' frame: g [1, 1, 1, 1, 2, 2, 3], x [1, 2, 4, None, 3, 3, 5],
// i [6, 3, 5, None, 12, 10, 7] and y [2, 1, 5, 7, None, 2, 1].
func statsFrame() *ursus.LazyFrame {
	ok := []bool{true, true, true, false, true, true, true}
	return ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 1, 2, 2, 3}),
		ursus.ValuesNullable("x", []float64{1, 2, 4, 0, 3, 3, 5}, ok),
		ursus.ValuesNullable("i", []int64{6, 3, 5, 0, 12, 10, 7}, ok),
		ursus.ValuesNullable("y", []float64{2, 1, 5, 7, 0, 2, 1}, []bool{true, true, true, true, false, true, true}))
}

// nearFloats reports whether got is want to a relative 1e-12, NaN to NaN and a null to
// a null (NaN-marked by floatsOf, so a null is told from NaN by want's own marker).
func nearFloats(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		g, w := got[i], want[i]
		switch {
		case math.IsNaN(w):
			if !math.IsNaN(g) {
				return false
			}
		case math.Abs(g-w) > 1e-12*math.Max(1, math.Abs(w)):
			return false
		}
	}
	return true
}

// TestSkewAndKurtosis is M1 of v0.6.6-scope.md (step 276). Every answer is Polars
// 1.44.1's, to a relative 1e-12: Polars computes the moments in two passes, ursus
// online. A null and a NaN are both NaN to floatsOf, so the nulls are checked apart.
func TestSkewAndKurtosis(t *testing.T) {
	c := ursus.Col
	x := c("x")
	lf := statsFrame()
	byG := func(e ursus.Expr) *ursus.LazyFrame {
		return lf.GroupBy(c("g")).Agg(e).Sort(ursus.Asc(c("g"))).Select(c("x"))
	}
	nan := math.NaN()
	for _, tc := range []struct {
		name   string
		e      ursus.Expr
		all    float64
		groups []float64
	}{
		{"skew", x.Skew(true), 0, []float64{0.38180177416060623, nan, nan}},
		{"skew, unbiased", x.Skew(false), 0, []float64{0.9352195295828243, nan, nan}},
		{"kurtosis", x.Kurtosis(true, true), -0.96, []float64{-1.4999999999999996, nan, nan}},
		{"kurtosis, Pearson's", x.Kurtosis(false, true), 2.04, []float64{1.5000000000000004, nan, nan}},
		{"kurtosis, unbiased", x.Kurtosis(true, false), -0.29999999999999893, []float64{nan, nan, nan}},
	} {
		if got := floatsOf(t, lf.Select(tc.e)); !nearFloats(got, []float64{tc.all}) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.all)
		}
		if got := floatsOf(t, byG(tc.e)); !nearFloats(got, tc.groups) {
			t.Errorf("%s by g: got %v, want %v", tc.name, got, tc.groups)
		}
	}
	// Polars' nulls: the unbiased skew below 3 values, the unbiased kurtosis below 4.
	nulls := byG(x.Skew(false).IsNull().Alias("x"))
	if got := csvOf(t, nulls); got != "x\nfalse\ntrue\ntrue\n" {
		t.Errorf("unbiased skew's nulls: %q", got)
	}
	if got := csvOf(t, byG(x.Kurtosis(true, false).IsNull().Alias("x"))); got != "x\ntrue\ntrue\ntrue\n" {
		t.Errorf("unbiased kurtosis's nulls: %q", got)
	}
	if got := csvOf(t, byG(x.Skew(true).IsNull().Alias("x"))); got != "x\nfalse\nfalse\nfalse\n" {
		t.Errorf("skew of one value is NaN, not null: %q", got)
	}
	if got := floatsOf(t, lf.Select(c("i").Skew(true))); !nearFloats(got, []float64{0.31101699785880416}) {
		t.Errorf("skew of integers: %v", got)
	}
	// Large values close together: the shift keeps their spread's digits.
	near := ursus.Frame(ursus.Values("x", []float64{1e9 + 1, 1e9 + 2, 1e9 + 4}))
	if got := floatsOf(t, near.Select(x.Skew(true))); !nearFloats(got, []float64{0.38180177416060623}) {
		t.Errorf("skew of 1e9+1, 1e9+2, 1e9+4: %v", got)
	}
	// Two thousand values near 1e9: unshifted, the running mean rounds and the skew is
	// wrong in its ninth digit. The answers are the exact ones, in rationals.
	far := make([]float64, 2000)
	for i := range far {
		far[i] = 1e9 + float64((i*i)%7) + float64(i%3)/4
	}
	if got := floatsOf(t, ursus.Frame(ursus.Values("x", far)).Select(x.Skew(true))); !nearFloats(got, []float64{0.2941145619267025}) {
		t.Errorf("skew near 1e9: %v", got)
	}
	if got := floatsOf(t, ursus.Frame(ursus.Values("x", far)).Select(x.Kurtosis(false, true))); !nearFloats(got, []float64{1.8354638681151405}) {
		t.Errorf("kurtosis near 1e9: %v", got)
	}
	// Merged from partial groups as a parallel group-by merges them.
	many := make([]float64, 4000)
	for i := range many {
		many[i] = math.Mod(float64(i*i), 97) + float64(i%7)/3
	}
	one, err := ursus.Frame(ursus.Values("x", many)).Select(x.Skew(true), x.Kurtosis(true, true).Alias("k")).
		Collect(t.Context(), ursus.WithThreads(1))
	if err != nil {
		t.Fatal(err)
	}
	four, err := ursus.Frame(ursus.Values("x", many)).Select(x.Skew(true), x.Kurtosis(true, true).Alias("k")).
		Collect(t.Context(), ursus.WithThreads(4))
	if err != nil {
		t.Fatal(err)
	}
	if one.String() != four.String() {
		t.Errorf("one thread and four disagree:\n%s\n%s", one, four)
	}
	// All null, and NaN.
	null := ursus.Frame(ursus.ValuesNullable("x", []float64{0, 0}, []bool{false, false}))
	if got := csvOf(t, null.Select(x.Skew(true), x.Kurtosis(true, true).Alias("k"))); got != "x,k\n,\n" {
		t.Errorf("all null: %q", got)
	}
	withNaN := ursus.Frame(ursus.Values("x", []float64{1, math.NaN(), 3}))
	if got := floatsOf(t, withNaN.Select(x.Skew(true))); !math.IsNaN(got[0]) {
		t.Errorf("NaN: %v", got)
	}
	if _, err := lf.Select(c("g").Cast(ursus.String).Skew(true)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "numeric") {
		t.Errorf("of text: %v", err)
	}
}

// TestBitwiseAndSmallAggregates: the bitwise reductions, has_nulls, is_empty,
// nan_min and nan_max, approx_n_unique and dot. Every answer is Polars 1.44.1's.
func TestBitwiseAndSmallAggregates(t *testing.T) {
	c := ursus.Col
	lf := statsFrame()
	byG := func(e ursus.Expr) *ursus.LazyFrame {
		return lf.GroupBy(c("g")).Agg(e.Alias("r")).Sort(ursus.Asc(c("g"))).Select(c("r"))
	}
	for _, tc := range []struct {
		name        string
		e           ursus.Expr
		all, groups string
	}{
		{"bitwise_and", c("i").BitwiseAnd(), "0", "0\n8\n7"},
		{"bitwise_or", c("i").BitwiseOr(), "15", "7\n14\n7"},
		{"bitwise_xor", c("i").BitwiseXor(), "1", "0\n6\n7"},
		{"has_nulls", c("x").HasNulls(), "true", "true\nfalse\nfalse"},
		{"is_empty", c("x").IsEmpty(), "false", "false\nfalse\nfalse"},
		{"nan_min", c("x").NanMin(), "1.0", "1.0\n3.0\n5.0"},
		{"approx_n_unique", c("i").ApproxNUnique(), "7", "4\n2\n1"},
		{"dot", c("x").Dot(c("y")), "35.0", "24.0\n6.0\n5.0"},
	} {
		if got, want := csvOf(t, lf.Select(tc.e.Alias("r"))), "r\n"+tc.all+"\n"; got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, want)
		}
		if got, want := csvOf(t, byG(tc.e)), "r\n"+tc.groups+"\n"; got != want {
			t.Errorf("%s by g:\ngot  %q\nwant %q", tc.name, got, want)
		}
	}
	// Booleans, the input's integer type, nulls alone, an empty frame, NaN.
	b := ursus.Frame(ursus.ValuesNullable("b", []bool{true, false, true, false}, []bool{true, true, true, false}))
	if got := csvOf(t, b.Select(c("b").BitwiseAnd().Alias("a"), c("b").BitwiseOr().Alias("o"),
		c("b").BitwiseXor().Alias("x"))); got != "a,o,x\nfalse,true,false\n" {
		t.Errorf("Booleans: %q", got)
	}
	small := ursus.Frame(ursus.Values("v", []int64{1, 2, 3})).Select(c("v").Cast(ursus.Int8).Alias("a"),
		c("v").Cast(ursus.Uint16).Alias("u"))
	got, err := small.Select(c("a").BitwiseOr(), c("u").BitwiseXor()).Collect(t.Context())
	if err != nil || got.Schema().Field(0).Type != ursus.Int8 || got.Schema().Field(1).Type != ursus.Uint16 {
		t.Errorf("the input's type: %v, %v", got, err)
	}
	if got := csvOf(t, small.Select(c("a").BitwiseAnd().Alias("neg"))); got != "neg\n0\n" {
		t.Errorf("Int8: %q", got)
	}
	nulls := ursus.Frame(ursus.ValuesNullable("i", []int64{0, 0}, []bool{false, false}))
	if got := csvOf(t, nulls.Select(c("i").BitwiseAnd().Alias("a"), c("i").HasNulls().Alias("h"),
		c("i").ApproxNUnique().Alias("n"), c("i").IsEmpty().Alias("e"))); got != "a,h,n,e\n,true,1,false\n" {
		t.Errorf("all null: %q", got)
	}
	empty := ursus.Frame(ursus.Values("i", []int64{})).Filter(c("i").Gt(0))
	if got := csvOf(t, empty.Select(c("i").BitwiseOr().Alias("o"), c("i").IsEmpty().Alias("e"),
		c("i").ApproxNUnique().Alias("n"))); got != "o,e,n\n,true,0\n" {
		t.Errorf("no rows: %q", got)
	}
	nan := ursus.Frame(ursus.Values("x", []float64{1, math.NaN(), 3}))
	if got := csvOf(t, nan.Select(c("x").NanMin().Alias("nmin"), c("x").Min().Alias("min"),
		c("x").NanMax().Alias("nmax"))); got != "nmin,min,nmax\nNaN,1.0,NaN\n" {
		t.Errorf("NaN: %q", got)
	}
	ints := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("b", []int64{3, 4}))
	if got := csvOf(t, ints.Select(c("a").Dot(c("b")))); got != "a\n11\n" {
		t.Errorf("an integer dot: %q", got)
	}
	for name, e := range map[string]ursus.Expr{
		"a float's bits": c("x").BitwiseAnd(),
		"an Int128's":    c("i").Cast(ursus.Int128).BitwiseOr(),
	} {
		if _, err := lf.Select(e).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "integer or a Boolean") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
