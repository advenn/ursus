package ursus

import (
	"math"
	"regexp"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
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

func wrap(n expr.Node) Expr { return Expr{n: n} }

// node is the only way to read e's node. The zero Expr — `var e ursus.Expr`, or a
// field never set — has none, and was a nil node that dereferenced deep inside
// resolution: a panic, from something that compiles. As an operand, an alias, or
// anything else built from it, it is now an error saying what it is.
//
// TestOnlyNodeReadsTheField keeps it the only way: String, nodes and OverWith check
// for the zero Expr themselves.
func (e Expr) node() expr.Node {
	if e.n == nil {
		// Fresh each time: plan layers annotate an error in place as it passes, so
		// one shared across queries would carry another query's attribution.
		return &expr.Err{E: uerr.New(uerr.KindValue, "", "the zero Expr is not an expression").
			Hint("build one with Col, Lit or another constructor; a declared `var e ursus.Expr` is the zero Expr")}
	}
	return e.n
}

// Err returns any error deferred during construction — a bad regex, say.
// Expression builders cannot return errors without destroying chaining, so the
// error rides in the tree and surfaces here or at Collect.
func (e Expr) Err() error { return expr.FirstErr(e.node()) }

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
		return e.node()
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

// ListLiteral is a Go slice that Lit makes a List literal (step 257): one value, a
// list of the elements' type, Lit([]int64{1, 2}) a List(Int64). Every element is a
// value; a list with a null element is built by an expression, not a literal.
//
// []byte is absent: it is a Binary literal, as it always was, and a List of Uint8 is
// Lit([]uint16{...}).Cast(List(Uint8)). The terms are exact, so a defined slice type
// must be converted to its slice first.
type ListLiteral interface {
	[]bool | []int | []int8 | []int16 | []int32 | []int64 |
		[]uint | []uint16 | []uint32 | []uint64 |
		[]float32 | []float64 | []string | []time.Time | []time.Duration
}

// Lit builds a literal: a value, or a Go slice, which is one List value (step 257).
//
// A List literal is one row: alone in Select it is a frame of one row, beside a
// column it is that list in every row, and against a column a List argument, the
// same for every row: Col("x").IsIn(Lit([]int64{1, 2})), whose list goes into a
// hash set once.
func Lit[T Literal | ListLiteral](v T) Expr { return wrap(litNode(any(v))) }

// Null builds a typed null literal.
func Null(dt dtype.DataType) Expr { return wrap(&expr.Lit{Value: nil, DT: dt}) }

// --- naming ------------------------------------------------------------------

// Alias renames the output column.
//
// It sets ONE fixed name, so applying it to a multi-column selection is an error.
// Use Prefix or Suffix to rename an expansion.
func (e Expr) Alias(name string) Expr {
	// An alias of an alias is the one name: the outer wins either way, and one node
	// keeps a plan's text as it was before Len was named (step 273).
	if a, ok := e.node().(*expr.Alias); ok {
		return wrap(&expr.Alias{Child: a.Child, Name: name})
	}
	return wrap(&expr.Alias{Child: e.node(), Name: name})
}

// Prefix prepends to the output name. Unlike Alias it is expansion-safe, deriving
// a distinct name per output column.
func (e Expr) Prefix(p string) Expr {
	return wrap(&expr.Rename{
		Child: e.node(),
		Fn:    func(s string) string { return p + s },
		Label: `name.prefix(` + quote(p) + `)`,
	})
}

// Suffix appends to the output name. Expansion-safe, like Prefix.
func (e Expr) Suffix(s string) Expr {
	return wrap(&expr.Rename{
		Child: e.node(),
		Fn:    func(x string) string { return x + s },
		Label: `name.suffix(` + quote(s) + `)`,
	})
}

// MapName derives the output name from the input name.
//
// fn is called while the query is planned. A panic in it is the caller's error, of
// kind ErrValue, naming the input name it panicked on. Two MapNames over one
// expression are two computations, whatever their functions (step 323).
func (e Expr) MapName(fn func(string) string) Expr {
	return wrap(expr.NewRename(expr.Rename{Child: e.node(), Fn: guardName("name.map", "MapName", fn),
		Label: "name.map(...)"}))
}

// guardName is fn, a panic in it the caller's error, of kind ErrValue, naming the
// name it panicked on. The planner and the evaluator call a name function where
// nothing returns an error, so the failure is raised; Collect's recover returns it
// as it is, not as ursus's bug.
func guardName(op, method string, fn func(string) string) func(string) string {
	return func(name string) string {
		defer func() {
			if v := recover(); v != nil {
				uerr.Raise(uerr.Attributed(v, uerr.KindValue, op,
					"the function passed to %s panicked on %q", method, name))
			}
		}()
		return fn(name)
	}
}

// --- casting -----------------------------------------------------------------

// Cast converts to another type, failing the query on the first unrepresentable
// value.
//
// What is representable depends on the target:
//
//   - An integer, a Decimal: exactly the value, in range. 3.5 is not an Int64, and
//     0.123 is not a Decimal(10, 2).
//   - A float: the nearest value of its width, so 0.1 cast to Float32 is the float32
//     nearest 0.1, and an Int64 above 2^53 is the float64 nearest it. Only a finite
//     value past the type's range — one that would round to an infinity — is
//     unrepresentable.
//   - From a String: the text parsed at the target's own width, by the same rules.
//     "256" is not a Uint8, and text that is no number cannot be parsed at all.
func (e Expr) Cast(to dtype.DataType) Expr {
	return wrap(&expr.Cast{Child: e.node(), To: to, Strict: true})
}

// CastLossy converts to another type, turning unrepresentable values into nulls.
// The result is always nullable as a consequence.
func (e Expr) CastLossy(to dtype.DataType) Expr {
	return wrap(&expr.Cast{Child: e.node(), To: to, Strict: false})
}

// ToPhysical is the values as they are stored, as Polars' to_physical (step 324): a
// Date's days as an Int32, a Datetime's, a Duration's and a Time's ticks as an Int64,
// a Decimal's unscaled units as an Int128, and an Enum's category as the smallest
// unsigned integer that holds every one; any other type as it is. Only an Enum's are
// copied. A nested type is refused.
func (e Expr) ToPhysical() Expr {
	return wrap(&expr.Call{Fn: expr.FnToPhysical, Args: []expr.Node{e.node()}})
}

// Reinterpret is an integer's bits as the integer of its width and the other
// signedness, as Polars' reinterpret (step 324): signed, a UInt64's maximum is an
// Int64's -1, and unsigned the reverse. Any other type is refused.
func (e Expr) Reinterpret(signed bool) Expr {
	return wrap(&expr.Call{Fn: expr.FnReinterpret, Args: []expr.Node{e.node(), litNode(signed)}})
}

// Pipe is fn of the expression, as Polars' pipe: a function of an expression applied
// in a chain of methods (step 324).
func (e Expr) Pipe(fn func(Expr) Expr) Expr { return fn(e) }

// --- arithmetic --------------------------------------------------------------
//
// Every binary operator takes an Operand, so both `a.Add(b)` and `a.Add(1)` work.
// The scalar form is by far the common one and requiring Lit(1) at every site
// would be a tax paid thousands of times.
//
// An integer result that leaves its type wraps, two's complement, as Polars' and
// Go's do: MaxInt64 + 1 is MinInt64, and MinInt64 // −1, the one quotient that
// leaves its type, is MinInt64 (step 178 decided it, audit row S26). An instant or
// a Duration is checked instead, and refuses; Sum and Mean widen to Int128, so a
// column's total cannot wrap.

func (e Expr) Add[T Operand](v T) Expr { return e.bin(expr.OpAdd, lift(v)) }
func (e Expr) Sub[T Operand](v T) Expr { return e.bin(expr.OpSub, lift(v)) }
func (e Expr) Mul[T Operand](v T) Expr { return e.bin(expr.OpMul, lift(v)) }

// Div is TRUE division: it always produces a float, so 7/2 is 3.5 even for two
// integers. Integer division is FloorDiv, so the surprising behaviour has to be
// asked for by name.
func (e Expr) Div[T Operand](v T) Expr { return e.bin(expr.OpDiv, lift(v)) }

// FloorDiv FLOORS, toward negative infinity, for integers, floats and a Duration
// alike: −7 // 2 is −4, not the −3 Go's / gives. Mod is its remainder, so
// a == b*FloorDiv(a, b) + Mod(a, b) and Mod takes the divisor's sign: −7 % 2 is 1.
// Both are Polars' and Python's rule. An integer divisor of zero is null; a float
// one follows IEEE.
func (e Expr) FloorDiv[T Operand](v T) Expr { return e.bin(expr.OpFloorDiv, lift(v)) }
func (e Expr) Mod[T Operand](v T) Expr      { return e.bin(expr.OpMod, lift(v)) }

// Pow raises the receiver to the power v.
//
// The result is ALWAYS a float, for the same reason Div's is: 2**-1 is 0.5, and a
// version that truncated negative powers to zero would be surprising in the
// direction that loses data silently. Cast the result if an integer is wanted.
func (e Expr) Pow[T Operand](v T) Expr { return e.bin(expr.OpPow, lift(v)) }

// TrueDiv is Div, Polars' truediv: true division, a float, as Div always is (step
// 324).
func (e Expr) TrueDiv[T Operand](v T) Expr { return e.Div(v) }

// Arctan2 is the angle of the point (x, y) from the positive x axis, in radians from
// -π to π, as Polars' arctan2(y, x), y first (step 324). Integers and Decimals give
// a Float64, two Float32s a Float32; a null either side is null.
func Arctan2(y, x Expr) Expr { return wrap(&expr.Binary{Op: expr.OpArctan2, L: y.node(), R: x.node()}) }

// Neg negates. Abs takes the absolute value.
//
// Both are defined for every signed numeric type, Int128 and Decimal included —
// which matters more than it sounds, because every integer Sum outputs Int128, so
// `Col("x").Sum().Abs()` is an ordinary query. Abs of an unsigned integer is
// itself. Neg of one is refused, because most of its negations do not fit: cast it
// to a signed type first.
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
//
// A Float32 result is the Float64 result rounded once to a float32, which is what
// Polars and PyArrow return. Until step 73 every Float32 result that was not exact
// at that width was a null.

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

// The trigonometric and hyperbolic functions, in radians, with Polars' names. Like
// Sqrt they widen to a float, keeping Float32, and a value outside the domain —
// ArcSin(2), ArcCosh(0) — is NaN rather than an error or a null.
func (e Expr) Sin() Expr     { return e.un(expr.OpSin) }
func (e Expr) Cos() Expr     { return e.un(expr.OpCos) }
func (e Expr) Tan() Expr     { return e.un(expr.OpTan) }
func (e Expr) ArcSin() Expr  { return e.un(expr.OpArcSin) }
func (e Expr) ArcCos() Expr  { return e.un(expr.OpArcCos) }
func (e Expr) ArcTan() Expr  { return e.un(expr.OpArcTan) }
func (e Expr) Sinh() Expr    { return e.un(expr.OpSinh) }
func (e Expr) Cosh() Expr    { return e.un(expr.OpCosh) }
func (e Expr) Tanh() Expr    { return e.un(expr.OpTanh) }
func (e Expr) ArcSinh() Expr { return e.un(expr.OpArcSinh) }
func (e Expr) ArcCosh() Expr { return e.un(expr.OpArcCosh) }
func (e Expr) ArcTanh() Expr { return e.un(expr.OpArcTanh) }

// Degrees converts radians to degrees, and Radians degrees to radians.
func (e Expr) Degrees() Expr { return e.un(expr.OpDegrees) }
func (e Expr) Radians() Expr { return e.un(expr.OpRadians) }

// Cot is the cotangent, 1/tan, as Polars computes it: Cot(0) is +Inf and Cot(-0)
// -Inf. It widens as Tan does. Its tangent is Go's, within 3 ulps of Polars' glibc
// one over ordinary values, as each trigonometric function here is within a few;
// near a multiple of pi/2 its relative error grows, its absolute error below 1e-30.
func (e Expr) Cot() Expr { return e.un(expr.OpCot) }

// RoundMode is how Round breaks a tie, a value halfway between its two roundings.
type RoundMode string

const (
	// RoundHalfAwayFromZero takes a tie away from zero, 2.5 to 3 and -2.5 to -3, as a
	// spreadsheet rounds: ursus's default, and Polars' mode="half_away_from_zero".
	RoundHalfAwayFromZero RoundMode = "half_away_from_zero"
	// RoundHalfToEven takes a tie to its even neighbour, 2.5 to 2 and 3.5 to 4:
	// banker's rounding, IEEE's, and Polars' default.
	RoundHalfToEven RoundMode = "half_to_even"
)

// Round rounds to decimals decimal places, HALF AWAY FROM ZERO, or by mode.
//
// Round(0.5) is 1 and Round(2.5) is 3, as a spreadsheet rounds, rather than IEEE's
// round-half-to-even. That is Polars' `mode="half_away_from_zero"`, and NOT Polars'
// default, which rounds half to even: Polars' `round(0)` gives 2 for 2.5, as
// Round(0, RoundHalfToEven) does here. It preserves the operand's type: rounding an
// integer column is the identity, not a widening.
//
// It rounds twice — the value is scaled by 10^decimals first, and that multiply
// rounds too. Round(2.675, 2) is 2.68, because 2.675 * 100 lands on exactly 267.5
// even though 2.675 itself is stored as 2.67499999999999982… The scaling is
// Polars', so each answer is Polars' to the bit.
//
// A Float32 is rounded as a Float64 and the result rounded once more to a float32,
// so Round(0.1, 1) of a Float32 is the float32 nearest 0.1.
func (e Expr) Round(decimals int, mode ...RoundMode) Expr {
	if decimals < 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "round",
			"decimals must not be negative, got %d", decimals)})
	}
	args := []expr.Node{e.node(), litNode(int64(decimals))}
	switch {
	case len(mode) > 1:
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "round", "round takes one mode, got %d", len(mode))})
	case len(mode) == 0 || mode[0] == RoundHalfAwayFromZero:
		// The default's plan is the one it always had.
	case mode[0] == RoundHalfToEven:
		args = append(args, litNode(string(mode[0])))
	default:
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "round", "%q is not a rounding mode", mode[0]).
			Hint("RoundHalfAwayFromZero or RoundHalfToEven")})
	}
	return wrap(&expr.Call{Fn: expr.FnMathRound, Args: args})
}

// Truncate drops each value's digits past decimals decimal places, toward zero:
// Polars' truncate, Round's scaling with math.Trunc for its rounding. Truncate(1) of
// 0.29 is 0.2 and of -0.99 -0.9; an integer is its own truncation.
func (e Expr) Truncate(decimals int) Expr {
	if decimals < 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "truncate",
			"decimals must not be negative, got %d", decimals)})
	}
	return wrap(&expr.Call{Fn: expr.FnMathTruncate, Args: []expr.Node{e.node(), litNode(int64(decimals))}})
}

// RoundSigFigs rounds to digits significant figures, half away from zero, in the
// operand's own type: Polars' round_sig_figs. 1234.5678 to two figures is 1200 and
// -0.0012345 is -0.0012. An integer is rounded through a float64 and brought back
// saturating, as Polars brings it, so an Int8 127 to two figures stays 127, its 130
// past the type. A Decimal is refused, as Round refuses it.
func (e Expr) RoundSigFigs(digits int) Expr {
	if digits < 1 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "round_sig_figs",
			"digits must be at least 1, got %d", digits)})
	}
	return wrap(&expr.Call{Fn: expr.FnMathRoundSigFigs, Args: []expr.Node{e.node(), litNode(int64(digits))}})
}

// The bit counts of an integer of 8 to 64 bits, each a Uint32, in the type's own
// width: Polars' bitwise_count_ones and its siblings. An Int8 -1 has eight ones and
// no zeros, and an Int8 1 has seven leading zeros where an Int64 1 has 63. Zero's
// trailing zeros are its width.
func (e Expr) BitwiseCountOnes() Expr     { return e.bitCall(expr.FnMathCountOnes) }
func (e Expr) BitwiseCountZeros() Expr    { return e.bitCall(expr.FnMathCountZeros) }
func (e Expr) BitwiseLeadingOnes() Expr   { return e.bitCall(expr.FnMathLeadingOnes) }
func (e Expr) BitwiseLeadingZeros() Expr  { return e.bitCall(expr.FnMathLeadingZeros) }
func (e Expr) BitwiseTrailingOnes() Expr  { return e.bitCall(expr.FnMathTrailingOnes) }
func (e Expr) BitwiseTrailingZeros() Expr { return e.bitCall(expr.FnMathTrailingZeros) }

func (e Expr) bitCall(fn expr.CallFn) Expr {
	return wrap(&expr.Call{Fn: fn, Args: []expr.Node{e.node()}})
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
	if !c.Valid() {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "is_between",
			"Closed(%d) is not a declared Closed", uint8(c)).
			Hint("use ClosedLeft, ClosedRight, ClosedBoth or ClosedNone")})
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

// IsIn tests membership in a set of values, Col("region").IsIn("eu", "us"), or in
// each row's own list, Col("x").IsIn(Col("tags")).
//
// # Values, or one expression
//
// Values are one set for every row. They share one type parameter, so the set is
// homogeneous by construction.
//
// One expression is a List column, and each row's value is looked for in the row's
// own list, as Polars' is_in(list) does (step 254). A null value or a null list is
// null; a value its list does not hold is false, a null element too, as Polars'
// is_in with nulls_equal=False. A column of the value's own type is refused, as
// Polars deprecates it for being ambiguous: membership among a column's values is
// IsIn(Col("b").Implode()), which is not there yet.
//
// Membership in another frame's column is a semi join wearing a predicate's
// clothes, and belongs with the join machinery.
//
// # IsIn(v) answers what Eq(v) answers
//
// The column and the values meet at the type == would compare them at, so IsIn
// refuses exactly the pairs Eq refuses, at plan time — a String against an Int64,
// an int against a Bool, an instant against a Date — and is false wherever Eq is:
// 5000 is not in an Int8 column, 0.1 is not in a Float32 one (they compare at
// Float64, as Polars does), and 00:00:01.5 is not in a Datetime(s) one. To match the
// float32 nearest 0.1, say so: IsIn(float32(0.1)).
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
func (e Expr) IsIn[T Operand](vs ...T) Expr {
	var zero T
	if _, ok := any(zero).(Expr); ok {
		return e.isInExpr(vs)
	}
	args := make([]expr.Node, 0, len(vs)+1)
	args = append(args, e.node())
	for _, v := range vs {
		args = append(args, litNode(any(v)))
	}
	return wrap(&expr.Call{Fn: expr.FnIsIn, Args: args})
}

// isInExpr is IsIn of an expression, which must be one, and a value a row.
func (e Expr) isInExpr(vs any) Expr {
	xs := vs.([]Expr)
	if len(xs) != 1 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindType, "is_in",
			"is_in takes one expression, a List column, and was given %d", len(xs)).
			Hint("for several values, pass the values: IsIn(1, 2, 3)")})
	}
	if x := xs[0].node(); expr.HasAgg(x) {
		return wrap(&expr.Err{E: uerr.New(uerr.KindUnsupported, "is_in",
			"%s.is_in(%s): membership among an aggregate's values is not there yet", e.node(), x).
			Hint("a List column is looked in a row at a time: IsIn(Col(\"tags\"))")})
	}
	return wrap(&expr.Call{Fn: expr.FnIsInExpr, Args: []expr.Node{e.node(), xs[0].node()}})
}

// Replace maps values: a row equal to old[j] becomes new[j], and every other row
// keeps its value, a null too, as Polars' replace does.
//
// The old values meet the column as IsIn's do, so Replace(old, new) replaces
// exactly the rows IsIn(old...) holds, NaN matching NaN. The answer is the type the
// conditional When(x.IsIn(old_j)).Then(new_j)….Otherwise(x) would have: a new value
// that fits the column's type exactly keeps it, so 20 into an Int32 is an Int32, and
// one that does not widens it, so 2.5 into an Int64 makes a Float64, where Polars
// truncates 2.5 to 2. old and new are as long as each other, and old holds no value
// twice. To replace a null, use FillNull.
func (e Expr) Replace[K, V Literal](old []K, new []V) Expr {
	return replaceCall(e, expr.FnReplace, old, new, nil)
}

// ReplaceStrict maps every value: a row equal to old[j] becomes new[j], a null stays
// null, and any other value fails the query, naming it, as Polars' replace_strict
// does. The answer is the type the new values meet at.
func (e Expr) ReplaceStrict[K, V Literal](old []K, new []V) Expr {
	return replaceCall(e, expr.FnReplaceStrict, old, new, nil)
}

// ReplaceOr maps values with a default: a row equal to old[j] becomes new[j], and
// every other row, a null too, becomes def, as Polars' replace_strict with a
// default does. The answer is the type the new values and def meet at.
func (e Expr) ReplaceOr[K, V Literal](old []K, new []V, def V) Expr {
	return replaceCall(e, expr.FnReplaceOr, old, new, &def)
}

func replaceCall[K, V Literal](e Expr, fn expr.CallFn, old []K, new []V, def *V) Expr {
	if len(old) != len(new) {
		return wrap(&expr.Err{E: uerr.New(uerr.KindSchema, fn.String(),
			"%s was given %d old values and %d new ones", fn, len(old), len(new)).
			Hint("give a new value for each old one, in the same order")})
	}
	args := make([]expr.Node, 0, 3+2*len(old))
	args = append(args, e.node(), litNode(int64(len(old))))
	for _, v := range old {
		args = append(args, litNode(any(v)))
	}
	for _, v := range new {
		// Replace's new values fill the column, so they are weak as FillNullWith's
		// value is: a value that fits the column's type takes it.
		n := litNode(any(v))
		if fn == expr.FnReplace {
			n = weaken(n)
		}
		args = append(args, n)
	}
	if def != nil {
		args = append(args, litNode(any(*def)))
	}
	return wrap(&expr.Call{Fn: fn, Args: args})
}

// --- boolean -----------------------------------------------------------------
//
// Kleene three-valued logic: `false AND null` is false, `true OR null` is true.

// And, Or and Xor of two Booleans are Kleene logic. Of two integers they are the
// integers' bits, at their common type as arithmetic's;
// a Go integer beside one is of its type where it fits, as Polars keeps
// Col("u8").And(6) a UInt8 (step 278). A Boolean beside an integer is refused.
func (e Expr) And[T Operand](v T) Expr { return e.bin(expr.OpAnd, liftWeak(v)) }
func (e Expr) Or[T Operand](v T) Expr  { return e.bin(expr.OpOr, liftWeak(v)) }
func (e Expr) Xor[T Operand](v T) Expr { return e.bin(expr.OpXor, liftWeak(v)) }

// Not is Kleene negation: NOT null is null. Of an integer it is its bits flipped,
// ~x, of its type (step 278).
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
	return wrap(&expr.Binary{Op: op, L: e.node(), R: r})
}

func (e Expr) un(op expr.UnaryOp) Expr {
	return wrap(&expr.Unary{Op: op, Child: e.node()})
}
