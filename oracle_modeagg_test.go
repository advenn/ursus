package ursus_test

// The modeagg group (step 500, from modeagg_test.go's table): a group's Mode as an
// aggregate of its own, each spelled as bench/scripts/oracle/modeagg.py asks Polars.
// Polars' mode without maintain_order comes in its hash table's order, where ursus's
// Mode(false) is sorted, so those are asked of Polars as mode().sort().

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	x, s, b, i, d, g, f := c("x"), c("s"), c("b"), c("i"), c("d"), c("g"), c("f")
	byG := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(exprs...).Sort(ursus.Asc(g)) }
	}
	oracleGroups["modeagg"] = map[string]oracleQuery{
		"as_they_come": byG(x.Mode(true), s.Mode(true), b.Mode(true), d.Mode(true), i.Mode(true).First().Alias("i1")),
		"sorted":       byG(x.Mode(false), s.Mode(false), b.Mode(false), d.Mode(false)),
		"over_the_frame": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(s.Mode(true).First(), i.Mode(true).Last())
		},
		"in_a_dynamic_group": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithRowIndex("r", 0).WithColumns(c("r").Cast(ursus.Int64)).
				GroupByDynamic(c("r"), ursus.DynamicOptions{Every: ursus.Every("4i")}).Agg(i.Mode(true))
		},
		"mode_of_a_sort":        byG(i.Sort().Mode(true)),
		"struct_mode":           byG(ursus.Struct(x).Mode(true).Alias("m")),
		"struct_mode_unordered": byG(ursus.Struct(x).Mode(false).Alias("m")),
		"mode_gen_first_seen":   byG(i.Mode(true).Alias("i"), s.Mode(true).Alias("s"), f.Mode(true).Alias("f"), b.Mode(true).Alias("b")),
		"mode_gen_sorted":       byG(i.Mode(false).Alias("i"), s.Mode(false).Alias("s"), f.Mode(false).Alias("f"), b.Mode(false).Alias("b")),
		"mode_gen_first":        byG(i.Mode(true).First().Alias("i1"), s.Mode(true).Last().Alias("sl")),
	}
}
