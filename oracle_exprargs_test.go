package ursus_test

// The exprargs group (step 497, from exprargs_test.go's pasted answers): E5's
// is_in against each row's own list, list.get and list.contains with an index and a
// value a row, each spelled as bench/scripts/oracle/exprargs.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	tags, a, i, v, g := c("tags"), c("a"), c("i"), c("v"), c("g")
	three := func(value, lists, index, needle ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(value.IsIn(lists).Alias("in"), lists.List().Get(index).Alias("get"), lists.List().Contains(needle).Alias("has"))
		}
	}
	agg := func(in oracleInputs) *ursus.LazyFrame {
		return in.df().GroupBy(g).MaintainOrder().Agg(a.IsIn(tags).Sum().Alias("n_in"), tags.List().Contains(v).Sum().Alias("n_has"))
	}
	strs := ursus.List(ursus.String)
	oracleGroups["exprargs"] = map[string]oracleQuery{
		"lists_plain":   three(a, tags, i, v),
		"lists_cast":    three(a.Cast(ursus.Int8), tags, i.Cast(ursus.Int8), v.Cast(ursus.Int8)),
		"lists_strings": three(a.Cast(ursus.String), tags.Cast(strs), i, v.Cast(ursus.String)),
		"lists_agg":     agg,
		"lists_lit_index": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(tags.List().Get(ursus.Lit(-1)).Alias("last"))
		},
		"is_in_list_nan_zero": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(g).MaintainOrder().Agg(c("f").Implode().Alias("l"), c("x").First()).Select(c("x").IsIn(c("l")))
		},
		"lists_gen":     three(a, tags, i, v),
		"lists_agg_gen": agg,
	}
}
