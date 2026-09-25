package kernel_test

import (
	"math/big"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// TestFloatAggregatesOverADecimalReadTheScale: every aggregate whose output over a
// Decimal is Float64 must answer what it answers over the same column cast to
// Float64 — which is scaled, and checked value by value in castdec_test.go.
//
// The ops are derived from the enumeration, not listed. What this catches is an
// aggregate that reads a Decimal through some path OTHER than the scaled reader:
// widenFloat used to carry its own copy of the Int128 arm, which a Decimal matched
// first, so every aggregate reading through it got the unscaled integer — 100x too
// large at scale 2, with no error.
func TestFloatAggregatesOverADecimalReadTheScale(t *testing.T) {
	const n, nGroups = 60, 4
	dec := dtype.Decimal(10, 2)
	u := make([]i128.Int128, n)
	groups := make([]int32, n)
	vb := bitmap.NewBuilder(n)
	for i := range n {
		u[i] = i128.FromInt64(int64((i*37)%1000 - 400)) // -4.00 .. 5.99
		groups[i] = int32((i * 7) % nGroups)
		vb.Append(i%11 != 5) // some nulls
	}
	col := data.NewFixed("v", dec, u, vb.Finish())
	floats, err := kernel.Cast("v", dtype.Float64, true, col)
	if err != nil {
		t.Fatal(err)
	}

	var checked int
	for i := range 256 {
		op := expr.AggOp(i)
		if op.String() == "?" {
			continue
		}
		bind, err := expr.ResolveAggBinding(op, dec)
		if err != nil || bind.Out != dtype.Float64 {
			continue
		}
		fbind, err := expr.ResolveAggBinding(op, dtype.Float64)
		if err != nil {
			t.Fatalf("%s over Float64: %v", op, err)
		}
		params := defaultParams(op)
		whole := [][2]int{{0, n}}
		got := accumulate(t, op, params, dec, bind, col, groups, nGroups, whole)
		want := accumulate(t, op, params, dtype.Float64, fbind, floats, groups, nGroups, whole)
		// columnsEqual allows floats 1e-12 relative: mean over a Decimal divides an
		// EXACT sum once, and over a Float64 sums in floating point, so the two may
		// differ in the last bit. A missing scale differs by a factor of 100.
		if err := columnsEqual(want, got); err != nil {
			t.Errorf("%s over %s disagrees with %s over its Float64 cast: %v", op, dec, op, err)
		}
		checked++
	}
	if checked != 6 {
		t.Fatalf("%d Float64 aggregates over a Decimal, want 6 — mean, var, std, "+
			"product, median, quantile", checked)
	}
}

// aggParts runs op over vals as one group, each partition in its own accumulator,
// merged in order.
func aggParts(t *testing.T, op expr.AggOp, dt dtype.DataType, vals []i128.Int128,
	parts [][2]int) (*data.Column, error) {
	t.Helper()
	bind, err := expr.ResolveAggBinding(op, dt)
	if err != nil {
		t.Fatal(err)
	}
	col := data.NewFixed("v", dt, vals, bitmap.AllSet(len(vals)))
	groups := make([]int32, len(vals))
	var first kernel.Accumulator
	for _, p := range parts {
		a, err := kernel.NewAccumulator(op, dt, bind, expr.AggParams{})
		if err != nil {
			t.Fatal(err)
		}
		a.Reserve(1)
		if err := a.AddBatch(groups[p[0]:p[1]], col.Slice(p[0], p[1]-p[0])); err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = a
		} else if err := first.Merge(a, nil); err != nil {
			t.Fatal(err)
		}
	}
	return first.Finish("out", 1)
}

// TestDecimalSumRefusesExactlyPastThirtyEightDigits is the Int128 sweep with the
// tighter bound: a Decimal(38, s) total must stay below 10^38, which is below 2^127,
// so a total can fit the accumulator and still be refused. Every permutation, under
// every partitioning, against math/big.
func TestDecimalSumRefusesExactlyPastThirtyEightDigits(t *testing.T) {
	parse := func(s string) i128.Int128 {
		v, ok := i128.Parse(s)
		if !ok {
			t.Fatalf("i128.Parse(%q)", s)
		}
		return v
	}
	dt := dtype.Decimal(38, 0)
	nines := parse("99999999999999999999999999999999999999") // 10^38 - 1
	big75 := parse("75000000000000000000000000000000000000") // 7.5e37
	big90 := parse("90000000000000000000000000000000000000")
	quarter := parse("85070591730234615865843651857942052869") // 2^126 + 5
	one := i128.One
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil)

	var tried, fit, refused int
	for _, c := range []struct {
		name string
		vals []i128.Int128
	}{
		{"nines", []i128.Int128{nines, one, i128.FromInt64(-1)}},
		{"nines+1", []i128.Int128{nines, one}},
		// Fits 128 bits, not 38 digits: only the precision check refuses it.
		{"1.5e38", []i128.Int128{big75, big75}},
		{"there-and-back", []i128.Int128{big90, big90, big90.Neg()}},
		// Wraps all the way round to 20: only the carry refuses it.
		{"full-wrap", []i128.Int128{quarter, quarter, quarter, quarter}},
	} {
		want := new(big.Int)
		for _, v := range c.vals {
			b, _ := new(big.Int).SetString(v.String(), 10)
			want.Add(want, b)
		}
		fits := new(big.Int).Abs(want).Cmp(limit) < 0

		permutations(c.vals, func(order []i128.Int128) {
			for _, parts := range partitionings(len(order)) {
				tried++
				out, err := aggParts(t, expr.AggSum, dt, order, parts)
				switch {
				case fits && err != nil:
					t.Errorf("%s %v: refused a total that fits (%s): %v", c.name, parts, want, err)
				case !fits && err == nil:
					t.Errorf("%s %v: kept a total of %s", c.name, parts, want)
				case fits:
					fit++
					if got := data.MustValues[i128.Int128](out)[0].String(); got != want.String() {
						t.Errorf("%s %v = %s, want %s", c.name, parts, got, want)
					}
				default:
					refused++
				}
			}
		})
	}
	if fit == 0 || refused == 0 || tried != 120 {
		t.Fatalf("%d tried, %d fit, %d refused — the sweep has gone vacuous", tried, fit, refused)
	}
}

// TestDecimalMeanIsTheNearestDouble: the mean of a Decimal is its exact sum divided
// ONCE, so it must be the double nearest the true mean — whatever path finishDecimal
// took, including a scale past 22, a sum past 2^53 and a sum past 128 bits.
func TestDecimalMeanIsTheNearestDouble(t *testing.T) {
	parse := func(s string) i128.Int128 {
		v, _ := i128.Parse(s)
		return v
	}
	quarter := parse("85070591730234615865843651857942052869")
	for _, c := range []struct {
		dt   dtype.DataType
		vals []i128.Int128
	}{
		{dtype.Decimal(10, 2), []i128.Int128{i128.FromInt64(1234), i128.FromInt64(500),
			i128.FromInt64(-5), i128.FromInt64(10000)}},
		{dtype.Decimal(10, 2), []i128.Int128{i128.FromInt64(1), i128.FromInt64(1), i128.FromInt64(2)}},
		// The mean of three 0.05s is 0.05. Dividing twice — by 10^2, then by 3 —
		// rounds twice and gives 0.049999999999999996; once, by 300, gives 0.05.
		{dtype.Decimal(10, 2), []i128.Int128{i128.FromInt64(5), i128.FromInt64(5), i128.FromInt64(5)}},
		{dtype.Decimal(38, 30), []i128.Int128{i128.FromInt64(1), i128.FromInt64(2)}},
		{dtype.Decimal(20, 0), []i128.Int128{i128.FromInt64(1 << 60), i128.FromInt64(3)}},
		{dtype.Decimal(38, 0), []i128.Int128{quarter, quarter, quarter, quarter}},
	} {
		want := new(big.Int)
		for _, v := range c.vals {
			b, _ := new(big.Int).SetString(v.String(), 10)
			want.Add(want, b)
		}
		den := new(big.Int).Mul(big.NewInt(int64(len(c.vals))),
			new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(c.dt.Scale())), nil))
		exact, _ := new(big.Rat).SetFrac(want, den).Float64()

		for _, parts := range partitionings(len(c.vals)) {
			out, err := aggParts(t, expr.AggMean, c.dt, c.vals, parts)
			if err != nil {
				t.Fatalf("mean over %s refused: %v", c.dt, err)
			}
			if got := data.MustValues[float64](out)[0]; got != exact {
				t.Errorf("mean of %v over %s %v = %v, want %v", c.vals, c.dt, parts, got, exact)
			}
		}
	}
}
