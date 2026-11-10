package ursus_test

// The seq group (step 500, from seq_test.go's tables): rle and rle_id, append and
// extend_constant, each spelled as bench/scripts/oracle/seq.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	a, f, s, g := c("a"), c("f"), c("s"), c("g")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	byG := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().GroupBy(g).Agg(exprs...).Sort(ursus.Asc(g)) }
	}
	// A rle's struct unnested in a Select of its own (the zipper refuses a reshape
	// and its unnest together); a group's, exploded first.
	runs := func(col ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Select(col.Rle().Alias("r")).Select(c("r").Struct().Field("*"))
		}
	}
	groupRuns := func(in oracleInputs) *ursus.LazyFrame {
		return in.df().GroupBy(g).Agg(a.Rle()).Sort(ursus.Asc(g)).Explode("a").Select(g, a.Struct().Field("*"))
	}
	nine := ursus.Lit(int64(9))
	oracleGroups["seq"] = map[string]oracleQuery{
		"rle":              runs(a),
		"rle_nan_zeros":    runs(f),
		"rle_text":         runs(s),
		"rle_id":           sel(a.RleID()),
		"rle_id_nan_zeros": sel(f.RleID()),
		"rle_id_over_g":    sel(a.RleID().Over(g)),
		"rle_by_group":     groupRuns,
		"rle_id_no_rows":   sel(a.RleID()),
		"rle_id_struct":    sel(ursus.Struct(a, s).RleID()),

		"append":                    sel(a.Append(g, true)),
		"append_constant":           sel(a.Append(nine, true)),
		"append_constant_by_group":  byG(a.Append(nine, true)),
		"append_by_group":           byG(a.Append(g, true)),
		"append_float":              sel(a.Append(f, true)),
		"extend_constant":           sel(a.ExtendConstant(7, 2)),
		"extend_null":               sel(a.ExtendConstant(nil, 1)),
		"extend_float_truncated":    sel(a.ExtendConstant(1.5, 1)),
		"extend_negative_truncated": sel(a.ExtendConstant(-1.5, 1)),
		"extend_past_int8":          sel(a.ExtendConstant(300, 1)),
		"extend_number_to_text":     sel(a.ExtendConstant(5, 1)),
		"extend_n_expression":       sel(a.ExtendConstant(0, ursus.Lit(int64(2)))),
		"extend_by_group":           byG(a.ExtendConstant(0, 1)),
		"extend_float_by_group":     byG(a.ExtendConstant(-1.5, 1)),

		"rle_id_gen": sel(a.RleID().Alias("a"), f.RleID().Alias("f"), s.RleID().Alias("s"), a.RleID().Over(g).Alias("ag"),
			ursus.Struct(a, s).RleID().Alias("as")),
		"rle_gen":             runs(a),
		"rle_text_gen":        runs(s),
		"rle_by_group_gen":    groupRuns,
		"append_gen":          sel(a.Append(c("b"), true)),
		"extend_gen":          sel(a.ExtendConstant(-3, 3)),
		"extend_by_group_gen": byG(a.ExtendConstant(9, 2)),
	}
}
