package ursus_test

// The dtexact group (step 498, from dtexact_test.go): a Date's epoch past 2262, the
// year 2200's epoch in milliseconds and microseconds, and a Duration's totals at the
// units Polars has, spelled as bench/scripts/oracle/dtexact.py asks Polars.

import "github.com/advenn/ursus"

func init() {
	c := ursus.Col
	d, n, t, tz := c("d").Dt(), c("n").Dt(), c("t").Dt(), c("tz").Dt()
	sel := func(exprs ...ursus.Expr) oracleQuery {
		return func(in oracleInputs) *ursus.LazyFrame { return in.df().Select(exprs...) }
	}
	totals := []ursus.Expr{n.TotalDays().Alias("days"), n.TotalHours().Alias("hours"), n.TotalMinutes().Alias("minutes"),
		n.TotalSeconds().Alias("seconds")}
	oracleGroups["dtexact"] = map[string]oracleQuery{
		"epoch_of_far_dates": sel(d.Epoch()),
		"epoch_2200_ms_us": sel(d.EpochIn(ursus.Second).Alias("ms"),
			c("d").Cast(ursus.Datetime(ursus.Micro, "UTC")).Dt().EpochIn(ursus.Second).Alias("us")),
		"totals_90min_ms":           sel(totals[:3]...),
		"totals_90min_us":           sel(totals[:3]...),
		"totals_90min_ns":           sel(totals[:3]...),
		"totals_three_centuries_ms": sel(totals...),

		"totals_gen":      sel(totals...),
		"epoch_dates_gen": sel(d.EpochIn(ursus.Second).Alias("s"), d.EpochDays().Alias("days"), d.EpochIn(ursus.Milli).Alias("ms")),
		"epoch_datetimes_gen": sel(t.EpochIn(ursus.Second).Alias("s"), t.EpochIn(ursus.Milli).Alias("ms"), t.EpochDays().Alias("days"),
			tz.EpochIn(ursus.Second).Alias("tz_s"), tz.EpochIn(ursus.Micro).Alias("tz_us")),
	}
}
