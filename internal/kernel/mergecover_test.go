package kernel_test

// Merge coverage, enumerated rather than listed.
//
// TestMergeEquivalence carries a comment that is verbatim the failure mode this
// file exists to end:
//
//	"Every aggregate defined over Float64. A new op MUST be added here — nothing
//	 enumerates them automatically, and an accumulator whose Merge is wrong is
//	 indistinguishable from one that is right until this test covers it."
//
// Its list omitted AggImplode, AggAny and AggAllTrue, and because it runs over
// Float64 alone it could not reach extremumStr, extremumI128 or extremumBool at
// all. extremumBool.Merge consequently had ZERO coverage while being reachable on
// every parallel group-by by default — the reverseSink.Merge configuration, which
// turned out to be actively wrong the moment someone wrote its test.
//
// AggOp is a uint8 whose String() returns "?" for anything undeclared, so the whole
// space can be walked without the unexported sentinel and without a list. Crossed
// with the dtype families NewAccumulator branches on, this reaches all fourteen
// accumulators, and it picks up op 21 automatically the day it is appended.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// aggFamilies is one column per dtype family NewAccumulator dispatches on.
//
// # Three float-ish families could not see an inexact accumulator
//
// It held Float64, String and Bool, and columnsEqual allows 1e-12 relative for
// floats — legitimately, since a merged partial tree rounds differently from one
// pass. So Merge had never been checked against an EXACT reference for any
// accumulator, and an aggregate that silently accumulated in float64 under an
// integer output type was invisible here by construction.
//
// The exact families below are compared bit for bit, and their values sit AROUND
// 2^53 — the float64 integer-exactness boundary — with one large value and many
// small ones per group, which is the shape that makes a lost addition visible:
// 2^53 + 1 is not representable, so a single pass drops each increment, while a
// merged partial sum of the increments does not.
func aggFamilies(t *testing.T, n int) map[string]struct {
	dt  dtype.DataType
	col *data.Column
} {
	t.Helper()
	valid := bitmap.NewBuilder(n)
	for i := range n {
		valid.Append(i%7 != 3) // a scattering of nulls
	}
	v := valid.Finish()

	f64 := make([]float64, n)
	strs := make([]string, n)
	bools := bitmap.NewBuilder(n)
	for i := range n {
		f64[i] = float64((i * 13) % 11)
		strs[i] = string(rune('a' + (i*5)%6))
		bools.Append(i%3 != 0)
	}

	// One value at the boundary per group, and ones everywhere else.
	const p53 = int64(1) << 53
	big := make([]int64, n)
	days := make([]int32, n)
	wide := make([]i128.Int128, n)
	for i := range n {
		big[i] = 1
		if i%15 == 0 {
			big[i] = p53
		}
		days[i] = int32(i % 9)
		wide[i] = i128.Int128{Hi: 0, Lo: uint64(big[i])}
	}
	fixed := func(dt dtype.DataType) *data.Column {
		return data.NewFixed("v", dt, big, v)
	}

	return map[string]struct {
		dt  dtype.DataType
		col *data.Column
	}{
		"Float64": {dtype.Float64, data.NewFixed("v", dtype.Float64, f64, v)},
		"String":  {dtype.String, data.NewString("v", strs, v)},
		"Bool":    {dtype.Bool, data.NewBool("v", bools.Finish(), v)},

		// Exact families: compared bit for bit by columnsEqual.
		"Int64":        {dtype.Int64, fixed(dtype.Int64)},
		"Int128":       {dtype.Int128, data.NewFixed("v", dtype.Int128, wide, v)},
		"Decimal":      {dtype.Decimal(18, 3), data.NewFixed("v", dtype.Decimal(18, 3), wide, v)},
		"Duration(ns)": {dtype.Duration(dtype.Nano), fixed(dtype.Duration(dtype.Nano))},
		"Duration(s)":  {dtype.Duration(dtype.Second), fixed(dtype.Duration(dtype.Second))},
		"Date":         {dtype.Date, data.NewFixed("v", dtype.Date, days, v)},
		"Datetime":     {dtype.Datetime(dtype.Micro, "UTC"), fixed(dtype.Datetime(dtype.Micro, "UTC"))},
	}
}

// defaultParams supplies the parameters an op cannot resolve without.
//
// Quantile and the variance family are the only ops whose BINDING is unaffected by
// params but whose RESULT is not, so a zero value would silently test only one
// configuration. Everything else ignores params entirely.
func defaultParams(op expr.AggOp) expr.AggParams {
	switch op {
	case expr.AggQuantile:
		return expr.AggParams{Q: 0.75, Interp: expr.InterpLinear}
	case expr.AggVar, expr.AggStd:
		return expr.AggParams{DDof: 1}
	default:
		return expr.AggParams{}
	}
}

// knownInexactMerges names every (op, family) pair whose merged partitions do NOT
// equal a single pass today, measured before any repair.
//
// sum and mean over a Duration accumulate in float64 (agg.go's sumAcc holds []f64
// whenever bind.Acc is not Int128, and ResolveAggBinding gives a Duration sum
// Acc: Duration). Adding 1 to 2^53 is not representable, so a single pass drops each
// increment while a merged partial sum of the increments does not — and since
// aggWorkers parallelises sum by default, the answer depends on the thread count.
//
// Emptied by the commit that makes the accumulator exact. The assertion runs both
// ways from then on: an unlisted disagreement fails, and a listed pair that now
// agrees fails as stale.
var knownInexactMerges = map[string]bool{
	"sum/Duration(ns)":  true,
	"sum/Duration(s)":   true,
	"mean/Duration(ns)": true,
	"mean/Duration(s)":  true,
}

// TestEveryAggregateMergeIsCovered walks the whole AggOp space against every dtype
// family, and for each pair the accumulator accepts, asserts that folding partial
// accumulators equals a single pass.
func TestEveryAggregateMergeIsCovered(t *testing.T) {
	const n, nGroups = 60, 4

	groups := make([]int32, n)
	for i := range n {
		groups[i] = int32((i * 7) % nGroups) // every group spans every partition
	}
	fams := aggFamilies(t, n)

	var ops, pairs int
	agreed := map[string]bool{}
	for i := range 256 {
		op := expr.AggOp(i)
		if op.String() == "?" {
			continue // undeclared
		}
		ops++

		var covered int
		for famName, fam := range fams {
			bind, err := expr.ResolveAggBinding(op, fam.dt)
			if err != nil {
				continue // not defined over this family; that is legal
			}
			params := defaultParams(op)
			if _, err := kernel.NewAccumulator(op, fam.dt, bind, params); err != nil {
				continue // resolvable but unimplemented; the evaluator refuses it
			}
			covered++
			pairs++

			key := op.String() + "/" + famName
			t.Run(key, func(t *testing.T) {
				single := accumulate(t, op, params, fam.dt, bind, fam.col, groups,
					nGroups, [][2]int{{0, n}})
				same := true
				for _, parts := range []int{2, 3, 5} {
					bounds := make([][2]int, parts)
					for p := range parts {
						bounds[p] = [2]int{p * n / parts, (p + 1) * n / parts}
					}
					bounds[parts-1][1] = n
					got := accumulate(t, op, params, fam.dt, bind, fam.col, groups,
						nGroups, bounds)
					// columnsEqual is the package's own comparator: exact for
					// everything but floats, where it allows 1e-12 relative
					// because a merged partial tree legitimately rounds
					// differently from one left-to-right pass.
					err := columnsEqual(single, got)
					if err == nil {
						continue
					}
					same = false
					if !knownInexactMerges[key] {
						t.Errorf("%s: merging %d partitions disagreed with a "+
							"single pass: %v", key, parts, err)
					}
				}
				if same {
					agreed[key] = true
				}
			})
		}

		// An op defined for NO family is one this test cannot see. That is a real
		// gap in the fixture, not a property of the op, so it must be loud.
		if covered == 0 {
			t.Errorf("%s is a declared aggregate that none of the %d dtype families "+
				"accepts — either it needs a family here, or it is unreachable",
				op, len(fams))
		}
	}

	for key := range knownInexactMerges {
		if agreed[key] {
			t.Errorf("%q now merges exactly — delete it from knownInexactMerges", key)
		}
	}

	// Anti-vacuity, both axes. The op walk is over a uint8 space, so a String()
	// that started returning "" for everything would silently skip all of them.
	if ops < 20 {
		t.Fatalf("only %d declared aggregates found — the enumeration has gone "+
			"vacuous", ops)
	}
	if pairs < 90 {
		t.Fatalf("only %d op x family pairs ran — too few for this to mean anything",
			pairs)
	}
	// The exact families are the point of the fixture: without them columnsEqual's
	// float tolerance hides an accumulator that is merely close.
	var exact int
	for _, fam := range fams {
		if !fam.dt.IsFloat() {
			exact++
		}
	}
	if exact < 7 {
		t.Fatalf("only %d exact (non-float) families — a merge can only be checked "+
			"against an exact reference by one of these", exact)
	}
}

// accumulate builds one accumulator per bound, folds them left to right, and
// finishes. A single bound is the reference single pass.
func accumulate(t *testing.T, op expr.AggOp, params expr.AggParams, dt dtype.DataType,
	bind expr.AggBinding, col *data.Column, groups []int32, nGroups int,
	bounds [][2]int,
) *data.Column {
	t.Helper()

	accs := make([]kernel.Accumulator, len(bounds))
	for i, b := range bounds {
		a, err := kernel.NewAccumulator(op, dt, bind, params)
		if err != nil {
			t.Fatal(err)
		}
		a.Reserve(nGroups)
		lo, hi := b[0], b[1]
		if err := a.AddBatch(groups[lo:hi], col.Slice(lo, hi-lo)); err != nil {
			t.Fatal(err)
		}
		accs[i] = a
	}
	// `other` always holds a strictly later slice than the receiver, which is the
	// Merge contract.
	for i := 1; i < len(accs); i++ {
		if err := accs[0].Merge(accs[i], nil); err != nil {
			t.Fatal(err)
		}
	}
	out, err := accs[0].Finish("out", nGroups)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
