package plan

import (
	"time"

	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
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

// checkIndexResolution refuses a window an index column cannot represent.
//
// IsTemporal is not enough, and the gap between the two is a family of silent wrong
// answers. A Date index is int32 DAYS, so Every("1h") asks for twenty-four windows
// per day and dtype.FromTime floors every one of them to the same day number. What
// comes out then depends on the Closed convention and all four are wrong differently:
// the default duplicates the group twenty-four times with twenty-three empty,
// ClosedBoth counts every row about twenty-four times, and ClosedNone drops every row.
//
// Rolling is worse to read, because nothing looks wrong: it never consults Every, so
// only Period can be too fine, and a one-hour window silently becomes a one-day one.
// A sub-day Offset is the mildest — it moves the grid by a whole tick rather than
// being inert.
//
// This is the refusal nodes_asof.go makes for a calendar tolerance on a key that is
// not an instant, and its comment applies verbatim with "grid" for "bound": without
// it the interval went silently INERT.
//
// # It is deliberately NOT what dt.truncate does
//
// truncateTemporal ACCEPTS an interval finer than the tick, as a no-op, because
// "every instant is already on a boundary". That is right there and wrong here, and
// the difference is what the answer is made of: truncate's answer is the input, so a
// no-op is the correct floor. A grid's answer is a window COUNT, and flooring
// twenty-four windows onto one tick does not make one window, it makes twenty-four
// indistinguishable ones.
func (t *TemporalGroup) checkIndexResolution(idx dtype.Field) error {
	// A Duration is temporal and is not an INSTANT, so there is nothing to cut
	// windows from. IsTemporal admits it, the operator buffers the whole input, and
	// ToTime then refuses with an internal error that asks the user to report a bug
	// in ursus for what is an ordinary type mistake.
	if idx.Type.ID() == dtype.TypeDuration {
		return uerr.New(uerr.KindType, t.op(),
			"the index column %q is %s, which is a span rather than an instant",
			idx.Name, idx.Type).
			Hint("a window grid is cut from points on a timeline; a Duration has no " +
				"position on one").
			Hint("group by a Date or Datetime column, or cast this one first")
	}

	npt, ok := idx.Type.NanosPerTick()
	if !ok || npt <= 0 {
		return nil // no tick length to compare against; nothing to say
	}
	for _, iv := range []struct {
		name string
		v    dtype.Interval
	}{{"every", t.Every}, {"period", t.Period}, {"offset", t.Offset}} {
		if iv.v.IsZero() {
			continue // period defaults to every, and offset is optional
		}
		// The other direction, and truncateOut's refusal word for word: a Time is a
		// wall clock with no date, so a month has nothing to measure from and the
		// whole column truncates to one instant.
		if iv.v.IsCalendar() && idx.Type.ID() == dtype.TypeTime {
			return uerr.New(uerr.KindType, t.op(),
				"%s=%s is a calendar interval and the index column %q is %s",
				iv.name, iv.v, idx.Name, idx.Type).
				Hint("a Time has no date, so it cannot be cut into days or months").
				Hint("use a sub-day interval, or cast the index to Datetime first")
		}
		// No IsCalendar exclusion on the comparison below, because it would not
		// change an answer: a PURE calendar interval carries no nanoseconds, so the
		// n > 0 guard already skips it. A MIXED one — Offset("1mo12h") — is checked
		// on its sub-day part, which is right: twelve hours is exactly as
		// unrepresentable on a Date index alone as it is beside a month.
		if n := iv.v.Nanos(); n > 0 && n < npt {
			return uerr.New(uerr.KindType, t.op(),
				"%s=%s is finer than the index column %q, which is %s and stores "+
					"whole ticks of %s", iv.name, iv.v, idx.Name, idx.Type,
				dtype.FromDuration(time.Duration(npt))).
				Hint("every window would floor onto the same tick, so the grid " +
					"would have many windows that cannot be told apart").
				Hint("use an interval no finer than the index's own resolution, or " +
					"cast the index to a finer type first")
		}
	}
	return nil
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
	if err := t.checkIndexResolution(idx); err != nil {
		return nil, err
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
