package ursus_test

// The enum_byhand group (step 499, from enum_byhand_test.go's hand values): an Enum
// built and used, each spelled as bench/scripts/oracle/enum_byhand.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	et, other := ursus.Enum("lo", "mid", "hi"), ursus.Enum("hi", "lo", "mid")
	cats := ursus.Enum("b", "a", "bb", "ab", "ba", "aa")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	concat := func(in oracleInputs) *ursus.LazyFrame {
		return ursus.Concat([]*ursus.LazyFrame{in.frame("top"), in.frame("bottom")})
	}
	oracleGroups["enum_byhand"] = map[string]oracleQuery{
		"sort": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Filter(c("e").IsNotNull()).Sort(ursus.Asc(c("e")))
		},
		"min_max": sel(c("e").Min().Alias("mn"), c("e").Max().Alias("mx")),
		"group_by": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Filter(c("e").IsNotNull()).GroupBy(c("e")).Agg(c("n").Sum().Cast(ursus.Int64)).Sort(ursus.Asc(c("e")))
		},
		"unique":               func(in oracleInputs) *ursus.LazyFrame { return in.df().Unique() },
		"concat":               concat,
		"shift":                sel(c("e").Shift(1)),
		"string_to_enum":       sel(c("s").Cast(et)),
		"string_to_enum_lossy": sel(c("s").CastLossy(et)),
		"enum_to_string":       sel(c("e").Cast(ursus.String)),
		"enum_to_enum":         sel(c("e").Cast(other)),
		"eq_mid":               sel(c("e").Eq("mid")),
		"eq_zzz":               sel(c("e").Eq("zzz")),
		"lt_mid":               sel(c("e").Lt("mid")),
		"eq_string_column":     sel(c("e").Eq(c("s"))),
		"is_in":                sel(c("e").IsIn("lo", "zzz")),
		"fill_null_lo":         sel(c("e").FillNullWith("lo")),
		"fill_null_zzz": func(in oracleInputs) *ursus.LazyFrame {
			in.t.Skip("finding: FillNullWith of a value outside the Enum's categories is a String column here, " +
				"where Polars 2.1 keeps the null and the Enum; not in feature-gaps.md §9")
			return nil
		},
		"concat_with_string": concat,

		"sort_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().Sort(ursus.Asc(c("e")).NullsLast(), ursus.Asc(c("k")).NullsLast())
		},
		"compare_gen": sel(c("e").Eq("ab").Alias("eq"), c("e").Lt("ab").Alias("lt"), c("e").Ge("ba").Alias("ge"),
			c("e").Eq(c("s")).Alias("eqs"), c("e").IsIn("a", "bb").Alias("isin")),
		"group_gen": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().GroupBy(c("k")).Agg(c("e").Min().Alias("mn"), c("e").Max().Alias("mx"), c("e").First().Alias("first"),
				c("e").Last().Alias("last")).Sort(ursus.Asc(c("k")))
		},
		"casts_gen": sel(c("e").Cast(ursus.String).Alias("txt"), c("e").FillNullWith("a").Alias("filled"), c("e").Shift(-1).Alias("sh"),
			c("s").CastLossy(cats).Alias("back")),
	}
}
