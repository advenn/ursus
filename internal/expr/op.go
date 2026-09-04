package expr

// BinaryOp is a two-operand operation.
type BinaryOp uint8

const (
	// Arithmetic.
	OpAdd BinaryOp = iota
	OpSub
	OpMul
	OpDiv      // true division: always produces a float
	OpFloorDiv // integer-truncating division
	OpMod

	// Ordering and equality. Three-valued: if either operand is null the result
	// is null, NOT false.
	OpEq
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe

	// Equality where nulls are DATA rather than absence: null == null is true and
	// the result is never null. Polars calls these eq_missing / ne_missing. They
	// need a separate kernel family, not a flag on the ordinary comparison.
	OpEqMissing
	OpNeMissing

	// Kleene three-valued logic. Note false AND null == false and true OR null ==
	// true — the output validity is NOT simply `va AND vb`.
	OpAnd
	OpOr
	OpXor

	// Exponentiation. APPENDED rather than placed beside the other arithmetic,
	// which reads oddly and is deliberate: both dispatchers — ResolveBinary and
	// kernel's arithmetic — route anything the classifiers below do not claim to
	// the arithmetic path through a `default:` arm, so position carries no meaning
	// for it, and renumbering the enum to make it look tidy would move every
	// constant for no gain.
	OpPow

	binaryOpCount
)

var binaryOpNames = [binaryOpCount]string{
	OpAdd: "+", OpSub: "-", OpMul: "*", OpDiv: "/", OpFloorDiv: "//", OpMod: "%",
	OpEq: "==", OpNe: "!=", OpLt: "<", OpLe: "<=", OpGt: ">", OpGe: ">=",
	OpEqMissing: "<=>", OpNeMissing: "<!>",
	OpAnd: "&", OpOr: "|", OpXor: "^",
	OpPow: "**",
}

func (o BinaryOp) String() string {
	// The `!= ""` half is the load-bearing one. binaryOpNames is sized by the enum's
	// sentinel, so `int(o) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the three maps that dedup on String(). dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(o) < len(binaryOpNames) && binaryOpNames[o] != "" {
		return binaryOpNames[o]
	}
	return "?"
}

// IsComparison reports whether the op yields Bool via three-valued comparison.
func (o BinaryOp) IsComparison() bool { return o >= OpEq && o <= OpGe }

// IsMissingComparison reports whether the op treats nulls as data.
func (o BinaryOp) IsMissingComparison() bool { return o == OpEqMissing || o == OpNeMissing }

// IsLogical reports whether the op is Kleene boolean.
func (o BinaryOp) IsLogical() bool { return o >= OpAnd && o <= OpXor }

// There is deliberately no IsArithmetic.
//
// There used to be — `o <= OpMod` — with zero callers anywhere in the module,
// because arithmetic is the `default:` arm of both dispatchers rather than a
// classified family. Appending OpPow made it silently wrong about a live operator,
// which is exactly the trap call.go's "Appending only" note warns about. A dead
// classifier that lies is worse than no classifier, and the next op to be appended
// would have inherited the lie.

// IsOrdering reports whether the op uses < / > semantics, as opposed to equality.
// Ordering comparisons are undefined for types that have no order.
func (o BinaryOp) IsOrdering() bool { return o >= OpLt && o <= OpGe }

// UnaryOp is a one-operand operation.
type UnaryOp uint8

const (
	OpNeg UnaryOp = iota
	OpAbs
	OpNot // Kleene NOT: NOT null is null

	// Null predicates. These are the only operations whose result is never null,
	// because the null-ness IS the answer.
	OpIsNull
	OpIsNotNull

	// NaN predicates. Distinct from null predicates: null.is_nan() is NULL, not
	// false. Null and NaN are different things all the way down.
	OpIsNan
	OpIsNotNan
	OpIsFinite
	OpIsInfinite

	// Transcendental and rounding maths. These are the ops whose OUTPUT TYPE
	// DIFFERS FROM THEIR INPUT — Sqrt(Int64) is Float64 — which is why they cannot
	// share unaryArith, whose whole shape assumes one type throughout.
	//
	// Contiguous, because IsMath is a range test over them.
	OpSqrt
	OpCbrt
	OpExp
	OpLn
	OpLog10
	OpLog1p

	// Sign, Floor and Ceil are NOT in that block: each keeps its operand's type, so
	// they travel with Neg and Abs through unaryArith. Grouping them with the
	// transcendentals would have made sign(Int64) and floor(Int64) into Float64s —
	// and floor on an integer is the identity, so widening it would be a schema
	// change in exchange for nothing.
	OpSign
	OpFloor
	OpCeil

	unaryOpCount
)

var unaryOpNames = [unaryOpCount]string{
	OpNeg: "neg", OpAbs: "abs", OpNot: "not",
	OpIsNull: "is_null", OpIsNotNull: "is_not_null",
	OpIsNan: "is_nan", OpIsNotNan: "is_not_nan",
	OpIsFinite: "is_finite", OpIsInfinite: "is_infinite",
	OpSqrt: "sqrt", OpCbrt: "cbrt", OpExp: "exp", OpLn: "ln",
	OpLog10: "log10", OpLog1p: "log1p",
	OpSign: "sign", OpFloor: "floor", OpCeil: "ceil",
}

func (o UnaryOp) String() string {
	// The `!= ""` half is the load-bearing one. unaryOpNames is sized by the enum's
	// sentinel, so `int(o) < len(...)` is true for every DECLARED constant and this
	// fallback would be unreachable without it — a forgotten entry would render as
	// the EMPTY STRING, and two ops both missing names would render identically and
	// collide in the three maps that dedup on String(). dtype.TypeID.String() has
	// had the correct guard since step 1; these eight did not.
	if int(o) < len(unaryOpNames) && unaryOpNames[o] != "" {
		return unaryOpNames[o]
	}
	return "?"
}

// IsNullPredicate reports whether the op's result is never null.
func (o UnaryOp) IsNullPredicate() bool { return o == OpIsNull || o == OpIsNotNull }

// IsMath reports whether the op widens its operand to a float and returns a float.
// A range comparison over declaration order, like the classifiers above, which is
// why that block is append-only.
func (o UnaryOp) IsMath() bool { return o >= OpSqrt && o <= OpLog1p }

// IsNanPredicate reports whether the op tests a float property.
func (o UnaryOp) IsNanPredicate() bool { return o >= OpIsNan && o <= OpIsInfinite }
