package ursus

import (
	"context"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// Upsample (step 306, P4 of v0.6.7-scope.md): Polars' upsample, and a lazy form.

// Upsample puts a row at every step of every from each group's first instant to its
// last, as Polars' upsample does. A row of the frame at an instant of that grid is
// kept, and where there is none, a row of nulls but the time and the group's keys.
// The time column is a Date or a Datetime, and every an interval as Every counts it:
// "1mo" steps by months from the first instant, Jan 31, Feb 29, Mar 31, as Polars
// does; a day in a zone is a day of its wall clock.
//
//	daily := lf.Upsample("time", ursus.Every("1d"), "station").
//		WithColumns(ursus.Col("reading").FillNull(ursus.FillForward))
//
// A row whose instant falls between two steps is left out, as Polars leaves it, and
// two rows at one instant are both kept. The groups come in the order each first
// appears, as Polars' with maintain_order, and each group's rows in the grid's order,
// two at one instant in the frame's: with a memory limit or without, so that a fill
// forward after it reads the rows it should.
//
// It is a join: a grid made by a group-by of each group's first and last instant,
// joined with the frame and sorted back into the grid's order, each part streaming
// and spilling as it does. The frame need not be sorted, which Polars' must be. A row
// of a null instant has no place on the grid and is left out, and a frame of no rows
// upsamples to none, where Polars refuses either.
func (lf *LazyFrame) Upsample[T Span](time string, every T, groupBy ...string) *LazyFrame {
	if lf.err != nil {
		return lf
	}
	iv := span(every)
	if err := iv.Err(); err != nil {
		return &LazyFrame{err: err}
	}
	if iv.IsZero() || iv.Negative() {
		return &LazyFrame{err: uerr.New(uerr.KindValue, "upsample",
			"upsample steps by a positive interval, and %s is not one", iv)}
	}
	s, err := lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	i := s.IndexOf(time)
	if i < 0 {
		return &LazyFrame{err: uerr.UnknownColumn("upsample", time, s.Names())}
	}
	t := Col(time)
	var steps Expr
	switch typ := s.Field(i).Type; typ.ID() {
	case dtype.TypeDate:
		if iv.Nanos()%nanosPerDay != 0 {
			// Polars steps a Date by 12h into each date twice, the second unmatched.
			return &LazyFrame{err: uerr.New(uerr.KindValue, "upsample",
				"upsample steps a Date by whole days, and %s is not", iv).
				Hint("cast %q to a Datetime to step it by less", time)}
		}
		steps = DateRanges(t.Min(), t.Max(), iv)
	case dtype.TypeDatetime:
		steps = DatetimeRanges(t.Min(), t.Max(), iv)
	default:
		return &LazyFrame{err: uerr.New(uerr.KindType, "upsample",
			"upsample's time column is a Date or a Datetime, and %q is %s", time, typ).
			Hint(`an integer index, Polars' every "1i", is not upsampled here`)}
	}
	keys := make([]Expr, 0, len(groupBy)+1)
	named := map[string]bool{time: true}
	for _, name := range groupBy {
		if s.IndexOf(name) < 0 {
			return &LazyFrame{err: uerr.UnknownColumn("upsample", name, s.Names())}
		}
		if named[name] {
			return &LazyFrame{err: uerr.New(uerr.KindValue, "upsample",
				"%q is named twice among the time column and groupBy", name)}
		}
		named[name] = true
		keys = append(keys, Col(name))
	}

	// The grid, a row for each group's step, numbered in its order; the frame's rows
	// numbered in theirs. A group of no instant has a null range, which explodes to a
	// row of a null instant, and is dropped.
	names := s.Names()
	step, row := freshName(names, "step"), freshName(names, "row")
	grid := lf.GroupBy(keys...).MaintainOrder().Agg(steps.Alias(time)).Explode(time).
		Filter(t.IsNotNull()).WithRowIndex(step, 0)
	joined := grid.Join(lf.WithRowIndex(row, 0), JoinOn(append(keys, t)...), JoinHow(JoinLeft),
		JoinNullsEqual(true))
	cols := make([]Expr, len(names))
	for k, name := range names {
		cols[k] = Col(name)
	}
	return joined.Sort(Asc(Col(step)), Asc(Col(row))).Select(cols...)
}

// Upsample is a DataFrame's: LazyFrame's.
func (df *DataFrame) Upsample[T Span](ctx context.Context, time string, every T, groupBy ...string) (*DataFrame, error) {
	return df.Lazy().Upsample(time, every, groupBy...).Collect(ctx)
}
