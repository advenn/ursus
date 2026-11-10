package ursus_test

// The join_byhand group (step 501, from join_byhand_test.go's hand answers): at what
// type a join key is compared, each spelled as bench/scripts/oracle/join_byhand.py asks
// Polars, which is asked with the narrow key cast where it refuses two widths.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	on := ursus.JoinOn(c("k"))
	asOfBy := func(sel ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("left").JoinAsOf(in.frame("right"), ursus.AsOfOn(c("k")), ursus.AsOfBy(c("g"))).Select(sel...)
		}
	}
	joined := func(sel ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("left").Join(in.frame("right"), on).Select(sel...)
		}
	}
	oracleGroups["join_byhand"] = map[string]oracleQuery{
		"asof_by_int32_against_int64": asOfBy(c("lv"), c("rv")),
		"asof_by_int64_against_int32": asOfBy(c("lv"), c("rv")),
		"right_join_null_key": func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("left").Join(in.frame("right"), ursus.JoinOn(c("k1"), c("k2")), ursus.JoinHow(ursus.JoinRight)).
				Filter(c("rv").Eq(-1).Or(c("lv").Eq(-12345))).Select(c("k1"), c("k2"), c("rv"))
		},
		"validate_1m_null_left_keys": func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("left").Join(in.frame("right"), on, ursus.JoinValidate(ursus.ValidateOneToMany))
		},
		"asof_by_null_matches_nothing":    asOfBy(c("k"), c("rv")),
		"int32_against_float32_exact":     joined(c("k").Cast(ursus.Int64)),
		"int64_against_uint64_at_int128":  joined(c("k").Cast(ursus.Int64)),
		"datetime_ms_against_ns_in_range": joined(c("lv"), c("rv")),
		// The output k is Int64, where 50000*50000 = 2.5e9 > 2e9. Pushed into the Int32
		// side unconverted, the product wraps negative and the row is lost (O6).
		"filter_on_a_coalesced_key_of_two_widths": func(in oracleInputs) *ursus.LazyFrame {
			return in.frame("left").Join(in.frame("right"), on).Filter(c("k").Mul(c("k")).Gt(int64(2_000_000_000))).Select(c("lv"), c("rv"))
		},
		"join_widths_gen":    joined(c("k").Cast(ursus.Int64), c("lv"), c("rv")),
		"asof_by_widths_gen": asOfBy(c("lv"), c("rv")),
	}
}
