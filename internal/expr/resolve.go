package expr

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Binding is the resolved form of a binary operation: the type each operand must
// be cast to, and the type the result will have.
//
// The evaluator MUST use this rather than re-deriving types from the operands.
// Two independent copies of the promotion rules would drift, and the drift would
// show up as a mismatch between the schema CollectSchema promised and the column
// Eval actually produced — which is precisely the invariant the evaluator
// contract test checks.
type Binding struct {
	CastL dtype.DataType // cast the left operand to this before the kernel
	CastR dtype.DataType
	Out   dtype.DataType // the kernel's output type
}

// NeedsCastL reports whether the left operand requires a cast.
func (b Binding) NeedsCastL(l dtype.DataType) bool { return l != b.CastL }
func (b Binding) NeedsCastR(r dtype.DataType) bool { return r != b.CastR }

// ResolveBinary computes the operand and result types of op applied to l and r.
func ResolveBinary(op BinaryOp, l, r dtype.DataType) (Binding, error) {
	switch {
	case op.IsLogical():
		return resolveLogical(op, l, r)
	case op.IsMissingComparison():
		return resolveMissingComparison(op, l, r)
	case op.IsComparison():
		return resolveComparison(op, l, r)
	default:
		return resolveArithmetic(op, l, r)
	}
}

func resolveLogical(op BinaryOp, l, r dtype.DataType) (Binding, error) {
	// Bool only. Integers are NOT implicitly truthy: `Col("count") & Col("flag")`
	// is far more likely to be a mistake than an intent, and bitwise operations on
	// integers get their own named methods rather than sharing a spelling with
	// logical AND.
	ok := func(d dtype.DataType) bool { return d.IsBool() || d.IsNull() }
	if !ok(l) || !ok(r) {
		return Binding{}, uerr.New(uerr.KindType, "",
			"operator %s requires Boolean operands, got %s and %s", op, l, r).
			Hint("use a comparison to produce a Boolean, e.g. col.Gt(0)")
	}
	return Binding{CastL: dtype.Bool, CastR: dtype.Bool, Out: dtype.Bool}, nil
}

func resolveMissingComparison(op BinaryOp, l, r dtype.DataType) (Binding, error) {
	common, ok := dtype.Promote(l, r)
	if !ok {
		return Binding{}, mismatch(op, l, r)
	}
	return Binding{CastL: common, CastR: common, Out: dtype.Bool}, nil
}

func resolveComparison(op BinaryOp, l, r dtype.DataType) (Binding, error) {
	common, ok := dtype.Promote(l, r)
	if !ok {
		return Binding{}, mismatch(op, l, r)
	}
	// Equality is defined for anything with a common type; ordering additionally
	// requires the type to actually be ordered. Comparing two Structs with < has
	// no defensible meaning, so it is rejected rather than given an arbitrary one.
	if op.IsOrdering() && !isOrdered(common) {
		return Binding{}, uerr.New(uerr.KindType, "",
			"operator %s is not defined for %s", op, common).
			Hint("only numeric, temporal, string and boolean types have an ordering")
	}
	return Binding{CastL: common, CastR: common, Out: dtype.Bool}, nil
}

// isOrdered delegates to the one definition. Kept as a local name because it
// reads better at the two call sites than dtype.DataType.IsOrdered does.
func isOrdered(d dtype.DataType) bool { return d.IsOrdered() }

func resolveArithmetic(op BinaryOp, l, r dtype.DataType) (Binding, error) {
	// Temporal arithmetic has its own algebra and does not go through numeric
	// promotion: subtracting two Datetimes yields a Duration, not a Datetime.
	if l.IsTemporal() || r.IsTemporal() {
		return resolveTemporalArithmetic(op, l, r)
	}

	common, ok := dtype.Promote(l, r)
	if !ok {
		return Binding{}, mismatch(op, l, r)
	}
	if !common.IsNumeric() {
		return Binding{}, uerr.New(uerr.KindType, "",
			"operator %s is not defined for %s", op, common).
			Hint("arithmetic requires numeric operands")
	}

	// Decimal survives promotion only when both sides are the SAME Decimal type
	// (Promote rejects every mixed case). Add and Sub are then exact: equal scales
	// mean the unscaled integers add directly, which is what the Int128 kernel does.
	//
	// Multiplication and division are not, and this is the trap. Decimal(10,2) *
	// Decimal(10,2) has scale 4, not 2 — 12.34 * 12.34 = 152.2756. Promote returns
	// the operand type unchanged, so without this guard the result would be labelled
	// scale 2 while holding scale-4 digits, and 152.2756 would print as 15227.56.
	// Off by 10^scale, entirely plausible, and no error anywhere.
	if common.ID() == dtype.TypeDecimal && op != OpAdd && op != OpSub {
		return Binding{}, uerr.New(uerr.KindType, "",
			"operator %s is not defined for %s", op, common).
			Hint("%s changes a decimal's scale and ursus does not implement the "+
				"precision calculus yet; only + and - are exact", op).
			Hint("cast to Float64 first if approximate arithmetic is acceptable")
	}

	// Int128 is the same trap one type over, and it was live: Int64 * Uint64
	// promotes to Int128 because neither can hold the other's range, but arithI128
	// implements ONLY add and sub. Field therefore promised Int128 and Eval refused
	// with "operator * is not implemented for Int128" — a refusal that CollectSchema
	// and Explain do not see, so a plan printed cleanly and then would not run.
	//
	// Step 49's own comment in op.go asserted "resolveArithmetic refuses the rest
	// before that". It did not. TestEvaluatorContract is what found it, on the first
	// run after the test the docs had promised for dozens of steps was finally
	// written.
	if common.ID() == dtype.TypeInt128 && op != OpAdd && op != OpSub &&
		op != OpDiv && op != OpPow {
		return Binding{}, uerr.New(uerr.KindType, "",
			"operator %s is not implemented for %s", op, common).
			Hint("%s and %s promote to Int128, where only + and - are implemented", l, r).
			Hint("cast to Int64 or Float64 first")
	}

	switch op {
	case OpDiv, OpPow:
		// True division always produces a float, even for two integers. 7/2 == 3.5.
		// Integer-truncating division is spelled FloorDiv, so the surprising
		// behaviour has to be asked for by name.
		//
		// Exponentiation shares the arm for the same reason and it is not optional:
		// 2 ** -1 is 0.5, so the default `Out: common` would truncate every negative
		// integer power to zero. Casting both operands to float here is also what
		// keeps Pow out of arithNum and arithI128 entirely — it only ever reaches
		// arithFloat.
		out := dtype.Float64
		if common == dtype.Float32 {
			out = dtype.Float32
		}
		return Binding{CastL: out, CastR: out, Out: out}, nil

	default:
		return Binding{CastL: common, CastR: common, Out: common}, nil
	}
}

// resolveTemporalArithmetic implements the small algebra of instants and durations.
//
//	Datetime - Datetime  → Duration      Date - Date          → Duration(s)
//	Datetime ± Duration  → Datetime      Date ± Duration      → Datetime
//	Duration ± Duration  → Duration      Duration * / number  → Duration
//
// Notably Datetime + Datetime is rejected: adding two instants is meaningless,
// and silently reinterpreting it as integer addition would produce a plausible
// number with no physical meaning.
func resolveTemporalArithmetic(op BinaryOp, l, r dtype.DataType) (Binding, error) {
	isDur := func(d dtype.DataType) bool { return d.ID() == dtype.TypeDuration }
	isInstant := func(d dtype.DataType) bool {
		return d.ID() == dtype.TypeDate || d.ID() == dtype.TypeDatetime || d.ID() == dtype.TypeTime
	}

	switch {
	// instant - instant → duration, at the finer of the two resolutions.
	case op == OpSub && isInstant(l) && isInstant(r):
		if l.ID() != r.ID() {
			return Binding{}, mismatch(op, l, r)
		}
		unit := finerUnit(l, r)
		out := dtype.Duration(unit)
		common := l
		if l.ID() == dtype.TypeDate {
			// Date has no unit of its own; differences are whole seconds.
			out = dtype.Duration(dtype.Second)
		} else {
			common = retimed(l, unit)
		}
		return Binding{CastL: common, CastR: common, Out: out}, nil

	// instant ± duration → instant
	//
	// The duration is RETIMED to the instant's unit first. Without that,
	// Datetime(s) + Duration(ns) adds a nanosecond count to a second count — both
	// are physically Int64, so the kernel runs happily and the answer is off by
	// 10^9. The duration ± duration case below always did this correctly; this one
	// did not, and nothing caught it because no test touched temporal arithmetic.
	//
	// Retiming the DURATION rather than the instant is deliberate: widening the
	// instant would change the output type, so `ts + 1h` would silently return a
	// nanosecond-resolution column for a second-resolution input.
	case (op == OpAdd || op == OpSub) && isInstant(l) && isDur(r):
		return Binding{CastL: l, CastR: durationLike(l), Out: l}, nil

	// duration + instant → instant (addition only; instant - duration is above)
	case op == OpAdd && isDur(l) && isInstant(r):
		return Binding{CastL: durationLike(r), CastR: r, Out: r}, nil

	// duration ± duration → duration at the finer resolution
	case (op == OpAdd || op == OpSub) && isDur(l) && isDur(r):
		out := dtype.Duration(finerUnit(l, r))
		return Binding{CastL: out, CastR: out, Out: out}, nil

	// duration scaled by a number stays a duration
	//
	// The scalar is cast to the duration's INTEGER storage type, not left as it
	// was. `Duration * 2.5` previously bound a Float64 operand to an Int64 kernel,
	// which failed at execution with "cannot be read as Int64" — a runtime error
	// for something the type checker had already accepted.
	//
	// Casting to Int64 makes it a truncating scale: 90m * 1.5 is 135m, but
	// 1ns * 2.5 is 2ns. That is the honest reading of an integer tick count, and it
	// is what the output type already promised.
	// OpDiv is deliberately NOT here. A duration is an integer tick count, so
	// dividing one by a number is truncating — and this file's own rule for that is
	// "integer-truncating division is spelled FloorDiv, so the surprising behaviour
	// has to be asked for by name". Binding it to Out: Duration sent it to arithNum,
	// which implements no integer OpDiv, so Field promised a Duration and Eval
	// refused with "operator / is not implemented for Duration(ns)". Refusing here
	// says the same thing at plan time, where Explain can see it.
	case op == OpDiv && isDur(l) && r.IsNumeric():
		return Binding{}, uerr.New(uerr.KindType, "",
			"operator / is not defined for %s and %s", l, r).
			Hint("dividing a duration by a number truncates; spell it FloorDiv").
			Hint("or cast the duration to Float64 for an approximate result")

	case (op == OpMul || op == OpFloorDiv) && isDur(l) && r.IsNumeric():
		if err := integralScale(op, l, r); err != nil {
			return Binding{}, err
		}
		return Binding{CastL: l, CastR: dtype.Int64, Out: l}, nil
	case op == OpMul && l.IsNumeric() && isDur(r):
		if err := integralScale(op, r, l); err != nil {
			return Binding{}, err
		}
		return Binding{CastL: dtype.Int64, CastR: r, Out: r}, nil

	// duration / duration → a dimensionless ratio, SAME UNIT ONLY.
	//
	// The operands are cast to Float64, because kernel.arithmetic dispatches on
	// Out.Physical() and that is Float64 here — binding them as Duration handed
	// Int64 storage to a float kernel, and Field promised a Float64 the evaluator
	// could not produce.
	//
	// A mixed-unit pair is REFUSED rather than converted, and the reason is that a
	// Binding carries one cast per side. Getting 1s / 1000000000ns to equal 1 needs
	// two: retime to the finer unit, then widen to float. Doing only the second —
	// which is what "cast both to Float64" means — divides 1.0 by 1e9 and returns
	// 1e-09. That was measured, not assumed: it is what this arm did before the
	// refusal was added, and a wrong ratio is worse than the type error it replaced.
	case op == OpDiv && isDur(l) && isDur(r):
		if l.TimeUnit() != r.TimeUnit() {
			return Binding{}, uerr.New(uerr.KindType, "",
				"operator / is not defined for %s and %s", l, r).
				Hint("a ratio of durations needs both sides at the same resolution").
				Hint("cast one explicitly, e.g. .Cast(ursus.Duration(ursus.Nano))")
		}
		return Binding{CastL: dtype.Float64, CastR: dtype.Float64, Out: dtype.Float64}, nil

	default:
		e := uerr.New(uerr.KindType, "",
			"operator %s is not defined for %s and %s", op, l, r)
		if op == OpAdd && isInstant(l) && isInstant(r) {
			e.Hint("adding two instants has no meaning; subtract them to get a Duration")
		}
		return Binding{}, e
	}
}

func finerUnit(a, b dtype.DataType) dtype.TimeUnit {
	if a.TimeUnit().Finer(b.TimeUnit()) {
		return a.TimeUnit()
	}
	return b.TimeUnit()
}

// integralScale rejects scaling a duration by a fractional number.
//
// A Duration is an integer tick count, so the arithmetic is integer arithmetic.
// Two wrong answers were available and both were worse than an error: binding the
// float operand straight to the Int64 kernel, which is what used to happen and
// failed at EXECUTION for something the type checker had accepted; or truncating
// the multiplier, which makes `1h * 2.5` silently 2h.
//
// So it is refused at plan time, and the message names the conversion that does
// what the user meant.
func integralScale(op BinaryOp, dur, num dtype.DataType) error {
	if !num.IsFloat() {
		return nil
	}
	return uerr.New(uerr.KindType, "",
		"cannot %s a %s by a fractional %s", opVerb(op), dur, num).
		Hint("a duration is an integer tick count, so scaling it by 2.5 has no "+
			"exact answer at its resolution").
		Hint("convert first: .Cast(ursus.Float64).Mul(2.5).Cast(%s)", dur)
}

func opVerb(op BinaryOp) string {
	switch op {
	case OpMul:
		return "multiply"
	case OpDiv, OpFloorDiv:
		return "divide"
	default:
		return op.String()
	}
}

// durationLike returns a Duration at the same resolution as the instant d, so an
// instant and a duration can be added without either one silently changing scale.
// Date has no TimeUnit of its own; its differences are whole seconds.
func durationLike(d dtype.DataType) dtype.DataType {
	if d.ID() == dtype.TypeDate {
		return dtype.Duration(dtype.Second)
	}
	return dtype.Duration(d.TimeUnit())
}

// retimed returns d with its resolution changed, preserving everything else.
func retimed(d dtype.DataType, u dtype.TimeUnit) dtype.DataType {
	switch d.ID() {
	case dtype.TypeDatetime:
		return dtype.Datetime(u, d.TimeZone())
	case dtype.TypeTime:
		return dtype.Time(u)
	case dtype.TypeDuration:
		return dtype.Duration(u)
	default:
		return d
	}
}

func mismatch(op BinaryOp, l, r dtype.DataType) error {
	e := uerr.New(uerr.KindType, "",
		"operator %s has no common type for %s and %s", op, l, r)
	if (l.ID() == dtype.TypeUint64 && r.IsSignedInteger()) ||
		(r.ID() == dtype.TypeUint64 && l.IsSignedInteger()) {
		e.Hint("no 64-bit type holds both ranges; cast one side explicitly, " +
			"e.g. .Cast(ursus.Int64) or .Cast(ursus.Float64)")
	}
	if l.IsString() != r.IsString() {
		e.Hint("strings do not convert implicitly; use .Cast(ursus.String) or a string literal")
	}
	return e
}

// ResolveUnary computes the result type of op applied to in.
func ResolveUnary(op UnaryOp, in dtype.DataType) (dtype.DataType, error) {
	switch op {
	case OpIsNull, OpIsNotNull:
		// Defined for every type: the question is about presence, not value.
		return dtype.Bool, nil

	case OpNot:
		if !in.IsBool() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "",
				"not() requires a Boolean operand, got %s", in)
		}
		return dtype.Bool, nil

	case OpIsNan, OpIsNotNan, OpIsFinite, OpIsInfinite:
		// Deliberately float-only. Asking whether an Int64 is NaN is a category
		// error, and answering "false" would hide the bug rather than expose it.
		if !in.IsFloat() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "",
				"%s() requires a floating-point operand, got %s", op, in).
				Hint("null and NaN are different: use is_null() to test for missing values")
		}
		return dtype.Bool, nil

	case OpNeg:
		if in.ID() == dtype.TypeDuration {
			return in, nil
		}
		if !in.IsNumeric() {
			return dtype.Null, uerr.New(uerr.KindType, "",
				"neg() requires a numeric operand, got %s", in)
		}
		if in.IsUnsignedInteger() {
			return dtype.Null, uerr.New(uerr.KindType, "",
				"neg() is not defined for the unsigned type %s", in).
				Hint("cast to a signed type first, e.g. .Cast(ursus.Int64)")
		}
		return in, nil

	case OpAbs:
		if in.ID() == dtype.TypeDuration {
			return in, nil
		}
		if !in.IsNumeric() {
			return dtype.Null, uerr.New(uerr.KindType, "",
				"abs() requires a numeric operand, got %s", in)
		}
		return in, nil

	case OpSign, OpFloor, OpCeil:
		// These keep the operand's type: floor on an integer is the identity, and
		// widening it to a float would be a schema change in exchange for nothing.
		//
		// ALL THREE are refused for Decimal, and sign() was the one that got away.
		// A decimal's physical type is Int128 and its value is an UNSCALED integer,
		// so flooring it floors 1234 rather than 12.34 — and sign() is worse, not
		// better: it returns unscaled 1, labelled Decimal(10,2), which renders as
		// "0.01". The argument the next paragraph gives for excluding Duration — a
		// Duration of -1, 0 or 1 TICKS is not a sign, it is a nanosecond — is the
		// same argument, and it was written three lines from the code that ignored it.
		if in.ID() == dtype.TypeDecimal {
			return dtype.Null, uerr.New(uerr.KindType, "",
				"%s() is not defined for %s", op, in).
				Hint("a decimal is stored as an unscaled integer, so %s() would "+
					"answer about the unscaled value rather than the decimal one", op).
				Hint("cast to Float64 first if approximate arithmetic is acceptable")
		}
		// Sign keeps the operand's type, so sign(Int64) is Int64 rather than a float.
		// Duration is excluded even though abs() admits it: a Duration of -1, 0 or 1
		// TICKS is not a sign, it is a nanosecond, and returning one would be a unit
		// error dressed up as an answer.
		if !in.IsNumeric() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "",
				"%s() requires a numeric operand, got %s", op, in)
		}
		return in, nil

	default:
		if op.IsMath() {
			// Widening to a float is the point: sqrt(4) is 2.0, not 2, and an integer
			// result would be wrong for every input that is not a perfect square.
			//
			// Float32 stays Float32 rather than being promoted, matching what Div
			// already does for two Float32 operands — a user who chose a narrow float
			// chose it, and silently doubling every column's width would be a memory
			// regression nobody asked for.
			if in.IsNull() {
				return dtype.Float64, nil
			}
			if !in.IsNumeric() {
				return dtype.Null, uerr.New(uerr.KindType, "",
					"%s() requires a numeric operand, got %s", op, in).
					Hint("cast it first, e.g. .Cast(ursus.Float64)")
			}
			if in.ID() == dtype.TypeDecimal {
				return dtype.Null, uerr.New(uerr.KindType, "",
					"%s() is not defined for %s", op, in).
					Hint("a decimal is stored as an unscaled integer; cast to Float64 " +
						"if approximate arithmetic is acceptable")
			}
			if in.ID() == dtype.TypeFloat32 {
				return dtype.Float32, nil
			}
			return dtype.Float64, nil
		}
		return dtype.Null, uerr.Internalf("ResolveUnary: unhandled op %d", op)
	}
}
