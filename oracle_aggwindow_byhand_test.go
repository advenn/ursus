package ursus_test

// The aggwindow_byhand group (step 500, from aggwindow_byhand_test.go's hand
// answers): audit.md §7's aggregation and window cases, each spelled as
// bench/scripts/oracle/aggwindow_byhand.py asks Polars. An integer Sum is an Int128
// here, so where the sum's type is not the question it is cast to Int64.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	v, x, g, w := c("v"), c("x"), c("g"), c("w")
	i64 := ursus.Int64
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	agg := func(e ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy().Agg(e) }
	}
	// The median of [-1.7e308, 1.7e308] and the midpoint of [1e308, 1.5e308], which
	// Polars 2.1 overflows to +Inf, are TestAggregatesAtTheFloatsEnds' since step 507,
	// held to math/big; var_alike_huge is asked of Polars of the values less the first.
	oracleGroups["aggwindow_byhand"] = map[string]oracleQuery{
		"shift_fill_float":    sel(v.ShiftFill(1, 1.5)),
		"shift_fill_int32":    sel(v.ShiftFill(1, 0)),
		"shift_fill_int64":    sel(v.ShiftFill(1, int64(-1))),
		"quantile_0_neg_inf":  agg(v.Quantile(0, ursus.InterpLinear)),
		"quantile_1_inf":      agg(v.Quantile(1, ursus.InterpLinear)),
		"midpoint_alike_huge": agg(v.Quantile(0.5, ursus.InterpMidpoint)),
		"median_alike_huge":   agg(v.Median()),
		"pct_change_u8":       sel(v.PctChange(1)),
		"pct_change_i8":       sel(v.PctChange(1)),
		"var_overflow":        agg(v.Var(0)),
		"std_overflow":        agg(v.Std(0)),
		"var_alike_huge":      agg(v.Var(0)),
		"bool_sum":            agg(v.Sum().Cast(i64)),
		"bool_sum_by_g": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(g).Agg(v.Sum().Cast(i64)).Sort(ursus.Asc(g))
		},
		"bool_cum_sum":  sel(v.CumSum(false).Cast(i64)),
		"product_zero":  agg(v.Product()),
		"cum_prod_zero": sel(v.CumProd(false)),
		// One WithColumns here, its later windows reading its earlier columns (W1).
		"window_over_window": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(x.CumSum(false).Over(g).Cast(i64).Alias("a"), c("a").Max().Over(g).Alias("m"))
		},
		"replaced_column_place": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(x.Mul(10).Alias("w"), w.Sum().Over(g).Cast(i64).Alias("s"))
		},
		"two_splits": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(x.Mul(10).Alias("w"), w.Sum().Over(g).Cast(i64).Alias("s"),
				c("s").Add(w).Alias("t"), c("t").Max().Over(g).Alias("m"), c("m").Sub(c("t")).Alias("d"))
		},
		"earlier_column_outside": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(x.Mul(10).Alias("w"), w.Add(x.Sum().Over(g)).Cast(i64).Alias("s"))
		},
		"shift_pct_gen": sel(v.ShiftFill(1, 1.5).Alias("f15"), v.ShiftFill(2, int64(-1)).Alias("m1"), v.ShiftFill(-1, 0).Alias("back"),
			v.PctChange(1).Alias("pct"), c("u").PctChange(1).Alias("upct"), c("u").ShiftFill(1, uint8(7)).Alias("u7")),
		"quantile_var_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(g).Agg(v.Quantile(0, ursus.InterpLinear).Alias("q0"), v.Quantile(1, ursus.InterpLinear).Alias("q1"),
				v.Median().Alias("med"), v.Quantile(0.5, ursus.InterpMidpoint).Alias("mid"), v.Quantile(0.3, ursus.InterpNearest).Alias("q3n"),
				v.Var(0).Alias("var0"), v.Std(0).Alias("std0"), v.Var(1).Alias("var1")).Sort(ursus.Asc(g))
		},
		"bool_sum_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(v.CumSum(false).Cast(i64).Alias("cs")).
				GroupBy(g).Agg(v.Sum().Cast(i64).Alias("s"), c("cs").Last().Alias("cs_last")).Sort(ursus.Asc(g))
		},
		"window_chain_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(x.CumSum(false).Over(g).Cast(i64).Alias("a"), c("a").Max().Over(g).Alias("m"), x.Mul(10).Alias("w"),
				c("w").Add(x.Sum().Over(g)).Cast(i64).Alias("s"))
		},
	}
}
