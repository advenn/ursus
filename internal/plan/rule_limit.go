package plan

// limitPushdown tells a scan how few rows will actually be consumed.
//
// `ScanCSV("10gb.csv").Head(10)` without this reads ten gigabytes and throws
// almost all of it away. With it the reader stops after ten rows. That is the
// difference between minutes and milliseconds, and it is the single most common
// thing anyone types when they first open a file.
//
// # It is a hint, and the Limit node stays
//
// The rewrite is Limit(N, Scan) -> Limit(N, Scan{MaxRows: N}), NOT a replacement.
// A source is free to ignore MaxRows or to overshoot it (a Parquet reader stops
// at a row-group boundary, not a row boundary), so the Limit above still does the
// enforcing. Nothing about correctness depends on the source's cooperation —
// which is the same shape as Projection, where a source that ignores the
// projection gets a trim operator inserted above it.
//
// # It also turns Sort into a top-k
//
// `Sort(...).Head(10)` over ten million rows sorted all ten million. The sink has
// always known how not to — kernel.ArgTopK is O(n log k) against ArgSort's
// O(n log n), with a contract that it returns exactly the indices ArgSort would,
// ties included — but the Sort.Limit field that selects it was documented as "set
// by the limit-pushdown rule" and no rule set it. The branch below is the producer
// that comment always described.
//
// # Where it reaches
//
// Anything between the Limit and its target that changes how many rows there are, or
// which come first, makes the rewrite wrong, and the case that matters is already in
// the tree:
//
//	Limit(2, Filter(Scan{Predicate: c > 5 INEXACT}))
//
// The residual Filter of an inexact pushdown sits between them. If the limit
// reached the scan, the source would return 2 rows of its SUPERSET, the Filter
// would drop some, and Head(2) would return fewer than 2 rows. So a filter stops
// it, as do a group-by, a window, a distinct, every other node that is not below,
// and every join but the one below.
//
// It reaches through what keeps each row, in order, one for one (step 371, P3 of
// v0.6.8-scope.md):
//
//   - a Project and a WithColumns, whose expressions are all row by row: the planner
//     makes a window, a sort, a reverse, a head or a unique of an expression nodes of
//     their own, below them;
//   - a RowIndex, whose first rows are numbered as they are below it;
//   - another Limit, the tighter of the two;
//   - a Union, into each input: its first n rows are of its inputs' first n each;
//   - a left join that keeps its left input's order, into that input: each left row
//     is a row or more of its answer, so its first n are of the left's first n.
//
// And a Slice of Len rows from Offset is a limit of Offset+Len beneath it.
//
// The Limit or Slice stays above, enforcing; what reaches the target is the hint.
type limitPushdown struct{}

func (limitPushdown) Name() string { return "limit_pushdown" }

func (limitPushdown) Apply(n Node, _ Flags) (Node, bool, error) {
	changed := false
	out, err := TransformUp(n, func(x Node) (Node, error) {
		var rows int
		switch t := x.(type) {
		case *Limit:
			rows = t.N
		case *Slice:
			if t.Len < 0 || t.Offset < 0 {
				return x, nil // to the end: no limit
			}
			rows = t.Offset + t.Len
		default:
			return x, nil
		}
		// 0 is a legal, degenerate query. Leave it alone: both MaxRows and Sort.Limit
		// use 0 for "unlimited", so pushing it would say the opposite of what it
		// means, and the Limit operator above already returns nothing at no cost.
		if rows <= 0 {
			return x, nil
		}
		in := x.Children()[0]
		if hinted, ok := limitRows(in, rows); ok {
			changed = true
			return x.WithChildren([]Node{hinted}), nil
		}
		return x, nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

// limitRows tells n that no more than rows of its rows will be read: its scan's
// MaxRows, or its sort's top-k, through what keeps each row in order. ok is false
// where nothing took the hint.
func limitRows(n Node, rows int) (Node, bool) {
	switch t := n.(type) {
	case *Scan:
		// An outer limit already tighter than this one wins; never widen.
		if t.MaxRows != 0 && t.MaxRows <= rows {
			return n, false
		}
		return t.WithMaxRows(rows), true

	case *Sort:
		// Sort(...).Head(k) is a top-k, and the sink already knows how to be one:
		// kernel.ArgTopK is O(n log k) time and O(k) memory against ArgSort's
		// O(n log n) and O(n), with a contract that it returns EXACTLY the indices
		// ArgSort would, ties included. The same never-widen guard applies to
		// Sort(...).Head(5).Head(10).
		if t.Limit != 0 && t.Limit <= rows {
			return n, false
		}
		return t.WithLimit(rows), true

	case *Project, *WithColumns, *RowIndex:
		kid, ok := limitRows(n.Children()[0], rows)
		if !ok {
			return n, false
		}
		return n.WithChildren([]Node{kid}), true

	case *Limit:
		kid, ok := limitRows(t.Input, min(rows, t.N))
		if !ok {
			return n, false
		}
		return t.WithChildren([]Node{kid}), true

	case *Union:
		kids := make([]Node, len(t.Inputs))
		changed := false
		for i, in := range t.Inputs {
			var ok bool
			kids[i], ok = limitRows(in, rows)
			changed = changed || ok
		}
		if !changed {
			return n, false
		}
		return t.WithChildren(kids), true

	case *Join:
		if t.Kind != JoinLeft || (t.Order != JoinOrderLeft && t.Order != JoinOrderLeftRight) {
			return n, false
		}
		left, ok := limitRows(t.Left, rows)
		if !ok {
			return n, false
		}
		return t.WithChildren([]Node{left, t.Right}), true
	}
	return n, false
}
