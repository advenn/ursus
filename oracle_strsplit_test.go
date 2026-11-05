package ursus_test

// The strsplit group (step 499, from strsplit_test.go's hand values): str.split
// telling an empty string from a null, its list fed on, extract_all, pad_start,
// pad_end and zfill, each spelled as bench/scripts/oracle/strsplit.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	s := c("s").Str()
	split := s.Split(",")
	with := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().WithColumns(exprs...) }
	}
	oracleGroups["strsplit"] = map[string]oracleQuery{
		"split": with(split.Alias("parts")),
		"split_feeds_the_namespace": with(split.List().Len().Alias("n"), split.List().Get(0).Alias("first"),
			split.List().Sort().List().Get(0).Alias("smallest")),
		"split_then_explode": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(split.Alias("part")).Explode("part")
		},
		"split_then_filter": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(split.Alias("parts")).Filter(c("id").Gt(int64(2)))
		},
		"extract_all":   with(s.ExtractAll(`\d+`).Alias("nums")),
		"pad_and_zfill": with(s.PadStart(4, "0").Alias("ps"), s.PadEnd(4, ".").Alias("pe"), s.ZFill(4).Alias("z")),
		"group_by_a_list": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(split.Alias("parts")).GroupBy(c("parts")).MaintainOrder().Agg(ursus.Len().Alias("n"))
		},

		"split_gen": with(split.Alias("parts"), split.List().Len().Alias("n"), split.List().Get(0).Alias("first"),
			split.List().Sort().List().Get(-1).Alias("largest")),
		"split_explode_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(split.Alias("part")).Explode("part")
		},
		"group_by_a_list_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(split.Alias("parts")).GroupBy(c("parts")).Agg(ursus.Len().Cast(ursus.Int64).Alias("n"),
				c("id").Min().Alias("first_id"))
		},
		"extract_all_gen": with(s.ExtractAll(`\d+`).Alias("nums"), s.ExtractAll("a+").Alias("as")),
		"pad_gen": with(s.PadStart(4, "0").Alias("ps"), s.PadEnd(6, ".").Alias("pe"), s.ZFill(5).Alias("z"), s.PadStart(3, "é").Alias("ps3"),
			s.ZFill(2).Alias("z2")),
	}
	// A count is a Uint64 here, Polars' a UInt32 (feature-gaps.md §9).
	oracleDifferences["strsplit/group_by_a_list"] = oracleDifference{
		why: "a count is a Uint64 here, Polars' a UInt32 (feature-gaps.md §9)", dtypes: map[string]string{"n": "Uint64"}}
}
