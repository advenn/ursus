package plan

import (
	"strings"

	"ursus/dtype"
	"ursus/internal/expr"
	"ursus/internal/uerr"
)

// TemporalGroup is GroupByDynamic and Rolling, which are one node because they are
// one algorithm with two window generators.
//
// # Both reduce to "aggregate a RANGE of rows"
//
// The naive reading is alarming: one row can belong to several windows, and every
// aggregate operator here indexes group state by row. But both operators require the
// index column SORTED, and that changes the shape completely —
//
//	If rows are ordered by the index, every temporal window covers a CONTIGUOUS RUN
//	of rows. A window is not a set of row ids; it is a pair (lo, hi).
//
// — which is what lets one mechanism serve both, and lets both reuse every existing
// accumulator unchanged. All nineteen aggregates work in a temporal window on the day
// this node lands, including the two that refuse to spill, because nothing about
// kernel.Accumulator moves.
//
// # Where they differ
//
//	GroupByDynamic  windows come from a GRID: offset + k*every, each period long.
//	                They exist even when no row falls in them, which is the whole
//	                difference from GroupBy(truncate(ts, every)) and the reason a
//	                gap in a time series shows up as a zero rather than as nothing.
//
//	Rolling         one window per ROW, ending at that row's own timestamp and
//	                reaching back by period. Output height equals input height.
//
// # It is a pipeline breaker, and the Explain sketch in the API doc is aspirational
//
// ursus-api.md shows GROUP_BY_DYNAMIC as `streaming: yes`, which is achievable on a
// sorted index — a window can be emitted as soon as the input passes its end. This
// implementation buffers instead, because the grid's extent is derived from the
// observed [min, max] and because that is what makes empty windows fall out for free.
// The cost is O(rows), the same as windowSink's and for the same reason.
type TemporalGroup struct {
	Input Node

	// Index is the temporal column the windows are cut from. It must resolve to a
	// Date or Datetime, and the operator verifies at run time that it arrives sorted.
	Index expr.Node

	// Keys are extra CATEGORICAL partitions — DynamicOptions.GroupBy. Windows are cut
	// independently inside each one, so a per-service hourly rollup is one node.
	Keys []expr.Node
	Aggs []expr.Node

	// Rolling picks the window generator: false is the grid, true is one per row.
	Rolling bool

	Every, Period, Offset dtype.Interval
	Closed                expr.Closed
}

func (t *TemporalGroup) planNode()        {}
func (t *TemporalGroup) Children() []Node { return []Node{t.Input} }

func (t *TemporalGroup) WithChildren(kids []Node) Node {
	if len(kids) != 1 {
		panic("plan: TemporalGroup takes exactly one child")
	}
	c := *t
	c.Input = kids[0]
	return &c
}

// Schema is the index column, then the categorical keys, then the aggregates — the
// same shape as Aggregate's, with the index taking the place of the first key.
//
// The index keeps its own name and type: for a dynamic group it holds the window's
// start instant, and for a rolling group the row's own instant. Both are values of
// the column the windows were cut from, so neither changes type.
func (t *TemporalGroup) Schema() (*dtype.Schema, error) {
	in, err := t.Input.Schema()
	if err != nil {
		return nil, err
	}

	fields := make([]dtype.Field, 0, 1+len(t.Keys)+len(t.Aggs))
	idx, err := expr.Resolve(t.Index, in)
	if err != nil {
		return nil, uerr.Annotate(err, t.op(), "TemporalGroup")
	}
	if !idx.Type.IsTemporal() {
		return nil, uerr.New(uerr.KindType, t.op(),
			"the index column %q is %s, which is not a temporal type",
			idx.Name, idx.Type).
			Hint("group by time needs a Date or Datetime column to cut windows from")
	}
	// Never null: a window start always exists, and a row whose index is null belongs
	// to no window at all — which the operator refuses rather than silently dropping.
	idx.Nullable = false
	fields = append(fields, idx)

	for _, k := range t.Keys {
		f, err := expr.Resolve(k, in)
		if err != nil {
			return nil, uerr.Annotate(err, t.op(), "TemporalGroup")
		}
		fields = append(fields, f)
	}
	for _, e := range t.Aggs {
		f, err := expr.Resolve(e, in)
		if err != nil {
			return nil, uerr.Annotate(err, "agg", "TemporalGroup")
		}
		fields = append(fields, f)
	}
	return dtype.NewSchema(fields...)
}

func (t *TemporalGroup) op() string {
	if t.Rolling {
		return "rolling"
	}
	return "group_by_dynamic"
}

func (t *TemporalGroup) Label() string {
	var b strings.Builder
	if t.Rolling {
		b.WriteString("ROLLING [")
	} else {
		b.WriteString("GROUP_BY_DYNAMIC [")
	}
	b.WriteString(t.Index.String())
	if len(t.Keys) > 0 {
		b.WriteString(", ")
		b.WriteString(expr.StringAll(t.Keys))
	}
	b.WriteString("] ")
	if !t.Rolling {
		b.WriteString("every=")
		b.WriteString(t.Every.String())
		b.WriteString(" ")
	}
	b.WriteString("period=")
	b.WriteString(t.Period.String())
	if !t.Offset.IsZero() {
		b.WriteString(" offset=")
		b.WriteString(t.Offset.String())
	}
	b.WriteString(" closed=")
	b.WriteString(t.Closed.String())
	b.WriteString(" -> [")
	b.WriteString(expr.StringAll(t.Aggs))
	b.WriteString("]")
	return b.String()
}
