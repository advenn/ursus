package ursus

import (
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// Rolling is e over each row's window of a rolling group, as Polars' Expr.rolling
// (step 337): index, which must be sorted, places each row's window as the frame's
// Rolling does under o, and e, an aggregate, is its value for each row. So
//
//	ursus.Col("x").Sum().Rolling(ursus.Col("t"), ursus.RollingOptions{Period: ursus.Every("2d")})
//
// is each row's sum of the rows of the two days ending at it, and any aggregate
// expression of them may be rolled, Col("x").Max().Sub(Col("x").Min()) as well. A
// row's value that is not aggregated is the list of its window's. Under Over it rolls
// within each partition, the answer in the frame's rows' order. Over gives its
// partitions, not RollingOptions.GroupBy, as Polars' rolling takes none.
//
// Where every aggregate in e is a Sum, a Mean, a Min, a Max, a Var, a Std, a Median
// or a Quantile, and o gives no Offset, each is rolled by its own window by time,
// RollingSumBy's and its kin's, a window found by value: the index need not be
// sorted, and in an aggregation it answers a list a group. Anything else is rolled
// by the frame's rolling group, whose index is checked sorted, within each partition
// under Over, where it holds every window until the input ends; in an aggregation it
// is refused.
func (e Expr) Rolling(index Expr, o RollingOptions) Expr {
	if len(o.GroupBy) > 0 {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "rolling",
			"an expression's rolling takes its partitions from Over, not RollingOptions.GroupBy").
			Hint("e.Rolling(index, o).Over(keys...)")})
	}
	if n, ok := rollingByWindows(e.node(), index.node(), o); ok {
		return wrap(n)
	}
	return wrap(&expr.Reshape{Op: expr.ReshapeRolling, Child: e.node(), By: index.node(),
		Roll: &expr.RollSpec{Period: o.Period, Offset: o.Offset, Closed: o.Closed}})
}

// rollingByWindows is e.Rolling(index, o) as rolling windows by index, false where it
// cannot be. Where o's window ends at its row, as the _by windows' do, and every
// aggregate in e has a window of its own over a value a row, each aggregate is its
// window over the same span, and the expression around them is computed a row at a
// time: a window found by value, where the frame's rolling group gives each row to
// each of its windows, a hundred times as long over a window of a hundred rows.
func rollingByWindows(e, index expr.Node, o RollingOptions) (expr.Node, bool) {
	if o.Offset.Given() {
		return nil, false
	}
	ok, any := true, false
	var walk func(expr.Node) expr.Node
	walk = func(n expr.Node) expr.Node {
		switch t := n.(type) {
		case *expr.Agg:
			fn, p, has := byWindow(t)
			if !has || t.Child == nil || expr.HasAgg(t.Child) || expr.HasWindow(t.Child) ||
				expr.LooseReshape(t.Child) != nil || expr.LooseFilter(t.Child) != nil {
				ok = false
				return n
			}
			p.By, p.Period, p.MinSamples, p.Closed = true, o.Period, 1, o.Closed
			any = true
			return &expr.WinFn{Fn: fn, Child: expr.PairOf(t.Child, index), Params: p}
		case *expr.Col, *expr.Match, *expr.Window, *expr.WinFn, *expr.Reshape, *expr.Filter:
			ok = false // a row's value outside an aggregate, a list's, or a window's own
			return n
		}
		kids := n.Children()
		if len(kids) == 0 {
			return n
		}
		out := make([]expr.Node, len(kids))
		for i, k := range kids {
			out[i] = walk(k)
		}
		return expr.Rebuild(n, out)
	}
	out := walk(e)
	return out, ok && any
}

// byWindow is aggregate a's rolling window by time, and its parameters.
func byWindow(a *expr.Agg) (expr.WinFnOp, expr.WinParams, bool) {
	switch a.Op {
	case expr.AggSum:
		return expr.WinRollingSum, expr.WinParams{}, true
	case expr.AggMean:
		return expr.WinRollingMean, expr.WinParams{}, true
	case expr.AggMin:
		return expr.WinRollingMin, expr.WinParams{}, true
	case expr.AggMax:
		return expr.WinRollingMax, expr.WinParams{}, true
	case expr.AggVar:
		return expr.WinRollingVar, expr.WinParams{Ddof: int64(a.Params.DDof)}, true
	case expr.AggStd:
		return expr.WinRollingStd, expr.WinParams{Ddof: int64(a.Params.DDof)}, true
	case expr.AggMedian:
		return expr.WinRollingMedian, expr.WinParams{}, true
	case expr.AggQuantile:
		return expr.WinRollingQuantile, expr.WinParams{Q: a.Params.Q, Interp: a.Params.Interp}, true
	}
	return 0, expr.WinParams{}, false
}
