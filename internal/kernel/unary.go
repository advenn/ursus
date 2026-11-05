package kernel

import (
	"math"

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

	// Kleene NOT: NOT null is null, so validity passes straight through. An
	// integer's is its bits' (step 278).
	case expr.OpNot:
		if c.DType().IsInteger() {
			return bitwiseNot(name, c)
		}
		return data.NewBool(name, bitmap.Not(c.Bools()), c.Validity()), nil

	// NaN predicates are NOT total. `null.is_nan()` is null, not false, because a
	// missing value is not a float that could be tested. Null and NaN are
	// different things and ursus keeps them different all the way down.
	case expr.OpIsNan, expr.OpIsNotNan, expr.OpIsFinite, expr.OpIsInfinite:
		return nanPredicate(op, name, c, n)

	case expr.OpNeg, expr.OpAbs, expr.OpSign, expr.OpFloor, expr.OpCeil:
		// A Decimal's sign, floor and ceil are its value's, at its scale (step 490),
		// into the type expr.DecimalRounded gives.
		if c.DType().ID() == dtype.TypeDecimal {
			switch op {
			case expr.OpSign:
				return decimalSign(name, out, c)
			case expr.OpFloor:
				return decimalRound("floor", name, out, c, 0, decFloor)
			case expr.OpCeil:
				return decimalRound("ceil", name, out, c, 0, decCeil)
			}
		}
		// The TYPE-PRESERVING unary family: out == in for all five.
		if (op == expr.OpNeg || op == expr.OpAbs) && c.DType().ID() == dtype.TypeDuration {
			if err := durationNegates(op, c); err != nil {
				return nil, err
			}
		}
		return unaryArith(op, name, out, c, n)

	case expr.OpSqrt, expr.OpCbrt, expr.OpExp, expr.OpLn, expr.OpLog10, expr.OpLog1p,
		expr.OpSin, expr.OpCos, expr.OpTan, expr.OpArcSin, expr.OpArcCos, expr.OpArcTan,
		expr.OpSinh, expr.OpCosh, expr.OpTanh, expr.OpArcSinh, expr.OpArcCosh, expr.OpArcTanh,
		expr.OpDegrees, expr.OpRadians, expr.OpCot, expr.OpErf, expr.OpErfc:
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
// abs is a sign test and a negation. Neg(Min) has no answer: Min's magnitude is one
// past Max. It used to be Min itself, wrapping as Int8..Int64 do, which broke the
// promise Int128's arithmetic makes since step 100, exact or refused: Lit(0).Sub(x)
// refused the same value Neg returned.
func unaryI128(op expr.UnaryOp, name string, out dtype.DataType,
	c *data.Column, n int) (*data.Column, error) {

	v, err := data.Values[i128.Int128](c)
	if err != nil {
		return nil, err
	}
	if op == expr.OpNeg || op == expr.OpAbs {
		valid := c.Validity()
		for i, x := range v {
			if x == i128.Min && valid.Get(i) {
				return nil, uerr.New(uerr.KindValue, op.String(),
					"%s of %s at row %d overflows Int128", op, x, i).
					Hint("Int128 holds about ±1.7e38, and its arithmetic is exact or refused").
					Hint("cast to Float64 first for an approximate answer")
			}
		}
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
		// Decimal never arrives, nor at sign: Unary rounds it at its scale first,
		// because the stored value is UNSCALED and flooring it here would floor 1234
		// rather than 12.34.
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
// So this widens first, computes in float64, and stores the result at the output's
// width with floatResult — which rounds, because a Float32 result is a rounded one.
// It used to narrow back through Cast's path, and a cast's round-trip test nulled
// every inexact Float32 result.
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
	case expr.OpErf:
		for i, v := range src {
			dst[i] = math.Erf(v)
		}
	case expr.OpErfc:
		// Erfc is not 1 - Erf: erfc(6) is 2.2e-17, a value where 1 - erf(6) is 0.
		for i, v := range src {
			dst[i] = math.Erfc(v)
		}
	case expr.OpCot:
		// A Float32's is 1/tan in float32, as Polars divides it, where the float64
		// cotangent rounded once can land a float32 apart: cot(1) is 0.6420926 there
		// and 0.64209265 here.
		f32 := out.ID() == dtype.TypeFloat32
		for i, v := range src {
			if f32 {
				dst[i] = float64(1 / float32(math.Tan(v)))
			} else {
				dst[i] = 1 / math.Tan(v)
			}
		}
	default:
		f, ok := trig[op]
		if !ok {
			return nil, uerr.Internalf("kernel: unaryMath got %s", op)
		}
		for i, v := range src {
			dst[i] = f(v)
		}
	}

	return floatResult(name, out, dst, c.Validity())
}

// trig is the trigonometric block, one Go function each. Outside its domain each
// gives NaN — arcsin(2), arccosh(0) — and arctanh(±1) gives ±Inf, as math does:
// values, as sqrt(-1) is.
var trig = map[expr.UnaryOp]func(float64) float64{
	expr.OpSin: math.Sin, expr.OpCos: math.Cos, expr.OpTan: math.Tan,
	expr.OpArcSin: math.Asin, expr.OpArcCos: math.Acos, expr.OpArcTan: math.Atan,
	expr.OpSinh: math.Sinh, expr.OpCosh: math.Cosh, expr.OpTanh: math.Tanh,
	expr.OpArcSinh: math.Asinh, expr.OpArcCosh: math.Acosh, expr.OpArcTanh: math.Atanh,
	expr.OpDegrees: func(r float64) float64 { return r * (180 / math.Pi) },
	expr.OpRadians: func(d float64) float64 { return d * (math.Pi / 180) },
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

// Cast, castTo and the temporal rescaling are cast.go; the numeric conversions
// castnum.go; formatting to text caststring.go; the Enum and Categorical casts, and
// ToPhysical, castenum.go.
