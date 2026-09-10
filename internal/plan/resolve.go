package plan

import (
	"strings"

	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Resolve prepares a freshly built plan for optimization and execution.
//
// It walks bottom-up and, at each node:
//
//  1. fetches source schemas (the only step that performs I/O, hence the context),
//  2. EXPANDS multi-column expressions against the now-known input schema,
//  3. TYPE-CHECKS every expression,
//  4. validates node-specific rules (Filter predicates must be Boolean, Project
//     output names must be unique).
//
// The result is a plan on which Schema() is total and cheap, every Project holds
// single-output expressions, and every expression's types are known.
//
// # Why expansion happens here and not earlier or later
//
// Not earlier: `ColDType(Float64)` has no meaning without a schema.
//
// Not later: projection pushdown asks each expression which columns it reads. An
// unexpanded matcher answers "none", so the optimizer would prune away exactly
// the columns the expression was about to select. Running expansion before the
// optimizer, rather than teaching every rule about matchers, is what keeps the
// rules simple and correct by default.
func Resolve(ctx context.Context, n Node) (Node, error) {
	return TransformUp(n, func(x Node) (Node, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch t := x.(type) {
		case *Scan:
			return resolveScan(ctx, t)
		case *Filter:
			return resolveFilter(t)
		case *Project:
			return resolveProject(t)
		case *WithColumns:
			return resolveWithColumns(t)
		case *Aggregate:
			return resolveAggregate(t)
		case *TemporalGroup:
			return resolveTemporalGroup(t)
		case *Sort:
			return resolveSort(t)
		case *Distinct:
			return resolveDistinct(t)
		case *Unpivot:
			return resolveUnpivot(t)
		case *Join:
			return resolveJoin(t)
		case *AsOfJoin:
			return resolveAsOfJoin(t)
		case *MergeSorted:
			return resolveMergeSorted(t)
		case *Union:
			return resolveUnion(t)
		default:
			return x, nil
		}
	})
}

// rejectAggregate refuses an aggregate expression in a row-preserving context.
//
// `Select(Col("x").Sum())` is a reasonable thing to type and a meaningless thing
// to execute: Select must produce one output row per input row, and a sum
// produces exactly one row regardless. Catching it here — naming the expression
// and pointing at GroupBy — is far better than letting a length-1 column reach the
// evaluator and fail as a shape mismatch three layers down.
func rejectAggregate(e expr.Node, op string) error {
	// HasUnboundAgg, not HasAgg: an aggregate beneath a window is bound by it and
	// preserves the frame's height, so `Col("x").Mean().Over(Col("g"))` is as legal
	// here as `x + 1`. The partition and order keys are still checked, so a window
	// partitioned BY an aggregate is refused.
	if !expr.HasUnboundAgg(e) {
		return nil
	}
	return uerr.New(uerr.KindType, op,
		"aggregate expression is not allowed here: %s", e.String()).
		Hint("%s produces one output row per input row; an aggregate produces one row in total", op).
		Hint("use GroupBy(...).Agg(...) to aggregate, or Select over an already-aggregated frame")
}

func resolveScan(ctx context.Context, s *Scan) (Node, error) {
	if s.Full != nil {
		return s, nil // already resolved; Resolve is idempotent
	}
	full, err := s.Src.Schema(ctx)
	if err != nil {
		return nil, uerr.Wrap(err, uerr.KindIO, "scan",
			"reading schema from %s source", s.Src.Name())
	}
	c := *s
	c.Full = full
	// Validate an explicitly requested projection now, so a typo in a
	// user-supplied column list is reported against the source's real schema.
	if c.Projection != nil {
		if _, err := full.Select(c.Projection); err != nil {
			return nil, uerr.Annotate(err, "scan", c.Label())
		}
	}

	// A predicate here normally arrived from an already-resolved Filter via
	// predicatePushdown, so this is checking a hand-built plan — the case where
	// getting it wrong hands a source an expression it cannot evaluate, at a point
	// far from anything that would explain why. Note the predicate resolves against
	// the FULL schema, not the projection: an Exact conjunct's columns are exactly
	// the ones the projection no longer contains.
	for _, p := range c.Predicate {
		fl, err := expr.Resolve(p, full)
		if err != nil {
			return nil, uerr.Annotate(err, "scan", c.Label())
		}
		if !fl.Type.IsBool() {
			return nil, uerr.New(uerr.KindType, "scan",
				"scan predicate must be Boolean, got %s", fl.Type).
				Hint("predicate: %s", p.String())
		}
	}
	return &c, nil
}

func resolveFilter(f *Filter) (Node, error) {
	in, err := f.Input.Schema()
	if err != nil {
		return nil, err
	}

	preds, err := expr.ExpandAll(f.Preds, in)
	if err != nil {
		return nil, uerr.Annotate(err, "filter", "Filter")
	}
	if len(preds) == 0 {
		// Every predicate expanded to nothing, e.g. a selector matching no
		// columns. Filtering on nothing keeps everything; drop the node rather
		// than execute a no-op.
		return f.Input, nil
	}

	for _, p := range preds {
		// A predicate over an aggregate is HAVING, which belongs after the
		// aggregation rather than in a row-filter. Rejecting it here with that
		// explanation is far better than letting it reach the evaluator, which
		// would report "no evaluator for *expr.Agg" as an internal ursus bug.
		if err := rejectAggregate(p, "filter"); err != nil {
			return nil, err
		}
		if err := rejectWindow(p, "filter"); err != nil {
			return nil, err
		}
		fl, err := expr.Resolve(p, in)
		if err != nil {
			return nil, uerr.Annotate(err, "filter", "Filter")
		}
		if !fl.Type.IsBool() {
			return nil, uerr.New(uerr.KindType, "filter",
				"predicate must be Boolean, got %s", fl.Type).
				Hint("predicate: %s", p.String()).
				Hint("did you mean a comparison, e.g. .Gt(0) or .IsNotNull()?")
		}
	}

	c := *f
	c.Preds = preds
	return &c, nil
}

// resolveUnpivot computes the node's schema and throws it away.
//
// That is the whole function, and it is resolveJoin's step 5 for the same reason:
// every check Unpivot has — the names exist, On is non-empty, nothing is both
// melted and kept, the value types promote, the invented names do not collide —
// lives in Schema(), and without this the first caller of Schema() is a rule inside
// Optimizer.Run, which wraps the failure as `rule %q failed`. A user who melted a
// column that does not exist would be told an optimizer rule broke.
//
// Explode and Unnest have the same shape and no such arm, so they have the same
// defect; fixing theirs is not this step's business, but it is worth knowing that
// this is a pattern rather than a one-off.
func resolveUnpivot(u *Unpivot) (Node, error) {
	if _, err := u.Schema(); err != nil {
		return nil, err
	}
	return u, nil
}

func resolveProject(p *Project) (Node, error) {
	in, err := p.Input.Schema()
	if err != nil {
		return nil, err
	}

	exprs, err := expr.ExpandAll(p.Exprs, in)
	if err != nil {
		return nil, uerr.Annotate(err, "select", "Project")
	}

	// Resolve each expression and check the output names are unique. Catching the
	// collision here — where the offending expression can be named — is much more
	// useful than letting NewSchema report an anonymous duplicate.
	seen := make(map[string]int, len(exprs))
	fields := make([]dtype.Field, len(exprs))
	for i, e := range exprs {
		if err := rejectAggregate(e, "select"); err != nil {
			return nil, err
		}
		fl, err := expr.Resolve(e, in)
		if err != nil {
			return nil, uerr.Annotate(err, "select", "Project")
		}
		if prev, dup := seen[fl.Name]; dup {
			return nil, uerr.New(uerr.KindSchema, "select",
				"two selected expressions are both named %q", fl.Name).
				Hint("expression %d: %s", prev, exprs[prev].String()).
				Hint("expression %d: %s", i, e.String()).
				Hint("use .Alias(...) to give one of them a different name")
		}
		seen[fl.Name] = i
		fields[i] = fl
	}

	if _, err := dtype.NewSchema(fields...); err != nil {
		return nil, err
	}

	// Windows become a node beneath this one. No trailing projection is needed:
	// a Project already names its own output columns, so the temporaries the
	// Window publishes simply are not selected.
	input, exprs, _, err := extractWindows(p.Input, exprs, "select")
	if err != nil {
		return nil, err
	}

	c := *p
	c.Input = input
	c.Exprs = exprs
	return &c, nil
}

// IsResolved reports whether every Scan in the plan has its source schema, which
// is the observable marker of a resolved plan.
func IsResolved(n Node) bool {
	ok := true
	Walk(n, func(x Node) bool {
		if s, isScan := x.(*Scan); isScan && s.Full == nil {
			ok = false
			return false
		}
		return true
	})
	return ok
}

func resolveWithColumns(w *WithColumns) (Node, error) {
	in, err := w.Input.Schema()
	if err != nil {
		return nil, err
	}

	// Both EXPANSION and RESOLUTION thread a running schema, so a later expression
	// sees the columns earlier ones added. Expanding against the input schema while
	// resolving against a running one — which an earlier draft did — makes
	// `WithColumns(Col("a").Alias("x"), All())` silently omit x.
	//
	// WithColumns.Schema() performs the identical walk. The two MUST agree: if
	// resolution accepts a plan whose Schema() then errors, the optimizer reports
	// it as "produced a plan whose schema does not resolve", which is a maximally
	// confusing message for a perfectly valid query.
	running := in
	out := make([]expr.Node, 0, len(w.Exprs))
	seen := make(map[string]int)

	for _, raw := range w.Exprs {
		expanded, err := expr.Expand(raw, running)
		if err != nil {
			return nil, uerr.Annotate(err, "with_columns", "WithColumns")
		}
		for _, e := range expanded {
			if err := rejectAggregate(e, "with_columns"); err != nil {
				return nil, err
			}
			f, err := expr.Resolve(e, running)
			if err != nil {
				return nil, uerr.Annotate(err, "with_columns", "WithColumns")
			}
			// Two expressions producing the same name is an error, not last-wins.
			// Silently dropping one of them would also leave both in Exprs for the
			// operator to reproduce by guesswork.
			if prev, dup := seen[f.Name]; dup {
				return nil, duplicateOutput("with_columns", f.Name, out[prev], e)
			}
			seen[f.Name] = len(out)
			out = append(out, e)

			fields := running.FieldSlice()
			if j := running.IndexOf(f.Name); j >= 0 {
				fields[j] = f // replace in place, preserving column position
			} else {
				fields = append(fields, f)
			}
			if running, err = dtype.NewSchema(fields...); err != nil {
				return nil, err
			}
		}
	}

	if len(out) == 0 {
		return w.Input, nil // every expression expanded to nothing; drop the node
	}

	input, out, temps, err := extractWindows(w.Input, out, "with_columns")
	if err != nil {
		return nil, err
	}

	c := *w
	c.Input = input
	c.Exprs = out
	if len(temps) == 0 {
		return &c, nil
	}
	// Unlike Project, WithColumns does not choose its output columns — it is its
	// input plus what it adds — so the Window's temporaries would appear in the
	// frame. Project them away, keeping the schema this node would have had.
	outSchema, err := c.Schema()
	if err != nil {
		return nil, err
	}
	keep := make([]string, 0, outSchema.Len())
	for _, f := range outSchema.FieldSlice() {
		if !strings.HasPrefix(f.Name, winTempPrefix) {
			keep = append(keep, f.Name)
		}
	}
	return dropTemps(&c, keep), nil
}

func resolveSort(s *Sort) (Node, error) {
	in, err := s.Input.Schema()
	if err != nil {
		return nil, err
	}
	if len(s.Keys) == 0 {
		return s.Input, nil
	}

	keys := make([]SortKey, 0, len(s.Keys))
	for _, k := range s.Keys {
		if err := rejectAggregate(k.Expr, "sort"); err != nil {
			return nil, err
		}
		if err := rejectWindow(k.Expr, "sort"); err != nil {
			return nil, err
		}
		expanded, err := expr.Expand(k.Expr, in)
		if err != nil {
			return nil, uerr.Annotate(err, "sort", "Sort")
		}
		for _, e := range expanded {
			f, err := expr.Resolve(e, in)
			if err != nil {
				return nil, uerr.Annotate(err, "sort", "Sort")
			}
			if !sortable(f.Type) {
				return nil, uerr.New(uerr.KindType, "sort",
					"cannot sort by %s, which is a %s", e.String(), f.Type).
					Hint("only numeric, temporal, string and boolean columns have an ordering")
			}
			keys = append(keys, SortKey{Expr: e, Descending: k.Descending, NullsLast: k.NullsLast})
		}
	}

	if len(keys) == 0 {
		// Every key expanded to nothing, e.g. ColRegex matching no column.
		// Ordering by nothing is the identity; drop the node rather than hand the
		// operator a degenerate one. Filter and WithColumns do the same.
		return s.Input, nil
	}

	c := *s
	c.Keys = keys
	return &c, nil
}

func sortable(d dtype.DataType) bool { return d.IsOrdered() }

func resolveAggregate(a *Aggregate) (Node, error) {
	in, err := a.Input.Schema()
	if err != nil {
		return nil, err
	}

	keys, err := expr.ExpandAll(a.Keys, in)
	if err != nil {
		return nil, uerr.Annotate(err, "group_by", "Aggregate")
	}
	aggs, err := expr.ExpandAll(a.Aggs, in)
	if err != nil {
		return nil, uerr.Annotate(err, "agg", "Aggregate")
	}

	seen := make(map[string]int, len(keys)+len(aggs))

	for _, k := range keys {
		if err := rejectAggregate(k, "group_by"); err != nil {
			return nil, err
		}
		if err := rejectWindow(k, "group_by"); err != nil {
			return nil, err
		}
		f, err := expr.Resolve(k, in)
		if err != nil {
			return nil, uerr.Annotate(err, "group_by", "Aggregate")
		}
		if !groupable(f.Type) {
			return nil, uerr.New(uerr.KindType, "group_by",
				"cannot group by %s, which is a %s", k.String(), f.Type).
				Hint("group keys must be hashable: numeric, temporal, string or boolean")
		}
		if prev, dup := seen[f.Name]; dup {
			return nil, duplicateOutput("group_by", f.Name, keys[prev], k)
		}
		seen[f.Name] = len(seen)
	}

	for _, e := range aggs {
		// Every path from the root to a column must pass through an aggregate.
		// `Sum(a) + 1` is fine; `Sum(a) + b` is not, because b is still per-row and
		// there is no defined way to combine the two shapes.
		if !expr.IsAggregation(e) {
			err := uerr.New(uerr.KindType, "agg",
				"expression is not an aggregate: %s", e.String())
			if bare := expr.BareColumns(e); len(bare) > 0 {
				err.Hint("column %q is used outside any aggregate function", bare[0])
				err.Hint("wrap it, e.g. Col(%q).First() or Col(%q).Mean()", bare[0], bare[0])
			}
			err.Hint("every column in an aggregate expression must be reduced")
			return nil, err
		}
		// A window inside Agg() has to be refused HERE, and until step 46 it was
		// refused nowhere. hashAggSink's own comment asserted this check existed —
		// "A window inside Agg() is refused at plan time, so there is no
		// non-row-local case to worry about" — and it did not: rejectWindow covered
		// filter, sort, join keys and the group-by KEYS, never the aggregates.
		//
		// The result was that Agg(Col("v").CumSum().Sum()) resolved cleanly, Explain
		// printed a plan, and Collect died with "a window reached the evaluator",
		// which announces itself as an ursus bug. Refusing at resolve time is the
		// discipline Expr.DropNulls documents: a refusal only Collect notices lets
		// Explain print a plan that cannot run.
		if err := rejectWindow(e, "agg"); err != nil {
			return nil, err
		}
		f, err := expr.Resolve(e, in)
		if err != nil {
			return nil, uerr.Annotate(err, "agg", "Aggregate")
		}
		if _, dup := seen[f.Name]; dup {
			return nil, uerr.New(uerr.KindSchema, "agg",
				"aggregate output %q collides with another output column", f.Name).
				Hint("expression: %s", e.String()).
				Hint("use .Alias(...) to give it a different name")
		}
		seen[f.Name] = len(seen)
	}

	c := *a
	c.Keys = keys
	c.Aggs = aggs
	return &c, nil
}

func groupable(d dtype.DataType) bool { return d.IsHashable() }

func duplicateOutput(op, name string, a, b expr.Node) error {
	return uerr.New(uerr.KindSchema, op,
		"two expressions are both named %q", name).
		Hint("expression: %s", a.String()).
		Hint("expression: %s", b.String()).
		Hint("use .Alias(...) to give one of them a different name")
}

func resolveDistinct(d *Distinct) (Node, error) {
	in, err := d.Input.Schema()
	if err != nil {
		return nil, err
	}
	for _, name := range d.Subset {
		if !in.Has(name) {
			return nil, uerr.UnknownColumn("distinct", name, in.Names())
		}
	}
	return d, nil
}

// resolveJoin expands and type-checks the join keys.
//
// Without this arm the node falls through Resolve's `default: return x, nil` and
// passes through UNRESOLVED — keys never expanded, never type-checked — and the
// first sign would be expr.Resolve reporting an unexpanded matcher as an internal
// error from somewhere inside the optimizer.
//
// The order of the checks matters, because each one's message is only useful once
// the earlier ones have passed.
func resolveJoin(j *Join) (Node, error) {
	ls, err := j.Left.Schema()
	if err != nil {
		return nil, err
	}
	rs, err := j.Right.Schema()
	if err != nil {
		return nil, err
	}

	// 1. Structure, per kind.
	if j.Kind == JoinCross {
		if len(j.LeftOn) > 0 || len(j.RightOn) > 0 {
			return nil, uerr.New(uerr.KindValue, "join",
				"a cross join takes no keys, but %d were given", len(j.LeftOn)+len(j.RightOn)).
				Hint("did you mean an inner join?")
		}
		if j.Validate != ValidateNone {
			return nil, uerr.New(uerr.KindValue, "join",
				"validate is not meaningful on a cross join").
				Hint("a cross join has no keys whose cardinality could be checked")
		}
	} else if j.Kind == JoinFull && j.Coalesce == CoalesceOn {
		// A full join can leave a key null on EITHER side, so the merged value is
		// genuinely coalesce(l, r) rather than one side or the other — and there is
		// no coalesce expression in ursus yet. Refusing beats emitting a column
		// that is null wherever the left side was unmatched.
		return nil, uerr.New(uerr.KindUnsupported, "join",
			"a full join cannot coalesce its keys yet").
			Hint("a full join can leave the key null on either side, so merging "+
				"them needs a coalesce expression ursus does not have").
			Hint("the default keeps both key columns; the right one is suffixed %q",
				j.suffix())
	} else if len(j.LeftOn) == 0 && len(j.RightOn) == 0 && !j.HasResidual() {
		// An equi-join with no keys must be an error, never a silent cross join:
		// the cardinality difference is |L| vs |L|x|R|.
		//
		// A RESIDUAL semi/anti join is the exception, and it is not a silent
		// anything: WhereExists builds one deliberately, the predicate is the match
		// rule, and collapse_cross_join turns whichever conjuncts are equalities
		// into keys afterwards. Before that rule runs the join is keyless and
		// correct — one key group, every pair tested — which is what keeps the rule
		// an optimisation rather than a requirement.
		return nil, uerr.New(uerr.KindValue, "join",
			"a %s join requires at least one key", j.Kind).
			Hint("use JoinOn(...), or JoinHow(ursus.JoinCross) for a cartesian product")
	}

	if j.HasResidual() && !j.Kind.filtersLeft() {
		// Inner needs no residual: a pair is the output row, so the predicate is a
		// Filter above the join. Right and Full would need `matched` per build ROW
		// rather than per key id — see joinProbeOp — which is a different step.
		return nil, uerr.New(uerr.KindUnsupported, "join",
			"a %s join cannot carry a residual predicate", j.Kind).
			Hint("only semi and anti joins evaluate a predicate before the match verdict").
			Hint("for an inner join, write the predicate as a filter: JoinWhere does this")
	}

	// 2. Expand, each side against ITS OWN schema.
	left, err := expandJoinKeys(j.LeftOn, ls, "left")
	if err != nil {
		return nil, err
	}
	right, err := expandJoinKeys(j.RightOn, rs, "right")
	if err != nil {
		return nil, err
	}

	// 3. Counts, after expansion.
	if len(left) != len(right) {
		return nil, uerr.New(uerr.KindValue, "join",
			"join has %d left keys and %d right keys", len(left), len(right)).
			Hint("left:  %s", expr.StringAll(left)).
			Hint("right: %s", expr.StringAll(right))
	}

	// 4. Reject aggregates. A join key is a row-level value.
	for i, e := range left {
		if err := rejectWindow(e, "join"); err != nil {
			return nil, err
		}
		if err := rejectAggregate(e, "join"); err != nil {
			return nil, err
		}
		if err := rejectWindow(right[i], "join"); err != nil {
			return nil, err
		}
		if err := rejectAggregate(right[i], "join"); err != nil {
			return nil, err
		}
	}

	c := *j
	c.LeftOn, c.RightOn = left, right

	// 4b. The residual, against the PAIR schema.
	//
	// It is resolved here rather than left to the evaluator for the same reason
	// step 5 computes a layout it throws away: an unknown column or a non-Boolean
	// predicate should be reported against the query the user wrote, not as a rule
	// failure or an internal evaluator error two phases later.
	if c.HasResidual() {
		pair, err := c.PairLayout()
		if err != nil {
			return nil, err
		}
		preds, err := expr.ExpandAll(c.Residual, pair.Schema)
		if err != nil {
			return nil, uerr.Annotate(err, "join", "join residual")
		}
		if len(preds) == 0 {
			return nil, uerr.New(uerr.KindValue, "join",
				"the join predicate expanded to nothing")
		}
		for _, p := range preds {
			if err := rejectAggregate(p, "join"); err != nil {
				return nil, err
			}
			if err := rejectWindow(p, "join"); err != nil {
				return nil, err
			}
			fl, err := expr.Resolve(p, pair.Schema)
			if err != nil {
				return nil, uerr.Annotate(err, "join", "join residual")
			}
			if !fl.Type.IsBool() {
				return nil, uerr.New(uerr.KindType, "join",
					"join predicate must be Boolean, got %s", fl.Type).
					Hint("predicate: %s", p.String()).
					Hint("columns are named as they would be after the join: "+
						"left names as they are, colliding right names suffixed %q", j.suffix())
			}
		}
		c.Residual = preds
	}

	// 5. Compute the layout and discard it.
	//
	// This is the most important line in the function. It surfaces a key type
	// error or a name collision HERE, where the message names the columns —
	// rather than at the first Schema() call inside Optimizer.Run, which wraps
	// every rule failure as "rule %q produced a plan whose schema does not
	// resolve". That is the exact maximally-confusing message defect P2 produced
	// for a perfectly valid query.
	if _, err := c.Layout(); err != nil {
		return nil, err
	}
	return &c, nil
}

// expandJoinKeys expands each key against its own schema, allowing only matchers
// whose expansion order is the order written.
//
// # Why most selectors are rejected
//
// A join pairs LeftOn[i] with RightOn[i] POSITIONALLY. Col("a","b") expands in
// the order written, so it pairs correctly. Every other matcher — All, ColRegex,
// ColDType, Exclude — expands in SCHEMA order, and two frames' schema orders are
// unrelated. JoinOn(ColRegex("^k_")) over left {k_a, k_b} and right {k_b, k_a}
// would pair k_a with k_b, silently, and return the wrong rows.
//
// Rejecting them costs a feature nobody has asked for and removes a whole
// category of silent mispairing.
func expandJoinKeys(keys []expr.Node, in *dtype.Schema, side string) ([]expr.Node, error) {
	for _, k := range keys {
		// NameMatcher is the only matcher documented to expand "in the order
		// written"; every other one expands in schema order.
		if m, ok := k.(*expr.Match); ok {
			if _, byName := m.M.(*expr.NameMatcher); byName {
				continue
			}
			return nil, uerr.New(uerr.KindValue, "join",
				"a join key may not be a multi-column selector: %s", k.String()).
				Hint("join keys are paired positionally, and a selector expands in "+
					"each frame's own column order, so the pairing would depend on "+
					"the two schemas' orders").
				Hint("name the key columns explicitly, e.g. JoinOn(Col(\"a\"), Col(\"b\"))").
				Hint("this is the %s side", side)
		}
	}
	out, err := expr.ExpandAll(keys, in)
	if err != nil {
		return nil, uerr.Annotate(err, "join", "Join")
	}
	// A join key has no output name, so Alias/Rename on one is inert. Stripping
	// them also makes the bare-column test in Layout robust.
	for i, e := range out {
		out[i] = stripNaming(e)
	}
	return out, nil
}

// resolveTemporalGroup expands and type-checks a dynamic or rolling group.
//
// It is resolveAggregate plus the index column and the interval validation, and it
// reuses the aggregate half verbatim rather than re-deriving it — the "every column
// must be reduced" rule is the same rule, and two copies of it would drift.
func resolveTemporalGroup(t *TemporalGroup) (Node, error) {
	in, err := t.Input.Schema()
	if err != nil {
		return nil, err
	}
	op := t.op()

	for _, iv := range []struct {
		name string
		v    dtype.Interval
	}{{"every", t.Every}, {"period", t.Period}, {"offset", t.Offset}} {
		if err := iv.v.Err(); err != nil {
			return nil, uerr.Annotate(err, op, iv.name)
		}
	}
	// A grid needs a positive step or it does not advance; a window needs a positive
	// width or it contains nothing. Both would otherwise be an infinite loop or an
	// empty answer, and neither says why.
	if !t.Rolling && (t.Every.IsZero() || t.Every.Negative()) {
		return nil, uerr.New(uerr.KindValue, op,
			"every must be a positive interval, got %s", t.Every).
			Hint(`set DynamicOptions.Every, e.g. ursus.Every("1h")`)
	}
	// Period defaults to Every, which gives windows that tile the input exactly. It
	// is defaulted HERE rather than in the physical planner so the plan node carries
	// the resolved value — Explain then shows the window width that will actually be
	// used, and the validation below sees the same thing the operator will.
	c := *t
	if c.Period.IsZero() && !c.Rolling {
		c.Period = c.Every
	}
	if c.Period.IsZero() || c.Period.Negative() {
		return nil, uerr.New(uerr.KindValue, op,
			"period must be a positive interval, got %s", c.Period).
			Hint("period defaults to every; set it explicitly for overlapping windows")
	}
	// Mixed intervals have no grid: "every 1 month and 3 days" does not tile the
	// line, because the two components advance at rates that never line up.
	if !t.Rolling && countComponents(t.Every) > 1 {
		return nil, uerr.New(uerr.KindUnsupported, op,
			"every=%s mixes months, days and sub-day units", t.Every).
			Hint("a window grid needs one unit, e.g. 1mo or 7d or 90m")
	}

	idxs, err := expr.ExpandAll([]expr.Node{t.Index}, in)
	if err != nil {
		return nil, uerr.Annotate(err, op, "TemporalGroup")
	}
	if len(idxs) != 1 {
		return nil, uerr.New(uerr.KindSchema, op,
			"the index must be exactly one column, got %d", len(idxs)).
			Hint("a selector that expands to several columns cannot be a time index")
	}
	keys, err := expr.ExpandAll(t.Keys, in)
	if err != nil {
		return nil, uerr.Annotate(err, op, "TemporalGroup")
	}
	aggs, err := expr.ExpandAll(t.Aggs, in)
	if err != nil {
		return nil, uerr.Annotate(err, "agg", "TemporalGroup")
	}

	c.Index, c.Keys, c.Aggs = idxs[0], keys, aggs

	// Reuse Aggregate's checks for the categorical keys and the aggregates. The
	// index is deliberately NOT passed as a key: it is checked by Schema, which
	// requires it to be temporal, and it is not required to be groupable in the
	// hash sense.
	probe := &Aggregate{Input: t.Input, Keys: keys, Aggs: aggs}
	if _, err := resolveAggregate(probe); err != nil {
		return nil, err
	}
	// And Schema is computed and thrown away, exactly as resolveJoin does with
	// Layout: it is where the index's type is checked, and the message is far more
	// useful here than from inside the physical planner.
	if _, err := c.Schema(); err != nil {
		return nil, err
	}
	return &c, nil
}

// countComponents is how many of an interval's three fields are non-zero.
func countComponents(iv dtype.Interval) int {
	n := 0
	for _, v := range []bool{iv.Months() != 0, iv.Days() != 0, iv.Nanos() != 0} {
		if v {
			n++
		}
	}
	return n
}
