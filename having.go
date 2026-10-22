package ursus

import (
	"slices"
	"strconv"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// A group-by's Having (step 309, G2 of v0.6.7-scope.md): Polars' GroupBy.having.

// Having keeps the groups whose predicates all hold, as Polars' having does: each
// predicate an aggregate of its group, Boolean, and a group whose predicate is null
// left out. Called again, or given several, every one must hold. It filters what
// Agg gives, the shorthands Sum, Len and the rest, and Head and Tail; a dynamic or a
// rolling group's windows too.
//
//	lf.GroupBy(ursus.Col("region")).
//		Having(ursus.Len().Gt(1), ursus.Col("qty").Sum().Gt(10)).
//		Agg(ursus.Col("price").Mean())
//
// The predicates are aggregated beside Agg's expressions, the groups filtered by
// them, and the predicates dropped: a filter after the aggregation, as Polars' doc
// says it is.
func (g *GroupBy) Having(preds ...Expr) *GroupBy {
	c := *g
	ns, err := nodes(preds)
	if err != nil {
		c.lf = &LazyFrame{err: err}
		return &c
	}
	for _, n := range ns {
		// Polars: cannot cast List type (inner: 'Boolean', to: 'Boolean').
		if !expr.IsAggregation(n) {
			e := uerr.New(uerr.KindType, "having", "having's predicate %s is not an aggregate of its group", n)
			if bare := expr.BareColumns(n); len(bare) > 0 {
				e.Hint("column %q is read outside an aggregate; e.g. Col(%q).Max().Gt(1)", bare[0], bare[0])
			}
			c.lf = &LazyFrame{err: e}
			return &c
		}
	}
	c.having = append(slices.Clip(g.having), ns...)
	return &c
}

// aggHaving is Agg with Having: its expressions and the predicates, under names no
// column has, then the groups whose predicates hold, without them.
func (g *GroupBy) aggHaving(exprs []Expr) *LazyFrame {
	if g.lf.err != nil {
		return g.lf
	}
	s, err := g.lf.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	taken := s.Names()
	names := make([]string, len(g.having))
	all := append([]Expr(nil), exprs...)
	for i, p := range g.having {
		names[i] = freshName(taken, "having"+strconv.Itoa(i))
		taken = append(taken, names[i])
		all = append(all, wrap(p).Alias(names[i]))
	}
	out := g.agg(all)
	if out.err != nil {
		return out
	}
	os, err := out.schemaNow()
	if err != nil {
		return &LazyFrame{err: err}
	}
	preds := make([]Expr, len(names))
	isPred := make(map[string]bool, len(names))
	for i, name := range names {
		f, _ := os.ByName(name)
		if !f.Type.IsBool() && !f.Type.IsNull() {
			// Polars casts one to a Boolean, a sum of 7 true.
			return &LazyFrame{err: uerr.New(uerr.KindType, "having",
				"having's predicate %s is %s, where a group's predicate is Boolean", wrap(g.having[i]), f.Type).
				Hint("compare the aggregate, e.g. Col(\"x\").Sum().Gt(0)")}
		}
		preds[i], isPred[name] = Col(name), true
	}
	cols := make([]Expr, 0, os.Len()-len(names))
	for _, name := range os.Names() {
		if !isPred[name] {
			cols = append(cols, Col(name))
		}
	}
	return out.Filter(preds...).Select(cols...)
}

// havingRows is Having's predicates as windows over parts, each row's its group's,
// which Head and Tail filter the rows by.
func (g *GroupBy) havingRows(parts []Expr) []Expr {
	preds := make([]Expr, len(g.having))
	for i, p := range g.having {
		preds[i] = wrap(p).Over(parts...)
	}
	return preds
}
