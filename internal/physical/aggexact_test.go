package physical

// Aggregates that promise an exact answer, checked on a group of one row.
//
// A one-row group is the smallest question an aggregate can be asked, and it is the
// one every exact aggregate must answer the same way: sum, min, max, first and last
// of a single value are all that value. `sum` over a Duration fails it — the value is
// widened to float64 on the way in (agg.go's widenFloat, before any addition), so a
// group holding 2^53+1 nanoseconds comes back one tick short while min and max beside
// it come back right.
//
// # Derived, not listed
//
// Every AggOp and every WinFnOp is walked through the `String() != "?"` idiom, crossed
// with step 61's allOperandTypes — which TestOperandTypesCoverTheEnum already keeps
// exhaustive over dtype.TypeIDCount. Every declared op must land in exactly one
// behaviour class or this file fails: an op added tomorrow is classified the day it is
// named, rather than silently skipped.
//
// # Why the cross-check exists beside the identity check
//
// The identity property compares an engine answer against a value this file computed.
// The cross-check compares FIVE engine answers against each other, through four
// different accumulators (sumAcc, extremumNum, extremumI128, positionAcc) — so a
// fixture I misunderstood cannot make it pass.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// --- the enums, derived --------------------------------------------------------------

func allAggOps() []expr.AggOp {
	var ops []expr.AggOp
	for i := range 256 {
		if op := expr.AggOp(i); op.String() != "?" {
			ops = append(ops, op)
		}
	}
	return ops
}

func allWinFnOps() []expr.WinFnOp {
	var fns []expr.WinFnOp
	for i := range 256 {
		if fn := expr.WinFnOp(i); fn.String() != "?" {
			fns = append(fns, fn)
		}
	}
	return fns
}

// aggClass is what an aggregate does to a one-row group.
type aggClass int

const (
	classIdentity    aggClass = iota // the value itself: sum, min, max, first, any…
	classImplode                     // a one-element list holding the value
	classCounting                    // a count, independent of the value
	classIndex                       // a row position
	classApproximate                 // declared Float64: var, std, median, quantile, product
)

// classify must account for every declared AggOp. The default is a failure, not a
// skip — the produce-or-excuse discipline TestOperandTypesCoverTheEnum uses one enum
// over.
func classify(op expr.AggOp) (aggClass, bool) {
	switch op {
	case expr.AggSum, expr.AggMean, expr.AggMin, expr.AggMax,
		expr.AggFirst, expr.AggLast, expr.AggAny, expr.AggAllTrue:
		return classIdentity, true
	case expr.AggImplode:
		return classImplode, true
	case expr.AggCount, expr.AggLen, expr.AggNUnique, expr.AggNullCount:
		return classCounting, true
	case expr.AggArgMin, expr.AggArgMax:
		return classIndex, true
	case expr.AggVar, expr.AggStd, expr.AggMedian, expr.AggQuantile, expr.AggProduct:
		return classApproximate, true
	}
	return 0, false
}

func TestEveryAggregateIsClassified(t *testing.T) {
	ops := allAggOps()
	for _, op := range ops {
		if _, ok := classify(op); !ok {
			t.Errorf("%s is a declared aggregate with no behaviour class — the sweep "+
				"cannot say what it should return for a one-row group", op)
		}
	}
	if len(ops) < 20 {
		t.Fatalf("only %d declared aggregates found; the walk has gone vacuous", len(ops))
	}
	if len(allWinFnOps()) < 8 {
		t.Fatalf("only %d declared window functions found", len(allWinFnOps()))
	}
}

// exact reports whether a type promises an exact answer. A Float64 output is a
// DECLARED approximation — AggProduct's binding says so in as many words — so it is
// skipped rather than asserted.
func exactType(d dtype.DataType) bool {
	return d.IsInteger() || d.IsTemporal() || d.ID() == dtype.TypeDecimal ||
		d.ID() == dtype.TypeBool
}

// render prints one row exactly, so two exact columns of different types can be
// compared without either being converted through a float.
func render(c *data.Column, row int) (string, bool) {
	if !c.IsValid(row) {
		return "null", true
	}
	switch c.DType().ID() {
	case dtype.TypeBool:
		return strconv.FormatBool(c.Bools().Get(row)), true
	case dtype.TypeDecimal, dtype.TypeInt128:
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return "", false
		}
		return v[row].String(), true
	}
	switch c.DType().Physical().ID() {
	case dtype.TypeInt64:
		v, err := data.Values[int64](c)
		if err != nil {
			return "", false
		}
		return strconv.FormatInt(v[row], 10), true
	case dtype.TypeInt32:
		v, err := data.Values[int32](c)
		if err != nil {
			return "", false
		}
		return strconv.FormatInt(int64(v[row]), 10), true
	case dtype.TypeUint64:
		v, err := data.Values[uint64](c)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(v[row], 10), true
	case dtype.TypeUint32:
		v, err := data.Values[uint32](c)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(uint64(v[row]), 10), true
	}
	return "", false
}

// aggregate runs one aggregate over a 3-row column whose groups are [0, 1, 2], so
// every group holds exactly one row and group 1's row is null.
func aggregate(op expr.AggOp, in dtype.DataType, col *data.Column) (*data.Column, error) {
	bind, err := expr.ResolveAggBinding(op, in)
	if err != nil {
		return nil, err
	}
	params := expr.AggParams{}
	switch op {
	case expr.AggQuantile:
		params = expr.AggParams{Q: 0.5, Interp: expr.InterpLinear}
	case expr.AggVar, expr.AggStd:
		params = expr.AggParams{DDof: 1}
	}
	acc, err := kernel.NewAccumulator(op, in, bind, params)
	if err != nil {
		return nil, err
	}
	acc.Reserve(3)
	if err := acc.AddBatch([]int32{0, 1, 2}, col); err != nil {
		return nil, err
	}
	return acc.Finish("out", 3)
}

// --- the ratchets ---------------------------------------------------------------------

// knownInexactAggregates is every (op, type) whose one-row answer is NOT its input
// today, measured before any repair.
//
// sum and mean over a Duration accumulate in float64 (agg.go:396-407, :504-511), so a
// value past 2^53 ticks loses precision on the way in and a value past int64's range
// comes back with the wrong SIGN — int64(float64) out of range is
// implementation-defined, which dispatch.go documents as the construct it removed
// from FloorDiv.
var knownInexactAggregates = map[string]bool{
	// Empty since sum and mean accumulate at 128 bits. Eight arms were listed here —
	// sum and mean over a Duration at each of the four units — measured before the
	// repair; see step-62-as-built.md. The check runs both ways, so an arm that goes
	// lossy again must be listed, and a listed one that is exact is stale.
}

// knownWindowTypeConfusions is worse than imprecision: winCumFloat writes a []float64
// into a column declared Duration (window.go:291-298), and data.NewFixed does not
// check the two against each other, so one hour reads back as about 152 years.
var knownWindowTypeConfusions = map[string]bool{
	"cum_sum/Duration(s)":  true,
	"cum_sum/Duration(ms)": true,
	"cum_sum/Duration(us)": true,
	"cum_sum/Duration(ns)": true,
}

// --- the sweep ------------------------------------------------------------------------

func TestAggregatesAreExactOnAOneRowGroup(t *testing.T) {
	var identityArms, nullGroups, agreements int
	var wrong []string
	observed := map[string]bool{}
	exactArms := map[string]bool{}

	for _, in := range allOperandTypes() {
		// A value past float64's integer-exactness boundary, and an ordinary one.
		col := buildOperand("v", in, (1<<53)+1, 7)
		if col == nil {
			continue
		}
		want, ok := render(col, 0)
		if !ok {
			continue // not an exactly renderable input; the identity has no meaning
		}

		for _, op := range allAggOps() {
			class, _ := classify(op)
			bind, err := expr.ResolveAggBinding(op, in)
			if err != nil {
				continue // refused at plan time; an agreed refusal, not a gap
			}
			got, err := aggregate(op, in, col)
			if err != nil {
				continue // resolvable but unimplemented; the evaluator refuses it
			}
			key := op.String() + "/" + in.String()

			// A group whose only row is null has no value to report, so the answer
			// is null — for everything that READS the rows.
			//
			// Two classes are excluded, and neither is an exception to the rule: a
			// counting aggregate counts rather than reads, and implode COLLECTS, so
			// a group holding one null row imploded is a one-element list holding a
			// null, which is a perfectly valid list. That distinction is the same
			// one NewList's doc draws between an empty list and a null list.
			if class != classCounting && class != classImplode {
				nullGroups++
				if got.IsValid(1) {
					t.Errorf("%s: the all-null group came back valid", key)
				}
			}

			if class != classIdentity || !exactType(bind.Out) {
				continue
			}
			exactArms[key] = true
			identityArms++

			out, ok := render(got, 0)
			if !ok {
				t.Errorf("%s: cannot read an exact %s result", key, bind.Out)
				continue
			}
			if out == want {
				continue
			}
			observed[key] = true
			if !knownInexactAggregates[key] {
				wrong = append(wrong, fmt.Sprintf("%s: a one-row group of %s returned %s",
					key, want, out))
			}
		}

		// The cross-check: five aggregates, four accumulators, one row. They must
		// agree with each other whatever this file believes the input to be.
		var answers []string
		for _, op := range []expr.AggOp{expr.AggSum, expr.AggMin, expr.AggMax,
			expr.AggFirst, expr.AggLast} {
			bind, err := expr.ResolveAggBinding(op, in)
			if err != nil || !exactType(bind.Out) {
				continue
			}
			got, err := aggregate(op, in, col)
			if err != nil {
				continue
			}
			if s, ok := render(got, 0); ok {
				answers = append(answers, op.String()+"="+s)
			}
		}
		if len(answers) > 1 {
			agreements++
			first := strings.SplitN(answers[0], "=", 2)[1]
			for _, a := range answers[1:] {
				if v := strings.SplitN(a, "=", 2)[1]; v != first {
					key := "sum/" + in.String()
					observed[key] = true
					if !knownInexactAggregates[key] {
						wrong = append(wrong, fmt.Sprintf(
							"%s: aggregates disagree on a one-row group: %s",
							in, strings.Join(answers, " ")))
					}
				}
			}
		}
	}

	report(t, "aggregate", wrong, observed, knownInexactAggregates)

	// Anti-vacuity.
	if identityArms < 100 {
		t.Errorf("only %d exact identity arms ran", identityArms)
	}
	if nullGroups < 180 {
		t.Errorf("only %d null-group assertions ran", nullGroups)
	}
	if agreements < 20 {
		t.Errorf("only %d cross-checks ran", agreements)
	}
	// The tempting wrong fix for an inexact accumulator is to widen its OUTPUT to
	// Float64, which removes the arm from the exact set instead of failing anything.
	for _, key := range []string{"sum/Duration(ns)", "mean/Duration(ns)",
		"sum/Int64", "min/Duration(ns)"} {
		if !exactArms[key] {
			t.Errorf("%s is no longer an exact arm — if its output type was widened, "+
				"this sweep stopped watching it rather than catching it", key)
		}
	}
	t.Logf("identityArms=%d nullGroups=%d crossChecks=%d inexact=%d",
		identityArms, nullGroups, agreements, len(observed))
}

// TestCumulativesAreExactOnAOneRowPartition is the window half: a cumulative over a
// partition of one row is that row.
func TestCumulativesAreExactOnAOneRowPartition(t *testing.T) {
	var arms int
	var wrong []string
	observed := map[string]bool{}
	seg := kernel.Segments{Perm: []int32{0, 1, 2}, Bounds: []int32{0, 1, 2, 3}}

	for _, in := range allOperandTypes() {
		col := buildOperand("v", in, (1<<53)+1, 7)
		if col == nil {
			continue
		}
		want, ok := render(col, 0)
		if !ok {
			continue
		}
		for _, fn := range allWinFnOps() {
			if !fn.IsCumulative() {
				continue
			}
			out, err := expr.ResolveWinFn(fn, expr.WinParams{}, in)
			if err != nil || !exactType(out) {
				continue
			}
			got, err := kernel.WinCall(fn, expr.WinParams{}, "out", out, col, nil, seg)
			if err != nil {
				continue
			}
			arms++
			key := fn.String() + "/" + in.String()
			s, ok := render(got, 0)
			if !ok {
				t.Errorf("%s: cannot read an exact %s result", key, out)
				continue
			}
			if s == want {
				continue
			}
			observed[key] = true
			if !knownWindowTypeConfusions[key] {
				wrong = append(wrong, fmt.Sprintf(
					"%s: a one-row partition of %s returned %s", key, want, s))
			}
		}
	}

	report(t, "cumulative", wrong, observed, knownWindowTypeConfusions)
	if arms < 40 {
		t.Errorf("only %d cumulative arms ran", arms)
	}
}

// report fails on an unlisted defect and on a listed one that has been fixed.
func report(t *testing.T, what string, wrong []string, observed, known map[string]bool) {
	t.Helper()
	slices.Sort(wrong)
	if len(wrong) > 0 {
		t.Errorf("%d unlisted %s defects:\n  %s", len(wrong), what,
			strings.Join(wrong, "\n  "))
	}
	for key := range known {
		if !observed[key] {
			t.Errorf("%q is listed as broken and is not — delete it", key)
		}
	}
}
