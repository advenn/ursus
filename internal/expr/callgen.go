package expr

import (
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// genCallOut types the family that does not dispatch on the receiver's type.
func genCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	switch fn := c.Fn; fn {
	case FnIsIn:
		// Membership is decided by the same encoding group_by and distinct use, so
		// the receiver must be a type that encoding accepts.
		if !in.IsHashable() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "is_in",
				"is_in is not defined for %s", in).
				Hint("the value must be hashable: numeric, temporal, string or boolean")
		}
		if !in.IsNull() {
			if _, err := MembershipType(c, in); err != nil {
				return dtype.Null, err
			}
		}
		return dtype.Bool, nil

	case FnReplace, FnReplaceStrict, FnReplaceOr:
		return replaceOut(c, in)

	case FnCatGetCategories:
		return dtype.Null, uerr.Internalf("expr: %s is written out when its expression is expanded", fn)

	case FnHash:
		if !in.IsHashable() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "hash", "hash reads values a group-by can key on, not %s", in)
		}
		return dtype.Uint64, nil

	case FnToPhysical:
		return PhysicalOf(in)

	case FnReinterpret:
		if len(c.Args) < 2 {
			return dtype.Null, uerr.Internalf("expr: reinterpret has no signed argument")
		}
		var signed bool
		lit, ok := c.Args[1].(*Lit)
		if ok {
			signed, ok = lit.Value.(bool)
		}
		if !ok {
			return dtype.Null, uerr.Internalf("expr: reinterpret has no signed argument")
		}
		return ReinterpretOf(in, signed)

	case FnOrderSign:
		// Numbers, instants and Booleans have a zero; text has none, and Polars'
		// peaks refuse it.
		if !in.IsOrdered() || in.HasStringStorage() || in.ID() == dtype.TypeEnum {
			return dtype.Null, uerr.New(uerr.KindType, "order_sign", "a %s has no zero to be measured from", in)
		}
		return dtype.Int8, nil

	case FnCatPhysical:
		if in.ID() != dtype.TypeEnum {
			return dtype.Null, uerr.New(uerr.KindType, "cat",
				"%s requires an Enum operand, got %s", fn, in)
		}
		return EnumPhysical(in), nil

	default:
		return dtype.Null, uerr.Internalf("expr: unknown general call %d", fn)
	}
}

// ReplaceParts reads a replace call's literals: its old values, its new ones, and
// its default, nil but for FnReplaceOr.
func ReplaceParts(c *Call) (old, new []*Lit, def *Lit, err error) {
	lits := make([]*Lit, 0, len(c.Args)-1)
	for _, a := range c.Args[1:] {
		l, ok := a.(*Lit)
		if !ok {
			return nil, nil, nil, uerr.Internalf("expr: %s argument %s is not a literal", c.Fn, a)
		}
		lits = append(lits, l)
	}
	if len(lits) == 0 {
		return nil, nil, nil, uerr.Internalf("expr: %s has no count", c.Fn)
	}
	k, ok := lits[0].Value.(int64)
	want := 1 + 2*int(k)
	if c.Fn == FnReplaceOr {
		want++
	}
	if !ok || k < 0 || len(lits) != want {
		return nil, nil, nil, uerr.Internalf("expr: %s with %d literals", c.Fn, len(lits))
	}
	old, new = lits[1:1+k], lits[1+k:1+2*k]
	if c.Fn == FnReplaceOr {
		def = lits[1+2*k]
	}
	return old, new, def, nil
}

// replaceOut types a value map.
//
// The old values meet the receiver as is_in's do (membershipOf), so `x.Replace(old,
// new)` replaces exactly the rows `x.IsIn(old...)` holds. Replace answers as the
// conditional `When(x.IsIn(old_j)).Then(new_j)…Otherwise(x)` would: each new value
// meets the receiver as a branch meets the other, a weak literal taking the
// column's type when it fits, so 20 into an Int32 is an Int32 and 2.5 into an Int64
// is a Float64, where Polars truncates it to 2. ReplaceStrict and ReplaceOr answer
// the type their new values and default meet at, as Polars' replace_strict does.
func replaceOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	old, new, def, err := ReplaceParts(c)
	if err != nil {
		return dtype.Null, err
	}
	if !in.IsHashable() && !in.IsNull() {
		return dtype.Null, uerr.New(uerr.KindType, c.Fn.String(),
			"%s is not defined for %s", c.Fn, in).
			Hint("the value must be hashable: numeric, temporal, string or boolean")
	}
	if !in.IsNull() {
		if _, err := membershipOf(c.Fn, in, old); err != nil {
			return dtype.Null, err
		}
	}
	if c.Fn == FnReplace {
		out := in
		for _, l := range new {
			if out, err = ResolveCond(l, c.Args[0], l.DT, out); err != nil {
				return dtype.Null, uerr.New(uerr.KindType, c.Fn.String(),
					"%s cannot put a %s value in a %s column", c.Fn, l.DT, in).
					Hint("cast the column, or use ReplaceStrict, whose answer is the new values' type")
			}
		}
		return out, nil
	}
	vals := new
	if def != nil {
		vals = append(slices.Clone(new), def)
	}
	if len(vals) == 0 {
		return dtype.Null, uerr.New(uerr.KindSchema, c.Fn.String(),
			"%s has no values to answer with", c.Fn).
			Hint("give it at least one old value and its new one")
	}
	out := vals[0].DT
	for _, l := range vals[1:] {
		t, ok := dtype.Promote(out, l.DT)
		if !ok {
			return dtype.Null, uerr.New(uerr.KindType, c.Fn.String(),
				"%s: its new values have no common type: %s and %s", c.Fn, out, l.DT).
				Hint("give new values of one type")
		}
		out = t
	}
	return out, nil
}

// MembershipType is the type is_in and list.contains compare at: the type Eq would
// compare the receiver — for list.contains, its element — and each value at. So
// `x.IsIn(v)` refuses exactly the pairs `x.Eq(v)` refuses, and answers the same
// rows wherever both are defined.
//
// The values used to be cast to the receiver's type, which answered a different
// question. A value the column cannot hold — 5000 against an Int8, −1 against a
// Uint64 — was an error where Eq says false; a string, a bool or an instant was
// parsed or converted, so an Int64 matched "1", a Bool matched 1 and a Date matched
// 13:00 on that day, all pairs Eq refuses; and a value was rounded to the column's
// precision, so a Datetime(s) matched 00:00:01.5 and a Float32 matched 0.1.
//
// Equality is still grouping equality, so NaN matches NaN — the one place the two
// differ, and on purpose.
func MembershipType(c *Call, against dtype.DataType) (dtype.DataType, error) {
	if c.Fn == FnReplace || c.Fn == FnReplaceStrict || c.Fn == FnReplaceOr {
		old, _, _, err := ReplaceParts(c)
		if err != nil {
			return dtype.Null, err
		}
		return membershipOf(c.Fn, against, old)
	}
	lits := make([]*Lit, 0, len(c.Args)-1)
	for _, a := range c.Args[1:] {
		l, ok := a.(*Lit)
		if !ok {
			return dtype.Null, uerr.Internalf("expr: %s argument %s is not a literal", c.Fn, a)
		}
		lits = append(lits, l)
	}
	return membershipOf(c.Fn, against, lits)
}

// membershipOf is MembershipType over the values lits.
func membershipOf(fn CallFn, against dtype.DataType, lits []*Lit) (dtype.DataType, error) {
	common := against
	for _, l := range lits {
		b, err := ResolveBinary(OpEq, common, l.DT)
		if err != nil {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s cannot compare %s with a %s value", fn, against, l.DT).
				Hint("it compares as == does, and == has no common type for these").
				Hint("cast one side so the two meet, e.g. .Cast(ursus.%s)", l.DT)
		}
		common = b.CastL
	}
	return common, nil
}

// DecimalRounded is the type of a Decimal rounded to places decimal places, by Round,
// Floor or Ceil, or of its Sign, as Polars 2.0 types them (step 490): Decimal(p+1, s),
// at most 38, where a carry can make a new integer digit, 9.99 to 10.00, or a sign
// of ±1 needs one the type has not, Decimal(2, 2)'s; the operand's type where nothing
// is dropped, places ≥ s, or where it has an integer digit for a sign.
func DecimalRounded(in dtype.DataType, places int, sign bool) dtype.DataType {
	p, s := int(in.Precision()), int(in.Scale())
	if sign && p > s || !sign && places >= s {
		return in
	}
	return dtype.Decimal(uint8(min(dtype.MaxDecimalPrecision, p+1)), uint8(s))
}

// mathCallOut types the parameterised maths family.
//
// Round PRESERVES the operand's type, like Floor and Ceil and for the same reason:
// rounding an integer to any number of decimals is the identity, and widening it
// to a float would change a schema in exchange for nothing.
func mathCallOut(fn CallFn, in dtype.DataType, c *Call) (dtype.DataType, error) {
	switch fn {
	case FnMathRound:
		// Null is refused, as it is for floor() and ceil() — round is their
		// Call-surface twin and shared their old mistake of promising `in` for a
		// Null operand, which is a type no rounding kernel can dispatch on. The
		// matrix cannot see this one: it has no Call arm, so it was found by
		// following floor and ceil rather than by measurement.
		if !in.IsNumeric() {
			return dtype.Null, uerr.New(uerr.KindType, "round",
				"round() requires a numeric operand, got %s", in).
				Hint("cast it first, e.g. .Cast(ursus.Float64)")
		}
		if in.ID() == dtype.TypeDecimal {
			// Rounded at its scale (step 490), the type by the places it keeps.
			d, ok := callLitInt64(c, 1)
			if !ok {
				return dtype.Null, uerr.Internalf("expr: round without its decimals")
			}
			return DecimalRounded(in, int(d), false), nil
		}
		return in, nil
	case FnMathRoundSigFigs, FnMathTruncate:
		// round's siblings, in the operand's own type, as Polars keeps it: an integer
		// is rounded to its figures, and truncating it is the identity. A Decimal is
		// refused as round refuses it, where Polars rounds its digits.
		if !in.IsNumeric() || in.ID() == dtype.TypeDecimal {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s requires a number other than a Decimal, got %s", fn, in).
				Hint("cast it first, e.g. .Cast(ursus.Float64)")
		}
		return in, nil
	case FnMathAsFloat:
		// Only PctChange builds this, so its refusal speaks for PctChange: the
		// change between two instants is a Duration, which has no ratio to an
		// instant.
		switch {
		case in.IsFloat():
			return in, nil
		case in.IsNumeric() || in.ID() == dtype.TypeDuration:
			return dtype.Float64, nil
		}
		return dtype.Null, uerr.New(uerr.KindType, "pct_change",
			"pct_change is not defined for %s", in).
			Hint("it is defined for numbers and Durations; for an instant, take Diff, a Duration")
	case FnMathCountOnes, FnMathCountZeros, FnMathLeadingOnes, FnMathLeadingZeros,
		FnMathTrailingOnes, FnMathTrailingZeros:
		// Counted in the type's own width, so the leading zeros of an Int8 1 are 7.
		// Int128 has no kernel here, and a Bool is not a run of bits.
		if !in.IsInteger() || in.ID() == dtype.TypeInt128 {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s requires an integer operand of 8 to 64 bits, got %s", fn, in)
		}
		return dtype.Uint32, nil
	case FnMathDiffWiden:
		// One width up, which holds every difference exactly: a UInt8 difference
		// is within ±255. UInt64's is within ±(2^64−1), which only Int128 holds;
		// Polars gives Int64 there, and nulls where it overflows.
		switch in.ID() {
		case dtype.TypeUint8:
			return dtype.Int16, nil
		case dtype.TypeUint16:
			return dtype.Int32, nil
		case dtype.TypeUint32:
			return dtype.Int64, nil
		case dtype.TypeUint64:
			return dtype.Int128, nil
		}
		return in, nil
	default:
		return dtype.Null, uerr.Internalf("expr: unknown maths call %d", fn)
	}
}

// PhysicalOf is the type to_physical answers in, Polars' (step 324): a Date's Int32 of
// days, a Datetime's, a Duration's and a Time's Int64 of ticks, a Decimal's Int128 of
// unscaled units, an Enum's smallest unsigned integer that holds its categories, as
// cat.physical's; any other scalar type itself. A nested type is refused.
func PhysicalOf(in dtype.DataType) (dtype.DataType, error) {
	switch {
	case in.IsNested():
		return dtype.Null, uerr.New(uerr.KindUnsupported, "to_physical",
			"to_physical of a %s is not implemented", in).
			Hint("take its fields or elements first")
	case in.ID() == dtype.TypeEnum:
		return EnumPhysical(in), nil
	case in.ID() == dtype.TypeCategorical:
		// Its codes, which the frame's window writes out before the plan is typed
		// (the root's categoryCodes, step 351).
		return dtype.Uint32, nil
	case in.IsTemporal(), in.ID() == dtype.TypeDecimal:
		return in.Physical(), nil
	}
	return in, nil
}

// ReinterpretOf is the type reinterpret answers in: the integer of in's width, signed
// or not, its bits unchanged (step 324). Polars' reinterpret, of integers only.
func ReinterpretOf(in dtype.DataType, signed bool) (dtype.DataType, error) {
	types := map[dtype.TypeID][2]dtype.DataType{
		dtype.TypeInt8: {dtype.Uint8, dtype.Int8}, dtype.TypeUint8: {dtype.Uint8, dtype.Int8},
		dtype.TypeInt16: {dtype.Uint16, dtype.Int16}, dtype.TypeUint16: {dtype.Uint16, dtype.Int16},
		dtype.TypeInt32: {dtype.Uint32, dtype.Int32}, dtype.TypeUint32: {dtype.Uint32, dtype.Int32},
		dtype.TypeInt64: {dtype.Uint64, dtype.Int64}, dtype.TypeUint64: {dtype.Uint64, dtype.Int64},
	}
	pair, ok := types[in.ID()]
	if !ok {
		return dtype.Null, uerr.New(uerr.KindType, "reinterpret",
			"reinterpret reads an integer's bits, and %s is no integer of 8 to 64 bits", in).
			Hint("cast it instead")
	}
	if signed {
		return pair[1], nil
	}
	return pair[0], nil
}
