package ursus

import (
	"github.com/advenn/ursus/internal/expr"
)

// When begins a conditional.
//
//	ursus.When(ursus.Col("score").Ge(90)).Then("A").
//	    When(ursus.Col("score").Ge(80)).Then("B").
//	    Otherwise("F").Alias("grade")
//
// # Why a builder and not a function
//
// The alternative was a variadic Case(cond1, val1, cond2, val2, …, default). That
// reads compactly and pairs its arguments POSITIONALLY, so a miscounted list is a
// runtime error rather than a type error — and the compiler cannot tell a condition
// from a value when both are Expr. The builder makes each pair a method call, so
// there is nothing to miscount.
//
// # An unterminated chain cannot be used, by construction
//
// Only Otherwise returns an Expr; WhenBuilder and ThenBuilder are not expressions
// and satisfy nothing that takes one. So a chain that forgets its else branch fails
// to compile rather than silently defaulting. Where a null default IS wanted, say
// so: Otherwise(ursus.Null(ursus.NullT)).
//
// # Both branches are evaluated
//
// This is a columnar engine, so Then and Otherwise are each computed for every row
// and then merged. That is unobservable — expressions have no side effects, and an
// arithmetic fault such as division by zero yields NULL rather than trapping — but
// it does mean a conditional does not make an expensive branch cheaper.
func When(pred Expr) WhenBuilder {
	return WhenBuilder{pred: pred.n}
}

// WhenBuilder is a conditional awaiting its Then.
type WhenBuilder struct {
	pairs []condPair
	pred  expr.Node
}

// ThenBuilder is a conditional that may take another When or be closed by Otherwise.
type ThenBuilder struct {
	pairs []condPair
}

type condPair struct{ pred, then expr.Node }

// Then supplies the value for the pending condition. The value lifts, so
// Then("A") needs no Lit.
func (w WhenBuilder) Then[T Operand](v T) ThenBuilder {
	// Copied rather than appended in place: a builder value may be reused as the
	// base of two different chains, and a shared backing array would let the second
	// overwrite the first's pair. Expression trees are immutable for the same
	// reason.
	pairs := make([]condPair, len(w.pairs), len(w.pairs)+1)
	copy(pairs, w.pairs)
	return ThenBuilder{pairs: append(pairs, condPair{pred: w.pred, then: lift(v)})}
}

// When adds another condition, the else-if of the chain.
func (t ThenBuilder) When(pred Expr) WhenBuilder {
	return WhenBuilder{pairs: t.pairs, pred: pred.n}
}

// Otherwise supplies the fall-through value and closes the chain.
//
// The chain becomes a right-nested tree of conditionals, in the order written:
// when(c1).then(v1).when(c2).then(v2).otherwise(d) is
// Cond{c1, v1, Cond{c2, v2, d}}. Nesting rather than a flat list is what keeps type
// unification well-defined — promotion is pairwise, and folding a flat list would
// make the result depend on an association order the user never chose.
func (t ThenBuilder) Otherwise[T Operand](v T) Expr {
	out := lift(v)
	for i := len(t.pairs) - 1; i >= 0; i-- {
		out = &expr.Cond{Pred: t.pairs[i].pred, Then: t.pairs[i].then, Else: out}
	}
	return wrap(out)
}

// Coalesce returns the first non-null value across the given expressions.
//
// # Built out of conditionals rather than its own kernel
//
// Coalesce(a, b, c) becomes when(a.is_not_null()).then(a).otherwise(coalesce(b, c)),
// which is correct and costs one thing worth knowing: each operand is MENTIONED
// twice, and there is no common-subexpression pass, so each is EVALUATED twice. For
// the ordinary Coalesce(Col("a"), Col("b")) that is two extra column reads; for an
// operand that is itself an expensive expression it is not free.
//
// A dedicated n-ary null-merge kernel would remove that and change no semantics. It
// is deliberately not built yet: the desugaring needs no new IR node, no new kernel
// and no new arms in the five type switches a node type must reach.
func Coalesce(exprs ...Expr) Expr {
	switch len(exprs) {
	case 0:
		return wrap(&expr.Err{E: errNoCoalesceArgs()})
	case 1:
		return exprs[0]
	}
	out := exprs[len(exprs)-1].n
	for i := len(exprs) - 2; i >= 0; i-- {
		a := exprs[i].n
		out = &expr.Cond{
			Pred: &expr.Unary{Op: expr.OpIsNotNull, Child: a},
			Then: a,
			Else: out,
		}
	}
	return wrap(out)
}
