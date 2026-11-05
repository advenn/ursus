package ursus_test

// The daterange group (step 498, from daterange_test.go's table): DateRange,
// DatetimeRange and TimeRange of literal ends, aliased r on both sides, and the per-row
// forms, each spelled as bench/scripts/oracle/daterange.py asks Polars.

import (
	"time"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	every := ursus.Every
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	r := func(e ursus.Expr) oracleQuery { return sel(e.Alias("r")) }
	ny := func(e ursus.Expr) ursus.Expr { return e.Dt().ReplaceTimeZone("America/New_York") }
	// A Time(ns) literal, Polars' Time being nanoseconds.
	clockNs := func(h, m int) ursus.Expr {
		return ursus.Lit(time.Date(1970, 1, 1, h, m, 0, 0, time.UTC)).Cast(ursus.TimeOf(ursus.Nano))
	}
	s, e := c("s"), c("e")
	oracleGroups["daterange"] = map[string]oracleQuery{
		"days":                       r(ursus.DateRange(dateLit(2024, 1, 30), dateLit(2024, 2, 2), every("1d"))),
		"months_each_from_the_start": r(ursus.DateRange(dateLit(2024, 1, 31), dateLit(2024, 5, 1), every("1mo"))),
		"weeks":                      r(ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 20), every("1w"))),
		"closed_left":                r(ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 3), every("1d"), ursus.ClosedLeft)),
		"closed_none":                r(ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 3), every("1d"), ursus.ClosedNone)),
		"reversed":                   r(ursus.DateRange(dateLit(2024, 1, 3), dateLit(2024, 1, 1), every("1d"))),
		"negative_interval":          r(ursus.DateRange(dateLit(2024, 1, 3), dateLit(2024, 1, 1), every("-1d"))),
		"from_an_instant":            r(ursus.DateRange(wallLit(2024, 1, 1, 12, 0), wallLit(2024, 1, 3, 0, 0), every("1d"))),
		"hours":                      r(ursus.DatetimeRange(wallLit(2024, 1, 1, 0, 0), wallLit(2024, 1, 1, 3, 0), time.Hour)),
		"ninety_minutes":             r(ursus.DatetimeRange(wallLit(2024, 1, 1, 0, 0), wallLit(2024, 1, 1, 3, 0), every("90m"))),
		"from_dates":                 r(ursus.DatetimeRange(dateLit(2024, 1, 1), dateLit(2024, 1, 2), every("12h"))),
		"ny_day_on_the_wall_clock":   r(ursus.DatetimeRange(ny(wallLit(2024, 3, 9, 12, 0)), ny(wallLit(2024, 3, 11, 12, 0)), every("1d"))),
		"ny_hour_elapsed":            r(ursus.DatetimeRange(ny(wallLit(2024, 3, 10, 0, 0)), ny(wallLit(2024, 3, 10, 4, 0)), time.Hour)),
		"months_at_six":              r(ursus.DatetimeRange(wallLit(2024, 1, 31, 6, 0), wallLit(2024, 4, 30, 6, 0), every("1mo"))),
		"a_month_and_a_day":          r(ursus.DatetimeRange(wallLit(2024, 1, 31, 0, 0), wallLit(2024, 6, 1, 0, 0), every("1mo1d"))),
		"times":                      r(ursus.TimeRange(clockNs(9, 0), clockNs(11, 0), 30*time.Minute)),
		"times_open":                 r(ursus.TimeRange(clockNs(9, 0), clockNs(11, 0), time.Hour, ursus.ClosedNone)),
		"range_beside_columns": func(in oracleInputs) *ursus.LazyFrame {
			return in.df().WithColumns(ursus.DateRange(dateLit(2024, 1, 1), dateLit(2024, 1, 3), every("1d")).Dt().Day().Alias("d"))
		},
		"date_ranges_per_row": sel(ursus.DateRanges(s, e, every("1d"))),

		"date_ranges_gen": sel(ursus.DateRanges(s, e, every("1d")).Alias("d1"), ursus.DateRanges(s, e, every("1w"), ursus.ClosedLeft).Alias("w1"),
			ursus.DateRanges(s, e, every("1mo"), ursus.ClosedRight).Alias("mo1"), ursus.DateRanges(s, e, every("3d"), ursus.ClosedNone).Alias("d3")),
		"datetime_ranges_gen": sel(ursus.DatetimeRanges(s, e, every("6h")).Alias("h6"),
			ursus.DatetimeRanges(s, e, every("1d"), ursus.ClosedLeft).Alias("d1"), ursus.DatetimeRanges(s, e, every("90m"), ursus.ClosedNone).Alias("m90")),
		"time_ranges_gen": sel(ursus.TimeRanges(s, e, 30*time.Minute).Alias("m30"), ursus.TimeRanges(s, e, time.Hour, ursus.ClosedRight).Alias("h1")),
	}
	// A Datetime's components are each an Int32 here (expr_dt.go), where Polars' day is
	// an Int8.
	oracleDifferences["daterange/range_beside_columns"] = oracleDifference{
		why:    "dt.day is an Int32 here, as every component is; Polars' is an Int8",
		dtypes: map[string]string{"d": "Int32"},
	}
}
