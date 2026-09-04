package ursus

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
	"github.com/advenn/ursus/internal/uerr"
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

	// Label, StartBy and IncludeBoundaries are not implemented. They are declared so
	// that setting one is an error naming it rather than silence.
	Label             string
	StartBy           string
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
	return lf.temporalGroup(index, false, o.Every, o.Period, o.Offset, o.Closed,
		o.GroupBy, refusedDynamicFields(o))
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

// refusedDynamicFields names any unimplemented option the caller set.
func refusedDynamicFields(o DynamicOptions) error {
	var set []string
	if o.Label != "" {
		set = append(set, "Label")
	}
	if o.StartBy != "" {
		set = append(set, "StartBy")
	}
	if o.IncludeBoundaries {
		set = append(set, "IncludeBoundaries")
	}
	if len(set) == 0 {
		return nil
	}
	e := uerr.New(uerr.KindUnsupported, "group_by_dynamic",
		"DynamicOptions.%s is not implemented yet", set[0])
	if len(set) > 1 {
		e = uerr.New(uerr.KindUnsupported, "group_by_dynamic",
			"these DynamicOptions fields are not implemented yet: %v", set)
	}
	return e.Hint("implemented: Every, Period, Offset, Closed, GroupBy").
		Hint("they are declared so that setting one is an error rather than silence")
}

func (lf *LazyFrame) temporalGroup(index Expr, rolling bool,
	every, period, offset Interval, closed Closed, groupBy []Expr, refused error,
) *GroupBy {

	keys := make([]expr.Node, len(groupBy))
	for i, k := range groupBy {
		keys[i] = k.n
	}
	return &GroupBy{
		lf:   lf,
		keys: keys,
		temporal: &plan.TemporalGroup{
			Index: index.n, Keys: keys, Rolling: rolling,
			Every: every, Period: period, Offset: offset, Closed: closed,
		},
		temporalErr: refused,
	}
}
