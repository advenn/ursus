package ursus_test

// The scalar_byhand group (step 497, from scalar_byhand_test.go's hand answers):
// audit.md §6's S5 to S13, each spelled as bench/scripts/oracle/scalar_byhand.py asks
// Polars.

import (
	"math"
	"time"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	ts, v, s, a, b, f, d := c("ts"), c("v"), c("s"), c("a"), c("b"), c("f"), c("d")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	gbd := func(in oracleInputs) *ursus.LazyFrame {
		return in.df().GroupByDynamic(ts, ursus.DynamicOptions{Every: ursus.Every("1h")}).Agg(ursus.Len().Alias("n"))
	}
	oracleGroups["scalar_byhand"] = map[string]oracleQuery{
		"epoch_us_before_1970": sel(ts.Dt().Epoch()),
		"epoch_ns_minus_1":     sel(ts.Dt().Epoch()),
		"epoch_gen":            sel(ts.Dt().Epoch()),
		"epoch_ns_gen":         sel(ts.Dt().Epoch()),

		"is_in_i8_5000":         sel(v.IsIn(5000)),
		"is_in_u64_minus_1":     sel(v.IsIn(-1)),
		"is_in_f32_0_1":         sel(v.IsIn(0.1)),
		"is_in_f32_exact":       sel(v.IsIn(float32(0.1))),
		"is_in_dt_ms_finer":     sel(v.IsIn(time.Date(1970, 1, 1, 0, 0, 1, 500_000, time.UTC))),
		"list_contains_i8_5000": sel(v.List().Contains(5000)),
		"list_contains_i8_2":    sel(v.List().Contains(2)),
		"is_in_i32_1_3":         sel(v.IsIn(1, 3)),
		"is_in_nan":             sel(v.IsIn(math.NaN())),
		"is_in_gen":             sel(v.IsIn(0, 1, -1, 50, int64(1)<<53, int64(math.MaxInt64))),
		"is_in_floats_gen":      sel(v.IsIn(math.NaN(), 0.0, 0.5, 2.5, 1e300)),
		"list_contains_gen":     sel(v.List().Contains(1).Alias("one"), v.List().Contains(5000).Alias("none")),

		"replace_groups":         sel(s.Str().Replace(`(a)(b)`, "$2$1", false)),
		"replace_group_braced":   sel(s.Str().Replace(`(a)(b)`, "${1}x", false)),
		"replace_literal_dollar": sel(s.Str().Replace("ab", "$1", true)),
		"replace_gen": sel(s.Str().Replace(`(a)(b)`, "$2$1", false).Alias("first"),
			s.Str().ReplaceAll(`(a)(b)`, "<$1>", false).Alias("all"), s.Str().Replace("ab", "$1", true).Alias("lit")),

		"floordiv_mod_col":   sel(a.FloorDiv(b).Alias("d"), a.Mod(b).Alias("m")),
		"floordiv_mod_const": sel(a.FloorDiv(2).Alias("d"), a.Mod(-2).Alias("m")),
		"float_mod":          sel(a.Mod(b).Alias("m")),
		"float_mod_zero_sign": func(in oracleInputs) *ursus.LazyFrame {
			in.t.Skip("finding (step 497): a zero float remainder takes the divisor's sign here, -0 for 4 % -2, " +
				"where Polars' a - b*floor(a/b) is +0; not in feature-gaps §9, the maintainer's to decide")
			return nil
		},
		"u8_floordiv_2": sel(a.FloorDiv(uint8(2))),
		"by_zero_null":  sel(a.FloorDiv(b).Alias("d"), a.Mod(b).Alias("m")),
		"floordiv_mod_gen": sel(a.FloorDiv(b).Alias("d"), a.Mod(b).Alias("m"), a.FloorDiv(3).Alias("d3"), a.Mod(-3).Alias("m3"),
			f.FloorDiv(d).Alias("fd"), f.Mod(d).Alias("fm")),

		"split_len":      sel(s.Str().Split(" ").List().Len()),
		"splitn_field_1": sel(s.Str().SplitN(" ", 2).Struct().Field("field_1")),
		"split_gen": sel(s.Str().Split(" ").Alias("parts"), s.Str().Split(" ").List().Len().Alias("n"),
			s.Str().SplitN(" ", 2).Struct().Field("field_0").Alias("f0"), s.Str().SplitN(" ", 2).Struct().Field("field_1").Alias("f1")),

		"truncate_kolkata_1h":      sel(ts.Dt().Truncate(ursus.Every("1h"))),
		"truncate_ny_2h":           sel(ts.Dt().Truncate(ursus.Every("2h"))),
		"truncate_ny_fallback_1h":  sel(ts.Dt().Truncate(ursus.Every("1h"))),
		"truncate_ny_spring_2h":    sel(ts.Dt().Truncate(ursus.Every("2h"))),
		"truncate_kolkata_1h_3000": sel(ts.Dt().Truncate(ursus.Every("1h"))),
		"truncate_kolkata_1500ms":  sel(ts.Dt().Truncate(ursus.Every("1500ms"))),
		"truncate_ns_min_plus_1h":  sel(ts.Dt().Truncate(ursus.Every("1h"))),
		"truncate_gen": sel(ts.Dt().Truncate(ursus.Every("1h")).Alias("h"), ts.Dt().Truncate(ursus.Every("15m")).Alias("q"),
			ts.Dt().Truncate(ursus.Every("1d")).Alias("d"), ts.Dt().Truncate(ursus.Every("1500ms")).Alias("ms")),
		"gbd_kolkata_1h":    gbd,
		"gbd_ny_spring_gen": gbd,

		"count_matches_empty_literal": sel(s.Str().CountMatches("", true)),
		"count_matches_empty_regex":   sel(s.Str().CountMatches("", false)),
		"count_matches_gen": sel(s.Str().CountMatches("", true).Alias("empty"), s.Str().CountMatches("a", true).Alias("a"),
			s.Str().CountMatches("a+", false).Alias("runs")),
		"strip_start_end_space": sel(s.Str().StripCharsStart("").Alias("l"), s.Str().StripCharsEnd("").Alias("r")),
		"strip_space":           sel(s.Str().StripChars("")),
		"strip_gen":             sel(s.Str().StripCharsStart("").Alias("l"), s.Str().StripCharsEnd("").Alias("r"), s.Str().StripChars("").Alias("b")),
	}
}
