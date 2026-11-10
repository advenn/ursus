package ursus_test

// The having group (step 501, from having_test.go's hand answers): GroupBy.having over
// Agg, the shorthands, Head and Tail and a dynamic group, each spelled as
// bench/scripts/oracle/having.py asks Polars, with maintain_order; a sum and a count
// are cast to Int64 on both sides (feature-gaps.md §9).

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	sum := c("v").Sum().Cast(ursus.Int64)
	count := ursus.Len().Cast(ursus.Int64)
	g := func(in oracleInputs) *ursus.GroupBy { return in.df().GroupBy(c("k")).MaintainOrder() }
	hv := func(f func(g *ursus.GroupBy) *ursus.LazyFrame) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return f(g(in)) }
	}
	oracleGroups["having"] = map[string]oracleQuery{
		"len_gt_1":     hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(ursus.Len().Gt(1)).Agg(sum) }),
		"a_sum_over_3": hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(c("v").Sum().Gt(3)).Agg(c("w").Mean()) }),
		"two_predicates": hv(func(g *ursus.GroupBy) *ursus.LazyFrame {
			return g.Having(ursus.Len().Gt(1), c("v").Max().Lt(4)).Agg(sum)
		}),
		"chained": hv(func(g *ursus.GroupBy) *ursus.LazyFrame {
			return g.Having(ursus.Len().Gt(1)).Having(c("v").Max().Lt(6)).Agg(sum)
		}),
		"a_null_predicate":                       hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(c("v").First().Gt(0)).Agg(count) }),
		"a_predicate_of_a_column_not_aggregated": hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(c("w").Min().Gt(2)).Agg(sum) }),
		"no_aggregate":                           hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(ursus.Len().Gt(1)).Agg() }),
		"sum_shorthand": hv(func(g *ursus.GroupBy) *ursus.LazyFrame {
			return g.Having(ursus.Len().Gt(1)).Sum().WithColumns(c("v").Cast(ursus.Int64))
		}),
		"len_shorthand": hv(func(g *ursus.GroupBy) *ursus.LazyFrame {
			return g.Having(ursus.Len().Gt(1)).Len("len").WithColumns(c("len").Cast(ursus.Int64))
		}),
		"head":                 hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(ursus.Len().Gt(1)).Head(1) }),
		"tail":                 hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(c("v").Sum().Gt(4)).Tail(1) }),
		"a_literal_true":       hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(ursus.Lit(true)).Agg(count) }),
		"every_group_left_out": hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(ursus.Len().Gt(10)).Agg(count) }),
		"a_dynamic_group": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupByDynamic(c("t"), ursus.DynamicOptions{Every: ursus.Every("2d")}).Having(ursus.Len().Gt(1)).Agg(sum)
		},
		"having_gen": hv(func(g *ursus.GroupBy) *ursus.LazyFrame {
			return g.Having(ursus.Len().Gt(20), c("v").Max().Lt(990)).Agg(sum, c("w").Max().Alias("w"), count.Alias("n"))
		}),
		"having_gen_head": hv(func(g *ursus.GroupBy) *ursus.LazyFrame { return g.Having(c("w").Min().Lt(-900)).Head(3) }),
	}
}
