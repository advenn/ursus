package ursus_test

import (
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// M1 of v0.6.6-scope.md (steps 276 and 277): skew and kurtosis, the bitwise and small
// aggregates, entropy, mode and Spearman's correlation. Their answers are Polars
// 2.1.0's since step 500, recorded in the aggstats group of testdata/oracle and
// spelled in oracle_aggstats_test.go, which took this file's tables. What stays
// asserts the refusals, the four-thread merge, and holds to an exact big.Float the
// moments of large values close together, which Polars 2.1 loses digits of.

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

// exactMoments is the biased skew and Pearson's kurtosis of xs in exact rationals,
// rounded once from a 256-bit float: m3 / m2^1.5 and m4 / m2^2 about the exact mean.
func exactMoments(xs []float64) (skew, kurtosis float64) {
	n := big.NewRat(int64(len(xs)), 1)
	sum := new(big.Rat)
	for _, x := range xs {
		sum.Add(sum, new(big.Rat).SetFloat64(x))
	}
	mean := new(big.Rat).Quo(sum, n)
	m2, m3, m4 := new(big.Rat), new(big.Rat), new(big.Rat)
	for _, x := range xs {
		d := new(big.Rat).Sub(new(big.Rat).SetFloat64(x), mean)
		d2 := new(big.Rat).Mul(d, d)
		m2.Add(m2, d2)
		m3.Add(m3, new(big.Rat).Mul(d2, d))
		m4.Add(m4, new(big.Rat).Mul(d2, d2))
	}
	m2.Quo(m2, n)
	m3.Quo(m3, n)
	m4.Quo(m4, n)
	f := func(r *big.Rat) *big.Float { return new(big.Float).SetPrec(256).SetRat(r) }
	root := new(big.Float).SetPrec(256).Sqrt(f(m2))
	skew, _ = new(big.Float).Quo(f(m3), new(big.Float).Mul(f(m2), root)).Float64()
	kurtosis, _ = new(big.Float).Quo(f(m4), new(big.Float).Mul(f(m2), f(m2))).Float64()
	return skew, kurtosis
}

// TestSkewAndKurtosis holds the moments of large values close together to their exact
// rationals: 1e9+1, 1e9+2 and 1e9+4, whose skew is [1, 2, 4]'s, and two thousand values
// near 1e9, where an unshifted running mean rounds and the skew is wrong in its ninth
// digit; Polars 2.1 answers the first 0.38180167858053743 against 0.38180177416060623,
// its float mean at 1e9 having lost seven digits. Then the merge of partial groups,
// as a parallel group-by merges them, and the refusal of text.
func TestSkewAndKurtosis(t *testing.T) {
	c := ursus.Col
	x := c("x")
	far := make([]float64, 2000)
	for i := range far {
		far[i] = 1e9 + float64((i*i)%7) + float64(i%3)/4
	}
	for name, vals := range map[string][]float64{
		"1e9+1, 1e9+2, 1e9+4":   {1e9 + 1, 1e9 + 2, 1e9 + 4},
		"two thousand near 1e9": far,
	} {
		skew, kurtosis := exactMoments(vals)
		got := floatsOf(t, ursus.Frame(ursus.Values("x", vals)).Select(x.Skew(true)))
		if !nearFloats(got, []float64{skew}) {
			t.Errorf("skew of %s: %v, want the exact %v", name, got, skew)
		}
		got = floatsOf(t, ursus.Frame(ursus.Values("x", vals)).Select(x.Kurtosis(false, true)))
		if !nearFloats(got, []float64{kurtosis}) {
			t.Errorf("kurtosis of %s: %v, want the exact %v", name, got, kurtosis)
		}
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
	if _, err := statsFrame().Select(c("g").Cast(ursus.String).Skew(true)).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "numeric") {
		t.Errorf("of text: %v", err)
	}
}

// TestBitwiseAndSmallAggregates: the bitwise reductions refuse a float and an Int128.
func TestBitwiseAndSmallAggregates(t *testing.T) {
	c := ursus.Col
	lf := statsFrame()
	for name, e := range map[string]ursus.Expr{
		"a float's bits": c("x").BitwiseAnd(),
		"an Int128's":    c("i").Cast(ursus.Int128).BitwiseOr(),
	} {
		if _, err := lf.Select(e).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "integer or a Boolean") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestEntropyModeSpearman: the refusals of M1's second half (step 277).
func TestEntropyModeSpearman(t *testing.T) {
	c := ursus.Col
	lf := statsFrame()
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "length 1 doesn't match the DataFrame height of 3".
		"mode beside a column": {ursus.Frame(ursus.Values("a", []int64{3, 1, 3})).Select(c("a"), c("a").Mode(false).Alias("m")), "length"},
		"entropy of Booleans":  {ursus.Frame(ursus.Values("b", []bool{true})).Select(c("b").Entropy(2, true)), "numeric"},
		"spearman of text":     {lf.Select(ursus.CorrSpearman(c("g").Cast(ursus.String), c("x"))), "numeric"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
