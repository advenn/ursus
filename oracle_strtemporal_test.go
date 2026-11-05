package ursus_test

// The strtemporal group (step 498, from strtemporal_test.go's table): Str().ToDate,
// ToDatetime and ToTime of no format, and Strptime of none, each spelled as
// bench/scripts/oracle/strtemporal.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	x := ursus.Col("x").Str()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	ny := "America/New_York"
	oracleGroups["strtemporal"] = map[string]oracleQuery{
		"to_date_day_first":                 sel(x.ToDate()),
		"to_date_lenient_null":              sel(x.ToDate()),
		"to_date_null_first":                sel(x.ToDate()),
		"to_date_never_month_first":         sel(x.ToDate()),
		"strptime_no_format":                sel(x.Strptime(ursus.Date, "", true)),
		"to_datetime_two_instants":          sel(x.ToDatetime(ursus.Micro, "")),
		"to_datetime_milliseconds":          sel(x.ToDatetime(ursus.Milli, "")),
		"to_datetime_nanoseconds":           sel(x.ToDatetime(ursus.Nano, "")),
		"to_datetime_of_a_date":             sel(x.ToDatetime(ursus.Micro, "")),
		"to_datetime_zone_wall_clock":       sel(x.ToDatetime(ursus.Micro, "Asia/Tokyo")),
		"to_datetime_offset_in_zone":        sel(x.ToDatetime(ursus.Micro, "Asia/Tokyo")),
		"to_datetime_offset_in_utc":         sel(x.ToDatetime(ursus.Micro, "UTC")),
		"to_time_one_digit_hour":            sel(x.ToTime("", false)),
		"to_datetime_skipped_wall_clock":    sel(x.ToDatetime(ursus.Micro, ny)),
		"to_datetime_wall_clock_read_twice": sel(x.ToDatetime(ursus.Micro, ny)),

		"to_date_gen_iso":       sel(x.ToDate()),
		"to_date_gen_day_first": sel(x.ToDate()),
		"to_date_gen_dots":      sel(x.ToDate()),
		"to_datetime_gen": sel(x.ToDatetime(ursus.Micro, "").Alias("us"), x.ToDatetime(ursus.Milli, "").Alias("ms"),
			x.ToDatetime(ursus.Nano, "").Alias("ns"), x.ToDatetime(ursus.Micro, "Asia/Kolkata").Alias("kolkata")),
		"to_datetime_gen_offsets": sel(x.ToDatetime(ursus.Micro, "UTC").Alias("utc"), x.ToDatetime(ursus.Micro, "Asia/Tokyo").Alias("tokyo")),
		"to_time_gen":             sel(x.ToTime("", true)),
	}
}
