package plan

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// collapseCrossJoin folds a filter above a cross join into a real join condition.
//
// This is what makes JoinWhere worth having. `JoinWhere(preds...)` builds exactly
// `Join(JoinCross).Filter(preds...)` — the two are the same query by definition,
// and join_test.go's TestInnerJoinIsAFilteredCross already treats cross-plus-filter
// as the reference a hash join is checked against. Left alone that plan is O(|L|x|R|)
// predicate evaluations. This rule turns the equality conjuncts into hash keys:
//
//	Filter(k == k_right AND ts >= start)        Filter(ts >= start)
//	  over Join(Cross)                     ->     over Join(Inner, on k, coalesce off)
//
// dataframe-features.md §7.4 specifies exactly this — "`on` equality is optimizable
// into a hash join, the rest into a loop join" — and §11 lists "collapse joins (fold
// a filter above a cross join into a real join condition)" as a wanted pass.
//
// # It is a rule rather than a plan node, and that is the whole design
//
// A JoinWhere plan node would need an Expressions arm (without which liveness
// analysis goes blind under it — *Window went four steps that way), a projection
// pushdown arm, its own Layout, and a physical operator. As a rule it needs none of
// them: the output is Filter over Join, both of which already exist, so the parallel
// probe, the spilling join and batch-size invariance all apply untouched. It also
// improves hand-written cross-plus-filter, which is a shape already in the suite.
//
// # Why it must run AFTER predicate pushdown
//
// Pushdown has already moved every ONE-SIDED conjunct into the children — the Cross
// row of rule_predicate's legality table is `left only -> left`, `right only ->
// right`. So what remains above the cross join is the two-sided conjuncts, which is
// exactly the set this rule classifies. Running first would leave `l.x > 5` sitting
// here to be misread.
//
// A one-sided conjunct left here is not a correctness problem — see equiKeys for
// what the side check does and does not buy — but `Col("k") == Lit(5)` becoming a
// join key puts every right row in one hash bucket, which is a cross join with extra
// steps. Running after pushdown means the case is rare as well as harmless.
type collapseCrossJoin struct{}

func (collapseCrossJoin) Name() string { return "collapse_cross_join" }

func (collapseCrossJoin) Apply(n Node, _ Flags) (Node, bool, error) {
	changed := false
	out, err := TransformUp(n, func(x Node) (Node, error) {
		f, ok := x.(*Filter)
		if !ok {
			return x, nil
		}
		j, ok := f.Input.(*Join)
		if !ok || j.Kind != JoinCross {
			return x, nil
		}

		layout, err := j.Layout()
		if err != nil {
			return nil, err
		}
		ls, err := j.Left.Schema()
		if err != nil {
			return nil, err
		}
		rs, err := j.Right.Schema()
		if err != nil {
			return nil, err
		}

		var leftOn, rightOn, residual []expr.Node
		for _, p := range f.Preds {
			l, r, ok := equiKeys(p, layout, ls, rs)
			if !ok {
				residual = append(residual, p)
				continue
			}
			leftOn = append(leftOn, l)
			rightOn = append(rightOn, r)
		}
		if len(leftOn) == 0 {
			// A predicate with no equality is a loop join. The keyless probe already
			// streams its pairs, so leaving the plan alone costs nothing that a
			// rewrite would recover.
			return x, nil
		}

		c := *j
		c.Kind = JoinInner
		c.LeftOn, c.RightOn = leftOn, rightOn
		// COALESCE OFF, and this is the trap the rule would otherwise walk into.
		// coalescesKey is false for Cross and true for Inner when the two key names
		// match, so an inherited Auto would MERGE the key columns and the rewritten
		// plan would have one column fewer than the plan it replaced. A rule may not
		// change the schema; dropping a column is a worse violation than reordering
		// rows, because nothing downstream can even name what went missing.
		c.Coalesce = CoalesceOff
		// NullsEqual stays false, which is what makes the rewrite legal rather than
		// merely plausible: `l.k == r.k` over a cross product drops a null key,
		// because a comparison against null is null and Filter drops it, and an inner
		// join with NullsEqual false drops it too. Setting it true here would MATCH
		// null keys to each other and invent rows.

		// A key type that cannot promote would make the new join fail to lay out.
		// Bail rather than propagate: a missed optimisation is a cost, and turning a
		// query that ran into one that errors is a defect.
		if _, err := c.Layout(); err != nil {
			return x, nil
		}

		changed = true
		return refilter(&c, residual), nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

// equiKeys splits `left_expr == right_expr` into the two child-namespace keys, or
// reports that the conjunct is not an equi-join condition.
//
// # What the side check buys, which is not what it looks like
//
// The obvious story is that sideMask is a correctness guard against `Col("k") ==
// Lit(5)`: a literal belongs to any namespace, so rewriteForSide alone would accept
// it and produce a join whose right key is the constant 5. The teeth disproved that.
// Joining `l.k` against a constant right key is EXACTLY equivalent to filtering the
// left side on `k == 5` and keeping every right row — the rows agree, and so do the
// nulls, because a null left key matches nothing under either reading. Removing the
// side check does not change a single answer in the suite.
//
// What it actually buys is two things worth having anyway:
//
//   - A plan nobody wants. A constant key puts every right row in one hash bucket,
//     which is a cross join with a hash table in front of it.
//   - The MIRROR. `k_right == k` is the same join condition written the other way
//     round, and only a check that knows which side is which can swap the operands
//     rather than refuse the conjunct.
//
// Correctness against the genuinely bad cases — both operands left-only, both
// right-only — is rewriteForSide's, and it holds without any of this: a left column
// cannot be re-expressed in the right child's namespace, so those conjuncts are
// refused and stay in the residual filter where they belong.
func equiKeys(p expr.Node, layout *JoinLayout, ls, rs *dtype.Schema) (expr.Node, expr.Node, bool) {
	b, isBin := p.(*expr.Binary)
	if !isBin || b.Op != expr.OpEq {
		return nil, nil, false
	}

	lLeft, lRight, lOK := sideMask(b.L, layout)
	rLeft, rRight, rOK := sideMask(b.R, layout)
	if !lOK || !rOK {
		return nil, nil, false
	}

	var lo, ro expr.Node
	switch {
	case lLeft && !lRight && rRight && !rLeft:
		lo, ro = b.L, b.R
	case rLeft && !rRight && lRight && !lLeft:
		// `right_col == left_col`, which is the same join condition written the
		// other way round. Swapping here rather than refusing means the rule does
		// not depend on which order the user typed.
		lo, ro = b.R, b.L
	default:
		return nil, nil, false
	}

	lk, ok := rewriteForSide(lo, FromLeft, layout, ls, rs)
	if !ok {
		return nil, nil, false
	}
	rk, ok := rewriteForSide(ro, FromRight, layout, ls, rs)
	if !ok {
		return nil, nil, false
	}
	return lk, rk, true
}

// sideMask reports which of the join's inputs an output-namespace expression reads.
//
// known is false if any name is not an output column, which means the expression is
// not something this rule understands and the caller must leave it alone.
func sideMask(p expr.Node, layout *JoinLayout) (usesLeft, usesRight, known bool) {
	known = true
	for _, name := range expr.RootNames(p) {
		i := layout.Schema.IndexOf(name)
		if i < 0 {
			known = false
			continue
		}
		if layout.Columns[i].Side == FromLeft {
			usesLeft = true
		} else {
			usesRight = true
		}
	}
	return usesLeft, usesRight, known
}
