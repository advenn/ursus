package ursus_test

// The tzreplace group (step 498, from tzreplace_test.go's table): replace_time_zone
// read back in UTC, each spelled as bench/scripts/oracle/tzreplace.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	d, t := c("d").Dt(), c("t").Dt()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	inUTC := func(e ursus.DtExpr, tz string, opts ...ursus.ZoneOption) ursus.Expr {
		return e.ReplaceTimeZone(tz, opts...).Dt().ConvertTimeZone("UTC")
	}
	london, ny, lordHowe := "Europe/London", "America/New_York", "Australia/Lord_Howe"
	oracleGroups["tzreplace"] = map[string]oracleQuery{
		"naive_gains_london":            sel(inUTC(d, london)),
		"spring_forward_null":           sel(inUTC(d, london, ursus.NonExistentNull)),
		"fall_back_earliest":            sel(inUTC(d, london, ursus.AmbiguousEarliest)),
		"fall_back_latest":              sel(inUTC(d, london, ursus.AmbiguousLatest)),
		"fold_and_gap_null":             sel(inUTC(d, london, ursus.AmbiguousNull, ursus.NonExistentNull)),
		"apia_day_never_read":           sel(inUTC(d, "Pacific/Apia", ursus.NonExistentNull)),
		"lord_howe_earliest":            sel(inUTC(d, lordHowe, ursus.AmbiguousEarliest)),
		"lord_howe_latest":              sel(inUTC(d, lordHowe, ursus.AmbiguousLatest)),
		"zoned_changes_zone":            sel(inUTC(d, "Asia/Tokyo")),
		"zoned_made_naive":              sel(d.ReplaceTimeZone("")),
		"fold_read_in_zone_without_one": sel(inUTC(d, london)),
		"same_zone_keeps_fold":          sel(d.ReplaceTimeZone(london)),
		"fold_in_another_zone":          sel(inUTC(d, "Europe/Paris")),
		"before_1970":                   sel(inUTC(d, london)),
		"nanoseconds":                   sel(inUTC(d, ny)),
		"keeps_unit_ms":                 sel(d.ReplaceTimeZone("Asia/Tokyo")),

		"replace_gen_naive": sel(inUTC(t, ny, ursus.AmbiguousEarliest, ursus.NonExistentNull).Alias("ny_earliest"),
			inUTC(t, ny, ursus.AmbiguousLatest, ursus.NonExistentNull).Alias("ny_latest"),
			inUTC(t, lordHowe, ursus.AmbiguousNull, ursus.NonExistentNull).Alias("lord_howe_null"),
			inUTC(t, "Asia/Tokyo").Alias("tokyo")),
		"replace_gen_zoned": sel(inUTC(t, "Europe/Paris", ursus.AmbiguousEarliest, ursus.NonExistentNull).Alias("paris"),
			t.ReplaceTimeZone("").Alias("naive")),
		"replace_gen_ms": sel(t.ReplaceTimeZone("Asia/Kolkata")),
	}
}
