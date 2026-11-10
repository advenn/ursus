package ursus_test

// The exprodds group (step 497, from exprodds_test.go's pasted answers): step 324's
// limit, truediv, pipe, arctan2, to_physical, reinterpret, item, the bounds and
// is_sorted, each spelled as bench/scripts/oracle/exprodds.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	a, all := c("a"), ursus.All
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	triple := func(e ursus.Expr) ursus.Expr { return e.Mul(3) }
	sorted4 := func(e ursus.Expr, suffix string) []ursus.Expr {
		return []ursus.Expr{e.IsSorted(false, false).Alias("asc" + suffix), e.IsSorted(true, false).Alias("desc" + suffix),
			e.IsSorted(false, true).Alias("asc_last" + suffix), e.IsSorted(true, true).Alias("desc_last" + suffix)}
	}
	isSorted := sel(sorted4(a, "")...)
	reg := map[string]oracleQuery{
		"limit_2":          sel(a.Limit(2)),
		"truediv":          sel(a.TrueDiv(c("b"))),
		"pipe_triple":      sel(a.Pipe(triple)),
		"limit_gen":        sel(a.Limit(7)),
		"truediv_pipe_gen": sel(a.TrueDiv(c("b")).Alias("q"), a.Pipe(triple).Alias("p")),

		"arctan2":     sel(ursus.Arctan2(c("y"), c("x"))),
		"arctan2_f32": sel(ursus.Arctan2(c("y"), c("x"))),
		"arctan2_gen": sel(ursus.Arctan2(c("y"), c("x"))),

		"to_physical":     sel(all().ToPhysical()),
		"to_physical_gen": sel(all().ToPhysical()),
		"reinterpret":     sel(c("u").Reinterpret(true), c("i").Reinterpret(false), c("i8").Reinterpret(false)),
		"reinterpret_gen": sel(c("u").Reinterpret(true), c("i").Reinterpret(false), c("i8").Reinterpret(false), c("u16").Reinterpret(true)),

		"item_one":           sel(a.Item(false), c("b")),
		"item_null":          sel(a.Item(false)),
		"item_empty_allowed": sel(a.Item(true)),
		"item_gen":           sel(a.Item(false), c("s").Item(false)),

		"lower_bounds":    sel(all().LowerBound()),
		"upper_bounds":    sel(all().UpperBound()),
		"bound_broadcast": sel(a.UpperBound().Alias("hi"), a),
		"bound_grouped": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("g")).MaintainOrder().Agg(a.LowerBound())
		},
		"bounds_gen": sel(all().LowerBound().Name().Suffix("_lo"), all().UpperBound().Name().Suffix("_hi")),

		"is_sorted_grouped": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("g")).MaintainOrder().Agg(a.IsSorted(false, false))
		},
		"is_sorted_gen": func(in oracleInputs) *ursus.LazyFrame {
			var exprs []ursus.Expr
			for _, col := range []struct{ name, suffix string }{{"asc", "_a"}, {"desc", "_d"}, {"asc_last", "_l"}, {"rnd", "_r"}} {
				exprs = append(exprs, sorted4(c(col.name), col.suffix)...)
			}
			return in.df().Select(exprs...)
		},
	}
	for _, name := range []string{"1_2_2_3", "3_2_1", "null_1_2", "1_2_null", "null_null", "none", "1_nan", "nan_1", "a_b_B"} {
		reg["is_sorted_"+name] = isSorted
	}
	oracleGroups["exprodds"] = reg
}
