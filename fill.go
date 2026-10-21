package ursus

import (
	"strconv"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Null repair, and the weak-literal machinery that makes it not widen columns.

// liftWeak is lift for the fill family: a Go scalar becomes a WEAK literal, an
// Expr passes through untouched.
//
// It is the ONLY place weakness enters the IR, which is what keeps Add, Gt, Then
// and Otherwise strong by construction rather than by remembering. An Expr operand
// is used exactly as written, because the caller chose its type — so
// FillNullWith(Lit(0)) widens, and that is the escape hatch for anyone who wants
// the promotion.
func liftWeak[T Operand](v T) expr.Node {
	if e, ok := any(v).(Expr); ok {
		return e.node()
	}
	return weaken(litNode(any(v)))
}

// weaken marks a freshly built literal weak.
//
// Applied to litNode's RESULT rather than duplicated across its thirty cases,
// which covers the reflection path — a defined type like `type UserID int64` — for
// free, and leaves an error node from either path alone.
func weaken(n expr.Node) expr.Node {
	l, ok := n.(*expr.Lit)
	if !ok {
		return n
	}
	c := *l
	c.Weak = true
	return &c
}

// FillNullWith replaces nulls with a value.
//
// # It does not widen the column
//
//	Col("i32").FillNullWith(0)     // stays Int32
//	Coalesce(Col("i32"), Lit(0))   // becomes Int64
//
// A Go scalar lifts to a WEAK literal, which adopts the column's own type when the
// value fits it exactly. That is deliberately not what arithmetic does:
// Col("i32").Add(1) still widens, because addition can overflow and filling a null
// cannot. An Expr operand is used as written, so FillNullWith(Lit(0)) widens.
//
// If the value does not fit — FillNullWith(int64(5000)) on an Int8 column — the
// type falls back to ordinary promotion rather than truncating, so the result is
// an Int64 column holding 5000 rather than an Int8 column holding -120.
//
// # Cost
//
// This desugars to a conditional, so the receiver is MENTIONED twice and, with no
// common-subexpression pass, EVALUATED twice — exactly as Coalesce is.
func (e Expr) FillNullWith[T Operand](v T) Expr {
	// A FillStrategy is an unsigned integer, so it is an Operand too; taken for its
	// value it filled with its number (step 249).
	if s, ok := any(v).(FillStrategy); ok {
		return e.FillNull(s)
	}
	return wrap(&expr.Cond{
		Pred:  &expr.Unary{Op: expr.OpIsNotNull, Child: e.node()},
		Then:  e.node(),
		Else:  liftWeak(v),
		Sugar: "fill_null",
	})
}

// FillNan replaces NaN with a value, leaving nulls alone.
//
// # Nulls survive, and the reason is worth knowing
//
// `e.IsNotNan()` on a null row is NULL, not true — null and NaN are different
// things all the way down — and kernel.Select makes a null mask take NEITHER
// branch. So a null row keeps its null and the fill value never lands on it. That
// is exactly right, and it looks accidental, which is why TestFillNanLeavesNullsAlone
// exists.
//
// # Why the predicate is IsNotNan rather than IsNan
//
// Both spell the same thing, and only one names the result correctly. OutputName
// takes the LEFTMOST column of an expression, so with `IsNan → Then: value,
// Else: e` the leftmost column reference is the literal and every filled column
// came back called "literal" — which for the frame-level form meant a new column
// instead of a replaced one.
func (e Expr) FillNan[T Operand](v T) Expr {
	return wrap(&expr.Cond{
		Pred:  &expr.Unary{Op: expr.OpIsNotNan, Child: e.node()},
		Then:  e.node(),
		Else:  liftWeak(v),
		Sugar: "fill_nan",
	})
}

// Clip bounds each value to [lo, hi].
//
// # Sugar, and that is what lets the bounds be expressions
//
//	When(e.Lt(lo)).Then(lo).When(e.Gt(hi)).Then(hi).Otherwise(e)
//
// A parameterised Call could not do this: CallArgs requires every argument to be a
// literal and rejects a column-valued one by name. As a conditional, Clip takes
// Expr bounds naturally — Col("x").Clip(Col("floor"), Col("cap")) works.
//
// # The bounds are WEAK literals, like a fill value
//
// So Col("i32").Clip(0, 100) stays Int32. It used to become Int64, because the
// When/Then builder lifts strongly — and Col("u64").Clip(0, 100) became Int128,
// which is a startling type for clipping an unsigned column to a small range.
// That is exactly the inconsistency weak literals were built to remove: both
// spellings look like "a Go scalar bound on a column", and only one of them
// preserved the type. A bound that does not fit falls back to ordinary promotion,
// the same as a fill value that does not fit.
//
// An Expr bound is used as written, so Clip(Lit(0), Lit(100)) still widens.
//
// # Edge cases, as Polars answers them
//
//	e is null       → null
//	e is NaN        → NaN < lo and NaN > hi are both false → e, unchanged
//	a bound is null → no bound on that side, for that row (step 283)
//	lo > hi         → lo wins, because the chain is right-nested in written order
//
// A null bound was a null result until step 283; it is no bound now, as Polars'
// None is. Each comparison's null is taken as false, so a row whose bound is null
// passes that side unchanged. ClipLower and ClipUpper bound one side alone.
func (e Expr) Clip[L, H Operand](lo L, hi H) Expr { return e.clip(liftWeak(lo), liftWeak(hi)) }

// ClipLower bounds each value below by lo, and ClipUpper above by hi: Polars' clip
// with one bound. Each is Clip's sugar, its bound weak as Clip's are.
func (e Expr) ClipLower[L Operand](lo L) Expr { return e.clip(liftWeak(lo), nil) }
func (e Expr) ClipUpper[H Operand](hi H) Expr { return e.clip(nil, liftWeak(hi)) }

// clip is the chain, a bound nil for none. Built directly rather than through
// When/Then, because that builder lifts its values strongly and there is
// deliberately no weak spelling of it — Otherwise(0) must keep widening. The shape is
// a right-nested pair of Conds in written order.
func (e Expr) clip(l, h expr.Node) Expr {
	past := func(op expr.BinaryOp, bound expr.Node) expr.Node {
		return wrap(&expr.Binary{Op: op, L: e.node(), R: bound}).FillNullWith(false).node()
	}
	out := e.node()
	if h != nil {
		out = &expr.Cond{Pred: past(expr.OpGt, h), Then: h, Else: out}
	}
	if l != nil {
		out = &expr.Cond{Pred: past(expr.OpLt, l), Then: l, Else: out}
	}
	return wrap(out)
}

// IsClose reports whether each value is within a relative or absolute tolerance of
// other.
//
//	|a - b| <= max(relTol * max(|a|, |b|), absTol)
//
// # Both guards are load-bearing, and neither is obvious
//
// The EQUALITY disjunct is what makes IsClose(Inf, Inf) true: the tolerance term
// alone would compute |Inf - Inf|, which is NaN, and NaN <= x is false.
//
// The FINITENESS conjunct is what makes IsClose(Inf, -Inf) false. Without it the
// relative tolerance scales by max(|a|, |b|) = Inf, the limit becomes Inf, and
// Inf <= Inf says "close" — which it plainly is not. Python's math.isclose has the
// same special case for the same reason.
//
// Together they give: equal infinities are close, opposite ones are not, and
// IsClose(NaN, NaN) is false because IEEE says NaN equals nothing including
// itself. With a null operand every term is null and Kleene OR gives null,
// matching every other comparison.
//
// # It computes in Float64
//
// Both operands are cast first, so an integer column works — integers are always
// finite — and the finiteness test, which is float-only, has something to test.
//
// # Cost
//
// The receiver is mentioned five times and other four, with no CSE. For two bare
// columns that is nine column reads; for expensive operands it is not free.
func (e Expr) IsClose(other Expr, relTol, absTol float64) Expr {
	a, b := e.Cast(Float64), other.Cast(Float64)
	ae, ao := a.Abs(), b.Abs()
	scale := When(ae.Gt(ao)).Then(ae).Otherwise(ao) // elementwise max
	tol := scale.Mul(relTol)
	limit := When(tol.Gt(absTol)).Then(tol).Otherwise(absTol)
	finite := a.IsFinite().And(b.IsFinite())
	return a.Eq(b).Or(finite.And(a.Sub(b).Abs().Le(limit)))
}

// Filter is the values where every predicate holds, Polars' Expr.filter. Its length
// is not the frame's, so it is an aggregate's input (step 251): a conditional
// aggregate, SQL's FILTER (WHERE ...).
//
//	Col("amount").Filter(Col("qty").Gt(1)).Sum()             // in Agg, Over, Rolling, or Select
//	Col("amount").Filter(Col("region").Eq("eu")).Len()       // the rows that pass
//
// The aggregate reads only the rows where the predicates are true, so Len counts
// them and First and Last pick among them, as Polars' do. A null predicate drops the
// row, as the frame's Filter does. A group none of whose rows pass aggregates no
// rows: its sum is null, as an all-null group's is here, where Polars gives 0.
//
// In Select it is a column of its own length, the values that pass (step 258):
// Select(Col("a").Filter(Col("b").Gt(0))). Anywhere else it is refused at planning,
// until a later step gives a column of another length a place; the frame's Filter
// keeps rows.
func (e Expr) Filter(preds ...Expr) Expr {
	if len(preds) == 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "filter", "Filter needs a predicate")})
	}
	pred := preds[0]
	for _, p := range preds[1:] {
		pred = pred.And(p)
	}
	return wrap(&expr.Filter{Child: e.node(), Pred: pred.node()})
}

// --- named fill strategies -------------------------------------------------------

// FillStrategy names a fill rule that needs no value at the call site.
//
// It is a root-package enum and never reaches the IR, so it is not a dedup key and
// carries none of the collision hazard the expression enums do.
//
// Its low byte is the rule; the bits above carry a limit, which Limit sets (step
// 301). So compare a strategy through kind, never with ==: FillForward.Limit(1) is
// not FillForward.
type FillStrategy uint64

const (
	// FillZero and FillOne fill with a constant. The literal is WEAK, so the
	// column's type is preserved rather than widened — which matters more here than
	// anywhere else, because the call site contains no literal at all to explain a
	// widening.
	FillZero FillStrategy = iota
	FillOne

	// FillForward and FillBackward carry the neighbouring value. Both are ordered
	// window functions, so both are pipeline breakers.
	FillForward
	FillBackward

	// FillMin, FillMax and FillMean fill with the column's own aggregate over the
	// WHOLE FRAME. That makes them pipeline breakers too: the window sink holds
	// every row, and past a memory limit writes them to one file and reads them
	// back beside the aggregate (step 197).
	//
	// FillMean returns a FLOAT even for an integer column, because a mean is a
	// float. FillMin and FillMax preserve the type.
	FillMin
	FillMax
	FillMean

	fillStrategyCount
)

var fillStrategyNames = [fillStrategyCount]string{
	FillZero: "zero", FillOne: "one",
	FillForward: "forward", FillBackward: "backward",
	FillMin: "min", FillMax: "max", FillMean: "mean",
}

// A strategy's bits above its rule: whether Limit was called, whether it was given
// what it cannot take, and the limit.
const (
	fillKindMask   FillStrategy = 0xff
	fillLimited    FillStrategy = 1 << 8
	fillLimitNoFwd FillStrategy = 1 << 9  // Limit of a rule that is not forward or backward
	fillLimitBelow FillStrategy = 1 << 10 // Limit of less than 1, or a second Limit
	fillLimitShift              = 11
)

func (s FillStrategy) String() string {
	k := s.kind()
	name := "?"
	if int(k) < len(fillStrategyNames) && fillStrategyNames[k] != "" {
		name = fillStrategyNames[k]
	}
	if n, ok := s.limit(); ok {
		return name + "(limit=" + strconv.FormatUint(uint64(n), 10) + ")"
	}
	return name
}

// Limit caps how many consecutive nulls one value fills: FillForward.Limit(1) fills
// the null after a value and no more, as Polars' fill_null(strategy="forward",
// limit=1). It is for FillForward and FillBackward, as Polars' limit is; another
// strategy's is refused when the fill is used, as Polars refuses it.
//
// n is at least 1. Polars' limit=0 fills nothing; here it is refused, because
// ForwardFill(0) and BackwardFill(0) mean no limit at all, and a strategy whose 0
// meant the opposite would be a trap. Leave the limit off for none.
func (s FillStrategy) Limit(n int) FillStrategy {
	k := s.kind()
	switch {
	case s&fillLimited != 0 || n < 1:
		return k | fillLimited | fillLimitBelow
	case k != FillForward && k != FillBackward:
		return k | fillLimited | fillLimitNoFwd
	}
	return k | fillLimited | FillStrategy(n)<<fillLimitShift
}

// kind is the strategy's rule, its limit set aside.
func (s FillStrategy) kind() FillStrategy { return s & fillKindMask }

// limit is the strategy's limit, and whether it has one.
func (s FillStrategy) limit() (int, bool) {
	if s&fillLimited == 0 || s&(fillLimitNoFwd|fillLimitBelow) != 0 {
		return 0, false
	}
	return int(s >> fillLimitShift), true
}

// limitErr is the error of a Limit a strategy cannot take, or nil.
func (s FillStrategy) limitErr() *uerr.Error {
	switch {
	case s&fillLimitNoFwd != 0:
		return uerr.New(uerr.KindValue, "fill_null",
			"a limit is for FillForward and FillBackward, not %s", s.kind()).
			Hint("as Polars' fill_null takes limit only with strategy forward or backward")
	case s&fillLimitBelow != 0:
		return uerr.New(uerr.KindValue, "fill_null",
			"a strategy's limit is at least 1, and is set once").
			Hint("leave the limit off for no limit; Polars' limit=0 fills nothing, which is no fill")
	}
	return nil
}

// FillNull replaces nulls according to a named strategy.
//
//	Col("temp").FillNull(ursus.FillForward)
//	Col("qty").FillNull(ursus.FillZero)
//
// A forward or backward fill takes a limit, FillForward.Limit(1), as ForwardFill(1)
// does.
//
// # Three of these buffer the whole frame
//
// FillMin, FillMax and FillMean compute an aggregate over every row before they
// can fill anything, so they turn a streaming query into one that holds its input.
// Inside .Over(g) the aggregate is each group's, as Polars computes it.
// FillForward and FillBackward are ordered windows and buffer for the same reason.
// Only FillZero and FillOne stream.
func (e Expr) FillNull(s FillStrategy) Expr {
	if err := s.limitErr(); err != nil {
		return wrap(&expr.Err{E: err})
	}
	n, _ := s.limit() // 0 is no limit, as ForwardFill(0) takes it
	switch s.kind() {
	case FillZero:
		return e.FillNullWith(int64(0))
	case FillOne:
		return e.FillNullWith(int64(1))
	case FillForward:
		return e.ForwardFill(n)
	case FillBackward:
		return e.BackwardFill(n)
	case FillMin:
		return sugared(coalesceNow(e, inheritedOver(e.Min())), "fill_null")
	case FillMax:
		return sugared(coalesceNow(e, inheritedOver(e.Max())), "fill_null")
	case FillMean:
		return sugared(coalesceNow(e, inheritedOver(e.Mean())), "fill_null")
	default:
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "fill_null",
			"unknown fill strategy %s", s)})
	}
}

// Explode is a List's elements, a column of their number (step 259): Polars'
// explode. An empty or null list is one null, as the frame-level lf.Explode makes
// it a null row.
//
// In Select it is the frame-level Explode of its one column; in an aggregation each
// group's lists end to end, and Col("tags").Explode().Sum() their sum. A column that
// is not a List is refused, where Polars returns it as it was.
func (e Expr) Explode() Expr { return e.reshape(expr.ReshapeExplode) }

// Unnest is a Struct column's fields, a column each named after its field: Polars'
// struct.unnest() (step 260). It denotes several columns, as Col("a", "b") does, and
// is written out when the query is planned:
//
//	lf.Select(Col("person").Unnest(), Col("id"))     // age, city, id
//	lf.WithColumns(Col("person").Unnest())            // the frame, then age and city
//	lf.Select(Col("point").Unnest().Mul(2))           // each field doubled
//
// The frame-level lf.Unnest replaces the struct in place; this keeps it where
// WithColumns keeps the frame's columns. A null struct's fields are null. An Alias
// names every field alike, and so is refused for a struct of two fields or more.
//
// The single-field form is `Col("person").Struct().Field("age")`.
func (e Expr) Unnest() Expr { return wrap(&expr.Unnest{Child: e.node()}) }

// inheritedOver is e.Over() for a sugar: over the whole frame alone, and over an
// enclosing Over's partitions inside one (expr.Window.Inherits).
func inheritedOver(e Expr) Expr {
	return wrap(&expr.Window{Child: e.node(), Inherits: true})
}

// sugared names the method a conditional was built for, so its refusal does; see
// expr.Cond.Sugar.
func sugared(e Expr, method string) Expr {
	if c, ok := e.node().(*expr.Cond); ok {
		cp := *c
		cp.Sugar = method
		return wrap(&cp)
	}
	return e
}
