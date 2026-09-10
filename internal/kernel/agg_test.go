package kernel_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

func mkF64(name string, vals []float64, valid []bool) *data.Column {
	b := bitmap.NewBuilder(len(vals))
	for i := range vals {
		b.Append(valid == nil || valid[i])
	}
	return data.NewFixed(name, dtype.Float64, vals, b.Finish())
}

func run(t *testing.T, op expr.AggOp, in dtype.DataType, col *data.Column, groups []int32, nGroups int) *data.Column {
	t.Helper()
	return runP(t, op, expr.AggParams{}, in, col, groups, nGroups)
}

// runP is run with aggregate parameters, for var/std/quantile.
func runP(t *testing.T, op expr.AggOp, params expr.AggParams, in dtype.DataType,
	col *data.Column, groups []int32, nGroups int,
) *data.Column {
	t.Helper()
	bind, err := expr.ResolveAggBinding(op, in)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := kernel.NewAccumulator(op, in, bind, params)
	if err != nil {
		t.Fatal(err)
	}
	acc.Reserve(nGroups)
	if err := acc.AddBatch(groups, col); err != nil {
		t.Fatal(err)
	}
	out, err := acc.Finish("out", nGroups)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSumOfAllNullIsNull pins the decision that separates SQL from Polars.
//
// Polars returns 0 for sum of an empty or all-null group. ursus returns NULL,
// because 0 cannot afterwards be distinguished from a genuine zero — the
// information is destroyed — whereas NULL converts to 0 with FillNull whenever
// that is what was wanted.
func TestSumOfAllNullIsNull(t *testing.T) {
	col := mkF64("v", []float64{0, 0, 1, 2}, []bool{false, false, true, true})
	groups := []int32{0, 0, 1, 1}

	out := run(t, expr.AggSum, dtype.Float64, col, groups, 2)
	s, err := data.TypedColumn[float64](out)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Get(0); ok {
		t.Error("sum of an all-null group must be NULL, not 0")
	}
	if v, ok := s.Get(1); !ok || v != 3 {
		t.Errorf("sum = %v (valid %v), want 3", v, ok)
	}
}

// TestMeanOfAllNullIsNullNotNaN: 0/0 must not be conflated with "missing".
func TestMeanOfAllNullIsNullNotNaN(t *testing.T) {
	col := mkF64("v", []float64{0, 0}, []bool{false, false})
	out := run(t, expr.AggMean, dtype.Float64, col, []int32{0, 0}, 1)
	s, _ := data.TypedColumn[float64](out)

	v, ok := s.Get(0)
	if ok {
		t.Errorf("mean of an all-null group must be NULL, got %v", v)
	}
	if math.IsNaN(v) && ok {
		t.Error("mean must be NULL, not NaN — nothing was computed, as opposed to a computation that was undefined")
	}
}

// TestMinMaxSkipNulls is the regression test for the identity
// trap the step-1 docs record: a SIMD implementation using Masked would zero-fill
// the null lanes and make Min([3,null,5]) return 0.
func TestMinMaxSkipNulls(t *testing.T) {
	col := mkF64("v", []float64{3, 0, 5}, []bool{true, false, true})
	groups := []int32{0, 0, 0}

	lo, _ := data.TypedColumn[float64](run(t, expr.AggMin, dtype.Float64, col, groups, 1))
	hi, _ := data.TypedColumn[float64](run(t, expr.AggMax, dtype.Float64, col, groups, 1))

	if v, ok := lo.Get(0); !ok || v != 3 {
		t.Errorf("min = %v (valid %v), want 3 — a zero-filled null would give 0", v, ok)
	}
	if v, ok := hi.Get(0); !ok || v != 5 {
		t.Errorf("max = %v (valid %v), want 5", v, ok)
	}
}

func TestCountVersusLen(t *testing.T) {
	col := mkF64("v", []float64{1, 0, 3}, []bool{true, false, true})
	groups := []int32{0, 0, 0}

	c, _ := data.TypedColumn[uint64](run(t, expr.AggCount, dtype.Float64, col, groups, 1))
	l, _ := data.TypedColumn[uint64](run(t, expr.AggLen, dtype.Float64, col, groups, 1))

	if v, _ := c.Get(0); v != 2 {
		t.Errorf("count = %d, want 2 (non-null values)", v)
	}
	if v, _ := l.Get(0); v != 3 {
		t.Errorf("len = %d, want 3 (rows)", v)
	}
}

// TestNUniqueSkipsNulls pins the second Polars divergence: Polars counts null as a
// distinct value, ursus does not — because every other aggregate here skips nulls
// and an inconsistent n_unique would be a permanent trap.
func TestNUniqueSkipsNulls(t *testing.T) {
	col := mkF64("v", []float64{1, 1, 2, 0, 0}, []bool{true, true, true, false, false})
	out := run(t, expr.AggNUnique, dtype.Float64, col, []int32{0, 0, 0, 0, 0}, 1)
	s, _ := data.TypedColumn[uint64](out)

	if v, _ := s.Get(0); v != 2 {
		t.Errorf("n_unique = %d, want 2 (Polars would say 3 by counting null)", v)
	}
}

// TestNUniqueUsesGroupingEquality: NaN must equal NaN and -0.0 must equal +0.0,
// the same equality group-by uses. IEEE equality would count every NaN separately.
func TestNUniqueUsesGroupingEquality(t *testing.T) {
	col := mkF64("v", []float64{
		math.NaN(), math.NaN(),
		0, math.Copysign(0, -1),
	}, nil)
	out := run(t, expr.AggNUnique, dtype.Float64, col, []int32{0, 0, 0, 0}, 1)
	s, _ := data.TypedColumn[uint64](out)

	// {NaN, 0} — two distinct values, not four.
	if v, _ := s.Get(0); v != 2 {
		t.Errorf("n_unique = %d, want 2 (NaN==NaN and -0.0==+0.0)", v)
	}
}

func TestFirstLastArePositional(t *testing.T) {
	col := mkF64("v", []float64{0, 2, 0}, []bool{false, true, false})
	groups := []int32{0, 0, 0}

	f, _ := data.TypedColumn[float64](run(t, expr.AggFirst, dtype.Float64, col, groups, 1))
	l, _ := data.TypedColumn[float64](run(t, expr.AggLast, dtype.Float64, col, groups, 1))

	// Positional: the first row IS null, so First is null. Skipping nulls would
	// mean First(a) and First(b) could come from different rows.
	if _, ok := f.Get(0); ok {
		t.Error("First must be positional and return the null in row 0")
	}
	if _, ok := l.Get(0); ok {
		t.Error("Last must be positional and return the null in row 2")
	}
}

// TestMergeEquivalence is what makes Merge real rather than dead code.
//
// Nothing calls Merge concurrently yet — it exists for v0.3's parallel and
// spilling aggregation. A Merge written now and first exercised then would be a
// Merge written against forgotten invariants. So: split the input, accumulate the
// partitions independently, merge, and require the result to match a single pass.
//
// The reverse-order pass is the half that catches an accidentally order-dependent
// merge, which is the failure mode for First and Last specifically.
func TestMergeEquivalence(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 13))
	const n = 60
	const nGroups = 4

	vals := make([]float64, n)
	valid := make([]bool, n)
	groups := make([]int32, n)
	for i := range n {
		vals[i] = float64(rng.IntN(20))
		valid[i] = rng.IntN(4) != 0
		groups[i] = int32(rng.IntN(nGroups))
	}
	col := mkF64("v", vals, valid)

	// Every aggregate defined over Float64. A new op MUST be added here — nothing
	// enumerates them automatically, and an accumulator whose Merge is wrong is
	// indistinguishable from one that is right until this test covers it.
	ops := []struct {
		op     expr.AggOp
		params expr.AggParams
	}{
		{op: expr.AggSum}, {op: expr.AggMean}, {op: expr.AggMin}, {op: expr.AggMax},
		{op: expr.AggCount}, {op: expr.AggLen}, {op: expr.AggNUnique},
		{op: expr.AggFirst}, {op: expr.AggLast}, {op: expr.AggNullCount},
		{op: expr.AggProduct}, {op: expr.AggArgMin}, {op: expr.AggArgMax},
		{op: expr.AggMedian},
		{op: expr.AggVar, params: expr.AggParams{DDof: 0}},
		{op: expr.AggVar, params: expr.AggParams{DDof: 1}},
		{op: expr.AggStd, params: expr.AggParams{DDof: 1}},
		{op: expr.AggQuantile, params: expr.AggParams{Q: 0.9, Interp: expr.InterpLinear}},
		{op: expr.AggQuantile, params: expr.AggParams{Q: 0.25, Interp: expr.InterpLower}},
	}

	for _, tc := range ops {
		op, params := tc.op, tc.params
		bind, err := expr.ResolveAggBinding(op, dtype.Float64)
		if err != nil {
			t.Fatal(err)
		}

		// Single pass, the reference.
		single := runP(t, op, params, dtype.Float64, col, groups, nGroups)

		for _, parts := range []int{2, 3, 5} {
			// Partition the input once, then fold the SAME partitioning two ways.
			//
			// Every Merge in both folds has `other` holding a strictly later slice
			// than the receiver, which is the contract. What differs is the
			// ASSOCIATION: left-to-right builds up a growing prefix, right-to-left
			// builds up a growing suffix. An accumulator that quietly assumes the
			// receiver is always the earlier-and-larger side passes the first and
			// fails the second.
			build := func() []kernel.Accumulator {
				accs := make([]kernel.Accumulator, parts)
				bounds := make([]int, parts+1)
				for i := range parts {
					bounds[i] = i * n / parts
				}
				bounds[parts] = n

				for p := range parts {
					a, err := kernel.NewAccumulator(op, dtype.Float64, bind, params)
					if err != nil {
						t.Fatal(err)
					}
					a.Reserve(nGroups)
					lo, hi := bounds[p], bounds[p+1]
					sub := col.Slice(lo, hi-lo)
					if err := a.AddBatch(groups[lo:hi], sub); err != nil {
						t.Fatal(err)
					}
					accs[p] = a
				}
				return accs
			}

			folds := map[string]func([]kernel.Accumulator) kernel.Accumulator{
				// ((a0 ⊕ a1) ⊕ a2) ⊕ a3
				"left-to-right": func(accs []kernel.Accumulator) kernel.Accumulator {
					for p := 1; p < len(accs); p++ {
						if err := accs[0].Merge(accs[p], nil); err != nil {
							t.Fatal(err)
						}
					}
					return accs[0]
				},
				// a0 ⊕ (a1 ⊕ (a2 ⊕ a3))
				"right-to-left": func(accs []kernel.Accumulator) kernel.Accumulator {
					for p := len(accs) - 2; p >= 0; p-- {
						if err := accs[p].Merge(accs[p+1], nil); err != nil {
							t.Fatal(err)
						}
					}
					return accs[0]
				},
			}

			for name, fold := range folds {
				merged, err := fold(build()).Finish("out", nGroups)
				if err != nil {
					t.Fatal(err)
				}
				if err := columnsEqual(single, merged); err != nil {
					t.Errorf("%s: %s merge of %d partitions disagreed with a single pass: %v",
						op, name, parts, err)
				}
			}
		}
	}
}

// TestMergeEquivalenceOverStrings is the coverage the "?" bug was hiding.
//
// TestMergeEquivalence only ever ran on Float64, and columnsEqual rendered every
// other type as "?" — so the string-carrying accumulators (extremumAcc and
// positionAcc both hold a *data.Column, and their merge concatenates through
// assembleRows) had no merge test at all. On this input they now have one, and it
// would have compared "?" to "?" before.
func TestMergeEquivalenceOverStrings(t *testing.T) {
	rng := rand.New(rand.NewPCG(29, 31))
	const n = 48
	const nGroups = 3

	words := []string{"alpha", "beta", "", "delta", "épée", "zzz"}
	vals := make([]string, n)
	groups := make([]int32, n)
	vb := bitmap.NewBuilder(n)
	for i := range n {
		vals[i] = words[rng.IntN(len(words))]
		groups[i] = int32(rng.IntN(nGroups))
		vb.Append(rng.IntN(4) != 0)
	}
	col := data.NewString("v", vals, vb.Finish())

	// Every aggregate defined for String: it selects or counts rather than computes.
	ops := []expr.AggOp{
		expr.AggMin, expr.AggMax, expr.AggFirst, expr.AggLast,
		expr.AggCount, expr.AggLen, expr.AggNUnique,
	}

	for _, op := range ops {
		bind, err := expr.ResolveAggBinding(op, dtype.String)
		if err != nil {
			t.Fatal(err)
		}
		single := run(t, op, dtype.String, col, groups, nGroups)

		for _, parts := range []int{2, 3} {
			accs := make([]kernel.Accumulator, parts)
			for p := range parts {
				a, err := kernel.NewAccumulator(op, dtype.String, bind, expr.AggParams{})
				if err != nil {
					t.Fatal(err)
				}
				a.Reserve(nGroups)
				lo, hi := p*n/parts, (p+1)*n/parts
				if p == parts-1 {
					hi = n
				}
				if err := a.AddBatch(groups[lo:hi], col.Slice(lo, hi-lo)); err != nil {
					t.Fatal(err)
				}
				accs[p] = a
			}
			for p := 1; p < parts; p++ {
				if err := accs[0].Merge(accs[p], nil); err != nil {
					t.Fatal(err)
				}
			}
			merged, err := accs[0].Finish("out", nGroups)
			if err != nil {
				t.Fatal(err)
			}
			if err := columnsEqual(single, merged); err != nil {
				t.Errorf("%s over strings: merging %d partitions disagreed with a single pass: %v",
					op, parts, err)
			}
		}
	}
}

// TestSumOverflowsPast64Bits: the reason integer sums accumulate at 128 bits.
func TestSumOverflowsPast64Bits(t *testing.T) {
	const maxI64 = int64(9223372036854775807)
	vals := []int64{maxI64, maxI64, maxI64}
	col := data.NewFixed("v", dtype.Int64, vals, bitmap.AllSet(3))

	out := run(t, expr.AggSum, dtype.Int64, col, []int32{0, 0, 0}, 1)
	if out.DType() != dtype.Int128 {
		t.Fatalf("sum type = %s, want Int128", out.DType())
	}
	s, err := data.TypedColumn[i128.Int128](out)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.Get(0)
	if got, want := v.String(), "27670116110564327421"; got != want {
		t.Errorf("sum = %s, want %s (an int64 accumulator would have wrapped negative)", got, want)
	}
}

func columnsEqual(a, b *data.Column) error {
	if a.Len() != b.Len() {
		return errf("lengths %d vs %d", a.Len(), b.Len())
	}
	if a.DType() != b.DType() {
		return errf("types %s vs %s", a.DType(), b.DType())
	}
	for i := range a.Len() {
		av, bv := a.IsValid(i), b.IsValid(i)
		if av != bv {
			return errf("row %d validity %v vs %v", i, av, bv)
		}
		if !av {
			continue
		}
		// Floats compare with a relative tolerance; everything else exactly.
		//
		// Floating-point addition is not associative, so a merged partial tree
		// legitimately rounds differently from a single left-to-right pass —
		// var() over this fixture differs in the last ULP. Demanding bit-identity
		// would be testing a property IEEE 754 does not have.
		//
		// The existing ops passed an exact comparison only because the fixture is
		// float64(rng.IntN(20)): sums and means of small whole numbers are exact, so
		// the question never arose. It arises for var, std, product and quantile,
		// which divide, multiply and take square roots.
		//
		// 1e-12 relative is roughly 1e4 ULP — far above rounding noise, and far
		// below any real merge bug. Dropping Chan's correction term from varAcc.Merge
		// changes the answer by tens of percent, not by 1e-12.
		if a.DType().IsFloat() {
			av, err := floatAt(a, i)
			if err != nil {
				return err
			}
			bv, err := floatAt(b, i)
			if err != nil {
				return err
			}
			if !closeEnough(av, bv) {
				return errf("row %d: %v vs %v", i, av, bv)
			}
			continue
		}

		as, err := renderRow(a, i)
		if err != nil {
			return err
		}
		bs, err := renderRow(b, i)
		if err != nil {
			return err
		}
		if as != bs {
			return errf("row %d: %s vs %s", i, as, bs)
		}
	}
	return nil
}

// renderRow stringifies one value, and ERRORS on a type it does not understand.
//
// It used to return "?" for anything outside {float64, uint64, Int128}, which made
// columnsEqual compare "?" against "?" and pass. Every aggregate added since would
// have been checked by a test that could not fail: Bool (any/all_true), Int64
// (a Duration sum), Uint32, and String (min/max over text) all rendered identically
// to each other and to a genuinely wrong answer.
//
// A comparison helper that cannot express "I don't know" is worse than no helper.
func renderRow(c *data.Column, i int) (string, error) {
	switch c.DType().ID() {
	case dtype.TypeBool:
		return strconv.FormatBool(c.Bools().Get(i)), nil
	case dtype.TypeString, dtype.TypeBinary, dtype.TypeEnum:
		return strconv.Quote(c.Strings().Get(i)), nil
	}

	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return fixedRow[int8](c, i)
	case dtype.TypeInt16:
		return fixedRow[int16](c, i)
	case dtype.TypeInt32:
		return fixedRow[int32](c, i)
	case dtype.TypeInt64:
		return fixedRow[int64](c, i)
	case dtype.TypeUint8:
		return fixedRow[uint8](c, i)
	case dtype.TypeUint16:
		return fixedRow[uint16](c, i)
	case dtype.TypeUint32:
		return fixedRow[uint32](c, i)
	case dtype.TypeUint64:
		return fixedRow[uint64](c, i)
	case dtype.TypeFloat32:
		return fixedRow[float32](c, i)
	case dtype.TypeFloat64:
		return fixedRow[float64](c, i)
	case dtype.TypeInt128:
		s, err := data.TypedColumn[i128.Int128](c)
		if err != nil {
			return "", err
		}
		v, _ := s.Get(i)
		return v.String(), nil
	default:
		return "", errf("agg_test: renderRow cannot compare %s — teach it this type "+
			"rather than letting the comparison pass vacuously", c.DType())
	}
}

func floatAt(c *data.Column, i int) (float64, error) {
	if c.DType().ID() == dtype.TypeFloat32 {
		s, err := data.TypedColumn[float32](c)
		if err != nil {
			return 0, err
		}
		v, _ := s.Get(i)
		return float64(v), nil
	}
	s, err := data.TypedColumn[float64](c)
	if err != nil {
		return 0, err
	}
	v, _ := s.Get(i)
	return v, nil
}

// closeEnough treats NaN as equal to NaN, matching ursus's total order, and allows
// a relative difference of 1e-12 elsewhere.
func closeEnough(a, b float64) bool {
	if math.IsNaN(a) && math.IsNaN(b) {
		return true
	}
	if a == b {
		return true
	}
	d := math.Abs(a - b)
	return d <= 1e-12*max(1, math.Abs(a), math.Abs(b))
}

func fixedRow[T data.Fixed](c *data.Column, i int) (string, error) {
	s, err := data.TypedColumn[T](c)
	if err != nil {
		return "", err
	}
	v, _ := s.Get(i)
	return fmt.Sprintf("%v", v), nil
}

func errf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// TestMinOverAnAllNullGroup pins a bug that shipped in step 2 and was found while
// building joins.
//
// assembleRows placed a data.NewNull placeholder for a group that produced no
// value, then concatenated it — and data.NewNull has no payload buffer, so the
// concat failed with "column has no fixed-width payload". Min over a group whose
// values are all null is an ordinary query, and it reported as an internal error.
//
// The existing Min/Max tests all had at least one non-null value in the group,
// which is exactly why this survived.
func TestMinOverAnAllNullGroup(t *testing.T) {
	// Group 0 is entirely null; group 1 has values.
	col := mkF64("v", []float64{0, 0, 3, 5}, []bool{false, false, true, true})
	groups := []int32{0, 0, 1, 1}

	for _, op := range []expr.AggOp{expr.AggMin, expr.AggMax, expr.AggFirst, expr.AggLast} {
		out := run(t, op, dtype.Float64, col, groups, 2)
		s, err := data.TypedColumn[float64](out)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if _, ok := s.Get(0); ok {
			t.Errorf("%s of an all-null group must be null", op)
		}
		if v, ok := s.Get(1); !ok || (op == expr.AggMin && v != 3) || (op == expr.AggMax && v != 5) {
			t.Errorf("%s of the second group = %v (valid %v)", op, v, ok)
		}
	}
}

// TestMinOverAnAllNullStringGroup is the same bug on the path that PANICKED
// rather than erroring: a zero-length StringAccessor indexes an empty slice.
func TestMinOverAnAllNullStringGroup(t *testing.T) {
	b := bitmap.NewBuilder(2)
	b.Append(false)
	b.Append(true)
	col := data.NewString("s", []string{"", "z"}, b.Finish())

	out := run(t, expr.AggMin, dtype.String, col, []int32{0, 1}, 2)
	s, err := data.TypedColumn[string](out)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(0); ok {
		t.Error("min of an all-null string group must be null")
	}
	if v, ok := s.Get(1); !ok || v != "z" {
		t.Errorf("min = %q (valid %v), want \"z\"", v, ok)
	}
}

// n_unique keeps one shared table keyed by GROUP ++ VALUE rather than a map per
// group, so the framing of that key is now load-bearing in a way it never was. The
// three tests below exist for mistakes the old representation could not make.

// strCol builds a String column, for the framing cases where the value's BYTES are
// the point.
func strCol(vals []string) *data.Column {
	return data.NewString("v", vals, bitmap.AllSet(len(vals)))
}

// TestNUniqueSeparatesGroupsThatShareValues is the first thing a missing group
// prefix breaks: without it every group reports the GLOBAL distinct count.
//
// The values deliberately overlap across groups. With distinct values per group the
// two designs agree and this proves nothing.
func TestNUniqueSeparatesGroupsThatShareValues(t *testing.T) {
	col := strCol([]string{"a", "b", "a", "b", "c", "a"})
	groups := []int32{0, 0, 1, 1, 1, 2}

	out := run(t, expr.AggNUnique, dtype.String, col, groups, 3)
	for i, want := range []uint64{2, 3, 1} {
		got, ok := data.MustValues[uint64](out)[i], out.IsValid(i)
		if !ok || got != want {
			t.Errorf("group %d has %d distinct values, want %d — the groups are "+
				"sharing one set", i, got, want)
		}
	}
}

// TestNUniqueFramesTheGroupPrefix: group ids whose spellings are prefixes of each
// other stay separate, and a group nothing was fed reports zero.
//
// It was written to prove the four-byte prefix must be fixed width, and it does not:
// swapping in a decimal prefix keeps it passing, because GroupKeyEncoder frames its
// own output with a validity byte and a length, so the byte after the prefix is never
// a digit. The case is worth keeping anyway — it pins that these groups are distinct
// — but the reason for fixed width is stated where the code is, not here.
func TestNUniqueFramesTheGroupPrefix(t *testing.T) {
	// With a decimal prefix these all collapse to "10x", "10x", "10x".
	col := strCol([]string{"0x", "x", "x"})
	groups := []int32{1, 10, 10}

	out := run(t, expr.AggNUnique, dtype.String, col, groups, 11)
	vals := data.MustValues[uint64](out)
	if vals[1] != 1 {
		t.Errorf("group 1 has %d distinct values, want 1", vals[1])
	}
	if vals[10] != 1 {
		t.Errorf("group 10 has %d distinct values, want 1", vals[10])
	}
	// And a group that was never fed must be zero, not borrowed from a neighbour.
	if vals[0] != 0 {
		t.Errorf("group 0 was never fed but has %d distinct values", vals[0])
	}
}

// TestNUniqueCountsTheEmptyString: an empty value is a value, and a null is not.
// The encoder distinguishes them; the key framing must not.
func TestNUniqueCountsTheEmptyString(t *testing.T) {
	b := bitmap.NewBuilder(3)
	b.Append(true)
	b.Append(true)
	b.Append(false) // null
	col := data.NewString("v", []string{"", "x", "ignored"}, b.Finish())

	out := run(t, expr.AggNUnique, dtype.String, col, []int32{0, 0, 0}, 1)
	if got := data.MustValues[uint64](out)[0]; got != 2 {
		t.Errorf("n_unique = %d, want 2 (the empty string counts, the null does not)", got)
	}
}
