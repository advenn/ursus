package ursus_test

// The argsort group (step 500, from argsort_test.go's tables): Nth, First and Last as
// selectors, and the index functions, each spelled as bench/scripts/oracle/argsort.py
// asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	a, b, p, g := c("a"), c("b"), c("p"), c("g")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	inAgg := func() []ursus.Expr {
		return []ursus.Expr{a.ArgSort(false, false).Alias("sort"), p.ArgTrue().Alias("true"), ursus.ArgWhere(b.Gt(6)).Alias("where"),
			a.ArgUnique().Alias("unique"), ursus.ArgSortBy(ursus.Asc(a)).Alias("by"), a.ArgSort(true, false).Alias("desc")}
	}
	byG := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(exprs...).Sort(ursus.Asc(g)) }
	}
	oracleGroups["argsort"] = map[string]oracleQuery{
		"nth_1":                          sel(ursus.Nth(1)),
		"nth_last":                       sel(ursus.Nth(-1)),
		"nth_0_2":                        sel(ursus.Nth(0, 2)),
		"nth_1_times_2":                  sel(ursus.Nth(1).Mul(2)),
		"first":                          sel(ursus.First()),
		"last":                           sel(ursus.Last()),
		"first_a":                        sel(ursus.First("a")),
		"arg_where":                      sel(ursus.ArgWhere(p)),
		"arg_where_comparison":           sel(ursus.ArgWhere(b.Gt(6))),
		"arg_sort":                       sel(a.ArgSort(false, false)),
		"arg_sort_descending":            sel(a.ArgSort(true, false)),
		"arg_sort_nulls_last":            sel(a.ArgSort(false, true)),
		"arg_sort_descending_nulls_last": sel(a.ArgSort(true, true)),
		"arg_sort_by":                    sel(ursus.ArgSortBy(ursus.Asc(a), ursus.Asc(b))),
		"arg_sort_by_descending_second":  sel(ursus.ArgSortBy(ursus.Asc(a), ursus.Desc(b))),
		"arg_sort_by_nulls_last":         sel(ursus.ArgSortBy(ursus.Desc(g).NullsLast(), ursus.Asc(a).NullsLast())),
		"arg_unique":                     sel(a.ArgUnique()),
		"arg_unique_key":                 sel(g.ArgUnique()),
		"arg_sort_beside": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(a.ArgSort(false, false).Alias("o")).Select(c("o"))
		},
		"in_aggregation": byG(inAgg()...),
		"positions_gen": sel(ursus.Nth(1).Alias("n1"), ursus.Nth(-1).Alias("nl"), ursus.Nth(1).Mul(2).Alias("n2"), ursus.First().Alias("f"),
			ursus.Last().Alias("l")),
		"arg_sort_gen": sel(a.ArgSort(false, false).Alias("s"), a.ArgSort(true, false).Alias("sd"), a.ArgSort(false, true).Alias("snl"),
			a.ArgSort(true, true).Alias("sdnl"), ursus.ArgSortBy(ursus.Asc(a), ursus.Asc(b)).Alias("by"),
			ursus.ArgSortBy(ursus.Desc(a).NullsLast(), ursus.Asc(b)).Alias("by2")),
		"arg_where_gen":      sel(ursus.ArgWhere(p)),
		"arg_unique_gen":     sel(a.ArgUnique()),
		"in_aggregation_gen": byG(inAgg()...),
	}
}
