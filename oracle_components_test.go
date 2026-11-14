package ursus_test

// The components group (step 498, from components_test.go's tables): MakeDate,
// MakeDatetime, MakeTime, MakeDuration, FromEpoch and BusinessDayCount, each spelled as
// bench/scripts/oracle/components.py asks Polars.

import (
	"time"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	day := func(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }
	monSat := ursus.BusinessCalendar{WeekMask: [7]bool{true, true, true, true, true, true, false}}
	winterMonSat := ursus.BusinessCalendar{WeekMask: monSat.WeekMask, Holidays: []time.Time{day(2024, 12, 25), day(2025, 1, 1)}}
	parts := func(p ursus.DurationParts) ursus.DurationParts { return p }
	oracleGroups["components"] = map[string]oracleQuery{
		"date":            sel(ursus.MakeDate(c("y"), c("m"), c("d"))),
		"date_int8_month": sel(ursus.MakeDate(c("y"), c("m").Cast(ursus.Int8), 3)),
		"datetime":        sel(ursus.MakeDatetime(c("y"), c("m"), c("d"), c("h"), 30, 15, 250)),
		"time":            sel(ursus.MakeTime(c("h"), 5, 7, 250)),
		"duration_every_part": sel(ursus.MakeDuration(parts(ursus.DurationParts{Weeks: 1, Days: 2, Hours: 3, Minutes: 4, Seconds: 5,
			Milliseconds: 6, Microseconds: 7}))),
		"duration_nanoseconds":      sel(ursus.MakeDuration(parts(ursus.DurationParts{Nanoseconds: 5}))),
		"duration_in_milliseconds":  sel(ursus.MakeDuration(parts(ursus.DurationParts{Seconds: 1}), ursus.Milli)),
		"duration_float_hours":      sel(ursus.MakeDuration(parts(ursus.DurationParts{Hours: c("f")}))),
		"duration_of_columns":       sel(ursus.MakeDuration(parts(ursus.DurationParts{Days: c("d"), Hours: c("h")}))),
		"duration_negative":         sel(ursus.MakeDuration(parts(ursus.DurationParts{Days: -1, Hours: 2}))),
		"duration_fractions_summed": sel(ursus.MakeDuration(parts(ursus.DurationParts{Microseconds: 0.6, Milliseconds: 0.0004}))),
		"duration_toward_zero":      sel(ursus.MakeDuration(parts(ursus.DurationParts{Microseconds: -0.6}))),
		"duration_no_part":          sel(ursus.MakeDuration(parts(ursus.DurationParts{}))),
		"from_epoch_seconds":        sel(ursus.FromEpoch(c("s"), ursus.Second)),
		"from_epoch_milliseconds":   sel(ursus.FromEpoch(c("ms"), ursus.Milli)),
		"from_epoch_microseconds":   sel(ursus.FromEpoch(c("ms"), ursus.Micro)),
		"from_epoch_nanoseconds":    sel(ursus.FromEpoch(c("ms"), ursus.Nano)),
		"from_epoch_float":          sel(ursus.FromEpoch(ursus.Lit(1.0000009), ursus.Second)),
		"from_epoch_float_negative": sel(ursus.FromEpoch(ursus.Lit(-1.0000009), ursus.Second)),
		"in_zone": sel(
			ursus.MakeDatetime(2024, 3, 10, 12, 0, 0, 0).Dt().ReplaceTimeZone("America/New_York").Dt().Strftime("%H:%M %z").Alias("a"),
			ursus.MakeDatetime(2024, 11, 3, 1, 30, 0, 0).Dt().ReplaceTimeZone("America/New_York", ursus.AmbiguousEarliest).
				Dt().Strftime("%H:%M %z").Alias("b")),
		"business_day_count":         sel(ursus.BusinessDayCount(c("s"), c("e"))),
		"business_day_count_mon_sat": sel(ursus.BusinessDayCount(c("s"), c("e"), monSat)),
		"business_day_count_holidays": sel(ursus.BusinessDayCount(c("s"), c("e"),
			ursus.BusinessCalendar{Holidays: []time.Time{day(2024, 1, 2), day(2024, 1, 6)}})),

		// Asked of Polars as minus the count from end to start (step 507): with the end
		// before the start ursus counts [end, start), Polars (end, start].
		"business_day_count_reversed": sel(ursus.BusinessDayCount(c("s"), c("e"))),

		"components_gen": sel(ursus.MakeDate(c("y"), c("m"), c("d")).Alias("date"),
			ursus.MakeDatetime(c("y"), c("m"), c("d"), c("h"), c("mi"), c("sec"), c("us")).Alias("datetime"),
			ursus.MakeTime(c("h"), c("mi"), c("sec"), c("us")).Alias("time")),
		"duration_gen": sel(ursus.MakeDuration(parts(ursus.DurationParts{Days: c("days"), Hours: c("hours")})).Alias("dh"),
			ursus.MakeDuration(parts(ursus.DurationParts{Milliseconds: c("ms")})).Alias("ms"),
			ursus.MakeDuration(parts(ursus.DurationParts{Days: c("days")}), ursus.Milli).Alias("d_ms")),
		"from_epoch_gen": sel(ursus.FromEpoch(c("s"), ursus.Second).Alias("s"), ursus.FromEpoch(c("ms"), ursus.Milli).Alias("ms"),
			ursus.FromEpoch(c("ms"), ursus.Micro).Alias("us"), ursus.FromEpoch(c("ms"), ursus.Nano).Alias("ns"),
			ursus.FromEpoch(c("f"), ursus.Second).Alias("f")),
		"business_day_count_gen": sel(ursus.BusinessDayCount(c("s"), c("e")).Alias("n"),
			ursus.BusinessDayCount(c("s"), c("e"), winterMonSat).Alias("n_ms")),
	}
	// MakeDatetime is named datetime here, where Polars 2 names it after its first
	// input (feature-gaps.md §11, step 487 §3); a duration of columns is named duration,
	// where Polars names it literal (§9, step 265).
	oracleDifferences["components/datetime"] = oracleDifference{
		why:   "MakeDatetime is named datetime here; Polars 2 names it after its first input, y",
		names: map[string]string{"y": "datetime"},
	}
	oracleDifferences["components/duration_float_hours"] = oracleDifference{
		why:   "a duration of columns is named duration here, literal in Polars",
		names: map[string]string{"literal": "duration"},
	}
	oracleDifferences["components/duration_of_columns"] = oracleDifference{
		why:   "a duration of columns is named duration here, literal in Polars",
		names: map[string]string{"literal": "duration"},
	}
}
