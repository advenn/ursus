package ursus_test

// The fold group (step 497, from fold_test.go's pasted answers): concat_list, fold,
// reduce, the running forms, cum_sum_horizontal and format, each spelled as
// bench/scripts/oracle/fold.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	a, b, f, s, l := c("a"), c("b"), c("f"), c("s"), c("l")
	add := func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }
	mul := func(acc, x ursus.Expr) ursus.Expr { return acc.Mul(x) }
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	// fields is the struct's fields as columns, as the Python side reads them.
	fields := func(e ursus.Expr, name string, names ...string) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			exprs := make([]ursus.Expr, len(names))
			for i, n := range names {
				exprs[i] = c(name).Struct().Field(n)
			}
			return in.df().Select(e).Select(exprs...)
		}
	}
	oracleGroups["fold"] = map[string]oracleQuery{
		"concat_list_two":        sel(ursus.ConcatList(a, b).Alias("r")),
		"concat_list_list_value": sel(ursus.ConcatList(l, a).Alias("r")),
		"concat_list_int_float":  sel(ursus.ConcatList(a, f).Alias("r")),
		"concat_list_lit":        sel(ursus.ConcatList(ursus.Lit(7), a).Alias("r")),
		"concat_list_two_lists":  sel(ursus.ConcatList(l, l).Alias("r")),
		"concat_list_gen": sel(ursus.ConcatList(a, b, c("c")).Alias("abc"), ursus.ConcatList(l, a).Alias("la"),
			ursus.ConcatList(a, f).Alias("af"), ursus.ConcatList(l, l).Alias("ll")),

		"fold":               sel(ursus.Fold(0, add, c("a", "b"))),
		"fold_from_col":      sel(ursus.Fold(f, mul, a, b)),
		"fold_all":           sel(ursus.Fold(0, add, ursus.All())),
		"reduce":             sel(ursus.Reduce(add, c("a", "b"))),
		"cum_fold":           fields(ursus.CumFold(0, add, c("a", "b")), "cum_fold", "a", "b"),
		"cum_reduce":         fields(ursus.CumReduce(add, c("a", "b")), "cum_reduce", "a", "b"),
		"cum_sum_horizontal": fields(ursus.CumSumHorizontal(a, f), "cum_sum", "a", "f"),
		"fold_agg":           sel(ursus.Fold(0, add, a.Sum(), b.Sum())),
		"fold_gen": sel(ursus.Fold(0, add, a, b, c("c")).Alias("fold"), ursus.Fold(f, mul, a, b).Alias("mul"),
			ursus.Reduce(add, a, b, c("c")).Alias("reduce")),
		"cum_fold_gen":           fields(ursus.CumFold(0, add, a, b, c("c")), "cum_fold", "a", "b", "c"),
		"cum_sum_horizontal_gen": fields(ursus.CumSumHorizontal(a, f, b), "cum_sum", "a", "f", "b"),

		"format_two":         sel(ursus.Format("{} and {}", a, s)),
		"format_text_around": sel(ursus.Format("n={}!", f)),
		"format_escaped":     sel(ursus.Format("{{}} {}", a)),
		"format_plain":       sel(a, ursus.Format("plain")),
		"format_gen":         sel(ursus.Format("{}-{}", a, s).Alias("as"), ursus.Format("[{}]", f).Alias("f")),
	}
}
