// Package ast is qcheck's typed query: expressions and frame operations over the
// generated tables, each node carrying the type it answers. A tree is built only
// through the constructors here, which refuse an ill-typed node, and Check proves a
// whole query well-typed again from its leaves.
//
// The ops are step 453's: Filter, Select, WithColumns and Count; GroupBy on keys
// with Sum, Mean, Min, Max, Count, Len and NUnique; Sort with Head or Slice; Join
// inner, left, semi and anti; comparisons, Booleans, arithmetic with FloorDiv and
// Mod, When, FillNull and Cast. Step 539 adds the string and temporal namespaces'
// functions (Fn), arithmetic and comparison over the unsigned integers, Float32,
// Decimal and the temporal types, and their casts. build turns a tree into a
// LazyFrame; step 513's sqlgen turns the same tree into DuckDB's SQL.
//
// The types follow ursus's own rules for the combinations qcheck generates, and
// self checks them against CollectSchema on every query: a literal has its Go
// value's type, Div is a Float64, an integer Sum an Int128, a count a Uint64
// (feature-gaps §9).
package ast

import (
	"fmt"
	"math"
	"math/big"
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

// Lit is a constant: V is an int64 for every signed integer type, a uint64 for an
// unsigned one, a float64 or a float32, a string, a bool, an int32 of days for a
// Date, an int64 of ticks for a Datetime, a Duration and a Time, and a Decimal's
// unscaled value as decimal text. Null is a typed null, ursus.Null.
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
	if lt.IsTemporal() || rt.IsTemporal() {
		if op.IsCompare() {
			if !model.Comparable(lt, rt) {
				return nil, fmt.Errorf("%s of %s and %s", op, lt, rt)
			}
			return &Bin{Op: op, L: l, R: r, T: model.Bool}, nil
		}
		t, err := temporalArith(op, lt, rt)
		if err != nil {
			return nil, err
		}
		return &Bin{Op: op, L: l, R: r, T: t}, nil
	}
	var t model.Type
	switch {
	case op <= Mul || op == FloorDiv || op == Mod:
		if !lt.Arith() || !rt.Arith() {
			return nil, fmt.Errorf("%s of %s and %s", op, lt, rt)
		}
		if lt.IsDecimal() || rt.IsDecimal() {
			var err error
			if t, err = decimalArith(op, lt, rt); err != nil {
				return nil, err
			}
			break
		}
		t, _ = model.Promote(lt, rt)
		if t == model.Int128 {
			// A Uint64 beside a signed integer meets at Int128, whose arithmetic is
			// exact or refused (R-I128-ARITH).
			return nil, fmt.Errorf("%s of %s and %s is an Int128", op, lt, rt)
		}
	case op == Div:
		if !lt.Arith() || !rt.Arith() {
			return nil, fmt.Errorf("%s of %s and %s", op, lt, rt)
		}
		// A float when the operands meet at Float32, else a Float64; a Decimal's
		// quotient is the Float64 nearest the exact one.
		t = model.Float64
		if p, ok := model.Promote(lt, rt); ok && p == model.Float32 {
			t = model.Float32
		}
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

// decimalArith is a Decimal's +, - and *, expr/resolve.go resolveDecimal's: a sum's
// integer digits the wider side's and one more, its scale the finer; a product's
// precision and scale each side's added. qcheck refuses one past 38 digits, where
// ursus caps the precision and a value may overflow it, a refusal at run time; a
// float beside it, whose arithmetic is the float's; and // and %, which ursus
// refuses.
func decimalArith(op BinOp, l, r model.Type) (model.Type, error) {
	pa, sa, oka := model.DecimalDigits(l)
	pb, sb, okb := model.DecimalDigits(r)
	if !oka || !okb || pa > model.MaxDecimalPrecision || pb > model.MaxDecimalPrecision {
		return model.Invalid, fmt.Errorf("%s of %s and %s", op, l, r)
	}
	var p, s int
	switch op {
	case Add, Sub:
		s = max(sa, sb)
		p = max(pa-sa, pb-sb) + s + 1
	case Mul:
		p, s = pa+pb, sa+sb
	default:
		return model.Invalid, fmt.Errorf("%s of %s and %s", op, l, r)
	}
	if p > model.MaxDecimalPrecision {
		return model.Invalid, fmt.Errorf("%s of %s and %s needs %d digits", op, l, r, p)
	}
	return model.Decimal(p, s), nil
}

// temporalArith is expr/resolve.go resolveTemporalArithmetic's answer for the
// shapes qcheck makes: an instant or a Time less one of its own kind and zone, a
// Duration at the finer unit, a Date less a Date one of seconds; an instant plus or
// less a Duration, the instant's type, a Date's a Datetime(s); two Durations at the
// finer unit; a Duration times an integer, the Duration.
func temporalArith(op BinOp, l, r model.Type) (model.Type, error) {
	bad := fmt.Errorf("%s of %s and %s", op, l, r)
	switch {
	case op == Sub && (l.IsInstant() || l.IsTime()) && l.Kind() == r.Kind():
		if l.ZoneIndex() != r.ZoneIndex() {
			return model.Invalid, bad
		}
		if l == model.Date {
			return model.Duration(model.Sec), nil
		}
		return model.Duration(max(l.Unit(), r.Unit())), nil
	case (op == Add || op == Sub) && l.IsInstant() && r.IsDuration():
		if l == model.Date {
			return model.Datetime(model.Sec, 0), nil
		}
		return l, nil
	case op == Add && l.IsDuration() && r.IsInstant():
		if r == model.Date {
			return model.Datetime(model.Sec, 0), nil
		}
		return r, nil
	case (op == Add || op == Sub) && l.IsDuration() && r.IsDuration():
		return model.Duration(max(l.Unit(), r.Unit())), nil
	case op == Mul && l.IsDuration() && r.IsInt() && r != model.Int128:
		return l, nil
	case op == Mul && l.IsInt() && l != model.Int128 && r.IsDuration():
		return r, nil
	}
	return model.Invalid, bad
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
		if !xt.Arith() || op == Neg && xt.IsUnsigned() {
			// ursus refuses an unsigned integer's Neg (expr.go).
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

// WeakLit is whether a literal of t is a Go scalar, which a fill lifts to a weak
// literal (fill.go): every integer, a float, text and a Boolean. A Date, a temporal
// and a Decimal literal are a cast of a scalar, an expression.
func WeakLit(t model.Type) bool {
	return t.IsInt() || t.IsFloat() || t == model.String || t == model.Bool
}

// NewFillNull is the typed fill. A weak literal (fill.go, expr/weak.go) keeps the
// column's type where promotion would widen it and its value fits the column
// exactly; one that does not fit falls back to ordinary promotion. An expression
// meets by Promote.
func NewFillNull(x, with Expr) (Expr, error) {
	xt, wt := x.Type(), with.Type()
	t, ok := model.Promote(xt, wt)
	if !ok {
		return nil, fmt.Errorf("FillNull of %s with %s", xt, wt)
	}
	if l, isLit := with.(*Lit); isLit && !l.Null && WeakLit(wt) && t != xt && FitsExactly(l.V, xt) {
		t = xt
	}
	return &FillNull{X: x, With: with, T: t}, nil
}

// FitsExactly is whether a literal's value is a value of t with no loss,
// expr/weak.go FitsExactly's rule: an integer in range, a float to an integer when
// whole and within 2^62, a float to Float32 when it round-trips, NaN among them, and
// an integer to a float within 2^53 for Float64 and 2^24 for Float32.
func FitsExactly(v any, t model.Type) bool {
	switch x := v.(type) {
	case int64:
		return intFits(x, t)
	case uint64:
		if x <= math.MaxInt64 {
			return intFits(int64(x), t)
		}
		return t == model.Uint64 || t == model.Int128
	case float32:
		return floatFits(float64(x), t)
	case float64:
		return floatFits(x, t)
	}
	return false
}

func intFits(v int64, t model.Type) bool {
	switch t {
	case model.Int8:
		return v >= math.MinInt8 && v <= math.MaxInt8
	case model.Int16:
		return v >= math.MinInt16 && v <= math.MaxInt16
	case model.Int32:
		return v >= math.MinInt32 && v <= math.MaxInt32
	case model.Int64, model.Int128:
		return true
	case model.Uint8:
		return v >= 0 && v <= math.MaxUint8
	case model.Uint16:
		return v >= 0 && v <= math.MaxUint16
	case model.Uint32:
		return v >= 0 && v <= math.MaxUint32
	case model.Uint64:
		return v >= 0
	case model.Float64:
		return v >= -(1<<53) && v <= 1<<53
	case model.Float32:
		return v >= -(1<<24) && v <= 1<<24
	}
	return false
}

func floatFits(v float64, t model.Type) bool {
	switch t {
	case model.Float64:
		return true
	case model.Float32:
		return math.IsNaN(v) || float64(float32(v)) == v
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v < -(1<<62) || v > 1<<62 {
		return false
	}
	return intFits(int64(v), t)
}

// fits is whether a signed integer literal's value is a value of its own type.
func fits(v int64, t model.Type) bool { return t.IsSignedInt() && intFits(v, t) }

// StrictCast is whether X.Cast(to) can never refuse a value of from: a widening
// integer, signed or not; an integer, a Boolean, Float32 or a Decimal to Float64,
// an integer to Float32; anything to text; a Date to its days and back; a temporal
// to its ticks and Int64 to a Datetime or a Duration; a Datetime to a coarser unit
// or another zone, whose ticks are kept, one of microseconds or finer to a Date,
// and a Date to a Datetime of milliseconds or coarser; a Datetime to a Time of any
// unit, a Time to another; a Duration to a coarser unit; a Decimal to one that holds
// its every value, an integer to a Decimal of its digits. Every other cast qcheck
// makes is CastLossy, whose nulls answer what does not fit: a strict cast's refusal
// depends on how many rows a Head lets through, which differs by batch size and by
// the optimizer's limit pushdown, and would read as a mismatch of configurations.
func StrictCast(from, to model.Type) bool {
	switch {
	case from == to:
		return true
	case to == model.Int128 || from == model.Invalid:
		return false
	case from.IsSignedInt() && to.IsSignedInt(), from.IsUnsigned() && to.IsUnsigned():
		return to.Bits() >= from.Bits()
	case from.IsUnsigned() && to.IsSignedInt():
		return to.Bits() > from.Bits()
	case from.IsInt() && to.IsFloat():
		return from != model.Int128 || to == model.Float64
	case from == model.Float32 && to == model.Float64:
		return true
	case to == model.String:
		return true
	case from == model.Bool && (to.IsInt() || to.IsFloat()):
		return true
	case from == model.Date && (to == model.Int32 || to == model.Int64):
		return true
	case from == model.Int32 && to == model.Date:
		return true
	case (from.IsDatetime() || from.IsDuration() || from.IsTime()) && to == model.Int64:
		return true
	case from == model.Int64 && (to.IsDatetime() || to.IsDuration()):
		return true
	case from.IsDatetime() && to.IsDatetime():
		return to.Unit() <= from.Unit()
	case from.IsDatetime() && to == model.Date:
		return from.Unit() >= model.Micro
	case from == model.Date && to.IsDatetime():
		return to.Unit() <= model.Milli
	case from.IsDatetime() && to.IsTime(), from.IsTime() && to.IsTime():
		return true
	case from.IsDuration() && to.IsDuration():
		return to.Unit() <= from.Unit()
	case from.IsDecimal() && to == model.Float64:
		return true
	case from.IsDecimal() && to.IsDecimal():
		return to.Scale() >= from.Scale() && to.Prec()-to.Scale() >= from.Prec()-from.Scale()
	case from.IsInt() && to.IsDecimal():
		p, _, _ := model.DecimalDigits(from)
		return to.Prec()-to.Scale() >= p
	}
	return false
}

// LossyCast is whether X.CastLossy(to) is a cast qcheck makes: text to a number, a
// Decimal, a Date, a Datetime, a Time or a Boolean; a float to an integer or a
// Decimal, Float64 to Float32; an integer to another or to a Decimal, Int64 to a
// Date; a Decimal to an integer or another Decimal; a Datetime to another, a
// Datetime to a Date and back; a Duration to another; Int64 to a Time.
func LossyCast(from, to model.Type) bool {
	switch {
	case to == model.Int128:
		return false
	case from == model.String && (to.Arith() || to == model.Date || to == model.Bool || to.IsDatetime() || to.IsTime()):
		return true
	case from.IsFloat() && (to.IsInt() || to.IsDecimal()):
		return true
	case from == model.Float64 && to == model.Float32:
		return true
	case from.IsInt() && to.IsInt():
		return true
	case from.IsInt() && to.IsDecimal():
		return from != model.Int128
	case from == model.Int64 && to == model.Date:
		return true
	case from.IsDecimal() && (to.IsInt() || to.IsDecimal()):
		return true
	case from.IsDatetime() && to.IsDatetime():
		return true
	case from.IsDatetime() && to == model.Date, from == model.Date && to.IsDatetime():
		return true
	case from.IsDuration() && to.IsDuration():
		return true
	case from == model.Int64 && to.IsTime():
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
// float's its float, a Decimal's a Decimal(38) of its scale, a Duration's itself; a
// Mean is a Float64, a Float32's a Float32 and a Duration's a Duration; Min and Max
// keep the type; Count, Len and NUnique are a Uint64 (agg.go, feature-gaps §9).
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
		case xt.IsFloat(), xt.IsDuration():
			t = xt
		case xt.IsInt() || xt == model.Bool:
			t = model.Int128
		case xt.IsDecimal():
			t = model.Decimal(model.MaxDecimalPrecision, xt.Scale())
		default:
			return nil, fmt.Errorf("Sum of %s", xt)
		}
	case Mean:
		switch {
		case xt == model.Float32, xt.IsDuration():
			t = xt
		case xt.Arith():
			t = model.Float64
		default:
			return nil, fmt.Errorf("Mean of %s", xt)
		}
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

// LitOK is whether a literal's value is one of its type, held as Lit says: an
// integer's in range, a Decimal's unscaled value within its precision, a Time's
// ticks within a day.
func LitOK(l *Lit) bool {
	if l.Null {
		return true
	}
	t := l.T
	switch v := l.V.(type) {
	case int64:
		switch {
		case t.IsSignedInt():
			return fits(v, t)
		case t.IsDatetime(), t.IsDuration():
			return true
		case t.IsTime():
			return v >= 0 && v < t.Unit().PerDay()
		}
	case uint64:
		return t.IsUnsigned() && (v <= math.MaxInt64 && intFits(int64(v), t) || t == model.Uint64)
	case float64:
		return t == model.Float64
	case float32:
		return t == model.Float32
	case string:
		if t == model.String {
			return true
		}
		if t.IsDecimal() {
			u, ok := new(big.Int).SetString(v, 10)
			return ok && u.CmpAbs(DecimalBound(t.Prec())) < 0
		}
	case bool:
		return t == model.Bool
	case int32:
		return t == model.Date
	}
	return false
}

// DecimalBound is 10^p, which every Decimal of precision p is below in magnitude.
func DecimalBound(p int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(p)), nil)
}

// DecimalText is an unscaled value of scale s as decimal text, as dtype writes it:
// "-0.05", "1.50", "123".
func DecimalText(unscaled string, s int) string {
	neg := strings.HasPrefix(unscaled, "-")
	digits := strings.TrimPrefix(unscaled, "-")
	if s > 0 {
		if len(digits) <= s {
			digits = strings.Repeat("0", s-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-s] + "." + digits[len(digits)-s:]
	}
	if neg && strings.Trim(digits, "0.") != "" {
		return "-" + digits
	}
	return digits
}

// --- Go source ------------------------------------------------------------------

// GoUnit is a unit as dtype's constant.
func GoUnit(u model.Unit) string {
	return [...]string{"dtype.Second", "dtype.Milli", "dtype.Micro", "dtype.Nano"}[u]
}

// GoType is t as Go source over package dtype.
func GoType(t model.Type) string {
	switch {
	case t.IsDatetime():
		return fmt.Sprintf("dtype.Datetime(%s, %q)", GoUnit(t.Unit()), t.Zone())
	case t.IsDuration():
		return "dtype.Duration(" + GoUnit(t.Unit()) + ")"
	case t.IsTime():
		return "dtype.Time(" + GoUnit(t.Unit()) + ")"
	case t.IsDecimal():
		return fmt.Sprintf("dtype.Decimal(%d, %d)", t.Prec(), t.Scale())
	}
	return "dtype." + t.String()
}

func (e *Col) Go() string { return "ursus.Col(" + strconv.Quote(e.Name) + ")" }

func (e *Lit) Go() string {
	switch {
	case e.Null:
		return "ursus.Null(" + GoType(e.T) + ")"
	case e.T == model.Date:
		return "ursus.Lit(" + e.scalar() + ").Cast(dtype.Date)"
	case e.T.IsDatetime(), e.T.IsDuration(), e.T.IsTime():
		return fmt.Sprintf("ursus.Lit(int64(%d)).Cast(%s)", e.V.(int64), GoType(e.T))
	case e.T.IsDecimal():
		return fmt.Sprintf("ursus.Lit(%q).Cast(%s)", DecimalText(e.V.(string), e.T.Scale()), GoType(e.T))
	}
	return "ursus.Lit(" + e.scalar() + ")"
}

// scalar is the literal as a Go scalar, which every operator takes as an Operand.
func (e *Lit) scalar() string {
	switch {
	case e.T.IsSignedInt():
		return fmt.Sprintf("%s(%d)", strings.ToLower(e.T.String()), e.V.(int64))
	case e.T.IsUnsigned():
		return fmt.Sprintf("%s(%d)", strings.ToLower(e.T.String()), e.V.(uint64))
	case e.T == model.Float64:
		return GoFloat(e.V.(float64))
	case e.T == model.Float32:
		return GoFloat32(e.V.(float32))
	case e.T == model.String:
		return strconv.Quote(e.V.(string))
	case e.T == model.Bool:
		return strconv.FormatBool(e.V.(bool))
	case e.T == model.Date:
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

// GoFloat32 is f as Go source that evaluates to its bits, a NaN as Go converts
// math.NaN() to a float32.
func GoFloat32(f float32) string {
	g := float64(f)
	switch {
	case math.IsNaN(g):
		return "float32(math.NaN())"
	case math.IsInf(g, 1):
		return "float32(math.Inf(1))"
	case math.IsInf(g, -1):
		return "float32(math.Inf(-1))"
	case g == 0 && math.Signbit(g):
		return "float32(math.Copysign(0, -1))"
	}
	return "float32(" + strconv.FormatFloat(g, 'g', -1, 32) + ")"
}

// operand is e as an Operand argument: a non-null literal as its scalar, where it
// has one.
func operand(e Expr) string {
	if l, ok := e.(*Lit); ok && !l.Null && WeakLit(l.T) {
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
	return e.X.Go() + "." + m + "(" + GoType(e.To) + ")"
}

func (e *Agg) Go() string {
	if e.Op == Len {
		return "ursus.Len()"
	}
	return e.X.Go() + "." + e.Op.String() + "()"
}
