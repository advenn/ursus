package ursus

import (
	"strings"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

// DynamicOptions configures GroupByDynamic.
//
// # Five fields, and three that are refused rather than ignored
//
// Every, Period, Offset, Closed and GroupBy are implemented. Label, StartBy and
// IncludeBoundaries are named in the design docs and are NOT — and they produce an
// error saying so rather than being accepted and dropped.
//
// That is deliberate and it is a lesson this library has already paid for once:
// plan.Aggregate.MaintainOrder sat assigned, rendered and unread for four steps, and
// its own doc had to say "the flag documents the guarantee rather than changing
// behaviour". A field that is accepted and does nothing is worse than one that does
// not exist, because the next reader believes it.
type DynamicOptions struct {
	// Every is where windows START. Consecutive windows are Every apart.
	Every Interval

	// Period is a window's WIDTH. Zero means Every, which gives windows that tile
	// the input exactly. A Period larger than Every gives OVERLAPPING windows, and a
	// row then belongs to several of them — so a Len() over the result exceeds the
	// input's row count, which is the correct answer and surprises everyone once.
	Period Interval

	// Offset shifts the whole grid. A day grid with Offset 9h starts its windows at
	// 09:00 rather than at midnight.
	Offset Interval

	// Closed says which end of a window belongs to it. The zero value is
	// ClosedLeft — [start, end) — which is the only convention under which a grid
	// partitions its input exactly.
	Closed Closed

	// GroupBy adds CATEGORICAL keys. Windows are cut independently inside each
	// distinct combination, so an hourly rollup per service is one call.
	GroupBy []Expr

	// Label is what each window's index column holds: "left", its start, the default;
	// "right", its end, the start plus Period; or "datapoint", the instant of its
	// first row, or its start for a window with no row. Polars' label (step 187).
	Label string

	// StartBy is where the grid begins: "window", Every's own grid, the default;
	// "datapoint", the first row's instant, with Offset ignored, as Polars ignores it;
	// or a weekday, "monday" to "sunday", that a grid of weeks starts on: the one at
	// or before the grid's own first Monday, plus Offset. A weekday on a grid not in
	// weeks is refused: Polars documents it for weeks alone. Polars' start_by.
	StartBy string

	// IncludeBoundaries adds _lower_boundary and _upper_boundary, each window's start
	// and end, before the index column: Polars' include_boundaries.
	IncludeBoundaries bool
}

// RollingOptions configures Rolling.
type RollingOptions struct {
	// Period is how far back each row's window reaches from its own instant.
	Period Interval

	// Offset shifts the window's end away from the row's own instant.
	Offset Interval

	// Closed says which end belongs to the window. The zero value is ClosedRight —
	// (end-period, end] — because a rolling window ends AT the row, so closing the
	// left end instead would drop every row from its own window.
	Closed Closed

	// GroupBy adds categorical keys, as in DynamicOptions.
	GroupBy []Expr
}

// GroupByDynamic groups rows into fixed temporal windows cut from a sorted index.
//
//	lf.GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
//	        Every:   ursus.Every("1h"),
//	        GroupBy: []ursus.Expr{ursus.Col("service")},
//	    }).
//	    Agg(ursus.Len().Alias("n"))
//
// # It is not GroupBy(truncate(ts, every)), and the difference is empty windows
//
// A hash group-by creates a group when a row arrives, so an hour with no rows simply
// is not in the output and a gap in a time series is invisible. A dynamic group
// generates the whole grid between the first and last instant, so the gap is a row
// with a zero in it. That is the entire reason this operator exists.
//
// # The index must be sorted, and it is CHECKED
//
// Windows are cut as contiguous ranges of a sorted index, so an unsorted one gives
// wrong groups. ursus verifies rather than trusting an assertion: an unchecked
// SetSorted-style hint is a user claim that deletes a correctness check, and its
// failure mode is wrong rows with no error. Sort first if the check refuses.
func (lf *LazyFrame) GroupByDynamic(index Expr, o DynamicOptions) *GroupBy {
	g := lf.temporalGroup(index, false, o.Every, o.Period, o.Offset, o.Closed, o.GroupBy, nil)
	g.temporal.WindowLabel = strings.ToLower(o.Label)
	g.temporal.StartBy = strings.ToLower(o.StartBy)
	g.temporal.Boundaries = o.IncludeBoundaries
	return g
}

// Rolling gives every row its own window, reaching back Period from that row's own
// instant. The output has one row per input row.
//
// Where GroupByDynamic answers "how many per hour", Rolling answers "how many in the
// hour before each event". The index must be sorted, and is checked.
func (lf *LazyFrame) Rolling(index Expr, o RollingOptions) *GroupBy {
	return lf.temporalGroup(index, true, Interval{}, o.Period, o.Offset, o.Closed,
		o.GroupBy, nil)
}

func (lf *LazyFrame) temporalGroup(index Expr, rolling bool,
	every, period, offset Interval, closed Closed, groupBy []Expr, refused error,
) *GroupBy {

	keys := make([]expr.Node, len(groupBy))
	for i, k := range groupBy {
		keys[i] = k.node()
	}
	return &GroupBy{
		lf:   lf,
		keys: keys,
		temporal: &plan.TemporalGroup{
			Index: index.node(), Keys: keys, Rolling: rolling,
			Every: every, Period: period, Offset: offset, Closed: closed,
		},
		temporalErr: refused,
	}
}
