package physical

// Temporal arithmetic, against an independent oracle.
//
// `arithmetic` guards Time and nothing else, so every other temporal arm reaches
// arithNum[int64] → addScalar, which the file's own comment calls "dst[i] = a[i] +
// b[i] on int64 with no check". A wrapped instant or span is a PLAUSIBLE value —
// 9e18ns minus -9e18ns renders as -124095h34m33.709551616s, an ordinary negative
// span — so nothing downstream can notice. That is the whole reason this sweep
// compares against math/big rather than against another int64 computation.
//
// # The arms are derived, not listed
//
// A hand-written list of an enum goes quiet exactly the way an allow-list does, which
// is the lesson evalcontract_test.go's allBinaryOps records: three declared ops had
// never been through that matrix and nothing could say so. So every arm here is
// whatever ResolveBinary accepts with a temporal result, over every operand type the
// type system can build — and allOperandTypes must account for every TypeID or
// TestOperandTypesCoverTheEnum fails.
//
// # Built at the POST-CAST types, deliberately
//
// Operands are built at bind.CastL/CastR, so NeedsCast is false and no cast runs.
// The oracle is then pure integer arithmetic on the stored ticks and never has to
// re-implement rescaleTemporal — computing the answer a second way, in the way the
// thing under test computes it, is how an oracle stops being one.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// --- the operand types, derived from the enum ---------------------------------------

// noOperand names every TypeID that cannot be an arithmetic operand, with the reason.
// A type added to the enum lands in neither map and fails the coverage test, which is
// the point: the sweep grows the day a type is named.
var noOperand = map[dtype.TypeID]string{
	dtype.TypeList:        "nested; arithmetic on elements goes through the .list namespace",
	dtype.TypeArray:       "declared but not constructible: there is no data.Column for it",
	dtype.TypeStruct:      "nested; arithmetic on fields goes through .struct",
	dtype.TypeEnum:        "not constructible from the public API yet",
	dtype.TypeCategorical: "reserved; its mapping grows at runtime",
	dtype.TypeUint128:     "reserved; Int128 covers every unsigned value",
}

func allTimeUnits() []dtype.TimeUnit {
	var us []dtype.TimeUnit
	for i := range 256 {
		if u := dtype.TimeUnit(i); u.String() != "?" {
			us = append(us, u)
		}
	}
	return us
}

// allOperandTypes returns one DataType per constructible TypeID, expanding the
// parameterised temporal types over every unit — and Datetime over both a zone and no
// zone, because resolveTemporalArithmetic refuses a mixed pair and that refusal is
// part of what is swept.
func allOperandTypes() []dtype.DataType {
	out := []dtype.DataType{
		dtype.Null, dtype.Bool,
		dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64,
		dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64,
		dtype.Float32, dtype.Float64, dtype.Int128,
		dtype.Decimal(18, 3), dtype.String, dtype.Binary, dtype.Date,
	}
	for _, u := range allTimeUnits() {
		out = append(out, dtype.Time(u), dtype.Duration(u),
			dtype.Datetime(u, "UTC"), dtype.Datetime(u, ""))
	}
	return out
}

func TestOperandTypesCoverTheEnum(t *testing.T) {
	seen := map[dtype.TypeID]bool{}
	for _, d := range allOperandTypes() {
		seen[d.ID()] = true
	}
	for id := dtype.TypeID(0); id < dtype.TypeIDCount; id++ {
		why, excused := noOperand[id]
		switch {
		case seen[id] && excused:
			t.Errorf("%s is both produced and excused (%q)", id, why)
		case !seen[id] && !excused:
			t.Errorf("%s is neither produced by allOperandTypes nor named in noOperand — "+
				"the sweep cannot see it", id)
		}
	}
	if n := len(allTimeUnits()); n != 4 {
		t.Errorf("%d time units derived, want 4", n)
	}
}

// --- the arms -----------------------------------------------------------------------

type arm struct {
	op     expr.BinaryOp
	lt, rt dtype.DataType
	bind   expr.Binding
}

func (a arm) String() string { return fmt.Sprintf("%s %s %s", a.lt, a.op, a.rt) }

// temporalArms is every (op, left, right) the resolver accepts with a temporal result.
func temporalArms() []arm {
	types := allOperandTypes()
	var arms []arm
	for _, op := range allBinaryOps() {
		for _, lt := range types {
			for _, rt := range types {
				bind, err := expr.ResolveBinary(op, lt, rt)
				if err != nil || !bind.Out.IsTemporal() {
					continue
				}
				arms = append(arms, arm{op, lt, rt, bind})
			}
		}
	}
	return arms
}

// --- fixtures -----------------------------------------------------------------------

// probes sit on every int64 boundary, plus one ordinary calendar magnitude (a day in
// nanoseconds). Every ordered pair is tried.
var probes = []int64{
	math.MinInt64, math.MinInt64 + 1, -1 << 62, -86_400_000_000_000, -1,
	0, 1, 86_400_000_000_000, 1 << 62, math.MaxInt64 - 1, math.MaxInt64,
}

// buildOperand makes a 3-row column of type d holding [a, null, b].
//
// Row 1 is null in BOTH operands and carries extreme payload, so "a null row is never
// judged" is asserted once per case rather than once per suite. A null slot's payload
// is arbitrary — kleene's comment in dispatch.go says so — and an engine that read it
// would refuse queries over data the user cannot see.
//
// A Time column is reduced into [0, 24h): data.CheckTimeRange is on in this package
// and NewBatch rejects anything else, which is itself why the Time arms cannot
// overflow.
func buildOperand(name string, d dtype.DataType, a, b int64) *data.Column {
	vb := bitmap.NewBuilder(3)
	vb.Append(true)
	vb.Append(false)
	vb.Append(true)
	v := vb.Finish()

	fit := func(x int64) int64 {
		if d.ID() == dtype.TypeTime {
			per, _ := dtype.TicksPerDay(d)
			return ((x % per) + per) % per
		}
		return x
	}
	wide := func(x int64) i128.Int128 { return i128.Int128{Hi: x >> 63, Lo: uint64(x)} }

	switch d.ID() {
	case dtype.TypeNull:
		return data.NewNull(name, dtype.Null, 3)
	case dtype.TypeBool:
		bits := bitmap.NewBuilder(3)
		bits.Append(a != 0)
		bits.Append(true)
		bits.Append(b != 0)
		return data.NewBool(name, bits.Finish(), v)
	case dtype.TypeString, dtype.TypeBinary:
		c := data.NewString(name, []string{"1", "2", "3"}, v)
		if d.ID() == dtype.TypeBinary {
			c = c.WithDType(dtype.Binary)
		}
		return c
	case dtype.TypeDecimal, dtype.TypeInt128:
		return data.NewFixed(name, d, []i128.Int128{wide(a), wide(math.MaxInt64), wide(b)}, v)
	}

	switch d.Physical().ID() {
	case dtype.TypeInt64:
		return data.NewFixed(name, d, []int64{fit(a), math.MinInt64, fit(b)}, v)
	case dtype.TypeInt32:
		return data.NewFixed(name, d, []int32{clamp32(a), math.MinInt32, clamp32(b)}, v)
	case dtype.TypeInt16:
		return data.NewFixed(name, d, []int16{int16(a), math.MinInt16, int16(b)}, v)
	case dtype.TypeInt8:
		return data.NewFixed(name, d, []int8{int8(a), math.MinInt8, int8(b)}, v)
	case dtype.TypeUint64:
		return data.NewFixed(name, d, []uint64{uint64(a), math.MaxUint64, uint64(b)}, v)
	case dtype.TypeUint32:
		return data.NewFixed(name, d, []uint32{uint32(a), math.MaxUint32, uint32(b)}, v)
	case dtype.TypeUint16:
		return data.NewFixed(name, d, []uint16{uint16(a), math.MaxUint16, uint16(b)}, v)
	case dtype.TypeUint8:
		return data.NewFixed(name, d, []uint8{uint8(a), math.MaxUint8, uint8(b)}, v)
	case dtype.TypeFloat64:
		return data.NewFixed(name, d, []float64{float64(a), 0, float64(b)}, v)
	case dtype.TypeFloat32:
		return data.NewFixed(name, d, []float32{float32(a), 0, float32(b)}, v)
	}
	return nil
}

func clamp32(x int64) int32 {
	switch {
	case x > math.MaxInt32:
		return math.MaxInt32
	case x < math.MinInt32:
		return math.MinInt32
	}
	return int32(x)
}

// storedTicks reads a column as int64 tick counts, whatever its storage width.
func storedTicks(c *data.Column) ([]int64, bool) {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt64:
		v, err := data.Values[int64](c)
		return v, err == nil
	case dtype.TypeInt32:
		v, err := data.Values[int32](c)
		if err != nil {
			return nil, false
		}
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = int64(x)
		}
		return out, true
	}
	return nil, false
}

func batchOf(t *testing.T, l, r *data.Column) *data.Batch {
	t.Helper()
	schema, err := dtype.NewSchema(dtype.Of("l", l.DType()), dtype.Of("r", r.DType()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(schema, []*data.Column{l, r})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func binaryNode(op expr.BinaryOp) expr.Node {
	return &expr.Binary{Op: op, L: &expr.Col{Name: "l"}, R: &expr.Col{Name: "r"}}
}

// --- the oracle ---------------------------------------------------------------------

// exact computes the arithmetic in math/big, which has no int64 to wrap. Integer
// FloorDiv truncates toward zero (Go's /, see divIntScalar) and nulls on zero, so the
// oracle uses Quo and declines the zero case rather than inventing one.
//
// A Time result is MODULAR by contract, not by accident: step 57 made Time ± Duration
// wrap into [0, 24h) because 25:00 is not a time of day. The oracle models that
// directly rather than skipping those arms, so this sweep asserts step 57's contract
// with an independent implementation as a side effect.
func exact(op expr.BinaryOp, out dtype.DataType, a, b int64) (*big.Int, bool) {
	x, y := big.NewInt(a), big.NewInt(b)
	z := new(big.Int)
	switch op {
	case expr.OpAdd:
		z.Add(x, y)
	case expr.OpSub:
		z.Sub(x, y)
	case expr.OpMul:
		z.Mul(x, y)
	case expr.OpFloorDiv:
		if b == 0 {
			return nil, false
		}
		z.Quo(x, y)
	default:
		return nil, false
	}
	if out.ID() == dtype.TypeTime {
		per, ok := dtype.TicksPerDay(out)
		if !ok {
			return nil, false
		}
		m := big.NewInt(per)
		z.Mod(z, m) // big.Int.Mod is Euclidean, so the result is already non-negative
	}
	return z, true
}

// --- the ratchet --------------------------------------------------------------------

// knownTemporalWraps is every (arm, operands) that wraps int64 TODAY, written down
// before the repair so the evidence is in git rather than in a commit message. The
// commit that adds the guard empties it, and the assertion runs both ways from then
// on: a wrap that is fixed and left listed fails, and a wrap that appears unlisted
// fails.
var knownTemporalWraps = map[string]bool{
	// Measured, 2026-09-18, before any repair. Every one of these produces a
	// plausible in-range value: 9e18ns - -9e18ns, two instants 570 years apart,
	// comes back as -124095h34m33.709551616s.
	"Datetime - Datetime -> Duration": true, // reachable from two valid ns instants
	"Datetime + Duration -> Datetime": true,
	"Datetime - Duration -> Datetime": true,
	"Duration + Datetime -> Datetime": true,
	"Duration + Duration -> Duration": true,
	"Duration - Duration -> Duration": true,
	"Duration * Int64 -> Duration":    true, // 1s * 1e10 overflows; so does any ns span past 0.93s
	"Int64 * Duration -> Duration":    true,
	"Duration // Int64 -> Duration":   true, // the single pair MinInt64 // -1
}

// wrapShape names an arm by its TYPES rather than its units, so the ratchet is a
// readable dozen lines instead of ten thousand. The concrete operands that wrap are in
// the failure message and in the as-built; what has to be pinned here is which SHAPES
// of arithmetic can lose a value.
func wrapShape(a arm) string {
	return fmt.Sprintf("%s %s %s -> %s", a.bind.CastL.ID(), a.op, a.bind.CastR.ID(), a.bind.Out.ID())
}

// --- the sweep ----------------------------------------------------------------------

func TestTemporalArithmeticNeverWraps(t *testing.T) {
	ctx := context.Background()
	arms := temporalArms()

	var compared, exactHits int
	var mismatches, examples []string
	refusals := map[expr.BinaryOp]int{}
	observed := map[string]bool{}
	var cannotOverflow []string

	for _, a := range arms {
		armWrapped, armRefused, armExact := 0, 0, 0

		for _, x := range probes {
			for _, y := range probes {
				l := buildOperand("l", a.bind.CastL, x, y)
				r := buildOperand("r", a.bind.CastR, y, x)
				if l == nil || r == nil {
					t.Fatalf("%s: no fixture for %s / %s", a, a.bind.CastL, a.bind.CastR)
				}
				lv, lok := storedTicks(l)
				rv, rok := storedTicks(r)
				if !lok || !rok {
					continue // a non-integer operand; the oracle does not apply
				}

				got, err := Eval(ctx, binaryNode(a.op), batchOf(t, l, r))
				if err != nil {
					armRefused++
					refusals[a.op]++
					var ue *uerr.Error
					if errors.As(err, &ue) && ue.Op == "arith" &&
						strings.Contains(err.Error(), "at row 1") {
						t.Errorf("%s: refused on the NULL row: %v", a, err)
					}
					continue
				}
				out, ok := storedTicks(got)
				if !ok {
					continue
				}
				compared++
				if got.IsValid(1) {
					t.Errorf("%s: the null row came back valid", a)
				}

				for _, row := range []int{0, 2} {
					want, ok := exact(a.op, a.bind.Out, lv[row], rv[row])
					if !ok || !got.IsValid(row) {
						continue
					}
					switch {
					case !want.IsInt64():
						armWrapped++
						observed[wrapShape(a)] = true
						if len(examples) < 400 {
							examples = append(examples, fmt.Sprintf("%s: %d %s %d = %d, exactly %s",
								a, lv[row], a.op, rv[row], out[row], want))
						}
					case out[row] != want.Int64():
						mismatches = append(mismatches, fmt.Sprintf("%s: %d %s %d = %d, want %d",
							a, lv[row], a.op, rv[row], out[row], want.Int64()))
					default:
						armExact++
						exactHits++
					}
				}
			}
		}

		switch {
		case armWrapped == 0 && armRefused == 0:
			cannotOverflow = append(cannotOverflow, a.String())
		case armExact == 0 && armRefused == 0:
			t.Errorf("%s: no case produced an exact answer, so the arm proves nothing", a)
		}
	}

	if len(mismatches) > 0 {
		slices.Sort(mismatches)
		t.Errorf("%d results disagree with the oracle; the first few:\n  %s",
			len(mismatches), strings.Join(firstN(mismatches, 8), "\n  "))
	}

	// Anti-vacuity: the derivation, the probes and the oracle all have to have done
	// something. Each of these has a way of going quietly to zero.
	if len(arms) < 200 {
		t.Errorf("only %d temporal arms derived; the enumeration has gone quiet", len(arms))
	}
	if compared < 10_000 {
		t.Errorf("only %d comparisons ran", compared)
	}
	if exactHits < 10_000 {
		t.Errorf("only %d exact agreements; the oracle is barely being consulted", exactHits)
	}

	// The ratchet, both ways.
	var unlisted, stale []string
	for key := range observed {
		if !knownTemporalWraps[key] {
			unlisted = append(unlisted, key)
		}
	}
	for key := range knownTemporalWraps {
		if !observed[key] {
			stale = append(stale, key)
		}
	}
	slices.Sort(unlisted)
	slices.Sort(stale)
	if len(unlisted) > 0 {
		slices.Sort(examples)
		t.Errorf("%d wrapping SHAPES are not in knownTemporalWraps:\n  %s\nexamples:\n  %s",
			len(unlisted), strings.Join(unlisted, "\n  "), strings.Join(firstN(examples, 6), "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d entries in knownTemporalWraps no longer wrap — delete them:\n  %s",
			len(stale), strings.Join(firstN(stale, 8), "\n  "))
	}

	t.Logf("arms=%d comparisons=%d exact=%d wraps=%d refusals=%v cannotOverflow=%d",
		len(arms), compared, exactHits, len(observed), refusals, len(cannotOverflow))
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

// TestEveryTemporalArmRuns asks the other question: an arm the PLANNER accepts must
// actually execute, at the types the user wrote rather than the ones the binding casts
// to. This is where a plan-accepts / kernel-rejects divergence shows up — the shape
// TestEvaluatorContract exists for, which its fixture cannot see for a type it has no
// column of.
// knownArmDivergences is every arm the PLANNER accepts and the kernel refuses today.
// Emptied by the commit that fixes them; stale entries fail, so a fix cannot be left
// undocumented and a new divergence cannot appear unnoticed.
var knownArmDivergences = map[string]bool{
	// Measured, 2026-09-18. resolveArithmetic refuses Decimal for * thirty lines
	// before integralScale accepts it, so the binding promises a Duration and
	// kernel.Cast refuses Decimal -> Int64. Two arms of one file answering one
	// question two ways; fixed in the next commit.
	"Decimal(18, 3) * Duration(s)":   true,
	"Duration(s) * Decimal(18, 3)":   true,
	"Duration(s) // Decimal(18, 3)":  true,
	"Decimal(18, 3) * Duration(ms)":  true,
	"Duration(ms) * Decimal(18, 3)":  true,
	"Duration(ms) // Decimal(18, 3)": true,
	"Decimal(18, 3) * Duration(us)":  true,
	"Duration(us) * Decimal(18, 3)":  true,
	"Duration(us) // Decimal(18, 3)": true,
	"Decimal(18, 3) * Duration(ns)":  true,
	"Duration(ns) * Decimal(18, 3)":  true,
	"Duration(ns) // Decimal(18, 3)": true,
}

func TestEveryTemporalArmRuns(t *testing.T) {
	ctx := context.Background()
	ran := 0
	var diverged []string
	seenDivergence := map[string]bool{}
	for _, a := range temporalArms() {
		l := buildOperand("l", a.lt, 1, 2)
		r := buildOperand("r", a.rt, 3, 4)
		if l == nil || r == nil {
			t.Fatalf("%s: no fixture", a)
		}
		got, err := Eval(ctx, binaryNode(a.op), batchOf(t, l, r))
		if err != nil {
			if !knownArmDivergences[a.String()] {
				diverged = append(diverged, fmt.Sprintf("%s: %v", a, err))
			}
			seenDivergence[a.String()] = true
			continue
		}
		if got.DType() != a.bind.Out {
			t.Errorf("%s: produced %s, the binding promised %s", a, got.DType(), a.bind.Out)
		}
		ran++
	}
	if len(diverged) > 0 {
		slices.Sort(diverged)
		t.Errorf("%d arms the planner accepts and the kernel refuses:\n  %s",
			len(diverged), strings.Join(diverged, "\n  "))
	}
	for key := range knownArmDivergences {
		if !seenDivergence[key] {
			t.Errorf("%q no longer diverges — delete it from knownArmDivergences", key)
		}
	}
	if ran < 200 {
		t.Errorf("only %d arms ran", ran)
	}
}
