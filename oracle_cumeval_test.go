package ursus_test

// The cumeval group (step 500, from cumeval_test.go's table): Polars'
// cumulative_eval, each spelled as bench/scripts/oracle/cumeval.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c, el := ursus.Col, ursus.Element
	x, i, g := c("x"), c("i"), c("g")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	oracleGroups["cumeval"] = map[string]oracleQuery{
		"first_less_last_squared": sel(x.CumulativeEval(el().First().Sub(el().Last().Pow(2)))),
		"sum":                     sel(x.CumulativeEval(el().Sum())),
		"mean_min_samples_2":      sel(x.CumulativeEval(el().Mean(), ursus.MinSamples(2))),
		"sum_over_g":              sel(x.CumulativeEval(el().Sum()).Over(g)),
		"rank_last":               sel(i.CumulativeEval(el().Rank(ursus.RankAverage, false).Last())),
		"null_first_sum":          sel(x.CumulativeEval(el().Sum())),
		"null_first_first":        sel(x.CumulativeEval(el().First())),
		"count":                   sel(x.CumulativeEval(el().Count())),
		"len":                     sel(x.CumulativeEval(el().Len())),
		"ints_max_less_min":       sel(i.CumulativeEval(el().Max().Sub(el().Min()))),
		"text_min":                sel(c("s").CumulativeEval(el().Min())),
		"floats_gen": sel(x.CumulativeEval(el().Sum()).Alias("sum"), x.CumulativeEval(el().Mean(), ursus.MinSamples(3)).Alias("mean3"),
			x.CumulativeEval(el().First().Sub(el().Last())).Alias("first_last"), x.CumulativeEval(el().Max()).Alias("max"),
			x.CumulativeEval(el().Count()).Alias("count"), x.CumulativeEval(el().Len()).Alias("len"),
			x.CumulativeEval(el().Sum()).Over(g).Alias("sum_g")),
		"ints_gen": sel(i.CumulativeEval(el().Max().Sub(el().Min())).Alias("range"), i.CumulativeEval(el().Rank(ursus.RankAverage, false).Last()).Alias("rank_last"),
			i.CumulativeEval(el().Min()).Alias("min")),
	}
}
