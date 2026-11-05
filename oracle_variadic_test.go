package ursus_test

// The variadic group (step 497, from variadic_test.go's pasted answers): step 302's
// variadic functions over a selection, inside a list's body, and format of one, each
// spelled as bench/scripts/oracle/variadic.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c, el, all := ursus.Col, ursus.Element, ursus.All
	a, b, x := c("a"), c("b"), c("x")
	add := func(acc, v ursus.Expr) ursus.Expr { return acc.Add(v) }
	text := func(e ursus.Expr) ursus.Expr { return e.Cast(ursus.String) }
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	each := func(body ursus.Expr) oracleQuery { return sel(x.List().Eval(body).Explode()) }
	concatPair := ursus.ConcatStr(":", text(el()), text(el()))
	oracleGroups["variadic"] = map[string]oracleQuery{
		"max_horizontal_all":       sel(ursus.MaxHorizontal(all())),
		"min_horizontal_all":       sel(ursus.MinHorizontal(all())),
		"sum_horizontal_cols":      sel(ursus.SumHorizontal(c("a", "b"))),
		"mean_horizontal_all":      sel(ursus.MeanHorizontal(all())),
		"any_horizontal_all_gt_1":  sel(ursus.AnyHorizontal(all().Gt(1))),
		"all_horizontal_all_gt_0":  sel(ursus.AllHorizontal(all().Gt(0))),
		"coalesce_all":             sel(ursus.Coalesce(all(), ursus.Lit(int64(-1)))),
		"concat_str_all":           sel(ursus.ConcatStr("-", all().Cast(ursus.String).FillNullWith("_"))),
		"struct_all_json":          sel(ursus.Struct(all()).Struct().JsonEncode()),
		"concat_list_all_len":      sel(ursus.ConcatList(all()).List().Len()),
		"list_concat_all":          sel(all().List().Concat(ursus.Lit([]int64{9})).List().Eval(el().Cast(ursus.String)).List().Join(",")),
		"sum_horizontal_all_null":  sel(ursus.SumHorizontal(a, b), ursus.MeanHorizontal(a, b).Alias("mean"), ursus.MaxHorizontal(a, b).Alias("max")),
		"all_minus_sum_horizontal": sel(all().Sub(ursus.SumHorizontal(all()))),
		"horizontal_gen": sel(ursus.MaxHorizontal(all()).Alias("max"), ursus.MinHorizontal(all()).Alias("min"),
			ursus.SumHorizontal(all()).Alias("sum"), ursus.MeanHorizontal(all()).Alias("mean"),
			ursus.AnyHorizontal(all().Gt(10)).Alias("any"), ursus.AllHorizontal(all().Gt(-10)).Alias("all"),
			ursus.Coalesce(all(), ursus.Lit(int64(-1))).Alias("coalesce"),
			ursus.ConcatStr("-", all().Cast(ursus.String).FillNullWith("_")).Alias("str"), ursus.ConcatList(all()).Alias("list")),

		"eval_concat_str":     sel(x.List().Eval(concatPair).List().Join("|")),
		"eval_sum_horizontal": each(ursus.SumHorizontal(el(), el())),
		"eval_coalesce":       each(ursus.Coalesce(ursus.When(el().Gt(1)).Then(el()).Otherwise(ursus.Null(ursus.Int64)), el().Mul(10))),
		"eval_struct_json":    sel(x.List().Eval(ursus.Struct(el().Alias("a"), el().Mul(2).Alias("b"))).Explode().Struct().JsonEncode()),
		"eval_fold":           each(ursus.Fold(0, add, el(), el())),
		"eval_in_group_by": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("g")).MaintainOrder().Agg(x.Implode().List().Eval(concatPair).List().Join("|")).Select(x)
		},
		"cumulative_eval_max_horizontal": sel(x.CumulativeEval(ursus.MaxHorizontal(el().Sum(), el().Max()))),
		"eval_gen": sel(x.List().Eval(ursus.SumHorizontal(el(), el().Mul(2))).Alias("sum"),
			x.List().Eval(ursus.Fold(1, add, el(), el())).Alias("fold"),
			x.List().Eval(ursus.Coalesce(ursus.When(el().Gt(0)).Then(el()).Otherwise(ursus.Null(ursus.Int64)), el().Mul(10))).Alias("coalesce"),
			x.List().Eval(concatPair).List().Join("|").Alias("str")),

		"format_variadic_lit_first": sel(ursus.Format("<{}>", ursus.ConcatStr("-", ursus.Lit("x"), text(a)))),
		"format_variadic_col_first": sel(ursus.Format("<{}>", ursus.Coalesce(b, a))),
		"format_variadic_gen":       sel(ursus.Format("<{}>", ursus.Coalesce(b, a, c("c"))), ursus.Format("[{}]", ursus.SumHorizontal(a, c("c"))).Alias("sum")),
	}
}
