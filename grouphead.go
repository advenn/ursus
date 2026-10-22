package ursus

import (
	"github.com/advenn/ursus/internal/uerr"
)

// A group's first and last rows (step 308, G1 of v0.6.7-scope.md): Polars'
// group_by().head(n) and tail(n).

// Head is each group's first n rows, as Polars' GroupBy.head: the key columns first,
// then the frame's others, the groups in the order each first appears, and each
// group's rows in the frame's order. A group of n rows or fewer gives them all, and a
// key computed from a column replaces a column of its name, as in Polars.
//
//	lf.GroupBy(ursus.Col("region")).Head(2)   // each region's first two orders
//
// It is a window over the keys numbering each group's rows, a filter and a sort, so it
// spills past a memory limit as each does, and its order holds there too. Polars
// builds it as an aggregation of every column's head, exploded; here an aggregation
// would hold every value of a group to keep n of them. Head(0) is no rows, where
// Polars' is a row of nulls for each group.
func (g *GroupBy) Head(n int) *LazyFrame { return g.slice("head", n, false) }

// Tail is each group's last n rows, in the frame's order: Polars' GroupBy.tail, and
// Head's in all else.
func (g *GroupBy) Tail(n int) *LazyFrame { return g.slice("tail", n, true) }

func (g *GroupBy) slice(op string, n int, fromEnd bool) *LazyFrame {
	if g.lf.err != nil {
		return g.lf
	}
	if g.temporalErr != nil {
		return &LazyFrame{err: g.temporalErr}
	}
	if g.temporal != nil {
		return &LazyFrame{err: uerr.New(uerr.KindUnsupported, op,
			"a dynamic or rolling group has no %s, as Polars' has none", op).
			Hint("its windows overlap, so a row is in several; Agg a Head or a Tail of each column")}
	}
	if n < 0 {
		return &LazyFrame{err: uerr.New(uerr.KindValue, op, "%s takes a count of rows, and %d is negative", op, n)}
	}
	if len(g.keys) == 0 {
		// One group, the frame, if Having keeps it.
		lf := g.lf
		if len(g.having) > 0 {
			lf = lf.Filter(g.havingRows(nil)...)
		}
		if fromEnd {
			return lf.Tail(n)
		}
		return lf.Head(n)
	}
	s, err := g.lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	keyExprs := make([]Expr, len(g.keys))
	for i, k := range g.keys {
		keyExprs[i] = wrap(k)
	}
	keyed, err := g.lf.Select(keyExprs...).schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}

	// The keys, as columns of their names, then each row's place in its group, from
	// its start or its end, and its group's first row, which orders the groups.
	keys := keyed.Names()
	taken := append(s.Names(), keys...)
	row := freshName(taken, "row")
	rank := freshName(append(taken, row), "rank")
	first := freshName(append(taken, row, rank), "first")
	parts := make([]Expr, len(keys))
	isKey := make(map[string]bool, len(keys))
	for i, name := range keys {
		parts[i], isKey[name] = Col(name), true
	}
	lf := g.lf.WithColumns(keyExprs...)
	if len(g.having) > 0 {
		// The groups Having keeps: its predicates, each row its group's.
		lf = lf.Filter(g.havingRows(parts)...)
	}
	lf = lf.WithRowIndex(row, 0).
		WithColumns(Col(row).CumCount(fromEnd).Over(parts...).Alias(rank), Col(row).Min().Over(parts...).Alias(first)).
		Filter(Col(rank).Le(n)).
		Sort(Asc(Col(first)), Asc(Col(row)))
	cols := make([]Expr, 0, s.Len()+len(keys))
	for _, name := range keys {
		cols = append(cols, Col(name))
	}
	for _, name := range s.Names() {
		if !isKey[name] {
			cols = append(cols, Col(name))
		}
	}
	return lf.Select(cols...)
}
