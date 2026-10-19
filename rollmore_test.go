package ursus_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// Tier 2 of v0.6.6-scope.md (step 284): rolling_skew, rolling_kurtosis, rolling_rank,
// rolling_cov and rolling_corr. Every answer is Polars 1.44.1's: the moments and the
// ranks by their bits, the covariance and correlation within a tolerance, as Polars'
// rolling means are its own.

func rollFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.ValuesNullable("x", []float64{1, 4, 2, 8, 0, 3, 3, 9, -1, 5},
			[]bool{true, true, true, true, false, true, true, true, true, true}),
		ursus.ValuesNullable("i", []int64{1, 4, 2, 8, 0, 3, 3, 9, -1, 5},
			[]bool{true, true, true, true, false, true, true, true, true, true}),
		ursus.ValuesNullable("y", []float64{2, 1, 5, 3, 4, 0, 6, 2, 0, 7},
			[]bool{true, true, true, true, true, false, true, true, true, true}),
		ursus.Values("g", []int64{1, 1, 1, 1, 1, 2, 2, 2, 2, 2}))
}

func TestRollingSkewAndKurtosis(t *testing.T) {
	c := ursus.Col
	x := c("x")
	nan := math.NaN()
	lf := rollFrame()
	big := ursus.Frame(ursus.Values("x", []float64{1e8 + 1, 1e8 + 4, 1e8 + 2, 1e8 + 8, 1e8 + 3, 1e8 + 3, 1e8 + 9}))
	alike := ursus.Frame(ursus.Values("x", []float64{1, 0.1, 0.1, 0.1, 0.1, 0.1, 7.3, 7.3, 7.3, 7.3}))
	up := math.Nextafter(1e8, math.Inf(1))
	ulp := ursus.Frame(ursus.Values("x", []float64{1e8, up, 1e8, up, up, 5}))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"skew", lf.Select(x.RollingSkew(4, true)),
			[]any{nil, nil, nil, 0.6568077344996992, nil, nil, nil, nil, 0.4118470835376499, -3.789790336082103e-17}},
		{"skew unbiased", lf.Select(x.RollingSkew(4, false)),
			[]any{nil, nil, nil, 1.1376243669576886, nil, nil, nil, nil, 0.7133400736362734, -6.564109412127733e-17}},
		{"skew from 2", lf.Select(x.RollingSkew(4, true, ursus.MinSamples(2))),
			[]any{nil, 0.0, 0.3818017741606063, 0.6568077344996992, 0.3818017741606063, 0.6309038567106235,
				0.7071067811865477, 0.7071067811865475, 0.4118470835376499, -3.789790336082103e-17}},
		{"kurtosis", lf.Select(x.RollingKurtosis(4, true, true)),
			[]any{nil, nil, nil, -1.0989792060491501, nil, nil, nil, nil, -0.9619377162629759, -1.1479289940828403}},
		{"kurtosis unbiased", lf.Select(x.RollingKurtosis(4, true, false)),
			[]any{nil, nil, nil, 0.7576559546313728, nil, nil, nil, nil, 1.7854671280276797, 0.3905325443786989}},
		{"kurtosis, Pearson's", lf.Select(x.RollingKurtosis(4, false, true)),
			[]any{nil, nil, nil, 1.9010207939508499, nil, nil, nil, nil, 2.038062283737024, 1.8520710059171597}},
		{"kurtosis from 1", lf.Select(x.RollingKurtosis(4, true, true, ursus.MinSamples(1))),
			[]any{nan, -2.0, -1.5, -1.0989792060491501, -1.5, -1.5000000000000004, -1.4999999999999993, -1.5,
				-0.9619377162629759, -1.1479289940828403}},
		{"skew of integers", lf.Select(c("i").RollingSkew(3, true)),
			[]any{nil, nil, 0.3818017741606063, 0.38180177416060623, nil, nil, nil, 0.7071067811865475,
				0.2390631469295448, -0.23906314692954475}},
		{"skew centred", lf.Select(x.RollingSkew(3, true, ursus.RollingCenter())),
			[]any{nil, 0.3818017741606063, 0.38180177416060623, nil, nil, nil, 0.7071067811865475,
				0.2390631469295448, -0.23906314692954475, nil}},
		{"skew over g", lf.Select(x.RollingSkew(3, true).Over(c("g"))),
			[]any{nil, nil, 0.3818017741606063, 0.38180177416060623, nil, nil, nil, 0.7071067811865475,
				0.2390631469295448, -0.2390631469295448}},
		{"alike, NaN", ursus.Frame(ursus.Values("x", []float64{2, 2, 2, 2, 5})).Select(x.RollingSkew(3, true)),
			[]any{nil, nil, nan, nan, 0.7071067811865475}},
		{"alike, kurtosis NaN", ursus.Frame(ursus.Values("x", []float64{2, 2, 2, 2, 5})).Select(x.RollingKurtosis(3, true, true)),
			[]any{nil, nil, nan, nan, -1.5}},
		{"a NaN in the window", ursus.Frame(ursus.Values("x", []float64{1, nan, 2, 3, 4, 5})).Select(x.RollingSkew(3, true)),
			[]any{nil, nil, nan, nan, 0.0, 0.0}},
		{"a NaN in the window, kurtosis", ursus.Frame(ursus.Values("x", []float64{1, nan, 2, 3, 4, 5})).Select(x.RollingKurtosis(3, true, true)),
			[]any{nil, nil, nan, nan, -1.5, -1.5}},
		// Near 1e8 the merged moments' rounding shows, and is Polars'.
		{"near 1e8", big.Select(x.RollingSkew(4, true)),
			[]any{nil, nil, nil, 0.6568077353301547, 0.8331504058926565, 1.0466216984729968, 0.04838227307971588}},
		{"near 1e8, kurtosis", big.Select(x.RollingKurtosis(4, true, true)),
			[]any{nil, nil, nil, -1.098979203940627, -0.9020177142482173, -0.7355371900826446, -1.9357525282569898}},
		{"near 1e8, centred", big.Select(x.RollingSkew(4, true, ursus.RollingCenter())),
			[]any{nil, nil, 0.6568077353301547, 0.8331504058926565, 1.0466216984729968, 0.04838227307971588, nil}},
		{"one row", ursus.Frame(ursus.Values("x", []float64{1, 2})).Select(x.RollingSkew(1, true)), []any{nan, nan}},
		// Alike after a different value: the merged moments' spread is zero only to
		// the mean's precision, which Polars' test takes as zero.
		{"alike after another", alike.Select(x.RollingSkew(3, true)),
			[]any{nil, nil, 0.7071067811865475, nan, nan, nan, 0.7071067811865475, -0.7071067811865474, nan, nan}},
		{"alike after another, kurtosis", alike.Select(x.RollingKurtosis(3, true, true)),
			[]any{nil, nil, -1.5, nan, nan, nan, -1.5000000000000002, -1.5000000000000004, nan, nan}},
		// An ulp apart: a spread of zero to the mean's precision, NaN as Polars'.
		{"an ulp apart", ulp.Select(x.RollingSkew(2, true)), []any{nil, nan, nan, nan, nan, 0.0}},
		{"an ulp apart, 3", ulp.Select(x.RollingSkew(3, true)), []any{nil, nil, nan, nan, nan, -0.7071067811865476}},
		{"an ulp apart, kurtosis", ulp.Select(x.RollingKurtosis(3, true, true)), []any{nil, nil, nan, nan, nan, -1.4999999999999998}},
		{"alike after another, 4", alike.Select(x.RollingSkew(4, true)),
			[]any{nil, nil, nil, 1.1547005383792517, nan, nan, 1.1547005383792515, 0.0, -1.1547005383792512, nan}},
	} {
		if got := typedCells[float64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	// 300 rows of sevenths, a jump of 1e6 every 25 and a null every 37th, against
	// Polars' answers by a fingerprint of their bits: the sum of each answer's bits,
	// and how many are null. Each merge's every rounding counts. The skew's x^1.5 is
	// glibc's pow there, which errs by up to 0.52 ulp: at row 201 of the skew of 5
	// it is 0.503 ulp from the exact value, where pow15's is 0.497, so that skew is
	// an ulp apart, and its fingerprint by one.
	xs, xok := make([]float64, 300), make([]bool, 300)
	for i := range xs {
		xs[i], xok[i] = float64(i*7919%1000)/7, i%37 != 5
		if i%50 < 25 {
			xs[i] += 1e6
		}
	}
	long := ursus.Frame(ursus.ValuesNullable("x", xs, xok))
	for _, tc := range []struct {
		name  string
		e     ursus.Expr
		sum   uint64
		nulls int
	}{
		{"skew, 5", x.RollingSkew(5, true), 857786169809949513 + 1, 44},
		{"kurtosis, 7", x.RollingKurtosis(7, true, true), 3808049609424043135, 61},
		{"kurtosis, 7, unbiased from 4", x.RollingKurtosis(7, true, false, ursus.MinSamples(4)), 4161455935930362652, 3},
		{"skew, 9, centred from 3", x.RollingSkew(9, true, ursus.RollingCenter(), ursus.MinSamples(3)), 1342547013668652064, 0},
	} {
		var sum uint64
		nulls := 0
		for _, v := range typedCells[float64](t, long.Select(tc.e)) {
			if v == nil {
				nulls++
				continue
			}
			sum += math.Float64bits(v.(float64))
		}
		if sum != tc.sum || nulls != tc.nulls {
			t.Errorf("%s: bits sum to %d with %d nulls, Polars' to %d with %d", tc.name, sum, nulls, tc.sum, tc.nulls)
		}
	}
	f32 := lf.Select(c("i").Cast(ursus.Float32).RollingSkew(3, true))
	if got, want := typedCells[float32](t, f32), []any{nil, nil, float32(0.3818017840385437), float32(0.3818017840385437),
		nil, nil, nil, float32(0.7071067690849304), float32(0.23906314373016357), float32(-0.23906314373016357)}; !sameCells(got, want) {
		t.Errorf("a Float32, a Float32:\ngot  %v\nwant %v", got, want)
	}
	for name, q := range map[string]*ursus.LazyFrame{
		// Polars: "`rolling_skew` operation not supported for dtype ...".
		"a Boolean": ursus.Frame(ursus.Values("x", []bool{true, false, true})).Select(x.RollingSkew(3, true)),
		"a date":    lf.Select(c("i").Cast(ursus.Date).RollingKurtosis(3, true, true)),
		"a Decimal": lf.Select(c("i").Cast(ursus.Decimal(38, 1)).RollingSkew(3, true)),
		"text":      ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.RollingSkew(1, true)),
	} {
		if _, err := q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "not defined") {
			t.Errorf("%s: got %v, want a refusal", name, err)
		}
	}
}

func TestRollingRank(t *testing.T) {
	c := ursus.Col
	x := c("x")
	nan, negz, inf := math.NaN(), math.Copysign(0, -1), math.Inf(1)
	lf := rollFrame()
	ties := ursus.Frame(ursus.Values("x", []int64{3, 3, 3, 1, 3, 2, 2}))
	centre := ursus.Frame(ursus.Values("x", []float64{5, 1, 4, 2, 3, 0}))
	dup := ursus.Frame(ursus.Values("x", []float64{1, 1, 2, 3, 3, 4, 1}))
	u := func(v ...any) []any {
		for i, x := range v {
			if n, ok := x.(int); ok {
				v[i] = uint32(n)
			}
		}
		return v
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"min", lf.Select(x.RollingRank(4, ursus.RankMin)), u(nil, nil, nil, 4, nil, nil, nil, nil, 1, 3)},
		{"max", lf.Select(x.RollingRank(4, ursus.RankMax)), u(nil, nil, nil, 4, nil, nil, nil, nil, 1, 3)},
		{"dense", lf.Select(x.RollingRank(4, ursus.RankDense)), u(nil, nil, nil, 4, nil, nil, nil, nil, 1, 3)},
		{"integers", lf.Select(c("i").RollingRank(3, ursus.RankMin)), u(nil, nil, 2, 3, nil, nil, nil, 3, 1, 2)},
		{"ties, min", ties.Select(x.RollingRank(3, ursus.RankMin)), u(nil, nil, 1, 1, 2, 2, 1)},
		{"ties, max", ties.Select(x.RollingRank(3, ursus.RankMax)), u(nil, nil, 3, 1, 3, 2, 2)},
		{"ties, dense", ties.Select(x.RollingRank(3, ursus.RankDense)), u(nil, nil, 1, 1, 2, 2, 1)},
		// A tie below the value: dense counts it once, min twice.
		{"dense past a tie", dup.Select(x.RollingRank(4, ursus.RankDense)), u(nil, nil, nil, 3, 3, 3, 1)},
		{"min past a tie", dup.Select(x.RollingRank(4, ursus.RankMin)), u(nil, nil, nil, 4, 3, 4, 1)},
		{"from 1", lf.Select(x.RollingRank(4, ursus.RankMin, ursus.MinSamples(1))), u(1, 2, 2, 4, nil, 2, 1, 3, 1, 3)},
		{"NaN above", ursus.Frame(ursus.Values("x", []float64{1, nan, 2, nan, 0.5})).Select(x.RollingRank(3, ursus.RankMin)),
			u(nil, nil, 2, 2, 1)},
		{"NaN above, max", ursus.Frame(ursus.Values("x", []float64{1, nan, 2, nan, 0.5})).Select(x.RollingRank(3, ursus.RankMax)),
			u(nil, nil, 2, 3, 1)},
		{"-0 with 0", ursus.Frame(ursus.Values("x", []float64{0, negz, 0, inf, -inf})).Select(x.RollingRank(2, ursus.RankMin)),
			u(nil, 1, 1, 2, 1)},
		{"-0 with 0, max", ursus.Frame(ursus.Values("x", []float64{0, negz, 0, inf, -inf})).Select(x.RollingRank(2, ursus.RankMax)),
			u(nil, 2, 2, 2, 1)},
		{"a Float32", ursus.Frame(ursus.Values("x", []float32{1, 2})).Select(x.RollingRank(2, ursus.RankMin)), u(nil, 2)},
		{"centred", centre.Select(x.RollingRank(3, ursus.RankMin, ursus.RollingCenter())), u(nil, 1, 3, 1, 3, nil)},
		{"centred, from 1", centre.Select(x.RollingRank(4, ursus.RankMin, ursus.RollingCenter(), ursus.MinSamples(1))),
			u(2, 1, 3, 2, 3, 1)},
		// Polars answers null at row 1, whose window's last row is null; its own is 2.
		{"centred, a null after", ursus.Frame(ursus.ValuesNullable("x", []float64{1, 2, 0, 4, 0.5}, []bool{true, true, false, true, true})).
			Select(x.RollingRank(3, ursus.RankMin, ursus.RollingCenter(), ursus.MinSamples(1))), u(1, 2, nil, 2, 1)},
	} {
		if got := typedCells[uint32](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"average", lf.Select(x.RollingRank(4, ursus.RankAverage)), []any{nil, nil, nil, 4.0, nil, nil, nil, nil, 1.0, 3.0}},
		{"average of integers", lf.Select(c("i").RollingRank(3, ursus.RankAverage)),
			[]any{nil, nil, 2.0, 3.0, nil, nil, nil, 3.0, 1.0, 2.0}},
		{"ties, average", ties.Select(x.RollingRank(3, ursus.RankAverage)), []any{nil, nil, 2.0, 1.0, 2.5, 2.0, 1.5}},
		{"dates", ursus.Frame(ursus.Values("x", []time.Time{time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC),
			time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)})).
			Select(x.Cast(ursus.Date).RollingRank(2, ursus.RankAverage)), []any{nil, 1.0, 2.0}},
		{"Booleans", ursus.Frame(ursus.Values("x", []bool{true, false, true})).Select(x.RollingRank(2, ursus.RankAverage)),
			[]any{nil, 1.0, 2.0}},
		{"UInt64 past 2^63", ursus.Frame(ursus.Values("x", []uint64{math.MaxUint64, 1 << 63, 5})).Select(x.RollingRank(2, ursus.RankAverage)),
			[]any{nil, 1.0, 1.0}},
		{"centred, average", centre.Select(x.RollingRank(4, ursus.RankAverage, ursus.RollingCenter())),
			[]any{nil, nil, 3.0, 2.0, 3.0, nil}},
	} {
		if got := typedCells[float64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "rolling_rank operation not supported for dtype `str`".
		"text":      {ursus.Frame(ursus.Values("x", []string{"b", "a"})).Select(x.RollingRank(2, ursus.RankMin)), "not defined"},
		"a Decimal": {lf.Select(c("i").Cast(ursus.Decimal(38, 1)).RollingRank(2, ursus.RankMin)), "not defined"},
		// Polars: "rank `method` must be one of {'min', 'max', 'average', 'dense', 'random'}".
		"ordinal": {lf.Select(x.RollingRank(2, ursus.RankOrdinal)), "min, max, average or dense"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

func TestRollingCovAndCorr(t *testing.T) {
	c := ursus.Col
	nan := math.NaN()
	lf := rollFrame()
	// Polars' rolling means and sums are its own, so the cancellation in
	// mean(a·b) − mean(a)·mean(b) leaves last bits apart: 2.3333333333333317 is
	// Polars' covariance where the exact one is 7/3.
	near := func(name string, got, want []any, tol float64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", name, got, want)
		}
		for i, w := range want {
			var g, wf float64
			switch w := w.(type) {
			case nil:
				if got[i] != nil {
					t.Errorf("%s row %d: %v, want null", name, i, got[i])
				}
				continue
			case float64:
				wf = w
				gv, ok := got[i].(float64)
				if !ok {
					t.Errorf("%s row %d: %v, want %v", name, i, got[i], w)
					continue
				}
				g = gv
			case float32:
				wf = float64(w)
				gv, ok := got[i].(float32)
				if !ok {
					t.Errorf("%s row %d: %v, want %v", name, i, got[i], w)
					continue
				}
				g = float64(gv)
			}
			if math.IsNaN(wf) != math.IsNaN(g) || !math.IsNaN(wf) && math.Abs(g-wf) > tol*math.Max(1, math.Abs(wf)) {
				t.Errorf("%s row %d: %v, want %v", name, i, g, wf)
			}
		}
	}
	near("cov", typedCells[float64](t, lf.Select(ursus.RollingCov(c("x"), c("y"), 4))),
		[]any{nil, nil, nil, -0.41666666666666663, nil, nil, nil, nil, nil, 3.6666666666666665}, 1e-12)
	near("corr", typedCells[float64](t, lf.Select(ursus.RollingCorr(c("x"), c("y"), 4))),
		[]any{nil, nil, nil, -0.07881104062391006, nil, nil, nil, nil, nil, 0.26655409641996103}, 1e-12)
	// Polars: "Correlation is invariant of `ddof`".
	near("corr, ddof 0", typedCells[float64](t, lf.Select(ursus.RollingCorr(c("x"), c("y"), 4, ursus.Ddof(0)))),
		[]any{nil, nil, nil, -0.07881104062391006, nil, nil, nil, nil, nil, 0.26655409641996103}, 1e-12)
	near("cov from 2, ddof 0", typedCells[float64](t, lf.Select(ursus.RollingCov(c("x"), c("y"), 4, ursus.MinSamples(2), ursus.Ddof(0)))),
		[]any{nil, -0.75, -0.8888888888888893, -0.3125, -1.333333333333334, -3.0, -3.75, -6.0, 2.222222222222223, 2.75}, 1e-12)
	near("cov of integers", typedCells[float64](t, lf.Select(ursus.RollingCov(c("i"), c("i"), 3))),
		[]any{nil, nil, 2.3333333333333317, 9.333333333333327, nil, nil, nil, 12.0, 25.333333333333336, 25.333333333333336}, 1e-12)
	near("corr of an integer", typedCells[float64](t, lf.Select(ursus.RollingCorr(c("i"), c("y"), 3))),
		[]any{nil, nil, -0.41931393468876754, -0.3273268353539887, nil, nil, nil, nil, 0.21677749238103008, 0.38572713814458875}, 1e-12)
	near("cov of Float32s, a Float32", typedCells[float32](t, lf.Select(ursus.RollingCov(c("x").Cast(ursus.Float32), c("y").Cast(ursus.Float32), 3))),
		[]any{nil, nil, float32(-1.3333332538604736), float32(-1.9999995231628418), nil, nil, nil, nil,
			float32(3.333332061767578), float32(6.999999046325684)}, 1e-6)
	mixed := ursus.Frame(ursus.Values("x", []float64{1, 2, 4}), ursus.Values("y", []float64{3, 1, 2}))
	near("a Float32 and a Float64, a Float64", typedCells[float64](t, mixed.Select(ursus.RollingCov(c("x").Cast(ursus.Float32), c("y"), 2))),
		[]any{nil, -1.0, 1.0}, 1e-12)
	near("corr of Float32s", typedCells[float32](t, mixed.Select(ursus.RollingCorr(c("x").Cast(ursus.Float32), c("y").Cast(ursus.Float32), 3))),
		[]any{nil, nil, float32(-0.32732653617858887)}, 1e-6)
	near("corr, one side alike, NaN", typedCells[float64](t, ursus.Frame(ursus.Values("x", []float64{1, 1, 1}), ursus.Values("y", []float64{3, 1, 2})).
		Select(ursus.RollingCorr(c("x"), c("y"), 2))), []any{nil, nan, nan}, 1e-12)
	near("cov over g, from 2", typedCells[float64](t, lf.Select(ursus.RollingCov(c("x"), c("y"), 3, ursus.MinSamples(2)).Over(c("g")))),
		[]any{nil, -1.5, -1.333333333333334, -2.000000000000001, -6.0, nil, nil, -12.0, 3.333333333333335, 7.000000000000002}, 1e-12)
	if got, want := typedCells[uint32](t, lf.Select(c("x").RollingRank(3, ursus.RankMin, ursus.MinSamples(1)).Over(c("g")))),
		[]any{uint32(1), uint32(2), uint32(2), uint32(3), nil, uint32(1), uint32(1), uint32(3), uint32(1), uint32(2)}; !sameCells(got, want) {
		t.Errorf("rank over g:\ngot  %v\nwant %v", got, want)
	}
	// Polars names it after its first input.
	if s, err := lf.Select(ursus.RollingCov(c("x"), c("y"), 4)).CollectSchema(t.Context()); err != nil || s.Field(0).Name != "x" {
		t.Errorf("rolling_cov's name: %v, %v", s, err)
	}
	if _, err := ursus.Frame(ursus.Values("x", []string{"a"}), ursus.Values("y", []float64{1})).
		Select(ursus.RollingCov(c("x"), c("y"), 1)).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Errorf("cov of text: %v", err)
	}
}

// TestRollingMoreStreamPastTheirLimit: each of step 284's rolling functions has a
// bounded need, its window's rows, so over the whole frame it streams past its limit
// as the rolling functions before it do (step 197), and over a key its partitions
// spill. 20,000 rows in batches of 64 under a 16 KiB limit, against the answer
// without one: the ranks equal, and the moments, covariance and correlation within
// 1e-9. Streamed, a window's state is rebuilt from each batch's history rows, so its
// rounding falls elsewhere and the last bits can differ, as RollingVar's and
// RollingStd's already did (feature-gaps.md §5).
func TestRollingMoreStreamPastTheirLimit(t *testing.T) {
	const n = 20_000
	v, vok, f, k := make([]int64, n), make([]bool, n), make([]float64, n), make([]int64, n)
	for i := range n {
		v[i], vok[i] = int64(i%977)-400, i%300 >= 150 || i%7 != 0
		f[i] = float64(i%61)/4 + float64(i%13)
		k[i] = int64(i % 3)
	}
	frame := ursus.Frame(ursus.ValuesNullable("v", v, vok), ursus.Values("f", f), ursus.Values("k", k))
	c := ursus.Col
	for name, cols := range map[string][]ursus.Expr{
		"the whole frame": {
			c("f").RollingSkew(7, true).Alias("sk"),
			c("v").RollingKurtosis(6, true, false, ursus.RollingCenter(), ursus.MinSamples(4)).Alias("ku"),
			c("v").RollingRank(9, ursus.RankDense).Alias("rd"),
			c("f").RollingRank(5, ursus.RankAverage, ursus.RollingCenter()).Alias("ra"),
			ursus.RollingCov(c("v"), c("f"), 8).Alias("cov"),
			ursus.RollingCorr(c("v"), c("f"), 10, ursus.MinSamples(4)).Alias("corr"),
		},
		"over a key": {
			c("f").RollingSkew(7, false).Over(c("k")).Alias("sk"),
			c("v").RollingRank(9, ursus.RankMax).Over(c("k")).Alias("rm"),
			ursus.RollingCorr(c("v"), c("f"), 10).Over(c("k")).Alias("corr"),
		},
	} {
		lf := frame.WithColumns(cols...)
		want, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s, under a 16 KiB limit: %v", name, err)
		}
		if st.Spills == 0 {
			t.Fatalf("%s answered without spilling, so it proves nothing", name)
		}
		var exact []string
		for _, col := range want.Columns() {
			wc, err := want.Column[float64](col)
			if err != nil {
				exact = append(exact, col)
				continue
			}
			gc, err := got.Column[float64](col)
			if err != nil {
				t.Fatalf("%s: %s is not a Float64 streamed: %v", name, col, err)
			}
			for i := range want.Height() {
				w, wok := wc.Get(i)
				g, gok := gc.Get(i)
				if wok != gok || wok && !(math.IsNaN(w) && math.IsNaN(g)) && math.Abs(g-w) > 1e-9*math.Max(1, math.Abs(w)) {
					t.Fatalf("%s: %s row %d streamed %v (%v), want %v (%v)", name, col, i, g, gok, w, wok)
				}
			}
		}
		keep := make([]ursus.Expr, len(exact))
		for i, col := range exact {
			keep[i] = c(col)
		}
		g, err := got.Select(t.Context(), keep...)
		if err != nil {
			t.Fatal(err)
		}
		w, err := want.Select(t.Context(), keep...)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, g, w)
	}
}
