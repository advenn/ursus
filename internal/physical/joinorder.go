package physical

import (
	"context"
	"slices"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// A join's order (step 314, J1 of v0.6.7-scope.md): Polars' maintain_order.
//
// # In memory, as it goes
//
// A join probes its table in the probe side's order and gives each probe row its
// matches in the table's, the build side's order, and a Right or Full join then
// flushes the table's unmatched rows in that order. So a join that fits keeps the
// left order as it runs, and keeps the right one with its inputs exchanged: the right
// input probes a table of the left (planJoin, swappedLayout). Nothing is sorted.
//
// # Spilled, sorted back
//
// A join whose table spills answers the probe rows of its resident buckets as they
// come, and each spilled bucket's after the probe side ends, so its rows leave in
// bucket order. Whether it spilled is settled when the build side ends, before a
// probe row is read, so then, and only then, orderedProbe numbers the probe rows,
// joins, and sorts the rows by that number, a stable sort that spills as any sort
// does. A probe row's rows are emitted together and in the table's order in every
// bucket, so its number alone orders them. A Right or Full join's unmatched table
// rows have no probe row, and each bucket flushes its own, so an ordered join of
// those kinds numbers its table's rows as they arrive, in planJoin, and the sort
// takes that number second.
//
// A cross join that spills keeps its order anyway, a block nested loop in the
// probe's order (crossspill.go), and is not sorted.

// swappedKind is the kind of a join with its inputs exchanged.
func swappedKind(k plan.JoinKind) plan.JoinKind {
	switch k {
	case plan.JoinLeft:
		return plan.JoinRight
	case plan.JoinRight:
		return plan.JoinLeft
	}
	return k
}

// swappedValidation names the same unique sides with the inputs exchanged.
func swappedValidation(v plan.JoinValidation) plan.JoinValidation {
	switch v {
	case plan.ValidateOneToMany:
		return plan.ValidateManyToOne
	case plan.ValidateManyToOne:
		return plan.ValidateOneToMany
	}
	return v
}

// swappedLayout is a join's layout for its inputs exchanged: each column read from
// the other input, and a merged key read from the right input's key, merged with the
// left's. The output is the join's own.
func swappedLayout(l *plan.JoinLayout) *plan.JoinLayout {
	cols := make([]plan.JoinColumn, len(l.Columns))
	for i, c := range l.Columns {
		switch {
		case c.Side == plan.FromRight:
			cols[i] = plan.JoinColumn{Side: plan.FromLeft, Index: c.Index, CoalesceWith: -1}
		case c.CoalesceWith >= 0:
			cols[i] = plan.JoinColumn{Side: plan.FromLeft, Index: c.CoalesceWith, CoalesceWith: c.Index}
		default:
			cols[i] = plan.JoinColumn{Side: plan.FromRight, Index: c.Index, CoalesceWith: -1}
		}
	}
	return &plan.JoinLayout{Schema: l.Schema, Columns: cols, KeyTypes: l.KeyTypes}
}

// ordinalOp appends each row's place in its input, from 0, an Int64.
type ordinalOp struct {
	child  Operator
	schema *dtype.Schema
	next   int64
}

func (o *ordinalOp) Schema() *dtype.Schema { return o.schema }
func (o *ordinalOp) Close() error          { return o.child.Close() }

func (o *ordinalOp) Next(ctx context.Context) (*data.Batch, error) {
	b, err := o.child.Next(ctx)
	if err != nil {
		return nil, err
	}
	n := b.Rows()
	ord := make([]int64, n)
	for i := range ord {
		ord[i] = o.next + int64(i)
	}
	o.next += int64(n)
	cols := append(slices.Clone(b.Columns()), data.NewFixed(o.schema.Field(o.schema.Len()-1).Name,
		dtype.Int64, ord, bitmap.AllSet(n)))
	return data.NewBatch(o.schema, cols)
}

// withOrdinal numbers op's rows in a last column, named apart from every name in
// taken, and gives the column's name.
func withOrdinal(op Operator, s *dtype.Schema, taken []string, tag string) (Operator, *dtype.Schema, string, error) {
	name := hiddenName(append(taken, s.Names()...), "__"+tag+"_ord")
	out, err := dtype.NewSchema(append(s.FieldSlice(), dtype.Of(name, dtype.Int64))...)
	if err != nil {
		return nil, nil, "", err
	}
	return &ordinalOp{child: op, schema: out}, out, name, nil
}

// hiddenName is base, or base with a number after it, whichever no name in taken is.
func hiddenName(taken []string, base string) string {
	name := base
	for i := 1; slices.Contains(taken, name); i++ {
		name = base + itoa(i)
	}
	return name
}

// wroteBuild reports whether the build side wrote rows to disk, so that the probe
// will replay buckets: spilled, asked before freeze, which is where the files are
// closed and spilled starts to see them.
func (s *joinBuildSink) wroteBuild() bool {
	for p := range s.bparts {
		if s.bparts[p] != nil || s.bfiles[p] != "" {
			return true
		}
	}
	return false
}

// orderedProbe is Probe for an ordered join whose table spilled: the probe rows
// numbered, joined, and the rows sorted back into the order by the probe rows'
// numbers, then the table rows' for a Right or Full join's unmatched ones, and the
// numbers dropped.
func (s *joinBuildSink) orderedProbe(ctx context.Context, probe Operator, opts Options) (Operator, error) {
	declared := s.out
	taken := append(append(s.left.Names(), s.right.Names()...), s.out.Names()...)
	probe, left, ord, err := withOrdinal(probe, s.left, taken, "probe")
	if err != nil {
		return nil, err
	}
	cols := append(slices.Clone(s.layout.Columns),
		plan.JoinColumn{Side: plan.FromLeft, Index: left.Len() - 1, CoalesceWith: -1})
	fields := append(s.out.FieldSlice(), dtype.Of(ord, dtype.Int64))
	keys := []plan.SortKey{{Expr: &expr.Col{Name: ord}, NullsLast: true}}
	if s.buildOrd != "" {
		cols = append(cols, plan.JoinColumn{Side: plan.FromRight, Index: s.right.IndexOf(s.buildOrd), CoalesceWith: -1})
		fields = append(fields, dtype.Of(s.buildOrd, dtype.Int64))
		keys = append(keys, plan.SortKey{Expr: &expr.Col{Name: s.buildOrd}})
	}
	out, err := dtype.NewSchema(fields...)
	if err != nil {
		return nil, err
	}
	s.left, s.out = left, out
	s.layout = &plan.JoinLayout{Schema: out, Columns: cols, KeyTypes: s.layout.KeyTypes}

	op, err := s.Probe(ctx, probe)
	if err != nil {
		return nil, err
	}
	kSch, err := keySchema(keys, out)
	if err != nil {
		op.Close()
		return nil, err
	}
	sorted := &breaker{child: op, sink: &sortSink{schema: out, keys: keys, kSch: kSch, budget: opts.Budget,
		mem: opts.Budget.Account("join"), batch: opts.batchSize(), maxHeld: opts.Budget.Limit() / 4}}
	return &dropTailOp{child: sorted, schema: declared}, nil
}

// dropTailOp gives its child's batches without their last columns, those past
// schema's.
type dropTailOp struct {
	child  Operator
	schema *dtype.Schema
}

func (o *dropTailOp) Schema() *dtype.Schema { return o.schema }
func (o *dropTailOp) Close() error          { return o.child.Close() }

func (o *dropTailOp) Next(ctx context.Context) (*data.Batch, error) {
	b, err := o.child.Next(ctx)
	if err != nil {
		return nil, err
	}
	return data.NewBatch(o.schema, b.Columns()[:o.schema.Len()])
}
