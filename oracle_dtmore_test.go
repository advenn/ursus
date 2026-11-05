package ursus_test

// The dtmore group (step 496, from dtmore_test.go's hand tables): iso_year, century
// and millennium, the business days, dt.replace, the zone offsets, a Duration's
// totals, the fractional second and the epochs, each spelled as
// bench/scripts/oracle/dtmore.py asks Polars.

import (
	"time"

	"github.com/advenn/ursus"
)

func init() {
	c := ursus.Col
	d, tcol, du, tz := c("d").Dt(), c("t").Dt(), c("du").Dt(), c("tz").Dt()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	day := func(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }
	christmas := ursus.BusinessCalendar{Holidays: []time.Time{day(2024, 12, 25)}}
	monSat := ursus.BusinessCalendar{WeekMask: [7]bool{true, true, true, true, true, true, false}}
	winter := ursus.BusinessCalendar{Holidays: []time.Time{day(2024, 12, 25), day(2025, 1, 1)}}
	winterMonSat := ursus.BusinessCalendar{WeekMask: monSat.WeekMask, Holidays: winter.Holidays}
	thuOff := ursus.BusinessCalendar{WeekMask: [7]bool{true, true, true, false, true, true, false}, Holidays: winter.Holidays}
	parts := func(p ursus.DateTimeParts) ursus.DateTimeParts { return p }
	epochs := func(e ursus.DtExpr) []ursus.Expr {
		return []ursus.Expr{e.EpochIn(ursus.Nano).Alias("ns"), e.EpochIn(ursus.Micro).Alias("us"),
			e.EpochIn(ursus.Milli).Alias("ms"), e.EpochIn(ursus.Second).Alias("s"), e.EpochDays().Alias("days")}
	}
	totals := func(e ursus.DtExpr) []ursus.Expr {
		return []ursus.Expr{e.TotalMilliseconds().Alias("ms"), e.TotalMicroseconds().Alias("us"),
			e.TotalNanoseconds().Alias("ns")}
	}
	fractional := func(e ursus.DtExpr, suffix string) []ursus.Expr {
		return []ursus.Expr{e.TotalDaysFractional().Alias("days" + suffix), e.TotalHoursFractional().Alias("hours" + suffix),
			e.TotalMinutesFractional().Alias("minutes" + suffix), e.TotalSecondsFractional().Alias("seconds" + suffix),
			e.TotalMillisecondsFractional().Alias("ms" + suffix), e.TotalMicrosecondsFractional().Alias("us" + suffix),
			e.TotalNanosecondsFractional().Alias("ns" + suffix)}
	}
	ms := c("d").Cast(ursus.Duration(ursus.Milli)).Dt()
	oracleGroups["dtmore"] = map[string]oracleQuery{
		"iso_century_millennium":           sel(d.IsoYear().Alias("iso"), d.Century().Alias("c"), d.Millennium().Alias("m")),
		"century_millennium_around_year_0": sel(d.Century().Alias("c"), d.Millennium().Alias("m")),
		"iso_year_datetime":                sel(d.IsoYear()),
		"iso_century_millennium_gen":       sel(d.IsoYear().Alias("iso"), d.Century().Alias("c"), d.Millennium().Alias("m")),

		"is_business_day":         sel(d.IsBusinessDay()),
		"is_business_day_holiday": sel(d.IsBusinessDay(christmas)),
		"is_business_day_mon_sat": sel(d.IsBusinessDay(monSat)),
		"add_1_forward":           sel(d.AddBusinessDays(1, ursus.RollForward)),
		"add_1_backward":          sel(d.AddBusinessDays(1, ursus.RollBackward)),
		"add_minus1_forward":      sel(d.AddBusinessDays(-1, ursus.RollForward)),
		"add_0_forward":           sel(d.AddBusinessDays(0, ursus.RollForward)),
		"add_0_backward":          sel(d.AddBusinessDays(0, ursus.RollBackward)),
		"add_count_per_row": sel(d.AddBusinessDays(ursus.Lit(int64(1)).Add(ursus.IntRange(0, ursus.Len(), 1)),
			ursus.RollForward)),
		"add_null_count":                    sel(d.AddBusinessDays(c("n"), ursus.RollRaise)),
		"add_1_past_christmas":              sel(d.AddBusinessDays(1, ursus.RollRaise, christmas)),
		"add_1000":                          sel(d.AddBusinessDays(1000, ursus.RollRaise)),
		"add_minus1000":                     sel(d.AddBusinessDays(-1000, ursus.RollRaise)),
		"add_1_datetime":                    sel(d.AddBusinessDays(1, ursus.RollRaise)),
		"is_business_day_saturday_datetime": sel(d.IsBusinessDay()),
		"add_1_zoned":                       sel(d.AddBusinessDays(1, ursus.RollRaise)),
		"add_10_over_two_holidays":          sel(d.AddBusinessDays(10, ursus.RollRaise, winter)),
		"add_minus10_over_two_holidays":     sel(d.AddBusinessDays(-10, ursus.RollRaise, winter)),
		"add_23_thursdays_off":              sel(d.AddBusinessDays(23, ursus.RollRaise, thuOff)),
		"business_days_gen": sel(d.IsBusinessDay().Alias("is"), d.IsBusinessDay(winterMonSat).Alias("is_ms"),
			d.AddBusinessDays(3, ursus.RollForward, winter).Alias("add3"),
			d.AddBusinessDays(-2, ursus.RollBackward, monSat).Alias("sub2")),

		"replace_hour_minute":    sel(d.Replace(parts(ursus.DateTimeParts{Hour: 23, Minute: 0}))),
		"replace_microsecond":    sel(d.Replace(parts(ursus.DateTimeParts{Microsecond: 5}))),
		"replace_day_per_row":    sel(d.Replace(parts(ursus.DateTimeParts{Day: c("day")}))),
		"replace_year_0":         sel(d.Replace(parts(ursus.DateTimeParts{Year: 0, Month: 3}))),
		"replace_ns_hour":        sel(d.Replace(parts(ursus.DateTimeParts{Hour: 5}))),
		"replace_ns_microsecond": sel(d.Replace(parts(ursus.DateTimeParts{Microsecond: 5}))),
		"replace_ms_microsecond": sel(c("d").Cast(ursus.Datetime(ursus.Milli, "")).Dt().Replace(parts(ursus.DateTimeParts{Microsecond: 999999}))),
		"replace_date_day":       sel(d.Replace(parts(ursus.DateTimeParts{Day: 2}))),
		"replace_date_hour_day":  sel(d.Replace(parts(ursus.DateTimeParts{Hour: 4, Day: 2}))),
		"replace_fold_earliest":  sel(d.Replace(parts(ursus.DateTimeParts{Hour: 1}), ursus.AmbiguousEarliest)),
		"replace_fold_latest":    sel(d.Replace(parts(ursus.DateTimeParts{Hour: 1}), ursus.AmbiguousLatest)),
		"replace_fold_null":      sel(d.Replace(parts(ursus.DateTimeParts{Hour: 1}), ursus.AmbiguousNull)),
		"replace_gen": sel(tcol.Replace(parts(ursus.DateTimeParts{Hour: 23, Second: 0})).Alias("h23"),
			tcol.Replace(parts(ursus.DateTimeParts{Microsecond: 1})).Alias("us1"),
			tcol.Replace(parts(ursus.DateTimeParts{Month: 6, Day: 15})).Alias("jun15")),

		"offsets_new_york":      sel(d.BaseUtcOffset().Alias("b"), d.DstOffset().Alias("o")),
		"offsets_lord_howe":     sel(d.BaseUtcOffset().Alias("b"), d.DstOffset().Alias("o")),
		"offsets_kolkata":       sel(d.BaseUtcOffset().Alias("b"), d.DstOffset().Alias("o")),
		"offsets_utc":           sel(d.BaseUtcOffset().Alias("b"), d.DstOffset().Alias("o")),
		"offsets_gen_new_york":  sel(tcol.BaseUtcOffset().Alias("b"), tcol.DstOffset().Alias("o")),
		"offsets_gen_lord_howe": sel(tcol.BaseUtcOffset().Alias("b"), tcol.DstOffset().Alias("o")),

		"duration_totals":            sel(totals(d)...),
		"duration_totals_fractional": sel(fractional(d, "")...),
		"duration_ms_fractional":     sel(ms.TotalSecondsFractional().Alias("s"), ms.TotalNanoseconds().Alias("ns")),
		"second_fractional":          sel(d.SecondFractional()),
		"second_fractional_ns":       sel(d.SecondFractional()),
		"second_fractional_time":     sel(d.SecondFractional()),
		"epochs":                     sel(append(epochs(d), d.Timestamp(ursus.Milli).Alias("ts"))...),
		"epochs_dates":               sel(d.EpochIn(ursus.Nano).Alias("ns"), d.EpochIn(ursus.Second).Alias("s"), d.EpochDays().Alias("days")),
		"epoch_zoned":                sel(d.EpochIn(ursus.Second)),
		"duration_totals_gen":        sel(append(totals(du), fractional(du, "_f")...)...),
		"duration_ms_totals_gen": sel(du.TotalSecondsFractional().Alias("s"), du.TotalNanoseconds().Alias("ns"),
			du.TotalDaysFractional().Alias("days")),
		"epochs_gen": sel(append(epochs(tcol), tcol.Timestamp(ursus.Nano).Alias("ts"), tcol.SecondFractional().Alias("sec"),
			tz.EpochIn(ursus.Second).Alias("tz_s"), tz.EpochIn(ursus.Nano).Alias("tz_ns"))...),
	}
}
