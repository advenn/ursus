package ursus_test

// The calendar group (step 498, from calendar_test.go's by-hand tables): offset_by,
// month_start and month_end, is_leap_year, round, convert_time_zone and truncate across
// a midnight that does not exist, each spelled as bench/scripts/oracle/calendar.py asks
// Polars. Two cases are findings, recorded and skipped: Polars 2.1 rounds a month by
// adding fourteen days and truncating, and starts group_by_dynamic's day at 23:00 the
// day before where midnight does not exist.

import (
	"time"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	d, t := c("d").Dt(), c("t").Dt()
	every := ursus.Every
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	tokyo := d.ConvertTimeZone("Asia/Tokyo")
	oracleGroups["calendar"] = map[string]oracleQuery{
		"offset_1mo_dates":            sel(d.OffsetBy(every("1mo"))),
		"offset_1y_leap_day":          sel(d.OffsetBy(every("1y"))),
		"offset_minus_1mo_utc":        sel(d.OffsetBy(every("-1mo"))),
		"offset_2d_date":              sel(d.OffsetBy(48 * time.Hour)),
		"offset_1d2h_naive":           sel(d.OffsetBy(every("1d2h"))),
		"offset_1h_ny_spring_forward": sel(d.OffsetBy(every("1h"))),
		"offset_minus_90m_ny":         sel(d.OffsetBy(-90 * time.Minute)),

		"month_start_dates":      sel(d.MonthStart()),
		"month_end_dates":        sel(d.MonthEnd()),
		"month_start_keeps_time": sel(d.MonthStart()),
		"month_end_tokyo":        sel(d.MonthEnd()),
		"is_leap_year":           sel(d.IsLeapYear()),
		"is_leap_year_zoned":     sel(d.IsLeapYear().Alias("utc"), tokyo.Dt().IsLeapYear().Alias("tokyo")),

		"round_15m":             sel(d.Round(15 * time.Minute)),
		"round_1h_before_epoch": sel(d.Round(every("1h"))),
		"round_1mo": oracleFinding("Polars 2.1 rounds a month by adding fourteen days and truncating, so noon on the 15th of a " +
			"29-day February rounds down; ursus rounds to the nearer month, a value halfway up (Round's doc)"),
		"round_1mo_dates": oracleFinding("Polars 2.1 rounds a month by adding fourteen days and truncating, so the 17th of " +
			"January rounds down; ursus rounds to the nearer month"),
		"round_1h_kolkata": sel(d.Round(every("1h"))),

		"convert_tokyo": sel(tokyo, tokyo.Dt().Hour().Alias("h"), tokyo.Dt().ConvertTimeZone("UTC").Alias("back")),

		"truncate_1d_santiago":  sel(d.Truncate(every("1d"))),
		"truncate_1d_havana":    sel(d.Truncate(every("1d"))),
		"truncate_1mo_asuncion": sel(d.Truncate(every("1mo"))),
		"midnight_gap_windows": oracleFinding("Polars 2.1's group_by_dynamic starts 2024-09-08's window at 23:00 on the 7th in " +
			"Santiago, a day off its own truncate; ursus starts it at 01:00, where the day begins (step 145)"),

		"offset_gen_dates": sel(d.OffsetBy(every("1mo")).Alias("mo"), d.OffsetBy(every("-1mo")).Alias("mo_back"),
			d.OffsetBy(every("1y")).Alias("y"), d.OffsetBy(every("1d")).Alias("day"), d.OffsetBy(every("2w")).Alias("w2"),
			d.OffsetBy(every("1mo1d")).Alias("mo1d")),
		"offset_gen_ny": sel(t.OffsetBy(every("1mo")).Alias("mo"), t.OffsetBy(every("1y")).Alias("y"),
			t.OffsetBy(every("1d")).Alias("day"), t.OffsetBy(every("2d3h")).Alias("d2h3"), t.OffsetBy(every("1h")).Alias("h"),
			t.OffsetBy(every("-90m")).Alias("m90")),
		"month_gen_dates": sel(d.MonthStart().Alias("start"), d.MonthEnd().Alias("end"), d.IsLeapYear().Alias("leap")),
		"month_gen_ny":    sel(t.MonthStart().Alias("start"), t.MonthEnd().Alias("end"), t.IsLeapYear().Alias("leap")),
		"round_gen_naive": sel(t.Round(every("15m")).Alias("m15"), t.Round(every("1h")).Alias("h"), t.Round(every("1d")).Alias("day")),
		"round_gen_utc":   sel(t.Round(every("15m")).Alias("m15"), t.Round(every("1h")).Alias("h")),
		"convert_gen": sel(t.ConvertTimeZone("Asia/Tokyo").Alias("tokyo"), t.ConvertTimeZone("America/New_York").Alias("ny"),
			t.ConvertTimeZone("Asia/Kolkata").Dt().Hour().Alias("kolkata_hour")),
	}
	// A Datetime's components are each an Int32 here (expr_dt.go), where Polars' hour is
	// an Int8.
	oracleDifferences["calendar/convert_tokyo"] = oracleDifference{
		why:    "dt.hour is an Int32 here, as every component is; Polars' is an Int8",
		dtypes: map[string]string{"h": "Int32"},
	}
	oracleDifferences["calendar/convert_gen"] = oracleDifference{
		why:    "dt.hour is an Int32 here, as every component is; Polars' is an Int8",
		dtypes: map[string]string{"kolkata_hour": "Int32"},
	}
}
