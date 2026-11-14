package ursus_test

// The castmore group (step 497, from castmore_test.go's pasted answers): step 325's
// str.to_time, str.to_decimal, dt.cast_time_unit and a float as text, each spelled as
// bench/scripts/oracle/castmore.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	t, v, f := c("t"), c("v"), c("f")
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	dt, du := ursus.Datetime(ursus.Micro, ""), ursus.Duration(ursus.Micro)
	oracleGroups["castmore"] = map[string]oracleQuery{
		"to_time_lenient":   sel(t.Str().ToTime("%H:%M:%S", false)),
		"to_time_format":    sel(t.Str().ToTime("%Hh%M", true)),
		"to_time_no_format": sel(t.Str().ToTime("", true)),
		"to_time_gen":       sel(t.Str().ToTime("%H:%M:%S", false)),
		"to_decimal_2":      sel(t.Str().ToDecimal(2)),
		// Asked of Python's decimal, ROUND_HALF_UP (step 507): ToDecimal rounds a tie
		// half away from zero, where Polars 2.1 rounds it half to even.
		"to_decimal_ties": sel(t.Str().ToDecimal(2).Alias("s2"), t.Str().ToDecimal(0).Alias("s0")),
		"to_decimal_gen":  sel(t.Str().ToDecimal(2).Alias("s2"), t.Str().ToDecimal(4).Alias("s4")),
		"cast_time_unit": sel(v.Cast(dt).Dt().CastTimeUnit(ursus.Milli).Alias("dt"), v.Cast(du).Dt().CastTimeUnit(ursus.Milli).Alias("du"),
			v.Cast(dt).Dt().CastTimeUnit(ursus.Nano).Alias("up")),
		"cast_time_unit_gen": sel(v.Cast(dt).Dt().CastTimeUnit(ursus.Milli).Alias("dt_ms"), v.Cast(dt).Dt().CastTimeUnit(ursus.Nano).Alias("dt_ns"),
			v.Cast(du).Dt().CastTimeUnit(ursus.Milli).Alias("du_ms"), v.Cast(du).Dt().CastTimeUnit(ursus.Nano).Alias("du_ns")),
		"float_as_text":     sel(f.Cast(ursus.String)),
		"f32_as_text":       sel(f.Cast(ursus.String)),
		"float_as_text_gen": sel(f.Cast(ursus.String)),
		"f32_as_text_gen":   sel(f.Cast(ursus.String)),
	}
}
