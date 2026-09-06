package ursus

import (
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
		return e.n
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
	return wrap(&expr.Cond{
		Pred: &expr.Unary{Op: expr.OpIsNotNull, Child: e.n},
		Then: e.n,
		Else: liftWeak(v),
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
		Pred: &expr.Unary{Op: expr.OpIsNotNan, Child: e.n},
		Then: e.n,
		Else: liftWeak(v),
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
// # Edge cases, each of which falls out rather than being coded
//
//	e is null      → the comparisons are null → neither branch → null
//	e is NaN       → NaN < lo and NaN > hi are both false → e, unchanged
//	a bound is null → every row's mask is null → the whole column is null
//	lo > hi        → lo wins, because the chain is right-nested in written order
//
// The third is the one to watch: a null bound is not "no bound", it is a null
// result. Use two separate clips if only one side should apply.
func (e Expr) Clip[L, H Operand](lo L, hi H) Expr {
	// Built directly rather than through When/Then, because that builder lifts its
	// values strongly and there is deliberately no weak spelling of it — Otherwise(0)
	// must keep widening. The shape is identical: a right-nested pair of Conds in
	// written order.
	l, h := liftWeak(lo), liftWeak(hi)
	return wrap(&expr.Cond{
		Pred: &expr.Binary{Op: expr.OpLt, L: e.n, R: l},
		Then: l,
		Else: &expr.Cond{
			Pred: &expr.Binary{Op: expr.OpGt, L: e.n, R: h},
			Then: h,
			Else: e.n,
		},
	})
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

// DropNulls is not an expression. It is refused here so the mistake is caught at
// build time with a message naming the operation that does exist.
//
// # Why there is no expression form
//
// Every expression must produce exactly one value per input row — the evaluator
// enforces it — and dropping rows changes the frame's height. There is no
// Expr.Filter, Expr.Slice or Expr.Gather family to hang a height-changing
// expression on, so this is not a missing case but a different shape of thing.
//
// Refused at BUILD time rather than at plan time on purpose: the deferred error is
// the first thing expression resolution looks at, so CollectSchema and Explain
// both fail too. A refusal that only Collect notices — which is what happened to
// one of the window mapping strategies — lets Explain print a plan that cannot run.
func (e Expr) DropNulls() Expr {
	return wrap(&expr.Err{E: uerr.New(uerr.KindUnsupported, "drop_nulls",
		"DropNulls is not an expression; it changes the frame's height").
		Hint("use the frame-level form: lf.DropNulls(\"x\"), or lf.DropNulls() for " +
			"every column").
		Hint("an expression produces one value per input row, so it cannot remove rows")})
}

// DropNans is refused for the same reason as DropNulls, and its hint carries the
// asymmetry that keeps a frame-level DropNans from being written the obvious way.
func (e Expr) DropNans() Expr {
	return wrap(&expr.Err{E: uerr.New(uerr.KindUnsupported, "drop_nans",
		"DropNans is not an expression; it changes the frame's height").
		Hint("use Filter(Col(\"x\").IsNotNan().Or(Col(\"x\").IsNull()))").
		Hint("the .Or is required: IsNotNan is NULL on a null row and Filter drops " +
			"null predicates, so the bare spelling would silently drop nulls too")})
}

// --- named fill strategies -------------------------------------------------------

// FillStrategy names a fill rule that needs no value at the call site.
//
// It is a root-package enum and never reaches the IR, so it is not a dedup key and
// carries none of the collision hazard the expression enums do.
type FillStrategy uint8

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
	// WHOLE FRAME. That makes them pipeline breakers too, and worse ones: the
	// window sink holds every row, and it is one of the operators that is accounted
	// and refused under a memory limit rather than spilled.
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

func (s FillStrategy) String() string {
	if int(s) < len(fillStrategyNames) && fillStrategyNames[s] != "" {
		return fillStrategyNames[s]
	}
	return "?"
}

// FillNull replaces nulls according to a named strategy.
//
//	Col("temp").FillNull(ursus.FillForward)
//	Col("qty").FillNull(ursus.FillZero)
//
// For a forward or backward fill with a limit, use ForwardFill(n) or
// BackwardFill(n) directly — the strategy form is unlimited.
//
// # Three of these buffer the whole frame
//
// FillMin, FillMax and FillMean compute an aggregate over every row before they
// can fill anything, so they turn a streaming query into one that holds its input.
// FillForward and FillBackward are ordered windows and buffer for the same reason.
// Only FillZero and FillOne stream.
func (e Expr) FillNull(s FillStrategy) Expr {
	switch s {
	case FillZero:
		return e.FillNullWith(int64(0))
	case FillOne:
		return e.FillNullWith(int64(1))
	case FillForward:
		return e.ForwardFill(0)
	case FillBackward:
		return e.BackwardFill(0)
	case FillMin:
		return Coalesce(e, e.Min().Over())
	case FillMax:
		return Coalesce(e, e.Max().Over())
	case FillMean:
		return Coalesce(e, e.Mean().Over())
	default:
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "fill_null",
			"unknown fill strategy %s", s)})
	}
}

// Explode is refused as an expression for the reason DropNulls is: it changes the
// frame's height, and an expression produces one value per input row.
//
// It is the same refusal rather than a new one on purpose. A caller who reaches
// for `Col("tags").Explode()` has made the height mistake, not a list mistake,
// and the answer is the same shape as it is for DropNulls.
func (e Expr) Explode() Expr {
	return wrap(&expr.Err{E: uerr.New(uerr.KindUnsupported, "explode",
		"Explode is not an expression; it changes the frame's height").
		Hint("use the frame-level form: lf.Explode(\"tags\")").
		Hint("an expression produces one value per input row, so it cannot add rows")})
}

// Unnest is refused as an expression, and for a DIFFERENT reason than Explode.
//
// Explode is not an expression because it changes the frame's HEIGHT. Unnest leaves
// the height alone and changes the WIDTH: an expression produces exactly one column,
// and this produces one per field. Saying which of the two rules was broken is the
// difference between a message that teaches and one that just refuses.
//
// The single-column form exists and is spelled `Col("person").Struct().Field("age")`.
func (e Expr) Unnest() Expr {
	return wrap(&expr.Err{E: uerr.New(uerr.KindUnsupported, "unnest",
		"Unnest is not an expression; it produces one column per struct field").
		Hint("use the frame-level form: lf.Unnest(\"person\")").
		Hint("for a single field, use Col(\"person\").Struct().Field(\"age\")")})
}
