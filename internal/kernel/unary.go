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
func Cast(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
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

	// A Decimal is stored as its UNSCALED integer, so every numeric cast below
	// would silently return the wrong number by a factor of 10^scale:
	// Decimal(10,2) holding 12.34 casts to Int64 as 1234, and Int64 5 casts to
	// Decimal(10,2) as 0.05. The identity cast is already handled above; a change
	// of precision or scale needs rescaling that is not written yet.
	//
	// Decimal TO STRING is the exception and is exact: the scale is known, so the
	// unscaled integer can be pointed at rather than divided. dtype.CanCast has
	// always promised it, and refusing it here was the same plan-accepts /
	// kernel-rejects divergence this file records for null casts and string casts.
	if (from.ID() == dtype.TypeDecimal || to.ID() == dtype.TypeDecimal) &&
		to.ID() != dtype.TypeString {
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cast from %s to %s is not implemented yet", from, to).
			Hint("a decimal is stored as an unscaled integer, so this cast would " +
				"be wrong by a factor of 10^scale rather than merely imprecise")
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

	fp, tp := from.Physical(), to.Physical()
	if !fp.IsNumeric() || !tp.IsNumeric() {
		return nil, uerr.New(uerr.KindUnsupported, "cast",
			"cast from %s to %s is not implemented yet", from, to)
	}

	// Int128 is handled before the float64 path below. Routing a 128-bit value
	// through a float64 would round above 2^53 — precisely the silent precision
	// loss that widening sums to 128 bits exists to prevent.
	if fp.ID() == dtype.TypeInt128 || tp.ID() == dtype.TypeInt128 {
		return castI128(name, to, strict, c)
	}

	// Everything else widens through float64 as the common currency. Exact for
	// every remaining pair: the widest non-128-bit integer ursus can produce is
	// Uint64, and any value large enough to round is caught by the range check in
	// narrow.
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

// castI128 converts to or from Int128.
//
// Narrowing from Int128 uses the CHECKED conversions, so a sum that genuinely
// exceeded int64 is reported rather than truncated. That check is the entire
// payoff of accumulating at 128 bits: the overflow becomes impossible internally
// and visible at the boundary.
func castI128(name string, to dtype.DataType, strict bool, c *data.Column) (*data.Column, error) {
	n := c.Len()
	from := c.DType()

	// TO Int128 — always exact from any integer, and from a float by truncation.
	if to.Physical().ID() == dtype.TypeInt128 {
		buf, dst := newValuesBuffer[i128.Int128](n)
		ok := bitmap.NewBuilder(n)
		valid := c.Validity()

		switch {
		case from.IsSignedInteger():
			src, err := widenToInt64(c)
			if err != nil {
				return nil, err
			}
			for i, v := range src {
				dst[i] = i128.FromInt64(v)
				ok.Append(true)
			}
		case from.IsUnsignedInteger():
			src, err := widenToUint64(c)
			if err != nil {
				return nil, err
			}
			for i, v := range src {
				dst[i] = i128.FromUint64(v)
				ok.Append(true)
			}
		default:
			src, err := toFloat64(c)
			if err != nil {
				return nil, err
			}
			for i, f := range src {
				v, fits := i128.FromFloat64(f)
				if !fits {
					if strict && valid.Get(i) {
						return nil, uerr.New(uerr.KindValue, "cast",
							"value %v at row %d is not representable as Int128", f, i)
					}
					ok.Append(false)
					continue
				}
				dst[i] = v
				ok.Append(true)
			}
			valid = bitmap.And(valid, ok.Finish())
		}
		return data.NewFixedBuffer(name, to, buf, n, valid), nil
	}

	// FROM Int128.
	src, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	if to.Physical().ID() == dtype.TypeFloat64 || to.Physical().ID() == dtype.TypeFloat32 {
		f := make([]float64, n)
		for i, v := range src {
			f[i] = v.Float64()
		}
		return fromFloat64(name, to, f, c.Validity(), strict)
	}

	// Narrowing to an integer: check every value.
	f := make([]float64, n)
	ok := bitmap.NewBuilder(n)
	valid := c.Validity()
	lossy := false
	for i, v := range src {
		iv, fits := v.Int64()
		if !fits {
			lossy = true
			if strict && valid.Get(i) {
				return nil, uerr.New(uerr.KindValue, "cast",
					"value %s at row %d does not fit in %s", v, i, to).
					Hint("the sum exceeded 64 bits; keep it as Int128 or cast to Float64")
			}
			ok.Append(false)
			continue
		}
		f[i] = float64(iv)
		ok.Append(true)
	}
	if lossy {
		valid = bitmap.And(valid, ok.Finish())
	}
	return fromFloat64(name, to, f, valid, strict)
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
