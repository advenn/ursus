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

	// A Decimal promotes only to itself — the a == b arm above. + and - of two
	// identical Decimals are exact, and CanCast converts between a Decimal and any
	// numeric type; what does not exist is a rule for the precision and scale of
	// Decimal(10,2) + Decimal(12,3), or of Decimal + Int64, and guessing one here
	// would silently commit the whole binary-operator calculus.
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

// PromoteExact is Promote restricted to a type that holds every value of BOTH
// operands exactly. It is the question a join key, a Concat column and an Unpivot
// value ask, where a value that rounds is a wrong match or a wrong value — not the
// expected inexactness of arithmetic, which keeps Promote.
//
// It differs from Promote in one place: an integer wider than 32 bits — Int64,
// Uint64, Int128 — with a float. Promote answers Float64, which holds an integer
// exactly only up to 2^53, so an Int64 key of 2^53+1 matched a Float64 key of 2^53
// and a Concat turned it into 2^53. No type here holds both, so PromoteExact answers
// none, as Polars and PyArrow refuse the same join. ExactMismatch says why.
//
// Temporal types are Promote's: Datetime(ms) and Datetime(ns) meet at Datetime(ns),
// and a value the finer unit cannot hold is a range question, refused by the strict
// cast at execution, not a type question.
func PromoteExact(a, b DataType) (DataType, bool) {
	if ExactMismatch(a, b) {
		return Null, false
	}
	return Promote(a, b)
}

// ExactMismatch reports whether a and b promote, but only to a type that rounds one
// of them: an integer wider than 32 bits with a float.
func ExactMismatch(a, b DataType) bool {
	if !a.IsNumeric() || !b.IsNumeric() || a.IsFloat() == b.IsFloat() {
		return false
	}
	i := a
	if a.IsFloat() {
		i = b
	}
	return i.IsInteger() && i.BitWidth() > 32
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
	// An ENUM first, because IsString is true for one and the parsing arm below
	// promised Enum -> numeric, which read its indices as text and panicked
	// (v0.3-scope.md §2.3). An Enum is built from text — a String, or another
	// Enum's — and becomes anything its text becomes, so Enum("1", "2") -> Int64
	// is 1 and 2, not the indices 0 and 1. Nothing else becomes an Enum: a number
	// has no category until it is text, and the cast should say which text.
	case to.ID() == TypeEnum:
		return from.ID() == TypeString || from.ID() == TypeEnum
	case from.ID() == TypeEnum:
		return to.ID() == TypeString || CanCast(String, to)
	// DECIMAL first, because it must not fall into the arms below: the numeric ->
	// temporal arm would promise Decimal -> Date, which relabels an unscaled integer
	// as a day count, and the numeric <-> Bool arm would promise a truth value.
	//
	// What it does promise is every numeric type in both directions, another Decimal,
	// and String as a target. Each numeric conversion is EXACT OR REFUSED per value —
	// 12.34 to an integer is refused, 0.1 to Decimal(10,2) is 0.10 — except a float
	// target, which rounds to the nearest value of its width, as it does from Int64.
	// The kernel arm is castDecimal.
	//
	// A target that no Decimal can be — Decimal(200, 3) constructs, because a type
	// constructor returns no error — is refused here, so the planner rejects it
	// rather than the kernel.
	case from.ID() == TypeDecimal || to.ID() == TypeDecimal:
		if to.id == TypeDecimal && !ValidDecimal(to.prec, to.scale) {
			return false
		}
		if from.ID() == TypeDecimal && to.id == TypeString {
			return true
		}
		// Parsed by i128.ParseDecimal, which rounds half away from zero past the
		// scale and refuses past the precision; the CSV reader parses the same way.
		if from.ID() == TypeString && to.id == TypeDecimal {
			return true
		}
		return from.IsNumeric() && to.IsNumeric()
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
		// Int128 included: i128.Parse detects overflow now, so text past the type's
		// range is refused rather than wrapped, as for every other integer.
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

// MeetHint is the advice for two types that do not meet exactly — a join key, a
// Concat column, an Unpivot value — naming a cast that would make them meet. Every
// cast it names is one CanCast permits.
func MeetHint(a, b DataType) string {
	switch {
	case ExactMismatch(a, b):
		i, f := a, b
		if a.IsFloat() {
			i, f = b, a
		}
		return "no type holds both " + i.String() + " and " + f.String() + " exactly: a " +
			f.String() + " holds an integer exactly only up to 2^" + mantissaBits(f) +
			". Cast the " + f.String() + " side to " + i.String() +
			", which is exact or refused, or both sides to Float64 if rounding is acceptable"
	case a.IsTemporal() && b.IsTemporal() && a.ID() == b.ID() && a.TimeZone() != b.TimeZone():
		return "an instant in one zone does not meet a wall clock or another zone; cast one " +
			"side to the other's type, e.g. to " + a.String() + ", which relabels it"
	}
	return "cast one side to the other's type, " + a.String() + " or " + b.String()
}

func mantissaBits(f DataType) string {
	if f.ID() == TypeFloat32 {
		return "24"
	}
	return "53"
}
