package kernel_test

// An integer mean is the double nearest the exact mean — audit.md A4.
//
// Sum of an integer column is exact: it accumulates in Int128. Mean accumulated in
// float64, so past 2^53 the two disagreed in one query: the mean of 2^53+1 and
// 2^53+2 came out as 2^53, where the nearest double to 2^53+1.5 is 2^53+2. The
// oracle is big.Rat's own nearest-double conversion of sum/count.

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// knownInexactIntMeans counts the wrong means per type.
var knownInexactIntMeans = map[string]int{
	"Int64":  6, // summed in float64: 2^53+1 is already 2^53
	"Uint64": 2,
	"Int128": 6,
}

// meanSets are the value sets each type is averaged over, kept to those the type
// holds: its own and every other type's edges, and sums that a float64 rounds.
func meanSets(t *testing.T, dt dtype.DataType) [][]*big.Int {
	t.Helper()
	p53 := new(big.Int).Lsh(big.NewInt(1), 53)
	add := func(a *big.Int, d int64) *big.Int { return new(big.Int).Add(a, big.NewInt(d)) }
	lo, hi := intRange(dt)
	candidates := [][]*big.Int{
		{add(p53, 1), add(p53, 2)},
		{add(p53, 1), add(p53, 1), add(p53, 1)},
		{hi, hi, hi},
		{lo, hi},
		{hi, add(hi, -1)},
		{lo, lo, big.NewInt(1)},
		{big.NewInt(1), big.NewInt(1), big.NewInt(2)},
	}
	var all []*big.Int
	for _, v := range edges(integerTypes(t)) {
		if inRange(v, dt) {
			all = append(all, v)
		}
	}
	candidates = append(candidates, all)
	var out [][]*big.Int
	for _, set := range candidates {
		ok := true
		for _, v := range set {
			ok = ok && inRange(v, dt)
		}
		if ok {
			out = append(out, set)
		}
	}
	return out
}

func TestIntegerMeanIsTheNearestDouble(t *testing.T) {
	var checked int
	for _, dt := range integerTypes(t) {
		bind, err := expr.ResolveAggBinding(expr.AggMean, dt)
		if err != nil {
			t.Fatal(err)
		}
		var wrong []string
		for _, set := range meanSets(t, dt) {
			sum := new(big.Int)
			for _, v := range set {
				sum.Add(sum, v)
			}
			want, _ := new(big.Rat).SetFrac(sum, big.NewInt(int64(len(set)))).Float64()

			col := intColumn(t, dt, set)
			parts := [][][2]int{{{0, len(set)}}}
			if len(set) > 1 {
				parts = append(parts, [][2]int{{0, 1}, {1, len(set)}})
			}
			for _, part := range parts {
				var first kernel.Accumulator
				for _, p := range part {
					a, err := kernel.NewAccumulator(expr.AggMean, dt, bind, expr.AggParams{})
					if err != nil {
						t.Fatal(err)
					}
					a.Reserve(1)
					groups := make([]int32, p[1]-p[0])
					if err := a.AddBatch(groups, col.Slice(p[0], p[1]-p[0])); err != nil {
						t.Fatal(err)
					}
					if first == nil {
						first = a
					} else if err := first.Merge(a, nil); err != nil {
						t.Fatal(err)
					}
				}
				out, err := first.Finish("m", 1)
				if err != nil {
					t.Fatal(err)
				}
				checked++
				if got := data.MustValues[float64](out)[0]; got != want {
					wrong = append(wrong, fmt.Sprintf("mean of %v %v = %v, want %v", set, part, got, want))
				}
			}
		}
		n, known := knownInexactIntMeans[dt.String()]
		switch {
		case known && len(wrong) == 0:
			t.Errorf("%s means are exact now; delete it from knownInexactIntMeans", dt)
		case known && len(wrong) != n:
			t.Errorf("%s: %d wrong, the ratchet says %d: %v", dt, len(wrong), n, wrong)
		case !known && len(wrong) > 0:
			t.Errorf("%s: %v", dt, wrong)
		}
	}
	if checked < 60 {
		t.Fatalf("only %d means checked", checked)
	}
}
