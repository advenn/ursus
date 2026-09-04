package expr

import (
	"math"

	"github.com/advenn/ursus/dtype"
)

// Weak literal typing.
//
// # The problem
//
// A literal's type is fixed at BUILD time, before any schema exists, so
// ursus.Lit(0) is Int64 and Promote(Int32, Int64) is Int64. That is correct for
// arithmetic — addition can overflow — and wrong for a null fill, where the value
// cannot change the column's range and the widening is invisible in the source.
//
// # It generalises a rule the IR already has
//
// dtype.Promote's first branch is already "a null literal has no type of its own;
// it takes the other operand's". A weak literal is that, extended from "no type"
// to "a DEFAULT type", with a fit check bolted on because a default type carries a
// value and an untyped null does not.
//
// # Weak typing can never unlock a conversion
//
// It only ever returns a type that ordinary promotion would already have accepted
// as one of its inputs, and it fires only where promotion would have WIDENED. So
// it can turn Int64 back into Int32; it can never turn a plan-time error into a
// success, and it can never reach a cast the kernel does not implement.

// IsWeakLit reports whether n is a literal whose type should adapt to its context.
func IsWeakLit(n Node) bool {
	l, ok := n.(*Lit)
	return ok && l.Weak
}

// weakTarget returns the type a conditional should take because exactly one of its
// branches is a weak literal that fits the other branch's type exactly.
func weakTarget(thenN, elseN Node, then, els dtype.DataType) (dtype.DataType, bool) {
	var l *Lit
	var other dtype.DataType
	switch {
	case IsWeakLit(thenN) && !IsWeakLit(elseN):
		l, other = thenN.(*Lit), els
	case IsWeakLit(elseN) && !IsWeakLit(thenN):
		l, other = elseN.(*Lit), then
	default:
		// Two weak literals have no column to defer to, and none is the ordinary case.
		return dtype.Null, false
	}

	// A weak NULL is a contradiction: Promote already lets an untyped null adopt the
	// other side's type, and a TYPED null was written out by the user. A Null-typed
	// other side has nothing to narrow towards.
	if l.Value == nil || other.IsNull() {
		return dtype.Null, false
	}

	out, ok := dtype.Promote(then, els)
	if !ok {
		// Promotion already refuses this pair — Decimal, a temporal, a String, a
		// Bool. There is no silent widening here to prevent, so there is nothing for
		// this rule to fix, and admitting it would move a plan-time error to
		// execution: kernel.Cast implements no Decimal conversion at all.
		return dtype.Null, false
	}
	if out == other {
		return dtype.Null, false // nothing was widened
	}
	if !FitsExactly(l.Value, other) {
		// Fall back rather than truncate. Col("i8").FillNullWith(int64(5000)) becomes
		// an Int64 column holding 5000 — which is what the hand-written Coalesce does
		// today — instead of an Int8 column holding -120.
		return dtype.Null, false
	}
	return other, true
}

// FitsExactly reports whether the Go value v is representable in to with no loss.
//
// # Why this is a SECOND range check
//
// kernel.narrow already answers this question — `t := T(v); if float64(t) != v` —
// and reusing it is impossible: internal/expr is L20 and internal/kernel is L30,
// so the import is a levels violation. Two copies of a rule is the shape this
// codebase has been bitten by before, so the two are pinned together by a
// differential test rather than by hope. See TestWeakFitAgreesWithCast.
//
// It is deliberately CONSERVATIVE at the edges — above 2^53 for a float target,
// and for every type it does not enumerate. Being conservative costs nothing: each
// such case falls back to the type ordinary promotion would have chosen anyway.
//
// The switch is on to.ID() and exhaustive, never on BitWidth(): Decimal reports 128
// bits and Enum reports 32, so a width-based rule would silently admit both.
func FitsExactly(v any, to dtype.DataType) bool {
	switch x := v.(type) {
	case int8:
		return intFits(int64(x), to)
	case int16:
		return intFits(int64(x), to)
	case int32:
		return intFits(int64(x), to)
	case int64:
		// Both literal paths normalise a Go int to int64 — the switch in litNode and
		// reflect.Value.Int in the reflection path — so there is no `case int` here,
		// and there is deliberately no `case uint` either. If that ever stops being
		// true, weak typing goes quiet rather than wrong: an unrecognised type falls
		// through to the conservative false below.
		return intFits(x, to)
	case uint8:
		return uintFits(uint64(x), to)
	case uint16:
		return uintFits(uint64(x), to)
	case uint32:
		return uintFits(uint64(x), to)
	case uint64:
		return uintFits(x, to)
	case float32:
		return floatFits(float64(x), to)
	case float64:
		return floatFits(x, to)
	default:
		// Strings, bools, temporals, Int128 literals. All unreachable — Promote
		// refuses their pairs before weakTarget asks — and failing closed is the
		// right default for the next type someone adds.
		return false
	}
}

func intFits(v int64, to dtype.DataType) bool {
	switch to.ID() {
	case dtype.TypeInt8:
		return v >= math.MinInt8 && v <= math.MaxInt8
	case dtype.TypeInt16:
		return v >= math.MinInt16 && v <= math.MaxInt16
	case dtype.TypeInt32:
		return v >= math.MinInt32 && v <= math.MaxInt32
	case dtype.TypeInt64, dtype.TypeInt128:
		return true
	case dtype.TypeUint8:
		return v >= 0 && v <= math.MaxUint8
	case dtype.TypeUint16:
		return v >= 0 && v <= math.MaxUint16
	case dtype.TypeUint32:
		return v >= 0 && v <= math.MaxUint32
	case dtype.TypeUint64:
		return v >= 0
	case dtype.TypeFloat64:
		return v >= -(1<<53) && v <= 1<<53
	case dtype.TypeFloat32:
		return v >= -(1<<24) && v <= 1<<24
	default:
		return false
	}
}

func uintFits(v uint64, to dtype.DataType) bool {
	if v <= math.MaxInt64 {
		return intFits(int64(v), to)
	}
	// Above MaxInt64 only the widest integers can hold it, and no float can hold it
	// exactly.
	return to.ID() == dtype.TypeUint64 || to.ID() == dtype.TypeInt128
}

func floatFits(v float64, to dtype.DataType) bool {
	switch to.ID() {
	case dtype.TypeFloat64:
		return true
	case dtype.TypeFloat32:
		// NaN needs its own arm: float32(NaN) == NaN is FALSE, so a bare round-trip
		// would widen Col("f32").FillNullWith(math.NaN()) to Float64. Signed zero
		// needs none — -0.0 round-trips and keeps its sign.
		if math.IsNaN(v) {
			return true
		}
		return float64(float32(v)) == v
	}
	// An integer target needs an integral value in range. The magnitude guard comes
	// first because int64(v) for a huge v is not defined.
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
		return false
	}
	if v < -(1<<62) || v > 1<<62 {
		return false
	}
	return intFits(int64(v), to)
}
