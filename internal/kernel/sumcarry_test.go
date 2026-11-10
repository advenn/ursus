package kernel_test

// A sum of 128-bit values is refused exactly when its TRUE total does not fit, and
// that answer cannot depend on the order the rows arrive in or on how they were
// split between workers.
//
// Wrapping plus a check on the result is not enough: four values of 2^126+5 total
// 2^128+20, which wraps to 20 — in range, and wrong. A checked add that refused on
// the first wrap is not enough either: [Max, 1, -1] fits, and would be refused in
// the order that adds the 1 first. So every permutation is tried under several
// partitionings, and each must agree with math/big: of the seven lists that show
// each hazard, and of forty generated ones (step 503).

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// sumParts sums vals as one group, each partition in its own accumulator, merged in
// order. It returns the total's rendering, or the refusal.
func sumParts(t *testing.T, dt dtype.DataType, vals []i128.Int128, parts [][2]int) (string, error) {
	t.Helper()
	bind, err := expr.ResolveAggBinding(expr.AggSum, dt)
	if err != nil {
		t.Fatal(err)
	}
	col := data.NewFixed("v", dt, vals, bitmap.AllSet(len(vals)))
	groups := make([]int32, len(vals))
	var first kernel.Accumulator
	for _, p := range parts {
		a, err := kernel.NewAccumulator(expr.AggSum, dt, bind, expr.AggParams{})
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
	out, err := first.Finish("out", 1)
	if err != nil {
		return "", err
	}
	v, err := data.Values[i128.Int128](out)
	if err != nil {
		t.Fatal(err)
	}
	return v[0].String(), nil
}

// permutations calls f with every ordering of vals.
func permutations(vals []i128.Int128, f func([]i128.Int128)) {
	var rec func(int)
	rec = func(k int) {
		if k == len(vals) {
			f(append([]i128.Int128(nil), vals...))
			return
		}
		for i := k; i < len(vals); i++ {
			vals[k], vals[i] = vals[i], vals[k]
			rec(k + 1)
			vals[k], vals[i] = vals[i], vals[k]
		}
	}
	rec(0)
}

// partitionings is one pass, every row alone, and two halves.
func partitionings(n int) [][][2]int {
	alone := make([][2]int, n)
	for i := range n {
		alone[i] = [2]int{i, i + 1}
	}
	return [][][2]int{{{0, n}}, alone, {{0, n / 2}, {n / 2, n}}}
}

func TestInt128SumRefusesExactlyWhenTheTotalDoesNotFit(t *testing.T) {
	parse := func(s string) i128.Int128 {
		v, ok := i128.Parse(s)
		if !ok {
			t.Fatalf("i128.Parse(%q)", s)
		}
		return v
	}
	one, neg := i128.One, i128.FromInt64(-1)
	quarter := parse("85070591730234615865843651857942052869") // 2^126 + 5

	lo, _ := new(big.Int).SetString(i128.Min.String(), 10)
	hi, _ := new(big.Int).SetString(i128.Max.String(), 10)

	var tried, fit, refused int
	for _, c := range append([]sumCase{
		{"max+1-1", []i128.Int128{i128.Max, one, neg}},
		{"min-1+1", []i128.Int128{i128.Min, neg, one}},
		{"both-ways", []i128.Int128{i128.Max, i128.Max, i128.Min, i128.Min}},
		{"max+1", []i128.Int128{i128.Max, one}},
		{"min-1", []i128.Int128{i128.Min, neg}},
		{"full-wrap", []i128.Int128{quarter, quarter, quarter, quarter}},
		{"full-wrap-back", []i128.Int128{quarter, quarter, quarter, quarter, i128.Min, i128.Min}},
	}, generatedSums(t, lo, hi)...) {
		want := new(big.Int)
		for _, v := range c.vals {
			b, _ := new(big.Int).SetString(v.String(), 10)
			want.Add(want, b)
		}
		fits := want.Cmp(lo) >= 0 && want.Cmp(hi) <= 0

		permutations(c.vals, func(order []i128.Int128) {
			for _, parts := range partitionings(len(order)) {
				tried++
				got, err := sumParts(t, dtype.Int128, order, parts)
				switch {
				case fits && err != nil:
					t.Errorf("%s %v %v: refused a total that fits (%s): %v",
						c.name, order, parts, want, err)
				case fits && got != want.String():
					t.Errorf("%s %v %v: = %s, want %s", c.name, order, parts, got, want)
				case !fits && err == nil:
					t.Errorf("%s %v %v: = %s, want a refusal — the true total is %s",
						c.name, order, parts, got, want)
				case fits:
					fit++
				default:
					refused++
				}
			}
		})
	}
	// Both outcomes must actually occur, or "always refuse" and "never refuse" would
	// each pass half of this.
	if fit == 0 || refused == 0 || tried < 5000 {
		t.Fatalf("%d tried, %d fit, %d refused — the sweep has gone vacuous", tried, fit, refused)
	}
}

// sumCase is a list of values summed as one group.
type sumCase struct {
	name string
	vals []i128.Int128
}

// generatedSums is forty seeded lists of two to five values (step 503), each drawn
// near an end of the Int128 range, near a quarter of it, small, or anywhere, so that
// their partial sums wrap one way, the other, both, or not at all: every ordering and
// partitioning of each is held to math/big as the literal cases are.
func generatedSums(t *testing.T, lo, hi *big.Int) []sumCase {
	t.Helper()
	rng := rand.New(rand.NewPCG(503, 128))
	quarter := new(big.Int).Lsh(big.NewInt(1), 126)
	draw := func() *big.Int {
		k := big.NewInt(rng.Int64N(1000))
		switch rng.IntN(6) {
		case 0:
			return new(big.Int).Sub(hi, k)
		case 1:
			return new(big.Int).Add(lo, k)
		case 2:
			return new(big.Int).Add(quarter, k)
		case 3:
			return new(big.Int).Neg(new(big.Int).Add(quarter, k))
		case 4:
			return big.NewInt(rng.Int64N(2001) - 1000)
		}
		v := new(big.Int).Lsh(big.NewInt(rng.Int64()), 64)
		return v.Add(v, new(big.Int).SetUint64(rng.Uint64()))
	}
	var out []sumCase
	for i := range 40 {
		vals := make([]i128.Int128, 2+rng.IntN(4))
		for j := range vals {
			v, ok := i128.Parse(draw().String())
			if !ok {
				t.Fatal("a drawn value is not an Int128")
			}
			vals[j] = v
		}
		out = append(out, sumCase{fmt.Sprintf("generated %d", i), vals})
	}
	return out
}
