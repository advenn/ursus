package plan

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// predicatePushdown moves filters as close to the scan as legality allows.
//
// # This rule can silently change results, so legality is the specification
//
// Every other optimization here is a performance question. This one is a
// correctness question wearing a performance costume: a filter moved past the
// wrong node returns different ROWS, with no error, and the difference usually
// only shows up on data larger than a test fixture.
//
//	Scan               offer each conjunct to the source; Exact conjuncts move in,
//	                   Inexact ones move in AND stay above, the rest stay above
//	Filter             merge (conjoin) and keep descending
//	Project            push WITH SUBSTITUTION of output name -> defining expression
//	WithColumns        same
//	Sort               push freely: reordering rows cannot change which rows survive
//	Distinct(*)        push freely
//	Distinct(subset)   push only if the predicate reads nothing outside the subset
//	Aggregate          BARRIER (see below)
//	Limit / Head       BARRIER (see below)
//	anything unknown   BARRIER — fail closed
//
// # Limit is a hard barrier, and this is the classic bug
//
//	Filter(Limit(2, [1,2,3,4,5]), x > 3)  ==  []
//	Limit(2, Filter([1,2,3,4,5], x > 3))  ==  [4,5]
//
// Not a different order — a different CARDINALITY and different rows. Limit is not
// row-local: which rows survive depends on how many precede them, so removing
// earlier rows promotes later ones into the window. The rewrite is correct on
// every dataset small enough that the limit never binds, which is exactly why it
// passes unit tests and fails in production.
//
// # Aggregate is a barrier here, though it need not be forever
//
// A predicate on a GROUP KEY could legally move below (with substitution), and a
// predicate on an aggregate OUTPUT is HAVING and never can. Telling them apart
// mechanically is a real piece of work, and getting it wrong computes the
// aggregate over a filtered subset — silent, plausible-looking numbers. Until that
// is built, both stay above.
//
// # Fail closed
//
// The default case pushes NOTHING through a node type it does not recognise. A
// rule that fails open means every future node type silently inherits
// "predicates may pass", which is how this class of bug gets introduced by someone
// who never read this file.
type predicatePushdown struct{}

func (predicatePushdown) Name() string { return "predicate_pushdown" }

func (p predicatePushdown) Apply(n Node, flags Flags) (Node, bool, error) {
	out, err := push(n, nil, flags)
	if err != nil {
		return nil, false, err
	}
	// Comparing rendered forms is a cheap, exact "did anything move" check, and it
	// avoids threading a changed flag through every branch below.
	before, err := Explain(n, ExplainOptions{})
	if err != nil {
		return nil, false, err
	}
	after, err := Explain(out, ExplainOptions{})
	if err != nil {
		return nil, false, err
	}
	return out, before != after, nil
}

// push returns a plan equivalent to Filter(n, preds), having moved as many
// predicates as legally possible below n.
func push(n Node, preds []expr.Node, flags Flags) (Node, error) {
	switch t := n.(type) {
	case *Scan:
		return pushIntoScan(t, preds)

	case *Filter:
		// Conjoin and keep going. The Filter node itself disappears; whatever
		// cannot descend further is re-emitted at the deepest legal point.
		return push(t.Input, append(append([]expr.Node(nil), preds...), t.Preds...), flags)

	case *Sort, *Reverse:
		// Filtering and reordering commute: both of these permute rows, and a
		// row-local predicate does not care about order.
		//
		// Reverse was a barrier until step 15 for no reason at all — it does no
		// comparison, so it is strictly weaker than Sort, to which this same sentence
		// already applied. It is worth naming the four nodes that must NOT join it:
		// Slice and Tail depend on how many rows precede them, RowIndex renumbers
		// every row, and HStack pairs its children by position — for all four, moving
		// a filter below changes which rows come out.
		child, err := push(t.Children()[0], preds, flags)
		if err != nil {
			return nil, err
		}
		return t.WithChildren([]Node{child}), nil

	case *Project:
		return pushThroughProjection(t, t.Exprs, preds, false, flags)

	case *WithColumns:
		return pushThroughProjection(t, t.Exprs, preds, true, flags)

	case *Distinct:
		return pushThroughDistinct(t, preds, flags)

	case *Join:
		if !flags.JoinPredicatePushdown {
			// The flag is off: fall back to the fail-closed default, which is what a
			// Join got before the arm below existed.
			return pushClosed(t, preds, flags)
		}
		return pushThroughJoin(t, preds, flags)

	case *Union:
		// A predicate pushes into EVERY child, and this is the one n-ary case where
		// that is unconditionally safe: a concat assigns each output row to exactly
		// one input, so filtering the inputs and stacking gives the same rows as
		// stacking and filtering. There is no null-extension to reason about, which
		// is what makes the join's four-cell legality table unnecessary here.
		//
		// By this point resolveUnion has given every child the full output schema,
		// so a predicate naming a column that only some input originally had still
		// resolves against all of them.
		kids := make([]Node, len(t.Inputs))
		for i, c := range t.Inputs {
			nk, err := push(c, preds, flags)
			if err != nil {
				return nil, err
			}
			kids[i] = nk
		}
		return t.WithChildren(kids), nil

	case *MergeSorted:
		// A row-local predicate commutes with an interleave: it removes the same rows
		// from the same runs whichever side of the merge it runs on, and both runs
		// stay sorted. Descend into both.
		l, err := push(t.Left, preds, flags)
		if err != nil {
			return nil, err
		}
		r, err := push(t.Right, preds, flags)
		if err != nil {
			return nil, err
		}
		return t.WithChildren([]Node{l, r}), nil

	case *AsOfJoin:
		// LEFT-ONLY predicates push; everything else is a barrier, which is JoinLeft's
		// row in the table above with one extra reason. Removing a RIGHT row does not
		// merely delete matches: it changes which row is NEAREST, so a left row that
		// matched quote A silently re-points at quote B. That is a different answer,
		// not a missing one, and no schema check can see it.
		return pushClosed(t, preds, flags)

	case *Limit, *Aggregate, *Window, *TemporalGroup:
		// BARRIERS. Descend to optimize below them, but leave every predicate above.
		//
		// Window is listed EXPLICITLY although the fail-closed default below would
		// treat it identically — for a single-child node the two are the same code.
		// It is here to state the reason, because the reason is not obvious and the
		// default is silent: pushing a filter below a window removes rows from the
		// PARTITIONS, so every window value changes. `mean over g` computed after
		// dropping half the rows is a different number, and nothing downstream could
		// detect the substitution.
		//
		// A predicate on a partition KEY is genuinely safe — it removes whole
		// partitions rather than shrinking them — and would be a real optimisation.
		// It is not done, and listing the node here is what makes that a visible
		// choice rather than an omission.
		child, err := push(t.Children()[0], nil, flags)
		if err != nil {
			return nil, err
		}
		return refilter(t.WithChildren([]Node{child}), preds), nil

	default:
		return pushClosed(n, preds, flags)
	}
}

// pushClosed is the fail-closed treatment: optimize every child independently
// with an EMPTY predicate set, and re-apply everything above the node.
//
// It is both the default arm for a node type this rule does not recognise and the
// explicit fallback when JoinPredicatePushdown is off, so the two are the same
// code rather than two things that happen to agree.
func pushClosed(n Node, preds []expr.Node, flags Flags) (Node, error) {
	kids := n.Children()
	if len(kids) == 0 {
		return refilter(n, preds), nil
	}
	newKids := make([]Node, len(kids))
	for i, c := range kids {
		nk, err := push(c, nil, flags)
		if err != nil {
			return nil, err
		}
		newKids[i] = nk
	}
	return refilter(n.WithChildren(newKids), preds), nil
}

// refilter re-applies predicates above a node, or returns it unchanged if there
// are none.
func refilter(n Node, preds []expr.Node) Node {
	if len(preds) == 0 {
		return n
	}
	return &Filter{Input: n, Preds: preds}
}

// pushIntoScan offers each conjunct to the source and keeps whatever the source
// does not promise to remove.
//
// # The Inexact case is the whole reason this function is not two lines
//
// A source may be able to SKIP data using a predicate without being able to
// SATISFY it. Parquet row-group pruning is the canonical example: min/max
// statistics prove that a whole group cannot match, but every group that survives
// still contains non-matching rows. Such a conjunct goes into the Scan — that is
// where the win is — and ALSO stays in a Filter above it.
//
// Dropping the Filter for an inexact source returns extra rows with no error, on
// data larger than a fixture, from a query that looks right. It is the worst
// failure mode available to an optimizer, which is why Unsupported is the zero
// value of Pushdown and why a source has to claim Exact explicitly to get the
// Filter deleted.
func pushIntoScan(s *Scan, preds []expr.Node) (Node, error) {
	if len(preds) == 0 {
		return s, nil
	}

	kinds := ClassifyPredicates(s.Src, preds)

	var into, above []expr.Node
	for i, p := range preds {
		switch kinds[i] {
		case Exact:
			into = append(into, p)
		case Inexact:
			into = append(into, p)
			above = append(above, p)
		default: // Unsupported
			// Not recorded on the Scan at all: the Filter node IS the record, so
			// Explain shows the truth either way.
			above = append(above, p)
		}
	}

	out := Node(s)
	if len(into) > 0 {
		c := *s
		c.Predicate = append(append([]expr.Node(nil), s.Predicate...), into...)
		out = &c
	}
	return refilter(out, above), nil
}

// pushThroughProjection moves predicates below a Project or WithColumns,
// substituting each referenced output column with the expression that defines it.
//
// Substitution is not optional. Without it,
//
//	WithColumns(Col("x").Mul(2).Alias("x")).Filter(Col("x").Gt(10))
//
// pushed verbatim would filter the UN-doubled column: the same name means two
// different things above and below the node. That is a wrong answer with no error.
func pushThroughProjection(n Node, exprs []expr.Node, preds []expr.Node, passthrough bool, flags Flags) (Node, error) {
	in, err := n.Children()[0].Schema()
	if err != nil {
		return nil, err
	}

	// Build name -> defining expression for everything this node produces.
	//
	// WithColumns threads a running schema, so a later expression may reference a
	// column an earlier one added; resolving everything against the input schema
	// would fail on exactly that case. WalkWithColumns is the single definition of
	// that walk.
	defs := make(map[string]expr.Node, len(exprs))
	if passthrough {
		if _, err := WalkWithColumns(in, exprs,
			func(e expr.Node, f dtype.Field, _ int, _ *dtype.Schema) error {
				defs[f.Name] = stripNaming(e)
				return nil
			}); err != nil {
			return nil, err
		}
	} else {
		for _, e := range exprs {
			f, err := expr.Resolve(e, in)
			if err != nil {
				return nil, err
			}
			defs[f.Name] = stripNaming(e)
		}
	}

	var down, stay []expr.Node
	for _, p := range preds {
		sub, ok := substitutable(p, defs, in, passthrough)
		if ok {
			down = append(down, sub)
		} else {
			stay = append(stay, p)
		}
	}

	child, err := push(n.Children()[0], down, flags)
	if err != nil {
		return nil, err
	}
	return refilter(n.WithChildren([]Node{child}), stay), nil
}

// substitutable rewrites a predicate in terms of the input schema, or reports
// that it cannot be.
//
// A column reference resolves in one of three ways:
//   - it names something the node computes  -> replace with the defining expression
//   - it exists unchanged in the input      -> keep (only for WithColumns, which
//     passes its input through; a Project drops
//     everything it does not select)
//   - neither                               -> not pushable
func substitutable(p expr.Node, defs map[string]expr.Node, in *dtype.Schema, passthrough bool) (expr.Node, bool) {
	ok := true

	var walk func(expr.Node) expr.Node
	walk = func(x expr.Node) expr.Node {
		if !ok {
			return x
		}
		switch t := x.(type) {
		case *expr.Col:
			if def, defined := defs[t.Name]; defined {
				// Substituting a definition DUPLICATES it: the predicate gets a
				// copy and the defining node keeps its own. A UDF must not be
				// duplicated — see expr.HasUDF for why it can only ever lose.
				if expr.HasUDF(def) {
					ok = false
					return t
				}
				return def
			}
			if passthrough && in.Has(t.Name) {
				return t
			}
			ok = false
			return t

		case *expr.Lit, *expr.Err:
			return x

		case *expr.Binary:
			return &expr.Binary{Op: t.Op, L: walk(t.L), R: walk(t.R)}
		case *expr.Unary:
			return expr.Rebuild(t, []expr.Node{walk(t.Child)})
		case *expr.Cast:
			return &expr.Cast{Child: walk(t.Child), To: t.To, Strict: t.Strict}
		case *expr.Alias:
			return &expr.Alias{Child: walk(t.Child), Name: t.Name}
		case *expr.Rename:
			return &expr.Rename{Child: walk(t.Child), Fn: t.Fn, Label: t.Label}

		// Cond and Call are ELEMENTWISE — one output row per input row, computed
		// from that row alone — so rewriting through them is exactly as safe as it
		// is for Binary and Unary. Leaving them in the default arm is not wrong, but
		// it silently costs a pushdown: a predicate mentioning a conditional or a
		// .str/.dt call would sit above the node it could have been pushed below.
		//
		// Call's non-receiver arguments must be literals, and the Lit arm above
		// returns them unchanged, so walking cannot break that invariant.
		case *expr.Cond:
			return &expr.Cond{Pred: walk(t.Pred), Then: walk(t.Then), Else: walk(t.Else)}
		case *expr.Call:
			args := make([]expr.Node, len(t.Args))
			for i, a := range t.Args {
				args[i] = walk(a)
			}
			return &expr.Call{Fn: t.Fn, Args: args}

		default:
			// An Agg, a Match, or a node type added later. Refuse rather than guess.
			ok = false
			return x
		}
	}

	out := walk(p)
	if !ok {
		return nil, false
	}
	return out, true
}

// stripNaming removes Alias/Rename wrappers, which carry a name rather than a
// computation. Substituting `col("a").alias("x")` into a predicate would leave a
// stray alias inside an expression where it means nothing.
func stripNaming(e expr.Node) expr.Node {
	for {
		switch t := e.(type) {
		case *expr.Alias:
			e = t.Child
		case *expr.Rename:
			e = t.Child
		default:
			return e
		}
	}
}

func pushThroughDistinct(d *Distinct, preds []expr.Node, flags Flags) (Node, error) {
	// Whole-row distinct: a predicate is a function of the row, so it is constant
	// on each duplicate class and filtering either side gives the same answer.
	if d.Subset == nil {
		child, err := push(d.Input, preds, flags)
		if err != nil {
			return nil, err
		}
		return d.WithChildren([]Node{child}), nil
	}

	// Subset distinct: only predicates confined to the subset may pass.
	//
	// Otherwise filtering first changes WHICH row is kept as the representative.
	// Rows [(k=1,v=9),(k=1,v=1)] with Distinct(subset=k) and predicate v<5:
	// filtering after gives [], filtering before gives [(1,1)]. Silent,
	// data-dependent, and a very common query shape.
	sub := make(map[string]struct{}, len(d.Subset))
	for _, n := range d.Subset {
		sub[n] = struct{}{}
	}

	var down, stay []expr.Node
	for _, p := range preds {
		confined := true
		for _, name := range expr.RootNames(p) {
			if _, in := sub[name]; !in {
				confined = false
				break
			}
		}
		if confined {
			down = append(down, p)
		} else {
			stay = append(stay, p)
		}
	}

	child, err := push(d.Input, down, flags)
	if err != nil {
		return nil, err
	}
	return refilter(d.WithChildren([]Node{child}), stay), nil
}

// pushThroughJoin moves each conjunct into the side that can evaluate it, when the
// join kind makes that legal.
//
// # The legality table, and why each barrier is there
//
//	p reads      | Inner | Left  | Right | Full  | Semi  | Anti  | Cross
//	-------------+-------+-------+-------+-------+-------+-------+-------
//	left only    | left  | left  | BAR   | BAR   | left  | left  | left
//	right only   | right | BAR   | right | BAR   |  n/a  |  n/a  | right
//	both sides   | BAR   | BAR   | BAR   | BAR   |  n/a  |  n/a  | BAR
//	Validate set | BAR   | BAR   | BAR   | BAR   | BAR   | BAR   |  n/a
//
// Inner and Cross, either side: an output row is a pair, and a one-sided predicate
// is a function of that side's row alone, so filtering the pairs equals filtering
// that input first.
//
// Left, left-only: left columns are never null-extended, so p means the same thing
// above and below.
//
// LEFT, RIGHT-ONLY — the trap, and the reason this rule was deferred a whole step.
// An unmatched left row carries all-null right columns; p(null) is null, so the
// Filter above drops it. Push p into the right input and it deletes right rows,
// which turns previously-MATCHED left rows into unmatched ones — they REAPPEAR,
// null-padded, and the filter that would have dropped them is gone. Extra rows, no
// error, and invisible on any fixture where every left row matches. Right and Full
// are the mirror and the union of that argument, hence four barrier cells.
//
// Semi and Anti: the output schema IS the left schema, so a predicate over right
// columns cannot exist. Every conjunct is left-only and every one descends, because
// both are pure filters of the left frame.
//
// Both sides: neither child can evaluate a column it does not have.
//
// VALIDATE — the cell that is not about rows at all. A pushed predicate can delete
// the duplicate keys a ValidateManyToOne check exists to catch, turning a query
// that MUST error into one that returns a plausible answer. Rule's contract is that
// a rewrite produces the same rows; converting an error into a result violates it,
// and no schema check can see that. So a join carrying any validation is a total
// barrier.
func pushThroughJoin(j *Join, preds []expr.Node, flags Flags) (Node, error) {
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

	var downLeft, downRight, stay []expr.Node
	for _, p := range preds {
		// Try left, then right. A conjunct over a COALESCED key succeeds for both —
		// the column denotes the same value on either side — and lands on the left,
		// which is arbitrary but deterministic. Pushing a copy to BOTH sides is the
		// classic key-propagation win and is deliberately not done here: it has to
		// reason about the promoted key type, and `k > 2^40` pushed into an Int32
		// side is a wrong answer rather than a slow one.
		if joinPushLegal(j, FromLeft) {
			if sub, ok := rewriteForSide(p, FromLeft, layout, ls, rs); ok {
				downLeft = append(downLeft, sub)
				continue
			}
		}
		if joinPushLegal(j, FromRight) {
			if sub, ok := rewriteForSide(p, FromRight, layout, ls, rs); ok {
				downRight = append(downRight, sub)
				continue
			}
		}
		stay = append(stay, p)
	}

	left, err := push(j.Left, downLeft, flags)
	if err != nil {
		return nil, err
	}
	right, err := push(j.Right, downRight, flags)
	if err != nil {
		return nil, err
	}
	return refilter(j.WithChildren([]Node{left, right}), stay), nil
}

// joinPushLegal is the table above, minus the per-predicate side classification.
func joinPushLegal(j *Join, side JoinSide) bool {
	if j.Validate != ValidateNone {
		return false
	}
	if side == FromLeft {
		// Right and Full null-extend the left side.
		return j.Kind != JoinRight && j.Kind != JoinFull
	}
	// Left and Full null-extend the right side; Semi and Anti emit no right column,
	// so nothing can name one.
	return j.Kind != JoinLeft && j.Kind != JoinFull && !j.Kind.filtersLeft()
}

// rewriteForSide re-expresses p in one child's namespace, or reports that it
// cannot.
//
// # Why this cannot classify by child-schema membership
//
// A join renames. A suffixed right column (`amount_right`) exists in NEITHER child:
// not in the right, where it is `amount`, and not in the left, because if it were
// there Layout would have refused the join rather than suffix twice. So membership
// in a child's schema answers "no" for a column that is perfectly pushable, and the
// name that would be pushed is one the child has never heard of.
//
// The layout is the only thing that knows. It is the same output-name -> child-name
// translation pushdownJoin does for projections; the difference is that this one has
// to rebuild the expression, not just record a name.
func rewriteForSide(p expr.Node, want JoinSide, layout *JoinLayout, ls, rs *dtype.Schema) (expr.Node, bool) {
	ok := true

	var walk func(expr.Node) expr.Node
	walk = func(x expr.Node) expr.Node {
		if !ok {
			return x
		}
		switch t := x.(type) {
		case *expr.Col:
			i := layout.Schema.IndexOf(t.Name)
			if i < 0 {
				ok = false
				return x
			}
			jc := layout.Columns[i]
			switch {
			case jc.Side == want && want == FromLeft:
				// Left names are never renamed, so this is an identity — rebuilt
				// anyway so the walk allocates uniformly and never aliases the input.
				return &expr.Col{Name: ls.Field(jc.Index).Name}
			case jc.Side == want && want == FromRight:
				return &expr.Col{Name: rs.Field(jc.Index).Name} // un-suffix
			case jc.CoalesceWith >= 0 && want == FromRight:
				// A merged key is both sides at once; the right's name may differ.
				return &expr.Col{Name: rs.Field(jc.CoalesceWith).Name}
			default:
				ok = false
				return x
			}

		case *expr.Lit, *expr.Err:
			return x

		case *expr.Binary:
			return &expr.Binary{Op: t.Op, L: walk(t.L), R: walk(t.R)}
		case *expr.Unary:
			return expr.Rebuild(t, []expr.Node{walk(t.Child)})
		case *expr.Cast:
			return &expr.Cast{Child: walk(t.Child), To: t.To, Strict: t.Strict}
		case *expr.Alias:
			return &expr.Alias{Child: walk(t.Child), Name: t.Name}
		case *expr.Rename:
			return &expr.Rename{Child: walk(t.Child), Fn: t.Fn, Label: t.Label}

		// Cond and Call are ELEMENTWISE — one output row per input row, computed
		// from that row alone — so rewriting through them is exactly as safe as it
		// is for Binary and Unary. Leaving them in the default arm is not wrong, but
		// it silently costs a pushdown: a predicate mentioning a conditional or a
		// .str/.dt call would sit above the node it could have been pushed below.
		//
		// Call's non-receiver arguments must be literals, and the Lit arm above
		// returns them unchanged, so walking cannot break that invariant.
		case *expr.Cond:
			return &expr.Cond{Pred: walk(t.Pred), Then: walk(t.Then), Else: walk(t.Else)}
		case *expr.Call:
			args := make([]expr.Node, len(t.Args))
			for i, a := range t.Args {
				args[i] = walk(a)
			}
			return &expr.Call{Fn: t.Fn, Args: args}

		default:
			// An Agg, a Match, or a node type added later. Refuse rather than guess.
			ok = false
			return x
		}
	}

	out := walk(p)
	if !ok {
		return nil, false
	}
	return out, true
}
