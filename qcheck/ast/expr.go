// Package ast is qcheck's typed query: expressions and frame operations over the
// generated tables, each node carrying the type it answers. A tree is built only
// through the constructors here, which refuse an ill-typed node, and Check proves a
// whole query well-typed again from its leaves.
//
// The ops are step 453's: Filter, Select, WithColumns and Count; GroupBy on keys
// with Sum, Mean, Min, Max, Count, Len and NUnique; Sort with Head or Slice; Join
// inner, left, semi and anti; comparisons, Booleans, arithmetic with FloorDiv and
// Mod, When, FillNull and Cast. build turns a tree into a LazyFrame; step 513's
// sqlgen will turn the same tree into DuckDB's SQL.
//
// The types follow ursus's own rules for the combinations qcheck generates, and
// self checks them against CollectSchema on every query: a literal has its Go
// value's type, Div is a Float64, an integer Sum an Int128, a count a Uint64
// (feature-gaps §9).
package ast

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/advenn/ursus/qcheck/model"
)

// Expr is a typed expression.
type Expr interface {
	Type() model.Type
	// Go is the expression as Go source over ursus's API.
	Go() string
	// Kids are its operands.
	Kids() []Expr
}

// Col reads a column.
type Col struct {
	Name string
	T    model.Type
}

// Lit is a constant: V is an int64 for every integer type, a float64, a string, a
// bool, or an int32 of days for a Date. Null is a typed null, ursus.Null.
type Lit struct {
	T    model.Type
	V    any
	Null bool
}

// BinOp is a binary operator.
type BinOp uint8

const (
	Add BinOp = iota
	Sub
	Mul
	Div
	FloorDiv
	Mod
	Eq
	Ne
	Lt
	Le
	Gt
	Ge
	And
	Or
)

var binNames = [...]string{"Add", "Sub", "Mul", "Div", "FloorDiv", "Mod", "Eq", "Ne", "Lt", "Le",
	"Gt", "Ge", "And", "Or"}

func (o BinOp) String() string { return binNames[o] }

// IsCompare is Eq to Ge.
func (o BinOp) IsCompare() bool { return o >= Eq && o <= Ge }

// Bin is a binary operation.
type Bin struct {
	Op   BinOp
	L, R Expr
	T    model.Type
}

// UnOp is a unary operator.
type UnOp uint8

const (
	Not UnOp = iota
	Neg
	Abs
	IsNull
	IsNotNull
)

var unNames = [...]string{"Not", "Neg", "Abs", "IsNull", "IsNotNull"}

func (o UnOp) String() string { return unNames[o] }

// Un is a unary operation.
type Un struct {
	Op UnOp
	X  Expr
	T  model.Type
}

// When is When(Cond).Then(Then).Otherwise(Else).
type When struct {
	Cond, Then, Else Expr
	T                model.Type
}

// FillNull is X.FillNullWith(With): a literal With is a Go scalar, which ursus
// lifts to a weak literal (fill.go), an expression is used as written.
type FillNull struct {
	X, With Expr
	T       model.Type
}

// Cast is X.Cast(To), or X.CastLossy(To) when Lossy.
type Cast struct {
	X     Expr
	To    model.Type
	Lossy bool
}

// AggOp is an aggregate.
type AggOp uint8

const (
	Sum AggOp = iota
	Mean
	Min
	Max
	Count
	Len
	NUnique
)

var aggNames = [...]string{"Sum", "Mean", "Min", "Max", "Count", "Len", "NUnique"}

func (o AggOp) String() string { return aggNames[o] }

// Agg is an aggregate of X; Len has none.
type Agg struct {
	Op AggOp
	X  Expr
	T  model.Type
}

func (e *Col) Type() model.Type      { return e.T }
func (e *Lit) Type() model.Type      { return e.T }
func (e *Bin) Type() model.Type      { return e.T }
func (e *Un) Type() model.Type       { return e.T }
func (e *When) Type() model.Type     { return e.T }
func (e *FillNull) Type() model.Type { return e.T }
func (e *Cast) Type() model.Type     { return e.To }
func (e *Agg) Type() model.Type      { return e.T }

func (e *Col) Kids() []Expr      { return nil }
func (e *Lit) Kids() []Expr      { return nil }
func (e *Bin) Kids() []Expr      { return []Expr{e.L, e.R} }
func (e *Un) Kids() []Expr       { return []Expr{e.X} }
func (e *When) Kids() []Expr     { return []Expr{e.Cond, e.Then, e.Else} }
func (e *FillNull) Kids() []Expr { return []Expr{e.X, e.With} }
func (e *Cast) Kids() []Expr     { return []Expr{e.X} }
func (e *Agg) Kids() []Expr {
	if e.X == nil {
		return nil
	}
	return []Expr{e.X}
}

// --- constructors -------------------------------------------------------------

// NewBin is the typed binary node, or an error naming why it is ill-typed.
func NewBin(op BinOp, l, r Expr) (Expr, error) {
	lt, rt := l.Type(), r.Type()
	var t model.Type
	switch {
	case op <= Mul || op == FloorDiv || op == Mod:
		if !lt.Arith() || !rt.Arith() {
			return nil, fmt.Errorf("%s of %s and %s", op, lt, rt)
		}
		t, _ = model.Promote(lt, rt)
	case op == Div:
		if !lt.Arith() || !rt.Arith() {
			return nil, fmt.Errorf("%s of %s and %s", op, lt, rt)
		}
		t = model.Float64
	case op.IsCompare():
		if !model.Comparable(lt, rt) {
			return nil, fmt.Errorf("%s of %s and %s", op, lt, rt)
		}
		t = model.Bool
	case op == And || op == Or:
		if lt != model.Bool || rt != model.Bool {
			return nil, fmt.Errorf("%s of %s and %s", op, lt, rt)
		}
		t = model.Bool
	default:
		return nil, fmt.Errorf("unknown operator %d", op)
	}
	return &Bin{Op: op, L: l, R: r, T: t}, nil
}

// NewUn is the typed unary node.
func NewUn(op UnOp, x Expr) (Expr, error) {
	xt := x.Type()
	switch op {
	case Not:
		if xt != model.Bool {
			return nil, fmt.Errorf("Not of %s", xt)
		}
		return &Un{Op: op, X: x, T: model.Bool}, nil
	case Neg, Abs:
		if !xt.Arith() {
			return nil, fmt.Errorf("%s of %s", op, xt)
		}
		return &Un{Op: op, X: x, T: xt}, nil
	case IsNull, IsNotNull:
		return &Un{Op: op, X: x, T: model.Bool}, nil
	}
	return nil, fmt.Errorf("unknown operator %d", op)
}

// NewWhen is the typed conditional: its branches meet by Promote.
func NewWhen(cond, then, els Expr) (Expr, error) {
	if cond.Type() != model.Bool {
		return nil, fmt.Errorf("When's condition is %s", cond.Type())
	}
	t, ok := model.Promote(then.Type(), els.Type())
	if !ok {
		return nil, fmt.Errorf("When's branches %s and %s do not meet", then.Type(), els.Type())
	}
	return &When{Cond: cond, Then: then, Else: els, T: t}, nil
}

// NewFillNull is the typed fill. A literal fill is weak (fill.go): an integer that
// fits the column's integer type, or a whole float, keeps the column's type; one
// that does not falls back to ordinary promotion. An expression meets by Promote.
func NewFillNull(x, with Expr) (Expr, error) {
	xt, wt := x.Type(), with.Type()
	if l, ok := with.(*Lit); ok && !l.Null {
		switch {
		case xt == wt:
			return &FillNull{X: x, With: with, T: xt}, nil
		case xt.IsSignedInt() && wt.IsSignedInt():
			if fits(l.V.(int64), xt) {
				return &FillNull{X: x, With: with, T: xt}, nil
			}
		case xt.IsSignedInt() && wt == model.Float64:
			f := l.V.(float64)
			if f == math.Trunc(f) && !math.IsInf(f, 0) && fits64(f, xt) {
				return &FillNull{X: x, With: with, T: xt}, nil
			}
		}
	}
	t, ok := model.Promote(xt, wt)
	if !ok {
		return nil, fmt.Errorf("FillNull of %s with %s", xt, wt)
	}
	return &FillNull{X: x, With: with, T: t}, nil
}

func fits(v int64, t model.Type) bool {
	switch t {
	case model.Int8:
		return v >= math.MinInt8 && v <= math.MaxInt8
	case model.Int16:
		return v >= math.MinInt16 && v <= math.MaxInt16
	case model.Int32:
		return v >= math.MinInt32 && v <= math.MaxInt32
	}
	return t == model.Int64 || t == model.Int128
}

func fits64(f float64, t model.Type) bool {
	if f < -9.223372036854775808e18 || f >= 9.223372036854775808e18 {
		return false
	}
	return fits(int64(f), t)
}

// StrictCast is whether X.Cast(to) can never refuse a value of from: a widening
// integer, an integer or a Boolean to Float64 or text, a float or a Date to text, a
// Date to its days and back. Every other cast qcheck makes is CastLossy, whose
// nulls answer what does not fit: a strict cast's refusal depends on how many rows
// a Head lets through, which differs by batch size and by the optimizer's limit
// pushdown, and would read as a mismatch of configurations.
func StrictCast(from, to model.Type) bool {
	switch {
	case from == to:
		return true
	case from.IsSignedInt() && to.IsSignedInt():
		return to.Bits() >= from.Bits()
	case from.IsSignedInt() && (to == model.Float64 || to == model.String):
		return true
	case from == model.Bool && (to.IsSignedInt() || to == model.Float64 || to == model.String):
		return true
	case from == model.Float64 && to == model.String:
		return true
	case from == model.Date && (to == model.Int32 || to == model.Int64 || to == model.String):
		return true
	case from == model.Int32 && to == model.Date:
		return true
	}
	return false
}

// LossyCast is whether X.CastLossy(to) is a cast qcheck makes: text to a number, a
// Date or a Boolean; a float to an integer; an integer to a narrower one or to a
// Date.
func LossyCast(from, to model.Type) bool {
	switch {
	case from == model.String && (to.Arith() || to == model.Date || to == model.Bool):
		return true
	case from == model.Float64 && to.IsSignedInt() && to != model.Int128:
		return true
	case from.IsSignedInt() && to.IsSignedInt() && to != model.Int128:
		return true
	case from == model.Int64 && to == model.Date:
		return true
	}
	return false
}

// NewCast is the typed cast.
func NewCast(x Expr, to model.Type, lossy bool) (Expr, error) {
	ok := StrictCast(x.Type(), to)
	if lossy {
		ok = LossyCast(x.Type(), to)
	}
	if !ok {
		return nil, fmt.Errorf("cast of %s to %s (lossy %v)", x.Type(), to, lossy)
	}
	return &Cast{X: x, To: to, Lossy: lossy}, nil
}

// NewAgg is the typed aggregate: an integer's or a Boolean's Sum is an Int128, a
// Float64's a Float64; a Mean is a Float64; Min and Max keep the type; Count, Len
// and NUnique are a Uint64 (agg.go, feature-gaps §9).
func NewAgg(op AggOp, x Expr) (Expr, error) {
	if op == Len {
		return &Agg{Op: Len, T: model.Uint64}, nil
	}
	if x == nil {
		return nil, fmt.Errorf("%s needs an operand", op)
	}
	xt := x.Type()
	var t model.Type
	switch op {
	case Sum:
		switch {
		case xt == model.Float64:
			t = model.Float64
		case xt.IsSignedInt() || xt == model.Bool:
			t = model.Int128
		default:
			return nil, fmt.Errorf("Sum of %s", xt)
		}
	case Mean:
		if !xt.Arith() {
			return nil, fmt.Errorf("Mean of %s", xt)
		}
		t = model.Float64
	case Min, Max:
		t = xt
	case Count, NUnique:
		t = model.Uint64
	default:
		return nil, fmt.Errorf("unknown aggregate %d", op)
	}
	return &Agg{Op: op, X: x, T: t}, nil
}

// IsAgg is whether e holds an aggregate.
func IsAgg(e Expr) bool {
	if _, ok := e.(*Agg); ok {
		return true
	}
	for _, k := range e.Kids() {
		if IsAgg(k) {
			return true
		}
	}
	return false
}

// Reads are the columns e reads.
func Reads(e Expr) []string {
	var out []string
	var walk func(Expr)
	walk = func(e Expr) {
		if c, ok := e.(*Col); ok {
			out = append(out, c.Name)
		}
		for _, k := range e.Kids() {
			walk(k)
		}
	}
	walk(e)
	return out
}

// --- Go source ------------------------------------------------------------------

func (e *Col) Go() string { return "ursus.Col(" + strconv.Quote(e.Name) + ")" }

func (e *Lit) Go() string {
	switch {
	case e.Null:
		return "ursus.Null(dtype." + e.T.String() + ")"
	case e.T == model.Date:
		return "ursus.Lit(" + e.scalar() + ").Cast(dtype.Date)"
	}
	return "ursus.Lit(" + e.scalar() + ")"
}

// scalar is the literal as a Go scalar, which every operator takes as an Operand.
func (e *Lit) scalar() string {
	switch e.T {
	case model.Int8, model.Int16, model.Int32, model.Int64:
		return fmt.Sprintf("%s(%d)", strings.ToLower(e.T.String()), e.V.(int64))
	case model.Float64:
		return GoFloat(e.V.(float64))
	case model.String:
		return strconv.Quote(e.V.(string))
	case model.Bool:
		return strconv.FormatBool(e.V.(bool))
	case model.Date:
		// No Go scalar is a Date: its days, cast.
		return fmt.Sprintf("int32(%d)", e.V.(int32))
	}
	return fmt.Sprintf("%v", e.V)
}

// GoFloat is f as Go source that evaluates to its bits.
func GoFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "math.NaN()"
	case math.IsInf(f, 1):
		return "math.Inf(1)"
	case math.IsInf(f, -1):
		return "math.Inf(-1)"
	case f == 0 && math.Signbit(f):
		return "math.Copysign(0, -1)"
	}
	return "float64(" + strconv.FormatFloat(f, 'g', -1, 64) + ")"
}

// operand is e as an Operand argument: a non-null literal as its scalar.
func operand(e Expr) string {
	if l, ok := e.(*Lit); ok && !l.Null && l.T != model.Date {
		return l.scalar()
	}
	return e.Go()
}

func (e *Bin) Go() string {
	return e.L.Go() + "." + e.Op.String() + "(" + operand(e.R) + ")"
}

func (e *Un) Go() string { return e.X.Go() + "." + e.Op.String() + "()" }

func (e *When) Go() string {
	return "ursus.When(" + e.Cond.Go() + ").Then(" + operand(e.Then) + ").Otherwise(" + operand(e.Else) + ")"
}

func (e *FillNull) Go() string { return e.X.Go() + ".FillNullWith(" + operand(e.With) + ")" }

func (e *Cast) Go() string {
	m := "Cast"
	if e.Lossy {
		m = "CastLossy"
	}
	return e.X.Go() + "." + m + "(dtype." + e.To.String() + ")"
}

func (e *Agg) Go() string {
	if e.Op == Len {
		return "ursus.Len()"
	}
	return e.X.Go() + "." + e.Op.String() + "()"
}
