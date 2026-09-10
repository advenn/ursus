// Package plan is ursus's logical plan IR.
//
// A plan is a tree of relational operations — scan, filter, project — over
// expressions. It is what the optimizer rewrites and what the physical planner
// lowers. It is deliberately a separate IR from expr: expressions describe
// column-level computation, plans describe row-set-level dataflow, and keeping
// them apart is what lets a rule like projection pushdown reason about columns
// without understanding arithmetic.
//
// # Two invariants
//
// IMMUTABLE. Nothing ever mutates a Node or a slice reachable from one. Rewrites
// allocate. This is what makes `base.Filter(x)` and `base.Filter(y)` independent
// even though they share a subtree, and it is what lets the optimizer keep the
// pre-rewrite tree around for Explain.
//
// SCHEMA WITHOUT DATA. Every node can compute its output schema from its
// children's schemas alone. That is the property behind CollectSchema, plan-time
// type errors, and projection pushdown. It only holds for a RESOLVED plan — see
// Resolve.
//
// # Resolved vs unresolved
//
// A freshly built plan may contain multi-column expressions (`All()`,
// `ColDType(Float64)`) whose meaning depends on the schema. Resolve walks the tree
// bottom-up, expands them, and type-checks. Everything downstream — Schema, the
// optimizer, the physical planner — assumes a resolved plan and may treat
// anything else as an internal error.
package plan

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// Node is one relational operation.
//
// Sealed by the unexported planNode method, so a type switch over Node is
// exhaustive by construction. The package is internal, but the identifier is
// exported because ursus, ursus/sql and the physical planner all consume it.
type Node interface {
	// Schema returns the node's output schema. Valid only on a resolved plan;
	// it never reads data.
	Schema() (*dtype.Schema, error)

	// Children returns the input nodes, in order.
	Children() []Node

	// WithChildren returns a copy of this node with new children. It is how every
	// rewrite is expressed: no rule ever mutates a node in place.
	WithChildren([]Node) Node

	// Label is the single-line description used by Explain, without children.
	Label() string

	planNode()
}

// Source is a thing a Scan can read from.
//
// This is ursus's TableProvider equivalent, and it is public from the start
// because "plug in your own source" is a first-class use case, not an
// afterthought — an engine whose only inputs are the file formats its authors
// happened to implement is much less useful than one you can point at your own
// storage.
//
// Note Schema takes a context: resolving a Parquet footer or inferring a CSV
// schema is real I/O, and a schema request that cannot be cancelled would be a
// hole in the cancellation story.
type Source interface {
	// Name identifies the source in Explain output, e.g. "parquet" or "memory".
	Name() string

	// Describe is a short human-readable detail line, e.g. a path or a row count.
	Describe() string

	// Schema returns the full schema of the source, before any projection.
	Schema(ctx context.Context) (*dtype.Schema, error)

	// Caps reports which pushdowns the source can honour itself.
	Caps() Caps
}

// Caps describes what a source can do for itself.
//
// A source that cannot honour a pushdown is not penalised: the physical planner
// inserts the equivalent operator above the scan. The logical plan always records
// the projection and predicate regardless, so Explain shows the user's intent and
// the optimizer's reasoning stays source-independent.
//
// Predicate support is deliberately NOT here — see PredicatePushdown. A single
// bool cannot express it correctly, because whether a source can honour a
// predicate depends on the predicate.
type Caps struct {
	// Projection: the source can return a subset of columns.
	Projection bool
}

// Pushdown says how completely a source can honour ONE predicate conjunct.
//
// # Why this is not a bool
//
// The obvious `Caps.Predicate bool` is wrong in two independent ways, and both
// return wrong ROWS rather than an error.
//
// First, capability is per-conjunct. In `x > 5 AND lower(y) = 'z'` a Parquet
// source can use the first against row-group statistics and can do nothing with
// the second. Filter.Preds is already a conjunct slice precisely so that
// conjuncts move independently; a single bool forces an all-or-nothing decision
// over a set whose members differ.
//
// Second, and worse: honouring a predicate is not the same as SATISFYING it.
// Row-group pruning skips groups that provably contain no match, but every
// surviving group still holds non-matching rows. A source that reports "yes I
// handle predicates" and is then trusted to have applied them exactly returns
// those extra rows to the user. Silent, data-dependent, and invisible on any
// fixture small enough to fit in one row group.
type Pushdown uint8

const (
	// Unsupported: the source cannot use this conjunct. The Filter stays above
	// the Scan and the conjunct is not recorded on the Scan at all.
	//
	// THIS IS THE ZERO VALUE, deliberately. A short slice, a missing switch arm,
	// a source that forgets to answer — every accident lands here, and the
	// consequence of landing here is a slower plan rather than a wrong one.
	Unsupported Pushdown = iota

	// Inexact: the source may use this conjunct to skip data, but what it returns
	// is a SUPERSET of the matching rows. The conjunct goes into the Scan AND
	// stays in a Filter above it. This is what Parquet row-group pruning is.
	Inexact

	// Exact: the source returns exactly the matching rows, with the same null
	// semantics the engine would apply (a row survives iff the predicate is valid
	// and true). Only then may the Filter be removed.
	//
	// Claim this only if it is true. It is the one value that deletes a correctness
	// check from the plan.
	Exact
)

func (p Pushdown) String() string {
	switch p {
	case Inexact:
		return "inexact"
	case Exact:
		return "exact"
	default:
		return "unsupported"
	}
}

// PredicatePushdown is implemented by a Source that can use predicates itself.
//
// It is optional, in the same way source.Openable is: a Source that does not
// implement it is fully usable and simply gets its filtering done above the scan.
// The optional-interface shape is what makes Unsupported the default for every
// source that has not thought about the question.
type PredicatePushdown interface {
	Source

	// ClassifyPredicates reports, for each conjunct in preds and in the same
	// order, how completely this source can honour it.
	//
	// The returned slice MUST have exactly len(preds) entries. A source that
	// violates this is treated as supporting nothing — see ClassifyPredicates,
	// the package-level helper, which is the only thing the optimizer calls.
	//
	// It must be a pure function of preds: the optimizer may call it more than
	// once, and it runs long before any data is read.
	ClassifyPredicates(preds []expr.Node) []Pushdown
}

// ClassifyPredicates asks src how it can handle each conjunct, failing closed.
//
// Every path that cannot be trusted — a source that does not implement the
// interface, a slice of the wrong length, a value outside the enum — yields
// Unsupported for every conjunct. The rule is that a source must actively and
// correctly claim a capability to get it; nothing is inferred.
func ClassifyPredicates(src Source, preds []expr.Node) []Pushdown {
	out := make([]Pushdown, len(preds)) // all Unsupported

	pp, ok := src.(PredicatePushdown)
	if !ok {
		return out
	}
	got := pp.ClassifyPredicates(preds)
	if len(got) != len(preds) {
		return out // contract violation: trust none of it
	}
	for i, p := range got {
		switch p {
		case Exact, Inexact:
			out[i] = p
		default:
			// Unsupported, or a value from a newer enum this build does not know.
		}
	}
	return out
}

// --- traversal ---------------------------------------------------------------

// Walk calls fn for every node, parents before children. Returning false stops
// descent into that subtree.
func Walk(n Node, fn func(Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for _, c := range n.Children() {
		Walk(c, fn)
	}
}

// TransformUp rewrites the tree bottom-up, applying fn to each node after its
// children have been rewritten. This is the shape almost every rule wants: a rule
// that changes a child's schema should see the change before it processes the
// parent.
func TransformUp(n Node, fn func(Node) (Node, error)) (Node, error) {
	kids := n.Children()
	if len(kids) > 0 {
		newKids := make([]Node, len(kids))
		changed := false
		for i, c := range kids {
			nk, err := TransformUp(c, fn)
			if err != nil {
				return nil, err
			}
			newKids[i] = nk
			if nk != c {
				changed = true
			}
		}
		if changed {
			n = n.WithChildren(newKids)
		}
	}
	return fn(n)
}

// Expressions returns every expression a node holds directly.
//
// # What actually consumes it
//
// The column-liveness SAFETY TEST, and nothing else. It used to claim projection
// pushdown as a consumer; that rule reads t.Exprs, t.Preds, t.Keys and the join's
// two key lists directly, and the WARNING on the *Join arm below explains why it
// must — unioning both sides here would ask each side for the other's columns.
//
// It still has to gain an arm when a node type gains an expression slot, or the
// liveness check silently stops seeing under that node. That is not hypothetical:
// *Window went four steps without one.
func Expressions(n Node) []expr.Node {
	switch t := n.(type) {
	case *Scan:
		return t.Predicate
	case *Filter:
		return t.Preds
	case *Project:
		return t.Exprs
	case *WithColumns:
		return t.Exprs
	case *Aggregate:
		return append(append([]expr.Node(nil), t.Keys...), t.Aggs...)
	case *MergeSorted:
		// The key is a column-reference slot in string form, like Distinct.Subset. It
		// must appear here or a liveness rule will prune the column the merge orders
		// on — the failure that arm's own comment records.
		return []expr.Node{t.mergeKeyExpr()}
	case *AsOfJoin:
		// The ordering key and the by-keys on BOTH sides. Like *Join's arm, the
		// result carries no side attribution — rule_projection reads the fields
		// directly for exactly that reason.
		out := []expr.Node{t.LeftOn, t.RightOn}
		out = append(out, t.LeftBy...)
		return append(out, t.RightBy...)
	case *TemporalGroup:
		// The INDEX must be here too, not just the keys and aggregates. It is the
		// column the windows are cut from, so a liveness rule that missed it would
		// prune the one column the operator cannot run without.
		out := append([]expr.Node{t.Index}, t.Keys...)
		return append(out, t.Aggs...)
	case *Sort:
		out := make([]expr.Node, len(t.Keys))
		for i, k := range t.Keys {
			out[i] = k.Expr
		}
		return out
	case *Distinct:
		// Subset is a column-reference slot in string form. It must appear here or
		// the first liveness rule that trusts Expressions will prune a column the
		// dedup compares on.
		out := make([]expr.Node, len(t.Subset))
		for i, name := range t.Subset {
			out[i] = &expr.Col{Name: name}
		}
		return out

	case *Explode, *Unnest:
		// Both name their columns as strings, which is Distinct.Subset's case: a
		// liveness rule that only walks expressions cannot see them, and a scan
		// below would prune the very column being exploded or unnested.
		//
		// They went without an arm from steps 30 and 35 until step 48, and it cost
		// nothing only because neither had a projection-pushdown arm either — so
		// the rule's fail-safe default kept every column alive. Teaching one to
		// prune without the other is what would have made it a defect.
		var names []string
		switch t := n.(type) {
		case *Explode:
			names = t.Columns
		case *Unnest:
			names = t.Columns
		}
		out := make([]expr.Node, len(names))
		for i, name := range names {
			out[i] = &expr.Col{Name: name}
		}
		return out

	case *Unpivot:
		// On and Index are Distinct.Subset's problem exactly: column references in
		// string form, invisible to a liveness rule that only walks expressions.
		//
		// Index is deliberately NOT expanded to its default here. An empty Index
		// means "everything else", so there is nothing this could name that the
		// input does not already have to keep — and naming the input's whole schema
		// would defeat the pushdown it is meant to inform.
		out := make([]expr.Node, 0, len(t.On)+len(t.Index))
		for _, name := range t.On {
			out = append(out, &expr.Col{Name: name})
		}
		for _, name := range t.Index {
			out = append(out, &expr.Col{Name: name})
		}
		return out
	case *Window:
		// The arm that was missing, and its absence made a SAFETY TEST VACUOUS:
		// plan_test walks the tree asserting no scan prunes a column read above it,
		// and under a Window it saw nothing at all — so every plan containing a
		// window passed that check for free.
		return t.Exprs

	case *Join:
		// Both sides' keys, flattened.
		//
		// WARNING: the result carries NO side attribution, so RootNames over it
		// answers "which names appear anywhere in this node" — its documented
		// purpose, liveness safety — and is WRONG for per-side reasoning. Left keys
		// name left columns and right keys name right columns, so a rule that hands
		// this set to both children asks each side for columns it does not have.
		// rule_projection reads LeftOn and RightOn directly for exactly that reason.
		//
		// The residual belongs here too, and it is the one part with no side
		// attribution even in principle: it is written in the PAIR namespace, where
		// a right column may carry a suffix that exists in neither input. Liveness
		// safety only needs "which names appear anywhere", which is what this is.
		out := append(append([]expr.Node(nil), t.LeftOn...), t.RightOn...)
		return append(out, t.Residual...)
	default:
		return nil
	}
}
