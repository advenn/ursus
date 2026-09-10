package plan

import (
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// projectionPushdown narrows every Scan to the columns actually used above it.
//
// This is the rule that justifies the whole lazy architecture. Without it,
// `ScanX(...).Select(Col("a"))` over a 41-column source reads 41 columns and
// throws 40 away, which is exactly what an eager library does — and at that point
// there is no reason for the plan IR to exist. With it, the scan reads one.
//
// # The algorithm
//
// One top-down sweep carrying a set of REQUIRED column names.
//
//	root      → require everything the root produces
//	Project   → require exactly the columns its expressions read; whatever the
//	            parent wanted is satisfied by the projection itself, so the
//	            requirement set is REPLACED rather than extended
//	Filter    → require the parent's set PLUS the columns its predicates read
//	Limit     → pass the parent's set through unchanged
//	Scan      → set the projection to the required set, in source order
//
// # The conservative default
//
// An unrecognised node type requires ALL of its input's columns. New node types
// are therefore correct-but-unoptimized the day they are added, rather than
// silently pruning columns they needed. A rule that fails open on unknown input is
// the only kind safe to have in a codebase that is still growing node types.
type projectionPushdown struct{}

func (projectionPushdown) Name() string { return "projection_pushdown" }

func (p projectionPushdown) Apply(n Node, _ Flags) (Node, bool, error) {
	root, err := n.Schema()
	if err != nil {
		return nil, false, err
	}
	// The root must produce everything it currently produces.
	required := make(map[string]struct{}, root.Len())
	for _, name := range root.Names() {
		required[name] = struct{}{}
	}

	out, changed, err := pushdown(n, required)
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

func pushdown(n Node, required map[string]struct{}) (Node, bool, error) {
	switch t := n.(type) {
	case *Scan:
		return pushdownScan(t, required)

	case *Project:
		// A projection defines its own inputs: what its expressions read is
		// exactly what the child must supply, regardless of what the parent asked
		// for. This is where the set shrinks.
		need := map[string]struct{}{}
		for _, e := range t.Exprs {
			for _, name := range expr.RootNames(e) {
				need[name] = struct{}{}
			}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Filter:
		// A filter consumes columns without producing them, so its predicates'
		// columns must be added to whatever the parent needs.
		need := cloneSet(required)
		for _, e := range t.Preds {
			for _, name := range expr.RootNames(e) {
				need[name] = struct{}{}
			}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Limit, *Slice, *Tail, *Reverse:
		// Pure pass-through: the output schema is the input's and the node reads no
		// column of its own, so it needs exactly what the parent asked for. Grouped
		// because writing them apart would be four copies of one rule.
		child, changed, err := pushdown(t.Children()[0], required)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *RowIndex:
		// It PREPENDS a column and reads none, so it needs what the parent wants
		// minus the one it invents. Pruning can only remove a name collision, never
		// create one, so Schema cannot start failing because of this.
		need := make(map[string]struct{}, len(required))
		for name := range required {
			if name != t.Name {
				need[name] = struct{}{}
			}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Aggregate:
		// The required set is REPLACED, not extended, and that is the whole subtlety
		// of this arm. An Aggregate's output is Keys ++ Aggs and nothing of the input
		// passes through, so what the parent wants is satisfied by this node's own
		// output; what it needs from BELOW is only what its own expressions read.
		//
		// The reflex from *Filter — "a node that consumes columns adds to what the
		// parent wants" — is exactly backwards here. Getting it wrong the other way
		// merely loses the win; getting it wrong this way would keep every column.
		//
		// RootNames sees through *Agg, so `Col("x").Sum()` names x. Note that
		// `Len()` reads no column and still names one, so a column nothing reads is
		// kept — a missed optimisation, not a wrong answer, and closing it needs
		// aggSpec to carry a "no input" marker that it does not have.
		need := requiredOf(append(append([]expr.Node(nil), t.Keys...), t.Aggs...))
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *TemporalGroup:
		// Same shape as Aggregate, plus the INDEX — the column the windows are cut
		// from, which appears in no key and no aggregate and without which the
		// operator cannot run.
		es := append([]expr.Node{t.Index}, t.Keys...)
		need := requiredOf(append(es, t.Aggs...))
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *WithColumns:
		// Walked BACKWARDS, and it has to be.
		//
		// WithColumns resolves each expression against the schema the previous ones
		// produced, so `WithColumns(a.Alias("x"), Col("x").Mul(2))` is legal. A
		// forward pass that unioned every read and subtracted every definition would
		// ask the child for x, which the child does not have. Going backwards, each
		// expression removes the name it defines from the requirement and adds the
		// names it reads — which is the same order the running schema was built in,
		// reversed.
		//
		// Replace-in-place makes subtract-then-union mandatory rather than stylistic:
		// `Col("x").Mul(2).Alias("x")` both defines and reads x.
		in, err := t.Input.Schema()
		if err != nil {
			return nil, false, err
		}
		defs, err := withColumnsDefs(in, t.Exprs)
		if err != nil {
			return nil, false, err
		}
		need := make(map[string]struct{}, len(required))
		for name := range required {
			need[name] = struct{}{}
		}
		for i := len(t.Exprs) - 1; i >= 0; i-- {
			delete(need, defs[i])
			for _, name := range expr.RootNames(t.Exprs[i]) {
				need[name] = struct{}{}
			}
		}
		// The output is a superset of the input, so the parent may name a column the
		// child cannot supply. The Window arm below filters for the same reason.
		for name := range need {
			if in.IndexOf(name) < 0 {
				delete(need, name)
			}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Distinct:
		// With a SUBSET, pruning is safe: distinctOp compares only the subset and
		// keeps the first row per key, so removing a non-subset column cannot change
		// which rows survive.
		//
		// WITHOUT one it is not, and this is the one arm in the rule whose careless
		// version returns wrong ROWS rather than a failed plan. Whole-row distinct
		// dedups on every column, so dropping one collapses rows that were distinct —
		// fewer rows out, no schema change, nothing to resolve against, and
		// TestPushdownSoundness would not catch it because Expressions(*Distinct)
		// returns an empty slice in exactly this case.
		if t.Subset == nil {
			// Require EVERYTHING, and descend anyway so a Project further down still
			// prunes. This is the conservative default's behaviour, written out here
			// rather than reached by falling through, because "the safe answer is
			// also the correct one" is worth being explicit about.
			in, err := t.Input.Schema()
			if err != nil {
				return nil, false, err
			}
			all := make(map[string]struct{}, in.Len())
			for _, name := range in.Names() {
				all[name] = struct{}{}
			}
			child, changed, err := pushdown(t.Input, all)
			if err != nil {
				return nil, false, err
			}
			if !changed {
				return t, false, nil
			}
			return t.WithChildren([]Node{child}), true, nil
		}
		need := make(map[string]struct{}, len(required)+len(t.Subset))
		for name := range required {
			need[name] = struct{}{}
		}
		for _, name := range t.Subset {
			need[name] = struct{}{}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Union:
		// Every child produces the union's output schema, so what each needs is
		// exactly what the parent asked for. The node holds no expressions of its
		// own — reconciliation is a Project beneath it — so there is nothing to add.
		kids := make([]Node, len(t.Inputs))
		any := false
		for i, c := range t.Inputs {
			nk, ch, err := pushdown(c, required)
			if err != nil {
				return nil, false, err
			}
			kids[i] = nk
			any = any || ch
		}
		if !any {
			return t, false, nil
		}
		return t.WithChildren(kids), true, nil

	case *Sort:
		// Sort preserves its input's schema exactly, so it needs what the parent
		// needs plus whatever its KEYS read — the keys are consumed for comparison
		// and then discarded, so they do not appear above.
		//
		// Without this arm Sort falls to the conservative default and requires every
		// column, which means any query containing a sort reads the whole table.
		// That defeats the top-k the limit-pushdown rule sets up: bounding the sort
		// to ten rows is worth much less if all forty columns were decoded to get
		// there.
		need := make(map[string]struct{}, len(required))
		for name := range required {
			need[name] = struct{}{}
		}
		for _, k := range t.Keys {
			for _, name := range expr.RootNames(k.Expr) {
				need[name] = struct{}{}
			}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Join:
		return pushdownJoin(t, required)

	case *Window:
		// A Window publishes its input's columns plus one temporary per window, so
		// what it needs from below is: whatever the parent still wants that the
		// input can supply, plus every column the windows themselves read.
		//
		// RootNames supplies the second half, and it works because a window's
		// operands are ordinary children — the aggregated value, the partition keys
		// and the order keys all appear. Without this arm the default below keeps
		// EVERY input column, so any query containing a window reads the whole
		// table.
		need := make(map[string]struct{}, len(required))
		in, err := t.Input.Schema()
		if err != nil {
			return nil, false, err
		}
		for name := range required {
			if in.IndexOf(name) >= 0 {
				need[name] = struct{}{}
			}
		}
		for _, e := range t.Exprs {
			for _, name := range expr.RootNames(e) {
				need[name] = struct{}{}
			}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Explode:
		// The output names are the input's, one for one — Explode retypes in place
		// — so the parent's set passes straight through. The exploded columns are
		// added unconditionally: they set the row count, so they are read whether or
		// not anyone selects them.
		need := cloneSet(required)
		for _, name := range t.Columns {
			need[name] = struct{}{}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Unnest:
		// The parent asks for FIELD names, which exist in no input schema — Unnest
		// splices a struct's fields in place of the struct. So required is filtered
		// against the input, exactly as the Window arm filters its own invented
		// temporaries, and the unnested structs are added back unconditionally.
		//
		// The aggressive version — drop a struct whose fields nobody reads — needs a
		// field-to-struct owner map, and is not worth it: the struct is named in the
		// query, so the caller has already said they want it.
		in, err := t.Input.Schema()
		if err != nil {
			return nil, false, err
		}
		need := make(map[string]struct{}, len(required))
		for name := range required {
			if in.IndexOf(name) >= 0 {
				need[name] = struct{}{}
			}
		}
		for _, name := range t.Columns {
			need[name] = struct{}{}
		}
		child, changed, err := pushdown(t.Input, need)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return t, false, nil
		}
		return t.WithChildren([]Node{child}), true, nil

	case *Unpivot:
		return pushdownUnpivot(t, required)

	default:
		// Conservative: require everything this node's children can produce, and
		// keep descending so that a Scan further down still gets *some* pruning
		// from a Project below an unknown node.
		kids := n.Children()
		if len(kids) == 0 {
			return n, false, nil
		}
		newKids := make([]Node, len(kids))
		anyChanged := false
		for i, c := range kids {
			cs, err := c.Schema()
			if err != nil {
				return nil, false, err
			}
			all := make(map[string]struct{}, cs.Len())
			for _, name := range cs.Names() {
				all[name] = struct{}{}
			}
			nk, changed, err := pushdown(c, all)
			if err != nil {
				return nil, false, err
			}
			newKids[i] = nk
			anyChanged = anyChanged || changed
		}
		if !anyChanged {
			return n, false, nil
		}
		return n.WithChildren(newKids), true, nil
	}
}

func pushdownScan(s *Scan, required map[string]struct{}) (Node, bool, error) {
	// Emit in the SOURCE's column order, not the required set's iteration order,
	// so the plan is deterministic. Golden Explain files depend on it, and a
	// non-deterministic projection would also defeat plan caching later.
	var cols []string
	for _, name := range s.Full.Names() {
		if _, want := required[name]; want {
			cols = append(cols, name)
		}
	}

	if sameProjection(s.Projection, cols) {
		return s, false, nil
	}
	return s.WithProjection(cols), true, nil
}

func sameProjection(a, b []string) bool {
	if a == nil {
		return false // nil means "everything"; b is always an explicit list
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneSet(s map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for k := range s {
		out[k] = struct{}{}
	}
	return out
}

// pushdownJoin narrows each side of a join to the columns it actually supplies,
// plus its own key columns.
//
// # Why this rule is safe to ship where the predicate rule is not
//
// It cannot change rows — only which columns a scan decodes — and when it is
// wrong the plan above stops resolving, which Verify and every collecting test
// catch immediately. A wrongly-pushed PREDICATE returns different rows silently.
//
// The win is concentrated and real: a SEMI or ANTI join emits no right column at
// all, so the right side needs only the key columns. A semi join against a
// forty-column dimension table reads one.
//
// # The naming-stability clause, without which this is a P2 repeat
//
// A right column's output name is a function of the LEFT side's name set — it is
// suffixed only if the left has that name. So narrowing the left can silently
// RENAME an output column:
//
//	left {a, b} ⋈ right {b, c}   ->   a, b, b_right, c
//	Select(a, b_right)  =>  left needs {a}, right needs {b}
//	                    =>  left no longer has "b"
//	                    =>  right's "b" is no longer suffixed
//	                    =>  the output column is now "b", and Select(b_right) fails
//
// Resolution succeeds and then the schema does not — exactly defect P2's
// signature. So a left column that CAUSES a suffix is kept alive even when nothing
// reads it. In the common no-collision case the set is empty and costs nothing.
// pushdownUnpivot narrows the input of a melt.
//
// On is always required in full and is never prunable: it sets the row count — k
// output rows per input row — and the value column's type is the promotion of every
// On field, so dropping one changes both the height and the schema.
//
// # The explicit and implicit Index cases are not the same, and the join's shape is
// WRONG for the first
//
// pushdownJoin iterates the layout and keeps what `required` asks for. Doing that
// here with an explicit Index prunes an index column nobody selected — and then
// Schema() fails with `unknown index column`, because IndexColumns returns the named
// list verbatim without consulting the input. Resolution succeeds and then the
// schema does not, which is defect P2's signature one node over. So an explicit
// Index is required in full: the node NAMES those columns, therefore it reads them.
//
// # Why the implicit case is safe, which is a proof rather than a hope
//
// With no Index the index columns are computed from the input schema, so narrowing
// the input narrows the output. That is sound because every one of Schema()'s
// failure modes is MONOTONE under pruning:
//
//	unknown index column   vacuous — the list IS the surviving columns
//	unknown On column      On is kept in full, so unchanged
//	both melted and kept   `seen` shrinks, so it fires strictly less often
//	no common value type   On is kept in full, so unchanged
//	duplicate output name  pruning removes names; a collision can only disappear
//
// The last line is the argument the RowIndex arm above already makes. And the
// columns that disappear are exactly the ones `required` did not name, so nobody
// can observe the difference — Apply seeds the root with "everything it currently
// produces", which is what stops this narrowing the user's own output.
//
// The residual, stated because it is contractual rather than structural: everywhere
// else in this rule an under-stated `required` fails loudly at a Scan. Here it would
// fail at the Unpivot's own Schema().
func pushdownUnpivot(u *Unpivot, required map[string]struct{}) (Node, bool, error) {
	in, err := u.Input.Schema()
	if err != nil {
		return nil, false, err
	}

	need := make(map[string]struct{}, len(required))
	for _, name := range u.On {
		need[name] = struct{}{}
	}
	if len(u.Index) > 0 {
		for _, name := range u.Index {
			need[name] = struct{}{}
		}
	} else {
		for _, name := range u.IndexColumns(in) {
			if _, want := required[name]; want {
				need[name] = struct{}{}
			}
		}
	}

	child, changed, err := pushdown(u.Input, need)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		return u, false, nil
	}
	return u.WithChildren([]Node{child}), true, nil
}

func pushdownJoin(j *Join, required map[string]struct{}) (Node, bool, error) {
	layout, err := j.Layout()
	if err != nil {
		return nil, false, err
	}
	ls, err := j.Left.Schema()
	if err != nil {
		return nil, false, err
	}
	rs, err := j.Right.Schema()
	if err != nil {
		return nil, false, err
	}

	leftNeed := map[string]struct{}{}
	rightNeed := map[string]struct{}{}

	// Iterate the LAYOUT and test membership in required — never iterate required
	// and look it up — so a stray name is ignored rather than an error. pushdownScan
	// already has this shape.
	for i, jc := range layout.Columns {
		if _, want := required[layout.Schema.Field(i).Name]; !want {
			continue
		}
		switch jc.Side {
		case FromRight:
			rightNeed[rs.Field(jc.Index).Name] = struct{}{}
		default:
			leftNeed[ls.Field(jc.Index).Name] = struct{}{}
			if jc.CoalesceWith >= 0 {
				// A merged key reads BOTH sides: Right takes its value from the
				// right column, and every kind needs it for the comparison.
				rightNeed[rs.Field(jc.CoalesceWith).Name] = struct{}{}
			}
		}
	}

	// Keys are read even when they are not emitted — which is the whole of what a
	// semi join needs from its right side. Left keys name left columns and right
	// keys name right columns, so they are routed separately; unioning
	// Expressions(*Join) here would ask each side for the other's columns.
	for _, e := range j.LeftOn {
		for _, n := range expr.RootNames(e) {
			leftNeed[n] = struct{}{}
		}
	}
	for _, e := range j.RightOn {
		for _, n := range expr.RootNames(e) {
			rightNeed[n] = struct{}{}
		}
	}

	// The residual reads columns from BOTH sides, and it is the one place the
	// paragraph above about semi joins stops being true: "a SEMI or ANTI join emits
	// no right column at all, so the right side needs only the key columns" is
	// exactly the optimisation that would DELETE what the residual reads.
	//
	// Its names are in the PAIR namespace, so a right column may carry the suffix
	// and must be translated back before it means anything to the right input. The
	// pair layout is the only thing that knows which — the same argument
	// rewriteForSide makes for predicate pushdown.
	if j.HasResidual() {
		pair, err := j.PairLayout()
		if err != nil {
			return nil, false, err
		}
		for _, e := range j.Residual {
			for _, n := range expr.RootNames(e) {
				i := pair.Schema.IndexOf(n)
				if i < 0 {
					// Not a pair column. Nothing can prune what it does not
					// recognise, so keep both sides whole rather than guess.
					return j, false, nil
				}
				if jc := pair.Columns[i]; jc.Side == FromRight {
					rightNeed[rs.Field(jc.Index).Name] = struct{}{}
				} else {
					leftNeed[ls.Field(jc.Index).Name] = struct{}{}
				}
			}
		}
	}

	// The naming-stability clause.
	for n := range rightNeed {
		if ls.Has(n) {
			leftNeed[n] = struct{}{}
		}
	}

	left, lc, err := pushdown(j.Left, leftNeed)
	if err != nil {
		return nil, false, err
	}
	right, rc, err := pushdown(j.Right, rightNeed)
	if err != nil {
		return nil, false, err
	}
	if !lc && !rc {
		return j, false, nil
	}
	return j.WithChildren([]Node{left, right}), true, nil
}

// requiredOf is the set of input columns a list of expressions reads.
//
// RootNames walks with expr.Walk and collects every *Col, so it sees through Agg,
// Window and Call alike — which is what lets one helper serve the three arms whose
// required set is "exactly what my own expressions read".
func requiredOf(es []expr.Node) map[string]struct{} {
	out := make(map[string]struct{}, len(es))
	for _, e := range es {
		for _, name := range expr.RootNames(e) {
			out[name] = struct{}{}
		}
	}
	return out
}

// withColumnsDefs is the output name each WithColumns expression defines.
//
// Through WalkWithColumns rather than by re-deriving names, because that function is
// already the single definition of the running-schema walk and its own doc records
// that the walk had been written four times with two copies diverged.
func withColumnsDefs(in *dtype.Schema, es []expr.Node) ([]string, error) {
	out := make([]string, 0, len(es))
	_, err := WalkWithColumns(in, es, func(_ expr.Node, f dtype.Field, _ int, _ *dtype.Schema) error {
		out = append(out, f.Name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
