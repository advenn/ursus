package ursus

import (
	"math"
	"regexp"
	"time"

	"ursus/dtype"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// Expr is a lazy description of a column transformation.
//
// It is a CONCRETE STRUCT rather than an interface, and that is forced by Go
// 1.27: interface methods may not declare type parameters, and a generic method
// can never satisfy an interface method. Since Gt[T Operand] — the thing that
// makes `Col("age").Gt(30)` work without wrapping 30 in Lit — must be a generic
// method, Expr cannot be an interface. Polymorphism lives in the unexported
// expr.Node instead.
type Expr struct {
	n expr.Node
}

func wrap(n expr.Node) Expr    { return Expr{n: n} }
func (e Expr) node() expr.Node { return e.n }

// Err returns any error deferred during construction — a bad regex, say.
// Expression builders cannot return errors without destroying chaining, so the
// error rides in the tree and surfaces here or at Collect.
func (e Expr) Err() error { return expr.FirstErr(e.n) }

// String renders the expression canonically.
func (e Expr) String() string {
	if e.n == nil {
		return "<nil>"
	}
	return e.n.String()
}

// --- Literal and Operand -----------------------------------------------------

// Literal is the set of Go types that lift to a column literal.
//
// Every accepted width is enumerated explicitly because Go infers a type parameter
// from the argument's type with NO implicit widening: `a.Gt(int32(5))` is a
// compile error unless ~int32 appears here.
//
// time.Duration is DELIBERATELY ABSENT. Its underlying type is int64, so listing
// it alongside ~int64 gives overlapping type sets and the union does not compile —
// this is the defect the design review found. ~int64 already admits it, and a
// type switch recovers the named type exactly, so nothing is lost.
//
// time.Time is a bare (non-~) term: it is a defined struct type, disjoint from
// every other term. A union term may be a type with methods; only interface terms
// with methods are forbidden.
type Literal interface {
	~bool |
		~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64 |
		~string | ~[]byte |
		time.Time
}

// Operand is either another Expr or a Go scalar that lifts to a literal.
//
// This union — a struct type alongside an embedded constraint interface — is what
// lets every binary operator accept both forms with no wrapper at the call site.
type Operand interface {
	Expr | Literal
}

// lift converts an Operand into an IR node.
func lift[T Operand](v T) expr.Node {
	if e, ok := any(v).(Expr); ok {
		return e.n
	}
	return litNode(any(v))
}

// --- constructors ------------------------------------------------------------

// Col selects one or more columns by name.
//
// With several names it becomes a multi-column selection that expands at plan
// time: `Col("a","b").Mul(2)` is two independent expressions.
func Col(names ...string) Expr {
	switch len(names) {
	case 0:
		return wrap(&expr.Err{E: errNoColumns()})
	case 1:
		return wrap(&expr.Col{Name: names[0]})
	default:
		return wrap(&expr.Match{M: &expr.NameMatcher{Names: names}})
	}
}

// ColRegex selects every column whose name matches pattern.
//
// The pattern is explicit rather than inferred from leading ^ and trailing $ the
// way Polars does. That inference makes a column literally named "^total$"
// unselectable and taxes every plain Col call with an anchor scan.
func ColRegex(pattern string) Expr {
	m, err := expr.NewRegexMatcher(pattern)
	if err != nil {
		return wrap(&expr.Err{E: err})
	}
	return wrap(&expr.Match{M: m})
}

// ColDType selects every column of one of the given types. How many columns that
// is is determined at plan time from the schema, so the same expression adapts as
// the schema changes.
func ColDType(types ...dtype.DataType) Expr {
	return wrap(&expr.Match{M: &expr.DTypeMatcher{Types: types}})
}

// All selects every column.
func All() Expr { return wrap(&expr.Match{M: &expr.AllMatcher{}}) }

// Exclude selects every column except the named ones.
func Exclude(names ...string) Expr {
	return wrap(&expr.Match{M: &expr.ExcludeMatcher{
		Inner: &expr.AllMatcher{},
		Names: names,
	}})
}

// ExcludeRegex selects every column whose name does not match pattern.
func ExcludeRegex(pattern string) Expr {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return wrap(&expr.Err{E: err})
	}
	return wrap(&expr.Match{M: &expr.ExcludeMatcher{
		Inner: &expr.AllMatcher{},
		Re:    re,
		ReSrc: pattern,
	}})
}

// Lit builds a literal.
func Lit[T Literal](v T) Expr { return wrap(litNode(any(v))) }

// Null builds a typed null literal.
func Null(dt dtype.DataType) Expr { return wrap(&expr.Lit{Value: nil, DT: dt}) }

// --- naming ------------------------------------------------------------------

// Alias renames the output column.
//
// It sets ONE fixed name, so applying it to a multi-column selection is an error.
// Use Prefix or Suffix to rename an expansion.
func (e Expr) Alias(name string) Expr {
	return wrap(&expr.Alias{Child: e.n, Name: name})
}

// Prefix prepends to the output name. Unlike Alias it is expansion-safe, deriving
// a distinct name per output column.
func (e Expr) Prefix(p string) Expr {
	return wrap(&expr.Rename{
		Child: e.n,
		Fn:    func(s string) string { return p + s },
		Label: `name.prefix(` + quote(p) + `)`,
	})
}

// Suffix appends to the output name. Expansion-safe, like Prefix.
func (e Expr) Suffix(s string) Expr {
	return wrap(&expr.Rename{
		Child: e.n,
		Fn:    func(x string) string { return x + s },
		Label: `name.suffix(` + quote(s) + `)`,
	})
}

// MapName derives the output name from the input name.
func (e Expr) MapName(fn func(string) string) Expr {
	return wrap(&expr.Rename{Child: e.n, Fn: fn, Label: "name.map(...)"})
}

// --- casting -----------------------------------------------------------------

// Cast converts to another type, failing the query on the first unrepresentable
// value.
func (e Expr) Cast(to dtype.DataType) Expr {
	return wrap(&expr.Cast{Child: e.n, To: to, Strict: true})
}

// CastLossy converts to another type, turning unrepresentable values into nulls.
// The result is always nullable as a consequence.
func (e Expr) CastLossy(to dtype.DataType) Expr {
	return wrap(&expr.Cast{Child: e.n, To: to, Strict: false})
}

// --- arithmetic --------------------------------------------------------------
//
// Every binary operator takes an Operand, so both `a.Add(b)` and `a.Add(1)` work.
// The scalar form is by far the common one and requiring Lit(1) at every site
// would be a tax paid thousands of times.

func (e Expr) Add[T Operand](v T) Expr { return e.bin(expr.OpAdd, lift(v)) }
func (e Expr) Sub[T Operand](v T) Expr { return e.bin(expr.OpSub, lift(v)) }
func (e Expr) Mul[T Operand](v T) Expr { return e.bin(expr.OpMul, lift(v)) }

// Div is TRUE division: it always produces a float, so 7/2 is 3.5 even for two
// integers. Integer-truncating division is FloorDiv, so the surprising behaviour
// has to be asked for by name.
func (e Expr) Div[T Operand](v T) Expr      { return e.bin(expr.OpDiv, lift(v)) }
func (e Expr) FloorDiv[T Operand](v T) Expr { return e.bin(expr.OpFloorDiv, lift(v)) }
func (e Expr) Mod[T Operand](v T) Expr      { return e.bin(expr.OpMod, lift(v)) }

// Pow raises the receiver to the power v.
//
// The result is ALWAYS a float, for the same reason Div's is: 2**-1 is 0.5, and a
// version that truncated negative powers to zero would be surprising in the
// direction that loses data silently. Cast the result if an integer is wanted.
func (e Expr) Pow[T Operand](v T) Expr { return e.bin(expr.OpPow, lift(v)) }

// Neg negates. Abs takes the absolute value.
//
// Both are defined for every numeric type, including the unsigned integers,
// Int128 and Decimal — which matters more than it sounds, because every integer
// Sum outputs Int128, so `Col("x").Sum().Abs()` is an ordinary query.
func (e Expr) Neg() Expr { return e.un(expr.OpNeg) }
func (e Expr) Abs() Expr { return e.un(expr.OpAbs) }

// --- maths -------------------------------------------------------------------
//
// Two families, and the split is visible in the result type.
//
// Sign, Floor and Ceil PRESERVE the operand's type: floor on an integer is the
// identity, and widening it to a float would change a schema in exchange for
// nothing. Sqrt, Cbrt, Exp, Ln, Log10 and Log1p WIDEN to Float64 (Float32 stays
// Float32), because sqrt(4) is 2.0 and an integer result would be wrong for every
// input that is not a perfect square.
//
// All of them are TOTAL. sqrt(-1) is NaN, ln(0) is -Inf, and neither becomes a
// null — the rule float division already set. Null in, null out, and nothing else
// manufactures one.

// Sign is -1, 0 or 1, keeping the operand's type.
//
// For floats it returns the operand itself in the zero case, so sign(-0.0) is
// -0.0 and sign(NaN) is NaN. That matches NumPy and falls out of the ordering test
// rather than being special-cased.
func (e Expr) Sign() Expr { return e.un(expr.OpSign) }

// Floor and Ceil round towards -Inf and +Inf. Both are the identity on an integer
// column and both are refused for Decimal, whose stored value is unscaled — so
// flooring it would floor 1234 rather than 12.34.
func (e Expr) Floor() Expr { return e.un(expr.OpFloor) }
func (e Expr) Ceil() Expr  { return e.un(expr.OpCeil) }

// Sqrt, Cbrt and Exp. Sqrt of a negative is NaN, not an error and not a null.
func (e Expr) Sqrt() Expr { return e.un(expr.OpSqrt) }
func (e Expr) Cbrt() Expr { return e.un(expr.OpCbrt) }
func (e Expr) Exp() Expr  { return e.un(expr.OpExp) }

// Ln is the natural logarithm; Log10 is base 10; Log1p is ln(1+x), which keeps its
// precision for x near zero where ln(1+x) would lose it.
//
// ln(0) is -Inf and ln(-1) is NaN. Both are values.
func (e Expr) Ln() Expr    { return e.un(expr.OpLn) }
func (e Expr) Log10() Expr { return e.un(expr.OpLog10) }
func (e Expr) Log1p() Expr { return e.un(expr.OpLog1p) }

// Round rounds to decimals decimal places, HALF AWAY FROM ZERO.
//
// Round(0.5) is 1 and Round(2.5) is 3, matching Polars and a spreadsheet rather
// than IEEE's round-half-to-even. It preserves the operand's type: rounding an
// integer column is the identity, not a widening.
//
// It rounds twice — the value is scaled by 10^decimals first, and that multiply
// rounds too. Round(2.675, 2) is 2.68, because 2.675 * 100 lands on exactly 267.5
// even though 2.675 itself is stored as 2.67499999999999982…
func (e Expr) Round(decimals int) Expr {
	if decimals < 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "round",
			"decimals must not be negative, got %d", decimals)})
	}
	return wrap(&expr.Call{
		Fn:   expr.FnMathRound,
		Args: []expr.Node{e.n, litNode(int64(decimals))},
	})
}

// Log is the logarithm to an arbitrary base.
//
// It is SUGAR — ln(x)/ln(base) — rather than a parameterised kernel, which is the
// same definition Polars uses and costs no new machinery. Two consequences worth
// knowing, both from the division:
//
//   - Log(10) and Log10() can differ in the last ULP. Log10 is the exact form.
//   - On a Float32 column Log10() stays Float32 and Log(10) becomes Float64,
//     because dividing by a float64 promotes. Cast the result back if the narrow
//     width was a deliberate choice.
func (e Expr) Log(base float64) Expr {
	if base <= 0 || base == 1 || math.IsNaN(base) || math.IsInf(base, 0) {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "log",
			"log base must be finite, positive and not 1, got %v", base)})
	}
	return e.Ln().Div(math.Log(base))
}

// --- comparison --------------------------------------------------------------
//
// Three-valued: if either operand is null the result is NULL, not false.

func (e Expr) Eq[T Operand](v T) Expr { return e.bin(expr.OpEq, lift(v)) }
func (e Expr) Ne[T Operand](v T) Expr { return e.bin(expr.OpNe, lift(v)) }
func (e Expr) Lt[T Operand](v T) Expr { return e.bin(expr.OpLt, lift(v)) }
func (e Expr) Le[T Operand](v T) Expr { return e.bin(expr.OpLe, lift(v)) }
func (e Expr) Gt[T Operand](v T) Expr { return e.bin(expr.OpGt, lift(v)) }
func (e Expr) Ge[T Operand](v T) Expr { return e.bin(expr.OpGe, lift(v)) }

// EqMissing is equality where nulls are DATA: null equals null, and the result is
// never null. NeMissing is its negation.
func (e Expr) EqMissing[T Operand](v T) Expr { return e.bin(expr.OpEqMissing, lift(v)) }
func (e Expr) NeMissing[T Operand](v T) Expr { return e.bin(expr.OpNeMissing, lift(v)) }

// IsBetween tests that e falls between lo and hi. Both bounds lift, so
// IsBetween(0, 100) works.
//
// The default is CLOSED-BOTH — lo <= e <= hi — which is what this method has always
// meant and what reads naturally from the name. Pass a Closed to say otherwise:
//
//	Col("ts").IsBetween(start, end, ursus.ClosedLeft)   // start <= ts < end
//
// Variadic rather than a required third argument, so every existing two-argument call
// keeps compiling and the common case stays short. More than one is a mistake and is
// refused rather than ignored.
func (e Expr) IsBetween[L, H Operand](lo L, hi H, closed ...Closed) Expr {
	c := ClosedBoth
	switch len(closed) {
	case 0:
	case 1:
		c = closed[0].Or(ClosedBoth)
	default:
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "is_between",
			"is_between takes at most one Closed, got %d", len(closed))})
	}
	lower := e.Ge(lo)
	if c.LowerOpen() {
		lower = e.Gt(lo)
	}
	upper := e.Lt(hi)
	if c.UpperClosed() {
		upper = e.Le(hi)
	}
	return lower.And(upper)
}

// IsIn tests membership in a set of values: Col("region").IsIn("eu", "us").
//
// # Values, not an expression
//
// The published sketch was IsIn(other Expr), and it cannot be written: Literal's
// only slice term is ~[]byte, so there is no way to spell a list-valued literal.
// The Expr-valued form — testing membership in another frame's column — is a semi
// join wearing a predicate's clothes, and it belongs with the join machinery.
//
// All values share one type parameter, so the set is homogeneous by construction.
// Values that are not representable in the column's type are an error rather than a
// silent non-match: comparing an Int8 column against 5000 is a question with no
// meaningful answer.
//
// # Equality is GROUPING equality
//
// Membership uses the same encoding GroupBy and Distinct use, so NaN matches NaN
// and -0.0 matches +0.0. An is_in built on IEEE equality would disagree with
// Distinct about the same data.
//
// # Nulls
//
// `null.IsIn(...)` is NULL, not false — the same rule as IsNan, and the reason
// Filter(p) and Filter(Not(p)) do not partition. The set itself can never contain a
// null, because these are Go values.
//
// An empty set makes every non-null row false, matching SQL's `x IN ()`.
func (e Expr) IsIn[T Literal](vs ...T) Expr {
	args := make([]expr.Node, 0, len(vs)+1)
	args = append(args, e.n)
	for _, v := range vs {
		args = append(args, litNode(any(v)))
	}
	return wrap(&expr.Call{Fn: expr.FnIsIn, Args: args})
}

// --- boolean -----------------------------------------------------------------
//
// Kleene three-valued logic: `false AND null` is false, `true OR null` is true.

func (e Expr) And[T Operand](v T) Expr { return e.bin(expr.OpAnd, lift(v)) }
func (e Expr) Or[T Operand](v T) Expr  { return e.bin(expr.OpOr, lift(v)) }
func (e Expr) Xor[T Operand](v T) Expr { return e.bin(expr.OpXor, lift(v)) }

// Not is Kleene negation: NOT null is null.
func (e Expr) Not() Expr { return e.un(expr.OpNot) }

// --- predicates --------------------------------------------------------------

// IsNull and IsNotNull ask about presence, so their results are never null.
func (e Expr) IsNull() Expr    { return e.un(expr.OpIsNull) }
func (e Expr) IsNotNull() Expr { return e.un(expr.OpIsNotNull) }

// IsNan and friends ask about a float VALUE, so `null.IsNan()` is null rather
// than false. Null and NaN are different things; use IsNull to test for missing.
func (e Expr) IsNan() Expr      { return e.un(expr.OpIsNan) }
func (e Expr) IsNotNan() Expr   { return e.un(expr.OpIsNotNan) }
func (e Expr) IsFinite() Expr   { return e.un(expr.OpIsFinite) }
func (e Expr) IsInfinite() Expr { return e.un(expr.OpIsInfinite) }

// --- internals ---------------------------------------------------------------

func (e Expr) bin(op expr.BinaryOp, r expr.Node) Expr {
	return wrap(&expr.Binary{Op: op, L: e.n, R: r})
}

func (e Expr) un(op expr.UnaryOp) Expr {
	return wrap(&expr.Unary{Op: op, Child: e.n})
}
