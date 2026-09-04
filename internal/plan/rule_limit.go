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
// # Why the rule only fires DIRECTLY above its target
//
// Anything between the Limit and the node that changes cardinality makes the
// rewrite wrong, and the case that matters is already in the tree:
//
//	Limit(2, Filter(Scan{Predicate: c > 5 INEXACT}))
//
// The residual Filter of an inexact pushdown sits between them. If the limit
// reached the scan, the source would return 2 rows of its SUPERSET, the Filter
// would drop some, and Head(2) would return fewer than 2 rows. Matching the child
// structurally rules that out rather than by a check someone has to remember to
// write.
//
// A Filter over an EXACT scan would be safe to push through, but it does not
// arise: an exact conjunct leaves no Filter behind. And a Filter between a Limit
// and a Sort does not arise either, because predicate pushdown runs FIRST and
// pushes filters below a Sort freely — sorting and filtering commute.
type limitPushdown struct{}

func (limitPushdown) Name() string { return "limit_pushdown" }

func (limitPushdown) Apply(n Node, _ Flags) (Node, bool, error) {
	changed := false
	out, err := TransformUp(n, func(x Node) (Node, error) {
		l, ok := x.(*Limit)
		if !ok {
			return x, nil
		}
		// N == 0 is a legal, degenerate query. Leave it alone: both MaxRows and
		// Sort.Limit use 0 for "unlimited", so pushing it would say the opposite of
		// what it means, and the Limit operator above already returns nothing at no
		// cost.
		if l.N <= 0 {
			return x, nil
		}

		switch in := l.Input.(type) {
		case *Scan:
			// An outer limit already tighter than this one wins; never widen.
			if in.MaxRows != 0 && in.MaxRows <= l.N {
				return x, nil
			}
			changed = true
			return l.WithChildren([]Node{in.WithMaxRows(l.N)}), nil

		case *Sort:
			// Sort(...).Head(k) is a top-k, and the sink already knows how to be one:
			// kernel.ArgTopK is O(n log k) time and O(k) memory against ArgSort's
			// O(n log n) and O(n), with a contract that it returns EXACTLY the indices
			// ArgSort would, ties included.
			//
			// The same never-widen guard applies to Sort(...).Head(5).Head(10).
			if in.Limit != 0 && in.Limit <= l.N {
				return x, nil
			}
			changed = true
			return l.WithChildren([]Node{in.WithLimit(l.N)}), nil

		default:
			return x, nil
		}
	})
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}
