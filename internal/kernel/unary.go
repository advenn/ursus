package kernel

import (
	"math"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Unary applies a one-operand operation.
func Unary(op expr.UnaryOp, name string, out dtype.DataType, c *data.Column) (*data.Column, error) {
	n := c.Len()

	// A NULL OPERAND, which is not the same question as a null value.
	//
	// The two null predicates below are TOTAL — the answer is the validity bit
	// itself — so they read a Null column like any other and are excluded here.
	// Every other op ResolveUnary admits for Null is null-in/null-out, and a Null
	// column has nothing to read: data.NewNull carries validity and no payload at
	// all, so Bools() is the zero View and any length taken from it is zero. That is
	// how `not` returned a Bool column of ZERO rows from an n-row input, with a nil
	// error, for as long as this file has existed.
	//
	// The answer is not a computation. It is n nulls labelled with the type
	// ResolveUnary promised, which is exactly the shape Cast's `from.IsNull()` arm
	// already takes — and NullColumn rather than data.NewNull for the reason
	// take.go gives: this is an operand somebody will gather from, not only an
	// answer.
	//
	// After ResolveUnary stopped admitting Null for sign/floor/ceil and the maths
	// family, the only ops that reach here with one are not and the four NaN
	// predicates, all of which promise Bool. Both of those answers are written down
	// — "Kleene NOT: NOT null is null" below, and "null.is_nan() is NULL, not
	// false" in three separate files — and neither was implemented.
	if c.DType().IsNull() && op != expr.OpIsNull && op != expr.OpIsNotNull {
		return NullColumn(name, out, n)
	}

	switch op {
	// Null predicates are TOTAL: the answer is the validity bit itself, so the
	// result is never null. This is the one place where reading the validity
	// bitmap as data is correct.
	case expr.OpIsNull:
		bits := bitmap.NewBuilder(n)
		for i := range n {
			bits.Append(!c.IsValid(i))
		}
		return data.NewBool(name, bits.Finish(), bitmap.AllSet(n)), nil

	case expr.OpIsNotNull:
		bits := bitmap.NewBuilder(n)
		bits.AppendView(c.Validity())
		return data.NewBool(name, bits.Finish(), bitmap.AllSet(n)), nil

	// Kleene NOT: NOT null is null, so validity passes straight through.
	case expr.OpNot:
		return data.NewBool(name, bitmap.Not(c.Bools()), c.Validity()), nil

	// NaN predicates are NOT total. `null.is_nan()` is null, not false, because a
	// missing value is not a float that could be tested. Null and NaN are
	// different things and ursus keeps them different all the way down.
	case expr.OpIsNan, expr.OpIsNotNan, expr.OpIsFinite, expr.OpIsInfinite:
		return nanPredicate(op, name, c, n)

	case expr.OpNeg, expr.OpAbs, expr.OpSign, expr.OpFloor, expr.OpCeil:
		// The TYPE-PRESERVING unary family: out == in for all five.
		return unaryArith(op, name, out, c, n)

	case expr.OpSqrt, expr.OpCbrt, expr.OpExp, expr.OpLn, expr.OpLog10, expr.OpLog1p:
		// The widening family. Listed rather than routed through op.IsMath() so that
		// a math op added to the enum without an arm here fails loudly in Unary's
		// default rather than silently taking the type-preserving path.
		return unaryMath(op, name, out, c, n)

	default:
		return nil, uerr.Internalf("kernel: unhandled unary op %s", op)
	}
}

func nanPredicate(op expr.UnaryOp, name string, c *data.Column, n int) (*data.Column, error) {
	bits := bitmap.NewBuilder(n)

	switch c.DType().Physical().ID() {
	case dtype.TypeFloat32:
		v, err := data.Values[float32](c)
		if err != nil {
			return nil, err
		}
		applyNanPred(op, bits, v)
	case dtype.TypeFloat64:
		v, err := data.Values[float64](c)
		if err != nil {
			return nil, err
		}
		applyNanPred(op, bits, v)
	default:
		return nil, uerr.Internalf("kernel: %s on non-float %s", op, c.DType())
	}
	return data.NewBool(name, bits.Finish(), c.Validity()), nil
}

func applyNanPred[T ~float32 | ~float64](op expr.UnaryOp, out *bitmap.Builder, v []T) {
	switch op {
	case expr.OpIsNan:
		isNanScalar(out, v)
	case expr.OpIsNotNan:
		tmp := bitmap.NewBuilder(len(v))
		isNanScalar(tmp, v)
		out.AppendView(bitmap.Not(tmp.Finish()))
	case expr.OpIsFinite:
		isFiniteScalar(out, v)
	case expr.OpIsInfinite:
		isInfScalar(out, v)
	}
}

// unaryArith implements neg and abs.
//
// # It must cover everything ResolveUnary accepts
//
// ResolveUnary lets abs through for every IsNumeric() type, and IsNumeric()
// includes the unsigned integers, Int128 and Decimal. For four steps this switch
// covered only the signed integers and the floats, so
//
//	Col("u32").Abs()          Col("i128").Abs()          Col("price_dec").Abs()
//
// type-checked, planned, rendered in Explain, and then failed at execution — the
// plan-accepts / kernel-rejects divergence this file already records twice, for
// Null casts and for string casts.
//
// The one that mattered is not exotic. EVERY integer Sum outputs Int128, so
//
//	GroupBy(g).Agg(Col("x").Sum().Abs())
//
// is an ordinary query that reached Int128 without anyone asking for it.
//
// The fix widens the KERNEL rather than narrowing ResolveUnary, because abs on an
// unsigned column is the identity — refusing it would be an error where the right
// answer is free — and because narrowing would break sum(x).abs().
func unaryArith(op expr.UnaryOp, name string, out dtype.DataType, c *data.Column, n int) (*data.Column, error) {
	switch out.Physical().ID() {
	case dtype.TypeInt8:
		return unaryInt[int8](op, name, out, c, n)
	case dtype.TypeInt16:
		return unaryInt[int16](op, name, out, c, n)
	case dtype.TypeInt32:
		return unaryInt[int32](op, name, out, c, n)
	case dtype.TypeInt64:
		return unaryInt[int64](op, name, out, c, n)

	case dtype.TypeUint8, dtype.TypeUint16, dtype.TypeUint32, dtype.TypeUint64:
		switch op {
		case expr.OpAbs, expr.OpFloor, expr.OpCeil:
			// All three are the identity on an unsigned integer, so this copies
			// nothing: it relabels.
			return c.Rename(name).WithDType(out), nil
		case expr.OpSign:
			return unaryUintSign(name, out, c, n)
		default:
			// neg never arrives: ResolveUnary refuses it for unsigned types with a
			// hint to cast first. A kernel that assumed that and was wrong would
			// silently return the operand, so it is checked.
			return nil, uerr.Internalf(
				"kernel: %s reached the unsigned path; ResolveUnary should have refused it", op)
		}

	case dtype.TypeInt128:
		// Covers Decimal too, whose physical type is Int128 — and abs on an unscaled
		// integer is EXACT, unlike the multiplication and division that
		// resolveArithmetic refuses for decimals, so no scale calculus is involved.
		return unaryI128(op, name, out, c, n)

	case dtype.TypeFloat32:
		return unaryFloat[float32](op, name, out, c, n)
	case dtype.TypeFloat64:
		return unaryFloat[float64](op, name, out, c, n)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "",
			"%s is not implemented for %s", op, out)
	}
}

// unaryI128 is neg and abs at 128 bits.
//
// i128 has Neg and Sign and no Mul, which is exactly enough: abs is a sign test
// and a negation. Neg(Min) is Min, matching two's complement at every other width
// — the same wrap absIntScalar has for Int8..Int64.
func unaryI128(op expr.UnaryOp, name string, out dtype.DataType,
	c *data.Column, n int) (*data.Column, error) {

	v, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	buf, dst := newValuesBuffer[i128.Int128](n)
	switch op {
	case expr.OpNeg:
		for i, x := range v {
			dst[i] = x.Neg()
		}
	case expr.OpAbs:
		for i, x := range v {
			if x.Sign() < 0 {
				dst[i] = x.Neg()
			} else {
				dst[i] = x
			}
		}
	case expr.OpSign:
		for i, x := range v {
			dst[i] = i128.FromInt64(int64(x.Sign()))
		}
	case expr.OpFloor, expr.OpCeil:
		// The identity at 128 bits, same as at every narrower integer width.
		// Decimal never arrives: ResolveUnary refuses floor/ceil for it, because the
		// stored value is UNSCALED and flooring it would floor 1234 rather than 12.34.
		copy(dst, v)
	default:
		return nil, uerr.Internalf("kernel: unaryI128 got %s", op)
	}
	return data.NewFixedBuffer(name, out, buf, n, c.Validity()), nil
}

func unaryInt[T ~int8 | ~int16 | ~int32 | ~int64](op expr.UnaryOp, name string,
	out dtype.DataType, c *data.Column, n int) (*data.Column, error) {

	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	buf, dst := newValuesBuffer[T](n)
	switch op {
	case expr.OpNeg:
		negScalar(dst, v)
	case expr.OpAbs:
		absIntScalar(dst, v)
	case expr.OpSign:
		signScalar(dst, v)
	case expr.OpFloor, expr.OpCeil:
		// The identity on an integer. Copied rather than relabelled only because
		// this function has already allocated the buffer; the unsigned arm above and
		// round() below both relabel, which is safe — Rename and WithDType return
		// value copies over immutable buffers.
		copy(dst, v)
	default:
		return nil, uerr.Internalf("kernel: unaryInt got %s", op)
	}
	return data.NewFixedBuffer(name, out, buf, n, c.Validity()), nil
}

func unaryFloat[T ~float32 | ~float64](op expr.UnaryOp, name string,
	out dtype.DataType, c *data.Column, n int) (*data.Column, error) {

	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	buf, dst := newValuesBuffer[T](n)
	switch op {
	case expr.OpNeg:
		negScalar(dst, v)
	case expr.OpAbs:
		absFloatScalar(dst, v)
	case expr.OpSign:
		// signScalar leaves ±0.0 and NaN alone rather than mapping them to 0, which
		// is what NumPy does and what falls out of `if v > 0 … else if v < 0 … else v`
		// with no special case. Both need Float64bits to observe, so both are in the
		// differential's corpus.
		signScalar(dst, v)
	case expr.OpFloor:
		for i, x := range v {
			dst[i] = T(math.Floor(float64(x)))
		}
	case expr.OpCeil:
		for i, x := range v {
			dst[i] = T(math.Ceil(float64(x)))
		}
	default:
		return nil, uerr.Internalf("kernel: unaryFloat got %s", op)
	}
	return data.NewFixedBuffer(name, out, buf, n, c.Validity()), nil
}

// unaryMath is the widening unary family: sqrt, cbrt, exp, ln, log10, log1p.
//
// # Why it cannot be unaryArith
//
// unaryArith dispatches on out.Physical().ID() and then reads the INPUT with that
// same type. That is exactly right when out == in, which holds for neg, abs, sign,
// floor and ceil — and impossible here, because sqrt(Int64) is Float64 and reading
// an Int64 column as []float64 is precisely what data.Values refuses.
//
// So this widens first, computes in float64, and narrows back. Both halves already
// existed for Cast; this reuses them rather than growing a third conversion table.
//
// # Every one of these is TOTAL
//
// sqrt(-1) is NaN, log(0) is -Inf, log1p(-2) is NaN. All are VALUES, not nulls and
// not errors — the rule float division already set: "IEEE division never traps, so
// x/0 is ±Inf and stays a value rather than becoming a null". Validity therefore
// passes straight through and no ok mask is built.
func unaryMath(op expr.UnaryOp, name string, out dtype.DataType,
	c *data.Column, n int) (*data.Column, error) {

	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	dst := make([]float64, n)
	switch op {
	case expr.OpSqrt:
		for i, v := range src {
			dst[i] = math.Sqrt(v)
		}
	case expr.OpCbrt:
		for i, v := range src {
			dst[i] = math.Cbrt(v)
		}
	case expr.OpExp:
		for i, v := range src {
			dst[i] = math.Exp(v)
		}
	case expr.OpLn:
		for i, v := range src {
			dst[i] = math.Log(v)
		}
	case expr.OpLog10:
		for i, v := range src {
			dst[i] = math.Log10(v)
		}
	case expr.OpLog1p:
		for i, v := range src {
			dst[i] = math.Log1p(v)
		}
	default:
		return nil, uerr.Internalf("kernel: unaryMath got %s", op)
	}

	// strict=false: narrowing to Float32 is the only conversion that happens here,
	// and it is a deliberate precision choice the user made by having a Float32
	// column — not a lossy cast to refuse.
	return fromFloat64(name, out, dst, c.Validity(), false)
}

// unaryUintSign is sign for the unsigned integers, where the answer is 0 or 1 and
// there is no negative case to consider.
func unaryUintSign(name string, out dtype.DataType, c *data.Column, n int) (*data.Column, error) {
	switch out.Physical().ID() {
	case dtype.TypeUint8:
		return uintSign[uint8](name, out, c, n)
	case dtype.TypeUint16:
		return uintSign[uint16](name, out, c, n)
	case dtype.TypeUint32:
		return uintSign[uint32](name, out, c, n)
	default:
		return uintSign[uint64](name, out, c, n)
	}
}

func uintSign[T ~uint8 | ~uint16 | ~uint32 | ~uint64](name string, out dtype.DataType,
	c *data.Column, n int) (*data.Column, error) {

	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	buf, dst := newValuesBuffer[T](n)
	for i, x := range v {
		if x != 0 {
			dst[i] = 1
		}
	}
	return data.NewFixedBuffer(name, out, buf, n, c.Validity()), nil
}

// Cast converts a column to another type.
//
// Non-strict casts turn an unrepresentable value into a null, which is why the
// type checker marks every non-strict cast nullable regardless of its input.
// Strict casts fail the query and name the offending row.
//
// # A Time target is normalised, and that is a fix rather than tidiness
//
// Cast is one of the two producers of a Time value, and it relabels whenever the two
// sides carry the same tick length — so `Datetime(s) -> Time(s)` reinterpreted
// SECONDS SINCE THE EPOCH as a time of day. It printed correctly by accident, because
// ToTime rebuilds the real instant and the "15:04:05" layout throws the date away,
// while the stored tick stayed a full epoch offset. Sorting such a column therefore
// ordered by DATE, and equality across two days never matched.
//
// Normalising into [0, 24h) does not merely restore an invariant here; it makes
// `Cast(Datetime -> Time)` mean what the user asked for. The wrapping is done once,
// after every arm, rather than in each of the four that can produce a Time.
func Cast(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	out, err := castTo(name, to, strict, c)
	if err != nil {
		return nil, err
	}
	if to.ID() == dtype.TypeTime {
		return normaliseTime(out)
	}
	return out, nil
}

// normaliseTime folds a Time column's ticks into [0, 24h).
//
// It SCANS before it allocates: every cast whose target is a Time comes through here,
// and the overwhelming majority are already in range, so the common case is one pass
// and no copy. It does not write through the input's buffer, which may be shared.
func normaliseTime(c *data.Column) (*data.Column, error) {
	m, ok := dtype.TicksPerDay(c.DType())
	if !ok {
		return c, nil
	}
	v, err := data.Values[int64](c)
	if err != nil {
		// A payload-free Time column is all nulls and has no ticks to fold.
		return c, nil
	}
	need := false
	for i, t := range v {
		if (t < 0 || t >= m) && c.IsValid(i) {
			need = true
			break
		}
	}
	if !need {
		return c, nil
	}
	buf, dst := newValuesBuffer[int64](len(v))
	for i, t := range v {
		dst[i] = ((t % m) + m) % m
	}
	return data.NewFixedBuffer(c.Name(), c.DType(), buf, len(v), c.Validity()), nil
}

func castTo(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	from := c.DType()
	if from == to {
		return c.Rename(name), nil
	}

	// --- from Null ----------------------------------------------------------------
	//
	// CanCast returns true for Null to ANY type — `if from == to || from.IsNull()` is
	// its first line — and the kernel refused every one of them. Same shape as the
	// string divergence below: `Null(NullT).Cast(Int64)` type-checked, planned, and
	// failed at execution with "not implemented yet".
	//
	// The answer is not a conversion at all. A Null column has no values to convert;
	// the result is n nulls that happen to be labelled `to`. It must be built with
	// NullColumn rather than relabelled with WithDType, because data.NewNull carries
	// no payload buffer and the relabelled column would fail in the first kernel that
	// tried to read one.
	if from.IsNull() {
		return NullColumn(name, to, c.Len())
	}
	// --- temporal ----------------------------------------------------------------
	//
	// A relabel is only sound when the TICK SCALE is unchanged. The obvious
	// "same physical layout, different logical meaning — free, just relabel" rule
	// is right for Int64 <-> Datetime, which is the documented way to reach the
	// underlying tick count, and catastrophically wrong for Datetime(ns) ->
	// Datetime(s): both are Int64, so the relabel passed, and 2024-01-01 silently
	// became the year 55,974,289. CastTimeUnit was a data-corruption bug wearing a
	// no-op's clothes.
	//
	// So: rescale when both sides carry a tick length, relabel only when they
	// agree or when one side is a bare integer.
	fn, fok := from.NanosPerTick()
	tn, tok := to.NanosPerTick()
	switch {
	case fok && tok:
		if fn == tn {
			return c.Rename(name).WithDType(to), nil
		}
		return rescaleTemporal(name, to, c, fn, tn, strict)

	case (fok || tok) && from.Physical() == to.Physical():
		// The tick-count escape hatch: Int64 <-> Datetime(u), Int32 <-> Date. The
		// integer side has no unit of its own, so there is nothing to rescale.
		return c.Rename(name).WithDType(to), nil
	}

	// A Decimal is stored as its UNSCALED integer, so every numeric cast below would
	// return the wrong number by a factor of 10^scale — Decimal(10,2) 12.34 would
	// cast to Int64 as 1234 — and castDecimal applies the scale instead. Decimal TO
	// STRING falls through to formatToString, which is exact: the scale is known, so
	// the unscaled digits can be pointed at rather than divided.
	if (from.ID() == dtype.TypeDecimal || to.ID() == dtype.TypeDecimal) &&
		to.ID() != dtype.TypeString {
		return castDecimal(name, to, strict, c)
	}

	// String <-> Binary is a RELABEL. Both are offsets plus a character buffer, so
	// there is nothing to convert; CanCast has a deliberate arm promising both
	// directions and the kernel refused both — "not implemented yet" one way and
	// "cannot format a Binary column as text" the other.
	//
	// Here the KERNEL moves, which is the opposite resolution from Bool <-> temporal
	// above, and that asymmetry is the point: which side is right is a judgement
	// about the types, not a lookup. Three of the four historical instances of this
	// divergence were also resolved by implementing what CanCast promised.
	if (from.ID() == dtype.TypeString && to.ID() == dtype.TypeBinary) ||
		(from.ID() == dtype.TypeBinary && to.ID() == dtype.TypeString) {
		return c.Rename(name).WithDType(to), nil
	}

	// --- parsing and formatting -----------------------------------------------------
	//
	// dtype.CanCast has always permitted string<->numeric and string<->temporal, and
	// the kernel has always refused them. That is a plan-accepts / kernel-rejects
	// divergence: Col("s").Cast(Date) type-checked, planned, and then failed at
	// execution — exactly the class of defect IsOrdered's doc comment was written to
	// prevent for a different predicate. The CSV reader even hinted at it: "read it
	// as String and cast, if a conversion exists".
	//
	// The two sides now agree because the kernel implements what CanCast promised.
	if from.IsString() && !to.IsString() {
		return parseFromString(name, to, strict, c)
	}
	if to.ID() == dtype.TypeString && !from.IsString() {
		return formatToString(name, c)
	}

	// Bool is not numeric — dtype excludes it on purpose, because `true + true` is
	// a bug in every query it appears in — so it needs its own arms rather than
	// falling into the numeric path below. CanCast has promised both directions
	// since step 1 and this kernel refused both, which is the third instance of the
	// plan-accepts / kernel-rejects divergence this file records for null casts and
	// string casts. Step 11 made it reachable with no user-written cast at all:
	// IsClose casts its operands to Float64.
	if from.IsBool() != to.IsBool() {
		// TEMPORAL is excluded, and this is the direction where the KERNEL is
		// wrong rather than CanCast. Bool -> Date reached boolToNumeric, went to
		// Int64 and came back relabelled Date; Date -> Bool read the day count and
		// compared it to zero. Both are physical puns: a date is not a truth value,
		// and 1970-01-01 is not "false".
		//
		// CanCast has refused these all along — its temporal arms cover numeric and
		// string, never Bool — so the two answers disagreed in the SAFE direction,
		// with the planner refusing what the kernel would have done. Narrowing the
		// kernel is what makes them agree; see TestCanCastAgreesWithTheKernel.
		if from.IsTemporal() || to.IsTemporal() {
			return nil, uerr.New(uerr.KindUnsupported, "cast",
				"cannot cast %s to %s", from, to).
				Hint("a temporal value is not a truth value; compare it instead, " +
					"e.g. .IsNotNull() or a comparison against an instant")
		}
		if from.IsBool() {
			return boolToNumeric(name, to, c)
		}
		return numericToBool(name, c)
	}

	// A List casts by casting its ELEMENTS. CanCast has promised List -> List all
	// along and the kernel refused it — the FOURTH plan-accepts / kernel-rejects
	// divergence this file records, after null casts, string casts and bool casts,
	// and the one that hid longest: the contract matrix had no List column and no
	// List cast target, and TestCanCastAgreesWithTheKernel names List as an
	// unsamplable source, so neither cast instrument could reach it.
	//
	// It gathers through listGather rather than casting the child in place, and the
	// reason is STRICTNESS rather than indexing. A sliced List keeps absolute
	// offsets over a whole child, so casting that child and keeping the offsets
	// would in fact line up — but the child holds elements belonging to rows this
	// column does not have, and a strict cast fails on a value the target cannot
	// hold. Casting in place therefore rejects a slice because of a row outside it.
	// Measured: a strict cast of [[999],[1,2,3]] sliced to its second row refuses
	// with "value 999 at row 0 is not representable as Int8".
	if from.ID() == dtype.TypeList && to.ID() == dtype.TypeList {
		offs, elems, err := listGather(c, listElements)
		if err != nil {
			return nil, err
		}
		child, err := Cast(elems.Name(), to.Inner(), strict, elems)
		if err != nil {
			return nil, err
		}
		return data.NewList(name, offs, child, c.Validity()), nil
	}

	fp, tp := from.Physical(), to.Physical()
	if !fp.IsNumeric() || !tp.IsNumeric() {
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cast from %s to %s is not implemented yet", from, to)
	}

	// Integer to integer never touches a float: a float64 holds every integer only
	// up to 2^53, and the widest integers here are 128 bits. See castInt.
	if from.IsInteger() && to.IsInteger() {
		return castInt(name, to, strict, c)
	}
	// Int128 to or from a float, handled before the float64 path below because a
	// float source needs castI128's fraction check.
	if fp.ID() == dtype.TypeInt128 || tp.ID() == dtype.TypeInt128 {
		return castI128(name, to, strict, c)
	}

	// Everything else has a float on at least one side, and widens through float64
	// as the common currency. A float source is exact in a float64; an integer
	// source bound for a float is approximate by definition, and a Float32 target is
	// range- and round-trip-checked by narrow.
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	return fromFloat64(name, to, src, c.Validity(), strict)
}

// boolToNumeric renders false as 0 and true as 1, which is what every other
// system does and the only choice that makes sum(flag) a count.
//
// It materialises Int64 and RE-ENTERS Cast rather than calling fromFloat64
// directly. Two reasons, and the second is the one a differential test found:
// bool -> T ought to be bool -> Int64 -> T by definition, and fromFloat64 cannot
// produce a 128-bit target at all — so `Col("flag").Cast(Int128)` failed with
// "cannot narrow to Int128" although CanCast promises it. Nothing is lost by going
// through Int64: 0 and 1 are exact in it and in every type reachable from it.
func boolToNumeric(name string, to dtype.DataType, c *data.Column) (*data.Column, error) {
	n := c.Len()
	bits := c.Bools()
	src := make([]int64, n)
	for i := range n {
		if bits.Get(i) {
			src[i] = 1
		}
	}
	ints := data.NewFixed(name, dtype.Int64, src, c.Validity())
	if to.ID() == dtype.TypeInt64 {
		return ints, nil
	}
	// strict=false: 0 and 1 are representable in every numeric type, so nothing can
	// be lossy and the flag cannot fire.
	return Cast(name, to, false, ints)
}

// numericToBool is `!= 0`, matching C, SQL and Polars. NaN is true — it is not
// zero — which is the one answer people find surprising and the one that follows
// from the rule.
func numericToBool(name string, c *data.Column) (*data.Column, error) {
	src, err := toFloat64(c)
	if err != nil {
		return nil, err
	}
	bits := bitmap.NewBuilder(len(src))
	for _, v := range src {
		bits.Append(v != 0)
	}
	return data.NewBool(name, bits.Finish(), c.Validity()), nil
}

func toFloat64(c *data.Column) ([]float64, error) {
	// A Decimal's physical type is Int128, so without this arm the switch below reads
	// its UNSCALED integer — 12.34 as 1234. Every float consumer comes through here:
	// the Float64 cast, mean, var, std, median, quantile, product, cum_prod. So this
	// is the one place the scale can be applied, and the one place it can be forgotten.
	if c.DType().ID() == dtype.TypeDecimal {
		return decimalFloats(c)
	}
	n := c.Len()
	out := make([]float64, n)
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return conv[int8](c, out)
	case dtype.TypeInt16:
		return conv[int16](c, out)
	case dtype.TypeInt32:
		return conv[int32](c, out)
	case dtype.TypeInt64:
		return conv[int64](c, out)
	case dtype.TypeUint8:
		return conv[uint8](c, out)
	case dtype.TypeUint16:
		return conv[uint16](c, out)
	case dtype.TypeUint32:
		return conv[uint32](c, out)
	case dtype.TypeUint64:
		return conv[uint64](c, out)
	case dtype.TypeFloat32:
		return conv[float32](c, out)
	case dtype.TypeFloat64:
		return data.Values[float64](c)
	case dtype.TypeInt128:
		// Rounds above 2^53, and that is inherent rather than a defect: this path
		// exists to feed sqrt, log and exp, none of which has an exact integer
		// answer anyway. Cast never reaches here — it routes Int128 through castI128
		// first — so adding this arm changes no existing behaviour. What it enables
		// is Col("x").Sum().Sqrt(), an ordinary query, since every integer Sum
		// outputs Int128.
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		for i := range v {
			out[i] = v[i].Float64()
		}
		return out, nil
	default:
		return nil, uerr.Internalf("kernel: cannot widen %s", c.DType())
	}
}

func conv[T data.Primitive](c *data.Column, out []float64) ([]float64, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = float64(v[i])
	}
	return out, nil
}

func fromFloat64(name string, to dtype.DataType, src []float64,
	valid bitmap.View, strict bool) (*data.Column, error) {

	n := len(src)
	switch to.Physical().ID() {
	case dtype.TypeInt8:
		return narrow[int8](name, to, src, valid, strict, n)
	case dtype.TypeInt16:
		return narrow[int16](name, to, src, valid, strict, n)
	case dtype.TypeInt32:
		return narrow[int32](name, to, src, valid, strict, n)
	case dtype.TypeInt64:
		return narrow[int64](name, to, src, valid, strict, n)
	case dtype.TypeUint8:
		return narrow[uint8](name, to, src, valid, strict, n)
	case dtype.TypeUint16:
		return narrow[uint16](name, to, src, valid, strict, n)
	case dtype.TypeUint32:
		return narrow[uint32](name, to, src, valid, strict, n)
	case dtype.TypeUint64:
		return narrow[uint64](name, to, src, valid, strict, n)
	case dtype.TypeFloat32:
		return narrow[float32](name, to, src, valid, strict, n)
	case dtype.TypeFloat64:
		buf, dst := newValuesBuffer[float64](n)
		copy(dst, src)
		return data.NewFixedBuffer(name, to, buf, n, valid), nil
	default:
		return nil, uerr.Internalf("kernel: cannot narrow to %s", to)
	}
}

func narrow[T data.Primitive](name string, to dtype.DataType, src []float64,
	valid bitmap.View, strict bool, n int) (*data.Column, error) {

	buf, dst := newValuesBuffer[T](n)
	ok := bitmap.NewBuilder(n)
	lossy := false
	toFloat := to.IsFloat()
	for i, v := range src {
		t := T(v)
		// NaN needs its own arm, because the round-trip test below cannot see it:
		// NaN != NaN, so `float64(t) != v` is true however faithfully it converted.
		// Without this, a STRICT Cast(Float64 -> Float32) refused every NaN row with
		// "value NaN is not representable as Float32" — which is simply false; NaN is
		// exactly representable in a float32. ±Inf needs no arm: it compares equal to
		// itself and passes through already.
		//
		// For an INTEGER target NaN really is unrepresentable, so the check is on the
		// target rather than on the value.
		if v != v && toFloat {
			dst[i] = t
			ok.Append(true)
			continue
		}
		if float64(t) != v {
			lossy = true
			if strict && valid.Get(i) {
				return nil, uerr.New(uerr.KindValue, "cast",
					"value %v at row %d is not representable as %s", v, i, to).
					Hint("use a non-strict cast to turn unrepresentable values into nulls")
			}
			ok.Append(false)
			continue
		}
		dst[i] = t
		ok.Append(true)
	}
	if lossy {
		valid = bitmap.And(valid, ok.Finish())
	}
	return data.NewFixedBuffer(name, to, buf, n, valid), nil
}

// castInt converts between integer types — every width, signed or not, and Int128 —
// without a float anywhere.
//
// Every integer fits an Int128 exactly, so widening to one loses nothing, and
// narrowI128 then range-checks against the TARGET's own bounds. Before this, only
// Int128 went through here and the other pairs took the float64 path below, on the
// argument that any value large enough to round would be caught by narrow's range
// check. It was not: narrow checks the value after it has been rounded, against
// itself. Int64 and Uint64 above 2^53 came back rounded, MaxInt64 cast to Uint64
// came back as 2^63 — one MORE than the input, which fits a Uint64 and so passed —
// and MaxInt64 cast from a Uint64 was refused. intcast_test.go sweeps every pair.
func castInt(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	src, err := widenToInt128(c)
	if err != nil {
		return nil, err
	}
	return narrowI128(name, to, c.DType(), src, c.Validity(), strict)
}

// narrowI128 converts Int128 values to the integer type to, refusing (strict) or
// nulling (lossy) any value outside its range. from is the source type, for the
// refusal's hint.
func narrowI128(name string, to, from dtype.DataType, src []i128.Int128,
	valid bitmap.View, strict bool) (*data.Column, error) {

	switch to.Physical().ID() {
	case dtype.TypeInt8:
		return narrowSigned[int8](name, to, from, src, valid, strict, math.MinInt8, math.MaxInt8)
	case dtype.TypeInt16:
		return narrowSigned[int16](name, to, from, src, valid, strict, math.MinInt16, math.MaxInt16)
	case dtype.TypeInt32:
		return narrowSigned[int32](name, to, from, src, valid, strict, math.MinInt32, math.MaxInt32)
	case dtype.TypeInt64:
		return narrowSigned[int64](name, to, from, src, valid, strict, math.MinInt64, math.MaxInt64)
	case dtype.TypeUint8:
		return narrowUnsigned[uint8](name, to, from, src, valid, strict, math.MaxUint8)
	case dtype.TypeUint16:
		return narrowUnsigned[uint16](name, to, from, src, valid, strict, math.MaxUint16)
	case dtype.TypeUint32:
		return narrowUnsigned[uint32](name, to, from, src, valid, strict, math.MaxUint32)
	case dtype.TypeUint64:
		return narrowUnsigned[uint64](name, to, from, src, valid, strict, math.MaxUint64)
	case dtype.TypeInt128:
		buf, dst := newValuesBuffer[i128.Int128](len(src))
		copy(dst, src)
		return data.NewFixedBuffer(name, to, buf, len(src), valid), nil
	default:
		return nil, uerr.Internalf("kernel: cannot narrow Int128 to %s", to)
	}
}

func narrowSigned[T int8 | int16 | int32 | int64](name string, to, from dtype.DataType,
	src []i128.Int128, valid bitmap.View, strict bool, lo, hi int64) (*data.Column, error) {

	return narrowInto(name, to, from, src, valid, strict, func(v i128.Int128) (T, bool) {
		x, ok := v.Int64()
		return T(x), ok && x >= lo && x <= hi
	})
}

func narrowUnsigned[T uint8 | uint16 | uint32 | uint64](name string, to, from dtype.DataType,
	src []i128.Int128, valid bitmap.View, strict bool, hi uint64) (*data.Column, error) {

	return narrowInto(name, to, from, src, valid, strict, func(v i128.Int128) (T, bool) {
		x, ok := v.Uint64()
		return T(x), ok && x <= hi
	})
}

func narrowInto[T data.Primitive](name string, to, from dtype.DataType, src []i128.Int128,
	valid bitmap.View, strict bool, conv func(i128.Int128) (T, bool)) (*data.Column, error) {

	n := len(src)
	buf, dst := newValuesBuffer[T](n)
	ok := bitmap.NewBuilder(n)
	lossy := false
	for i, v := range src {
		t, fits := conv(v)
		if !fits {
			lossy = true
			if strict && valid.Get(i) {
				e := uerr.New(uerr.KindValue, "cast",
					"value %s at row %d is not representable as %s", v, i, to).
					Hint("use a non-strict cast to turn unrepresentable values into nulls")
				if from.ID() == dtype.TypeInt128 {
					e = e.Hint("an integer sum is an Int128 so that it cannot overflow; " +
						"keep it as one, or cast to Float64 for an approximate value")
				}
				return nil, e
			}
			ok.Append(false)
			continue
		}
		dst[i] = t
		ok.Append(true)
	}
	if lossy {
		valid = bitmap.And(valid, ok.Finish())
	}
	return data.NewFixedBuffer(name, to, buf, n, valid), nil
}

// castI128 converts between Int128 and a float. Integer pairs go through castInt.
func castI128(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	n := c.Len()

	// TO Int128, from a float: exact or refused, like every other float -> integer
	// cast. i128.FromFloat64 truncates, and this used to take its answer, so a strict
	// Cast(Int128) turned 3.7 into 3 where Cast(Int64) refuses it — two rules for one
	// question, one of which broke Cast's promise to fail on an unrepresentable value.
	if to.Physical().ID() == dtype.TypeInt128 {
		src, err := toFloat64(c)
		if err != nil {
			return nil, err
		}
		buf, dst := newValuesBuffer[i128.Int128](n)
		ok := bitmap.NewBuilder(n)
		valid := c.Validity()
		for i, f := range src {
			v, fits := i128.FromFloat64(f)
			if !fits || f != math.Trunc(f) {
				if strict && valid.Get(i) {
					return nil, uerr.New(uerr.KindValue, "cast",
						"value %v at row %d is not representable as Int128", f, i).
						Hint("use a non-strict cast to turn unrepresentable values into nulls").
						Hint("to drop the fraction on purpose, apply .Floor() or .Ceil() first")
				}
				ok.Append(false)
				continue
			}
			dst[i] = v
			ok.Append(true)
		}
		return data.NewFixedBuffer(name, to, buf, n, bitmap.And(valid, ok.Finish())), nil
	}

	// FROM Int128, to a float.
	src, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	f := make([]float64, n)
	for i, v := range src {
		f[i] = v.Float64()
	}
	return fromFloat64(name, to, f, c.Validity(), strict)
}

func widenToInt64(c *data.Column) ([]int64, error) {
	n := c.Len()
	out := make([]int64, n)
	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return convInt[int8](c, out)
	case dtype.TypeInt16:
		return convInt[int16](c, out)
	case dtype.TypeInt32:
		return convInt[int32](c, out)
	case dtype.TypeInt64:
		return data.Values[int64](c)
	default:
		return nil, uerr.Internalf("kernel: %s is not a signed integer", c.DType())
	}
}

func convInt[T data.Primitive](c *data.Column, out []int64) ([]int64, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = int64(v[i])
	}
	return out, nil
}

func widenToUint64(c *data.Column) ([]uint64, error) {
	n := c.Len()
	out := make([]uint64, n)
	switch c.DType().Physical().ID() {
	case dtype.TypeUint8:
		return convUint[uint8](c, out)
	case dtype.TypeUint16:
		return convUint[uint16](c, out)
	case dtype.TypeUint32:
		return convUint[uint32](c, out)
	case dtype.TypeUint64:
		return data.Values[uint64](c)
	default:
		return nil, uerr.Internalf("kernel: %s is not an unsigned integer", c.DType())
	}
}

func convUint[T data.Primitive](c *data.Column, out []uint64) ([]uint64, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = uint64(v[i])
	}
	return out, nil
}

// rescaleTemporal converts a tick count from one resolution to another.
//
// # Why the division floors rather than truncating
//
// Go's integer division truncates toward zero, so -1 nanosecond before the epoch
// would divide to day 0 — 1970-01-01 — while +1 nanosecond after also gives day 0.
// Two different instants collapsing onto the same date, asymmetrically around the
// epoch, is the classic pre-1970 date bug. Flooring keeps the mapping monotone.
//
// Widening (a coarser unit to a finer one) can overflow: year 3000 in seconds is
// 3.2e10, which times 1e9 exceeds int64. Overflow produces a NULL rather than a
// wrapped instant, because a wrapped timestamp is a plausible-looking wrong answer
// and a null is not.
//
// # Strict, which this ignored until step 49
//
// The four other lossy paths in this file — narrow, both castI128 arms and
// parseFromString — all refuse under strict and name the offending row, and Cast's
// own doc states the contract as a fact about the kernel: "Strict casts fail the
// query and name the offending row." This one took no strict parameter at all, so
// Cast.Field's `Nullable: cf.Nullable || !c.Strict` declared a strict widening cast
// non-nullable and the kernel then handed it nulls.
//
// It matters beyond a user-written Cast: evalBinary casts operands to the binding
// with strict=true, and three arms of the temporal algebra widen — `Datetime(s) -
// Datetime(ns)` retimes the seconds column to nanoseconds — so an ordinary
// subtraction over two non-nullable columns could produce nulls with no cast
// anywhere in the query to explain them.
func rescaleTemporal(name string, to dtype.DataType, c *data.Column, fromNanos, toNanos int64, strict bool) (*data.Column, error) {
	src, err := readTicks(c)
	if err != nil {
		return nil, err
	}
	n := len(src)
	out := make([]int64, n)
	valid := c.Validity()
	overflowed := false

	if fromNanos > toNanos {
		// Coarser -> finer: multiply, and check for overflow.
		f := fromNanos / toNanos
		limit := math.MaxInt64 / f
		ok := bitmap.NewBuilder(n)
		for i, v := range src {
			fits := v <= limit && v >= -limit
			if !fits && strict && valid.Get(i) {
				return nil, uerr.New(uerr.KindValue, "cast",
					"instant %d at row %d overflows %s", v, i, to).
					Hint("widening to a finer unit multiplies the tick count, and " +
						"int64 nanoseconds only span about 1677 to 2262").
					Hint("use a non-strict cast to turn unrepresentable instants into nulls")
			}
			ok.Append(fits && valid.Get(i))
			overflowed = overflowed || !fits
			if fits {
				out[i] = v * f
			}
		}
		if overflowed {
			valid = ok.Finish()
		}
	} else {
		// Finer -> coarser: floor-divide, losing precision by definition.
		f := toNanos / fromNanos
		for i, v := range src {
			q := v / f
			if v%f != 0 && v < 0 {
				q-- // floor, not truncate
			}
			out[i] = q
		}
	}

	if to.Physical().ID() == dtype.TypeInt32 {
		// Date is int32 days, and this used to be a bare int32(v).
		//
		// The finer-to-coarser branch above has no overflow guard because dividing
		// cannot overflow — but the NARROWING to int32 can, and did so silently.
		// Datetime(s) -> Date divides by 86400 and still leaves room for a tick
		// count far past int32, so the truncation produced a wrapped date: verbatim
		// the outcome this function's own doc says it exists to prevent, "a
		// plausible-looking wrong answer".
		d32 := make([]int32, n)
		ok := bitmap.NewBuilder(n)
		wrapped := false
		for i, v := range out {
			fits := v >= math.MinInt32 && v <= math.MaxInt32
			if !fits && strict && valid.Get(i) {
				return nil, uerr.New(uerr.KindValue, "cast",
					"day %d at row %d does not fit in %s", v, i, to).
					Hint("Date is a signed 32-bit day count, spanning about " +
						"5.88 million years either side of 1970")
			}
			ok.Append(fits && valid.Get(i))
			wrapped = wrapped || !fits
			if fits {
				d32[i] = int32(v)
			}
		}
		if wrapped {
			valid = ok.Finish()
		}
		return data.NewFixed(name, to, d32, valid), nil
	}
	return data.NewFixed(name, to, out, valid), nil
}

// readTicks reads a temporal column's storage as int64, whatever its width.
func readTicks(c *data.Column) ([]int64, error) {
	switch c.DType().Physical().ID() {
	case dtype.TypeInt32:
		v, err := data.Values[int32](c)
		if err != nil {
			return nil, err
		}
		out := make([]int64, len(v))
		for i, x := range v {
			out[i] = int64(x)
		}
		return out, nil
	case dtype.TypeInt64:
		return data.Values[int64](c)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cannot read %s as a tick count", c.DType())
	}
}

// parseFromString converts a String column into to.
//
// Non-strict is the default and turns an unparseable value into a NULL, which is
// why the type checker marks every non-strict cast nullable. Strict fails the query
// and names the offending row and text — the same contract the CSV reader honours,
// and for the same reason: "cannot parse" on a ten-million-row frame is useless.
func parseFromString(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	acc := c.Strings()
	n := c.Len()
	valid := c.Validity()
	ok := bitmap.NewBuilder(n)

	// One parser resolved per column rather than per value, matching the CSV
	// reader's builder discipline.
	var (
		ints   []int64
		floats []float64
		bools  *bitmap.Builder
	)
	switch {
	case to.IsTemporal(), to.Physical().IsInteger():
		ints = make([]int64, n)
	case to.IsFloat():
		floats = make([]float64, n)
	case to.IsBool():
		bools = bitmap.NewBuilder(n)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cast from %s to %s is not implemented yet", c.DType(), to)
	}

	for i := range n {
		if !valid.Get(i) {
			ok.Append(false)
			if bools != nil {
				bools.Append(false)
			}
			continue
		}
		s := acc.Get(i)
		good := true
		switch {
		case to.IsTemporal():
			v, p := dtype.ParseTemporal(to, s)
			ints[i], good = v, p
		case to.Physical().IsInteger():
			v, err := strconv.ParseInt(s, 10, 64)
			ints[i], good = v, err == nil
		case to.IsFloat():
			v, err := strconv.ParseFloat(s, 64)
			floats[i], good = v, err == nil
		case to.IsBool():
			v, err := strconv.ParseBool(s)
			bools.Append(v)
			good = err == nil
		}
		if !good && strict {
			return nil, uerr.New(uerr.KindValue, "cast",
				"cannot parse %s as %s at row %d", strconv.Quote(s), to, i).
				Hint("use CastLossy to turn unparseable values into nulls instead")
		}
		ok.Append(good)
	}

	outValid := ok.Finish()
	switch {
	case bools != nil:
		return data.NewBool(name, bools.Finish(), outValid), nil
	case floats != nil:
		return narrowFloat(name, to, floats, outValid)
	default:
		return narrowInt(name, to, ints, outValid)
	}
}

// formatToString renders a column as text, using the same formatter the frame
// renderer and the CSV writer use, so all three agree.
func formatToString(name string, c *data.Column) (*data.Column, error) {
	n := c.Len()
	out := make([]string, n)
	valid := c.Validity()
	dt := c.DType()

	if dt.IsTemporal() {
		ticks, err := readTicks(c)
		if err != nil {
			return nil, err
		}
		for i := range n {
			if valid.Get(i) {
				out[i] = dtype.FormatTemporal(dt, ticks[i])
			}
		}
		return data.NewString(name, out, valid), nil
	}
	if dt.IsBool() {
		bits := c.Bools()
		for i := range n {
			if valid.Get(i) {
				out[i] = strconv.FormatBool(bits.Get(i))
			}
		}
		return data.NewString(name, out, valid), nil
	}
	if dt.IsFloat() {
		f, err := toFloat64(c)
		if err != nil {
			return nil, err
		}
		bits := 64
		if dt == dtype.Float32 {
			bits = 32
		}
		for i := range n {
			if valid.Get(i) {
				out[i] = strconv.FormatFloat(f[i], 'g', -1, bits)
			}
		}
		return data.NewString(name, out, valid), nil
	}
	ticks, err := readAnyInt(c)
	if err != nil {
		return nil, err
	}
	for i := range n {
		if valid.Get(i) {
			out[i] = ticks[i]
		}
	}
	return data.NewString(name, out, valid), nil
}

// narrowInt places parsed int64s into the target's storage width, nulling any
// value that does not fit rather than wrapping it.
func narrowInt(name string, to dtype.DataType, v []int64, valid bitmap.View) (*data.Column, error) {
	f := make([]float64, len(v))
	for i, x := range v {
		f[i] = float64(x)
	}
	if to.IsTemporal() {
		// Temporal storage is exactly int32 days or an int64 tick count; there is
		// nothing to narrow through a float.
		if to.Physical().ID() == dtype.TypeInt32 {
			d32 := make([]int32, len(v))
			for i, x := range v {
				d32[i] = int32(x)
			}
			return data.NewFixed(name, to, d32, valid), nil
		}
		return data.NewFixed(name, to, v, valid), nil
	}
	return fromFloat64(name, to, f, valid, false)
}

func narrowFloat(name string, to dtype.DataType, v []float64, valid bitmap.View) (*data.Column, error) {
	return fromFloat64(name, to, v, valid, false)
}

// readAnyInt formats an integer column exactly.
//
// # Exactly, which it did not used to be
//
// It routed everything through toFloat64, so a Uint64 above 2^53 formatted as its
// rounded neighbour — and Count, Len, NUnique and NullCount all output Uint64.
// Int128 had no path at all and failed outright, which mattered because Int128 is
// what EVERY integer Sum produces, so Col("x").Sum().Cast(String) was the same
// ordinary query that broke Abs. Both were promises dtype.CanCast made and this
// kernel did not keep.
func readAnyInt(c *data.Column) ([]string, error) {
	n := c.Len()
	out := make([]string, n)

	switch c.DType().Physical().ID() {
	case dtype.TypeInt128:
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		// A Decimal is physically an Int128 of unscaled units, so it lands here and
		// would otherwise print 12.34 as "1234" — wrong by a factor of 10^scale, and
		// plausible enough that nobody would question it. This is the same guard
		// DataFrame.String() carries for the same reason.
		if dt := c.DType(); dt.ID() == dtype.TypeDecimal {
			for i := range n {
				out[i] = dtype.FormatDecimal(v[i].String(), dt.Scale())
			}
			return out, nil
		}
		for i := range n {
			out[i] = v[i].String()
		}
		return out, nil

	case dtype.TypeUint8:
		return formatUints[uint8](c, out)
	case dtype.TypeUint16:
		return formatUints[uint16](c, out)
	case dtype.TypeUint32:
		return formatUints[uint32](c, out)
	case dtype.TypeUint64:
		return formatUints[uint64](c, out)
	case dtype.TypeInt8:
		return formatInts[int8](c, out)
	case dtype.TypeInt16:
		return formatInts[int16](c, out)
	case dtype.TypeInt32:
		return formatInts[int32](c, out)
	case dtype.TypeInt64:
		return formatInts[int64](c, out)
	default:
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cannot format a %s column as text", c.DType())
	}
}

func formatUints[T ~uint8 | ~uint16 | ~uint32 | ~uint64](c *data.Column, out []string) ([]string, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = strconv.FormatUint(uint64(v[i]), 10)
	}
	return out, nil
}

func formatInts[T ~int8 | ~int16 | ~int32 | ~int64](c *data.Column, out []string) ([]string, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	for i := range v {
		out[i] = strconv.FormatInt(int64(v[i]), 10)
	}
	return out, nil
}
