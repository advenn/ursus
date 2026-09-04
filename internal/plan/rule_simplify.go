package plan

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// ConstEvaluator folds an all-literal expression to a single literal.
//
// # Why this is an interface rather than a call into internal/kernel
//
// Folding needs a real evaluator, and there is exactly one correct one — the
// kernel the executor itself uses. Anything else is a second implementation of
// promotion, overflow and null semantics, and the whole point of folding is that
// the answer cannot differ from what the query would have computed.
//
// But internal/kernel is L30 and reaching it from here would drag Arrow, the
// allocator and GOEXPERIMENT=simd into a package that today imports none of them.
// design/logical.md §13 states what that buys: this layer's tests run "without an
// executor, without Arrow, and without a Parquet file", so a plan can be built,
// resolved, optimized and explained with no data anywhere. TestPlanPackageNeedsNoArrow
// asserts it rather than trusting it.
//
// So the evaluator is INJECTED, from a layer that already has Arrow. Nothing in
// this signature names an Arrow type, which is what makes that work: the rule
// below is written once and simplifies with or without a folder attached. Without
// one — which is what this package's own tests see — every structural rewrite
// still runs, and only Lit⊕Lit is skipped.
type ConstEvaluator interface {
	// Fold evaluates n, which is guaranteed to contain no column references, and
	// returns the equivalent literal.
	//
	// Returning (nil, false, nil) declines: an expression the folder does not
	// handle is left exactly as it was. An ERROR is different and must be
	// propagated as "do not fold" by the caller, never as a query failure — see
	// foldExpr.
	Fold(n expr.Node, in *dtype.Schema) (expr.Node, bool, error)
}

// simplify is the expression-simplification and node-removal rule.
//
// # It runs LAST, and Once
//
// Last, because the two pushdowns MAKE work for it that does not exist before
// they run:
//
//   - An identity Project only comes into being once projection pushdown has
//     narrowed the scan beneath it. `Select(Col("id"))` over a six-column source is
//     not a no-op when it is built; it becomes one the moment the scan below is
//     told to read `[id]` alone.
//   - Predicate pushdown REWRITES predicates as it moves them — rewriteForSide
//     rebuilds one in a child's namespace — so it produces newly-foldable
//     expressions a front pass could not have seen.
//
// Once, because these rewrites do enable each other but always DOWNWARD, and both
// walks are bottom-up. `lit(2) > lit(1)` folds to `lit(true)`, which lets
// `x AND lit(true)` become `x`, which may leave a Filter with a literal predicate,
// which removes the node — and every step of that chain is a parent consuming an
// already-rewritten child, so one sweep completes it. See the registration in
// optimize.go for the measurement.
//
// # The two bottom-up walks are not the same walk
//
// Local drives TransformUp over PLAN nodes; simplify.walk drives its own
// traversal over EXPRESSIONS. Only the second is load-bearing for the rewrites
// here, because the interesting nesting — four negations, a chain of ANDs — lives
// inside one node's expression list. Making Local top-down changes nothing that
// any test can see; making walk top-down would leave collapsed negations behind.
//
// # What it deliberately does not touch
//
// Only Project, Filter, WithColumns and Aggregate expression slots. Sort keys and
// join keys hold columns in practice, and a rule that rewrote them would need the
// same care for no observable gain. Recorded rather than assumed: adding a slot
// here is a two-line change if a query ever wants it.
type simplify struct{ fold *constFolder }

func (simplify) Name() string { return "simplify_exprs" }

func (s simplify) Match(n Node, _ Flags) (Node, bool, error) {
	in, ok := inputSchema(n)
	if !ok {
		// Unresolvable input is not this rule's business to report; the plan either
		// resolved earlier or the query is already failing for a better reason.
		return n, false, nil
	}

	switch t := n.(type) {
	case *Project:
		out, changed, err := s.rewriteAll(t.Exprs, in, true)
		if err != nil {
			return nil, false, err
		}
		if changed {
			c := *t
			c.Exprs = out
			t = &c
		}
		// Identity removal happens AFTER simplification, so a Project whose
		// expressions only become bare columns by folding is still caught.
		if isIdentityProject(t, in) {
			return t.Input, true, nil
		}
		return t, changed, nil

	case *WithColumns:
		out, changed, err := s.rewriteAll(t.Exprs, in, true)
		if err != nil || !changed {
			return n, false, err
		}
		c := *t
		c.Exprs = out
		return &c, true, nil

	case *Aggregate:
		keys, kc, err := s.rewriteAll(t.Keys, in, true)
		if err != nil {
			return nil, false, err
		}
		aggs, ac, err := s.rewriteAll(t.Aggs, in, true)
		if err != nil {
			return nil, false, err
		}
		if !kc && !ac {
			return n, false, nil
		}
		c := *t
		c.Keys, c.Aggs = keys, aggs
		return &c, true, nil

	case *Filter:
		// Names are irrelevant in a predicate: Filter's schema is its child's, and
		// nothing reads the name of the Boolean column it tests.
		out, changed, err := s.rewriteAll(t.Preds, in, false)
		if err != nil {
			return nil, false, err
		}
		return s.simplifyFilter(t, out, changed)
	}
	return n, false, nil
}

// simplifyFilter drops a filter that keeps everything and truncates one that
// keeps nothing.
//
// # The null predicate is the trap
//
// A Boolean predicate has THREE outcomes, and only one of them keeps a row.
// `lit(true)` keeps every row and the node can go. `lit(false)` keeps none. And
// `lit(null)` also keeps none — a null predicate is not true, which is the same
// rule TestNullSemantics pins for `discount > 0.2` over a null discount.
//
// Treating null like true, which is what "remove the node when the predicate is
// constant" would do, returns EVERY row instead of none. No schema check can see
// it: the schema is identical either way. It is the one silent wrong answer
// available in this rule, which is why the three cases are spelled out separately
// below rather than folded into a cleverer condition.
func (s simplify) simplifyFilter(t *Filter, preds []expr.Node, changed bool) (Node, bool, error) {
	keepsNothing := false
	kept := make([]expr.Node, 0, len(preds))
	for _, p := range preds {
		l, ok := p.(*expr.Lit)
		if !ok {
			kept = append(kept, p)
			continue
		}
		if v, isBool := l.Value.(bool); isBool && v {
			continue // lit(true) constrains nothing; drop this conjunct.
		}
		// lit(false), lit(null), and a literal of any other type — which resolution
		// would already have refused, so reaching here means it kept no rows.
		keepsNothing = true
	}

	if keepsNothing {
		// Limit{N: 0} rather than a new Empty node: rule_limit.go already calls it
		// "a legal, degenerate query" and notes that "the Limit operator above
		// already returns nothing at no cost". That same comment is why the rows
		// cannot be dropped by removing the Filter, and why limitPushdown refuses to
		// push an N of 0 — MaxRows and Sort.Limit use 0 for UNLIMITED, so pushing it
		// would say the exact opposite.
		return &Limit{Input: t.Input, N: 0}, true, nil
	}
	if len(kept) == 0 {
		// Every conjunct was lit(true).
		return t.Input, true, nil
	}
	if !changed && len(kept) == len(preds) {
		return t, false, nil
	}
	c := *t
	c.Preds = kept
	return &c, true, nil
}

// isIdentityProject reports whether p projects exactly its input, in order.
//
// design/logical.md:1557 names this as the LocalRule demonstration. The test is
// structural — bare *expr.Col, positionally matching the input schema — because
// `Select(Col("b"), Col("a"))` over `[a, b]` has the same FIELD SET as its input
// and is emphatically not a no-op.
//
// Honest about how much work that positional check does: weakening it to a set
// comparison breaks no test, because Local's sameSchema declines any rewrite that
// reorders the output anyway. It is written precisely here because saying what is
// meant is cheaper than relying on a backstop to catch what was not — but it is a
// clarification, not the guard.
func isIdentityProject(p *Project, in *dtype.Schema) bool {
	if len(p.Exprs) != in.Len() {
		return false
	}
	for i, e := range p.Exprs {
		c, ok := e.(*expr.Col)
		if !ok || c.Name != in.FieldSlice()[i].Name {
			return false
		}
	}
	return true
}

// rewriteAll simplifies each expression, keeping the originals on any refusal.
//
// namesMatter distinguishes an expression whose output name becomes a column name
// — a Project or WithColumns entry — from one whose name nothing reads. See
// fieldEqual and sameValue for the two guards and why they are checked at
// different levels.
func (s simplify) rewriteAll(es []expr.Node, in *dtype.Schema, namesMatter bool) ([]expr.Node, bool, error) {
	out := es
	changed := false
	for i, e := range es {
		got, ok, err := s.rewriteExpr(e, in, namesMatter)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		if !changed {
			// Copy on first write: the input slice belongs to the caller's node, and
			// plan nodes are shared between the optimized and unoptimized forms of
			// the same query — Explain(Optimized(false)) renders the original.
			out = make([]expr.Node, len(es))
			copy(out, es)
			changed = true
		}
		out[i] = got
	}
	return out, changed, nil
}

// rewriteExpr simplifies one expression bottom-up.
//
// The top-level name check happens HERE and once, rather than at every node,
// because an interior node's name is not an output name — it is an input to
// OutputName's leftmost-column-wins search, and whether that search's answer moved
// is a question only the root can answer.
func (s simplify) rewriteExpr(e expr.Node, in *dtype.Schema, namesMatter bool) (expr.Node, bool, error) {
	got, changed, err := s.walk(e, in)
	if err != nil || !changed {
		return e, false, err
	}
	if namesMatter && !fieldEqual(e, got, in) {
		return e, false, nil
	}
	return got, true, nil
}

// walk rewrites bottom-up: children first, then this node.
func (s simplify) walk(e expr.Node, in *dtype.Schema) (expr.Node, bool, error) {
	kids := e.Children()
	changed := false
	if len(kids) > 0 {
		next := make([]expr.Node, len(kids))
		for i, k := range kids {
			got, ok, err := s.walk(k, in)
			if err != nil {
				return nil, false, err
			}
			next[i] = got
			changed = changed || ok
		}
		if changed {
			e = expr.Rebuild(e, next)
		}
	}

	got, ok, err := s.node(e, in)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return e, changed, nil
	}
	// sameValue and not fieldEqual: see sameValue's doc. The name is checked once,
	// at the root, by rewriteExpr.
	if !sameValue(e, got, in) {
		return e, changed, nil
	}
	return got, true, nil
}

// node applies every rewrite that looks at a single node.
func (s simplify) node(e expr.Node, in *dtype.Schema) (expr.Node, bool, error) {
	if got, ok, err := s.foldExpr(e, in); ok || err != nil {
		return got, ok, err
	}

	switch t := e.(type) {
	case *expr.Unary:
		// Not(Not(x)) -> x. Kleene-safe: not(not(null)) is null.
		//
		// The guard matters here more than anywhere: the doubled negation is named
		// by leftmost-column-wins while the bare child may carry an Alias, so
		// Not(Not(Col("a").Alias("b"))) is named "a" and its child "b".
		if t.Op == expr.OpNot {
			if inner, ok := t.Child.(*expr.Unary); ok && inner.Op == expr.OpNot {
				return inner.Child, true, nil
			}
		}

	case *expr.Binary:
		if got, ok := boolIdentity(t); ok {
			return got, true, nil
		}
	}
	return e, false, nil
}

// boolIdentity applies the AND/OR identities, each sound under Kleene logic.
//
//	x AND true  = x        null AND true  = null, which is what x already is
//	x OR  false = x        mirror
//	x AND false = false    null AND false = false — the null does NOT survive
//	x OR  true  = true     mirror
//
// The bottom two are where the value semantics and the SCHEMA part company, and
// the caller's sameValue guard is what reconciles them: `x AND false` really is
// false for every x, but Binary.Field has no absorbing-element case, so the
// rewrite is accepted only when x is already non-nullable and refused otherwise.
// Making it unconditional would need Binary.Field to model absorption, which is a
// change to the null contract rather than to this rule.
func boolIdentity(b *expr.Binary) (expr.Node, bool) {
	if b.Op != expr.OpAnd && b.Op != expr.OpOr {
		return nil, false
	}
	// Both operands are tried, so the identity fires whichever side the literal is
	// on: AND and OR are commutative, including on nulls.
	for _, side := range [2][2]expr.Node{{b.L, b.R}, {b.R, b.L}} {
		lit, other := side[0], side[1]
		l, ok := lit.(*expr.Lit)
		if !ok {
			continue
		}
		v, isBool := l.Value.(bool)
		if !isBool {
			// A null Boolean literal is NOT an identity element: `x AND null` is
			// null when x is true and false when x is false, so it depends on x.
			continue
		}
		switch {
		case b.Op == expr.OpAnd && v, b.Op == expr.OpOr && !v:
			return other, true // x AND true, x OR false
		case b.Op == expr.OpAnd && !v, b.Op == expr.OpOr && v:
			return l, true // x AND false, x OR true
		}
	}
	return nil, false
}

// foldExpr evaluates an all-literal expression through the real kernel.
//
// # Two refusals
//
// No evaluator means no folding — the injected-evaluator design, not a failure.
//
// And a kernel ERROR means no folding either. Folding moves the failure from
// Collect to PLAN TIME, which makes `Explain` fail for a query that has not been
// run: `Lit(300).Cast(Int8)` is a strict cast that cannot represent its value,
// and asking to see the plan of a doomed query should still show you the plan.
// On any error the node is left exactly as it was and the failure happens where
// it always did.
//
// Note which example is NOT here. Division by zero looks like the obvious case
// and is not one: float division by zero is +Inf and integer division by zero is
// a null, both of them the kernel's documented answers rather than errors. The
// first folds — reproducing what the executor would compute is the whole job —
// and the second is declined by the nullability guard instead, because
// Binary.Field says two non-null operands give a non-null result.
func (s simplify) foldExpr(e expr.Node, in *dtype.Schema) (expr.Node, bool, error) {
	if s.fold == nil || s.fold.e == nil || !foldable(e) {
		return e, false, nil
	}
	got, ok, err := s.fold.e.Fold(e, in)
	if err != nil || !ok || got == nil {
		return e, false, nil
	}
	return got, true, nil
}

// foldable reports whether e can be evaluated with no input data.
//
// The set is deliberately narrow — literals under arithmetic, comparison, logic
// and casts. Not Cond, whose untaken branch is the reason the error guard above
// exists and which is better served by leaving it alone; not Call, whose members
// range from pure string functions to regexes with their own error paths; not Agg
// or Window, which are not expressions over one row at all.
func foldable(e expr.Node) bool {
	switch t := e.(type) {
	case *expr.Lit:
		// Already folded. Returning false here is what stops the UntilStable loop
		// from reporting "changed" forever on a plan that is already constant.
		return false
	case *expr.Binary:
		return foldableOperand(t.L) && foldableOperand(t.R)
	case *expr.Unary:
		return foldableOperand(t.Child)
	case *expr.Cast:
		return foldableOperand(t.Child)
	}
	return false
}

func foldableOperand(e expr.Node) bool {
	if _, ok := e.(*expr.Lit); ok {
		return true
	}
	return foldable(e)
}

// inputSchema resolves a single-input node's child schema. The false return is
// "no schema to resolve against", which every caller treats as "leave this node
// alone" rather than as a failure.
func inputSchema(n Node) (*dtype.Schema, bool) {
	kids := n.Children()
	if len(kids) != 1 {
		return nil, false
	}
	s, err := kids[0].Schema()
	if err != nil {
		return nil, false
	}
	return s, true
}
