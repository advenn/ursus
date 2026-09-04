package dtype

// Promote returns the common type two operands are cast to before a binary
// operation, or ok=false if there is no lossless-enough common type.
//
// The rule is: never silently lose information that the user is likely to care
// about, and never silently produce a wrong answer.
//
//	Null + T                 → T          (a null literal adopts the other side)
//	same type                → that type
//	intN + intM  (same sign) → the wider
//	intN + uintM             → the smallest SIGNED type holding both
//	signed + uint64          → Int128     (see below)
//	Int128 + any integer     → Int128
//	float + float            → the wider
//	int + float              → a float wide enough for the int's mantissa
//	anything else            → not ok
//
// Uint64 combined with a signed type has no common type at 64 bits: the choices
// would be to wrap (silently wrong for large values) or to route through Float64
// (silently lossy above 2^53), and Polars wraps. ursus used to reject the
// combination outright and make the user cast.
//
// Int128 removes the dilemma — every uint64 and every int64 fits — so the
// combination now promotes losslessly. That was a free side effect of widening
// integer `sum` to 128 bits, and it is the better answer: an explicit cast that
// exists only to work around a missing type is a tax, not a safety feature.
//
// Bool is not numeric here: `true + true` is rejected. Comparisons of two Bools
// are handled by the caller before reaching Promote.
func Promote(a, b DataType) (DataType, bool) {
	if a == b {
		return a, true
	}
	// A null literal has no type of its own; it takes the other operand's.
	if a.IsNull() {
		return b, true
	}
	if b.IsNull() {
		return a, true
	}

	// Two temporal columns of the SAME KIND differing only in resolution promote to
	// the finer one, so no precision is silently discarded.
	//
	// # This makes a doc comment true rather than deleting it
	//
	// TimeUnit.Finer already says "Used by type promotion: combining two temporal
	// columns keeps the finer unit" — and until step 15 its only caller was
	// resolveTemporalArithmetic. So `ts_a - ts_b` across two resolutions worked and
	// `ts_a > ts_b` refused, which is the same question answered two ways by one
	// library. The conversion itself was never the obstacle: kernel.rescaleTemporal
	// has done it correctly all along, flooring rather than truncating and turning
	// overflow into a null.
	//
	// # What is deliberately NOT promoted
	//
	// A ZONE mismatch. Datetime(us,"UTC") against Datetime(us,"") is an instant
	// against a wall clock, and reconciling them would have to invent an offset. Two
	// different named zones would leave the result's own zone undecidable — it
	// matters for Coalesce and conditionals even though a comparison discards it.
	//
	// DATE against DATETIME. A date is not an instant at midnight until someone
	// chooses a zone to put the midnight in, which is the same objection.
	if a.IsTemporal() && b.IsTemporal() {
		if a.ID() == b.ID() && a.TimeZone() == b.TimeZone() {
			if a.TimeUnit().Finer(b.TimeUnit()) {
				return a, true
			}
			return b, true
		}
		return Null, false
	}

	// String types promote only to themselves. Enum and String are deliberately NOT
	// interchangeable: comparing an Enum to a String requires an explicit cast so the
	// category set is visible.
	if !a.IsNumeric() || !b.IsNumeric() {
		return Null, false
	}

	// Decimal has no kernels yet; combining it with anything else would need a
	// precision/scale calculus that is not worth guessing at.
	if a.id == TypeDecimal || b.id == TypeDecimal {
		return Null, false
	}

	af, bf := a.IsFloat(), b.IsFloat()

	switch {
	case af && bf:
		return wider(a, b), true

	case af || bf:
		f, i := a, b
		if bf {
			f, i = b, a
		}
		// Float32 has a 24-bit mantissa, Float64 a 53-bit one. Widen the float
		// until it can hold every value of the integer type exactly.
		if f.id == TypeFloat32 && i.BitWidth() > 16 {
			return Float64, true
		}
		return f, true

	default: // both integers
		// Int128 absorbs every other integer type, signed or unsigned, because it
		// is the only one wide enough to hold both ranges. This is what makes
		// `sum(x) > 100` work after Sum widens to 128 bits, and it incidentally
		// closes the Uint64-plus-signed hole that had no answer at 64 bits.
		if a.id == TypeInt128 || b.id == TypeInt128 {
			return Int128, true
		}
		as, bs := a.IsSignedInteger(), b.IsSignedInteger()
		if as == bs {
			return wider(a, b), true
		}
		// Mixed signedness: find the smallest signed type covering both ranges.
		s, u := a, b
		if bs {
			s, u = b, a
		}
		if u.id == TypeUint64 {
			// Every uint64 fits in an Int128, so once 128-bit support exists there
			// IS a lossless common type. The old rejection stands only in the doc
			// comment's historical note.
			return Int128, true
		}
		// A uintN needs a signed type of at least N+1 bits.
		need := max(s.BitWidth(), u.BitWidth()*2)
		return signedOfWidth(need)
	}
}

func wider(a, b DataType) DataType {
	if a.BitWidth() >= b.BitWidth() {
		return a
	}
	return b
}

func signedOfWidth(bits int) (DataType, bool) {
	switch {
	case bits <= 8:
		return Int8, true
	case bits <= 16:
		return Int16, true
	case bits <= 32:
		return Int32, true
	case bits <= 64:
		return Int64, true
	case bits <= 128:
		return Int128, true
	default:
		return Null, false
	}
}

// CanCast reports whether an explicit Cast from `from` to `to` is defined.
//
// This is broader than Promote: a cast may lose information (Int64 → Int8,
// Float64 → Int32), because the user asked for it. What it may not do is be
// meaningless — there is no cast from Struct to Int64.
//
// Non-strict casts turn unrepresentable values into nulls rather than erroring;
// that is a kernel-level concern, not a type-level one.
func CanCast(from, to DataType) bool {
	if from == to || from.IsNull() {
		return true
	}
	switch {
	case from.IsNumeric() && to.IsNumeric():
		return true
	case from.IsNumeric() && to.IsBool(), from.IsBool() && to.IsNumeric():
		return true
	// Temporal types share integer storage, so numeric casts are the documented
	// way to reach the underlying tick count.
	case from.IsTemporal() && to.IsNumeric(), from.IsNumeric() && to.IsTemporal():
		return true
	case from.IsTemporal() && to.IsTemporal():
		return true
	// Parsing and formatting.
	case from.IsString() && (to.IsNumeric() || to.IsTemporal() || to.IsBool()):
		return true
	case (from.IsNumeric() || from.IsTemporal() || from.IsBool()) && to.id == TypeString:
		return true
	// IsString includes Enum, so this arm used to promise String -> Enum and
	// Enum -> String, neither of which kernel.Cast implements — the same
	// plan-accepts / kernel-rejects divergence that unary.go records twice and
	// claims to have closed. An Enum's categories are part of its TYPE, so
	// converting either way needs a dictionary lookup that does not exist yet.
	case from.HasStringStorage() && to.HasStringStorage():
		return true
	case from.id == TypeString && to.id == TypeBinary, from.id == TypeBinary && to.id == TypeString:
		return true
	case from.id == TypeList && to.id == TypeList:
		return CanCast(from.Inner(), to.Inner())
	case from.id == TypeArray && to.id == TypeList:
		return CanCast(from.Inner(), to.Inner())
	default:
		return false
	}
}
