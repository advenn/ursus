package plan

import (
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// --- WithColumns -------------------------------------------------------------

// WithColumns adds or replaces columns, keeping everything else.
//
// It is distinct from Project rather than sugar over `Project(All(), newcols...)`
// because the replace-in-place semantics matter: recomputing an existing column
// must leave it in its original position, not move it to the end. Users notice
// column order, and a rewrite that reshuffles it on every recomputation is
// endlessly annoying.
type WithColumns struct {
	Input Node
	Exprs []expr.Node
}

func (w *WithColumns) planNode()        {}
func (w *WithColumns) Children() []Node { return []Node{w.Input} }

func (w *WithColumns) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: WithColumns takes exactly one child")
	}
	c := *w
	c.Input = kids[0]
	return &c
}

func (w *WithColumns) Schema() (*dtype.Schema, error) {
	in, err := w.Input.Schema()
	if err != nil {
		return nil, err
	}
	return WalkWithColumns(in, w.Exprs, nil)
}

// WalkWithColumns performs the running-schema walk that defines WithColumns.
//
// Each expression resolves against the schema produced by the ones before it, so
// `WithColumns(a.Alias("x"), Col("x").Mul(2))` works. A name that already exists is
// REPLACED IN PLACE, keeping its column position — users notice column order, and
// recomputing a column should not move it to the end.
//
// This exists as one function because it had been written out four separate times
// (Schema, Resolve, physical planning, predicate pushdown) and two of those copies
// had already diverged: one resolved against the input schema and one against the
// running schema, so a self-referencing WithColumns resolved successfully and then
// failed its own Schema(). Four copies of a subtle walk is four chances to be wrong.
//
// visit, if non-nil, is called for each expression with its resolved field and the
// position it replaces (or -1 when it appends).
func WalkWithColumns(in *dtype.Schema, exprs []expr.Node,
	visit func(e expr.Node, f dtype.Field, pos int, after *dtype.Schema) error,
) (*dtype.Schema, error) {
	running := in
	for _, e := range exprs {
		f, err := expr.Resolve(e, running)
		if err != nil {
			return nil, uerr.Annotate(err, "with_columns", "WithColumns")
		}
		fields := running.FieldSlice()
		pos := running.IndexOf(f.Name)
		if pos >= 0 {
			fields[pos] = f
		} else {
			fields = append(fields, f)
		}
		next, err := dtype.NewSchema(fields...)
		if err != nil {
			return nil, err
		}
		if visit != nil {
			if err := visit(e, f, pos, next); err != nil {
				return nil, err
			}
		}
		running = next
	}
	return running, nil
}

func (w *WithColumns) Label() string {
	return "WITH_COLUMNS [" + expr.StringAll(w.Exprs) + "]"
}

// --- Sort --------------------------------------------------------------------

// SortKey is one component of an ordering.
//
// An alias rather than a second declaration: a window carries its own ordering and
// internal/expr may not import internal/plan, so the type has to live at the lower
// level. Two structurally identical types would be two places for the null-placement
// rule to drift apart.
type SortKey = expr.OrderKey

// Sort orders rows.
//
// This is a PIPELINE BREAKER: it must see every input row before it can emit the
// first output row. It is therefore an Operator rather than a BatchOp, and it is
// where memory pressure first becomes real — which is why the streaming engine's
// external merge sort has this node as its target.
type Sort struct {
	Input Node
	Keys  []SortKey
	// Limit > 0 means only the first Limit rows are needed, which turns a full
	// sort into a bounded top-k. Set by the limit-pushdown rule.
	Limit int
}

func (s *Sort) planNode()        {}
func (s *Sort) Children() []Node { return []Node{s.Input} }

func (s *Sort) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Sort takes exactly one child")
	}
	c := *s
	c.Input = kids[0]
	return &c
}

// WithLimit returns a copy of the sort carrying a row-count bound.
//
// Mirrors Scan.WithMaxRows, and unlike it this bound is EXACT rather than a hint:
// sortSink honours it by switching to ArgTopK, whose contract is that it returns
// the same indices ArgSort would, in the same order, including for ties. So the
// enclosing Limit becomes redundant rather than load-bearing — but it stays, for
// the same reason limitPushdown leaves it above a Scan: the rule is a performance
// rewrite and nothing about correctness should depend on it having fired.
func (s *Sort) WithLimit(n int) *Sort {
	c := *s
	c.Limit = n
	return &c
}

// Schema is the input schema: sorting reorders rows and never changes columns.
func (s *Sort) Schema() (*dtype.Schema, error) { return s.Input.Schema() }

func (s *Sort) Label() string {
	parts := make([]string, len(s.Keys))
	for i, k := range s.Keys {
		parts[i] = k.String()
	}
	label := "SORT [" + strings.Join(parts, ", ") + "]"
	if s.Limit > 0 {
		label += " top " + strconv.Itoa(s.Limit)
	}
	return label
}

// --- Aggregate ---------------------------------------------------------------

// Aggregate groups rows and reduces each group.
//
// The output is one row per distinct key: the key columns first, in the order
// written, then the aggregate columns.
//
// Also a PIPELINE BREAKER, and the one that motivates Accumulator.Merge: a
// parallel or spilling implementation computes partial aggregates independently
// and combines them, so every aggregate must be expressible as
// (init, step, merge, finish) rather than as a single pass.
type Aggregate struct {
	Input Node
	Keys  []expr.Node
	Aggs  []expr.Node

	// MaintainOrder emits groups in first-appearance order rather than in hash
	// order. Without it the output order is genuinely unspecified, and tests that
	// assume otherwise are flaky.
	//
	// # What it costs, stated as a number rather than as "time and memory"
	//
	// Nothing at all when the aggregation fits in memory: the group table is already
	// built in first-appearance order, so the flag changes no work.
	//
	// When it SPILLS the cost is two things. Every routed row carries an extra int64
	// input ordinal into its partition file, and the result cannot stream — first
	// appearance interleaves resident and spilled groups, so emitting the group
	// created at input row 3 requires partition 9's aggregation to be finished. The
	// whole answer is materialised and permuted at the end, so an ordered spilling
	// group-by peaks at budget + result rather than at budget. The result is
	// O(distinct keys), which is what a non-spilling aggregation holds anyway.
	//
	// BenchmarkGroupBySpillingOrdered beside BenchmarkGroupBySpilling is the measured
	// figure.
	MaintainOrder bool
}

func (a *Aggregate) planNode()        {}
func (a *Aggregate) Children() []Node { return []Node{a.Input} }

func (a *Aggregate) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Aggregate takes exactly one child")
	}
	c := *a
	c.Input = kids[0]
	return &c
}

func (a *Aggregate) Schema() (*dtype.Schema, error) {
	in, err := a.Input.Schema()
	if err != nil {
		return nil, err
	}

	fields := make([]dtype.Field, 0, len(a.Keys)+len(a.Aggs))
	for _, k := range a.Keys {
		f, err := expr.Resolve(k, in)
		if err != nil {
			return nil, uerr.Annotate(err, "group_by", "Aggregate")
		}
		fields = append(fields, f)
	}
	for _, e := range a.Aggs {
		f, err := expr.Resolve(e, in)
		if err != nil {
			return nil, uerr.Annotate(err, "agg", "Aggregate")
		}
		fields = append(fields, f)
	}
	return dtype.NewSchema(fields...)
}

func (a *Aggregate) Label() string {
	var b strings.Builder
	// Render the aggregates themselves, not just how many there are. Golden plan
	// files are the mechanism for catching optimizer regressions, and a label that
	// prints "2 aggregates" makes two structurally different queries indis-
	// tinguishable in the snapshot.
	b.WriteString("AGGREGATE [")
	b.WriteString(expr.StringAll(a.Keys))
	b.WriteString("] -> [")
	b.WriteString(expr.StringAll(a.Aggs))
	b.WriteString("]")
	if a.MaintainOrder {
		b.WriteString(" (ordered)")
	}
	return b.String()
}

// --- Distinct ----------------------------------------------------------------

// Distinct removes duplicate rows.
//
// It shares the hash table and row encoder with Aggregate — a distinct is an
// aggregation with no aggregates — but is a separate node so the optimizer can
// reason about it (it preserves the schema exactly, which Aggregate does not).
// Distinct keeps the first row of each distinct key.
//
// # There is deliberately no MaintainOrder here
//
// There used to be, copied from Aggregate, and it was never assigned, never read
// and not even rendered by Label(). Unlike Sort.Limit — which step 9 found dead and
// WOKE UP, because ArgTopK was waiting behind it — this one had no implementation
// to switch to: distinctOp is a streaming filter that keeps the first row per key
// and emits in input order unconditionally, so `MaintainOrder = false` meant
// nothing. A flag whose only evidence of purpose is its own doc comment is worse
// than no flag, because the next reader believes it.
type Distinct struct {
	Input Node
	// Subset restricts the comparison to these columns. nil means every column.
	Subset []string
}

func (d *Distinct) planNode()        {}
func (d *Distinct) Children() []Node { return []Node{d.Input} }

func (d *Distinct) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: Distinct takes exactly one child")
	}
	c := *d
	c.Input = kids[0]
	return &c
}

func (d *Distinct) Schema() (*dtype.Schema, error) { return d.Input.Schema() }

func (d *Distinct) Label() string {
	if d.Subset == nil {
		return "DISTINCT [*]"
	}
	return "DISTINCT [" + strings.Join(d.Subset, ", ") + "]"
}
