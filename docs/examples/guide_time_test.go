package examples_test

import (
	"time"

	"github.com/advenn/ursus"
)

// days is the dates chapter's frame of three days: a leap day, a Saturday and a
// year's last.
func days() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("day", []string{"2024-02-29", "2026-01-03", "2026-12-31"}),
	).Select(ursus.Col("day").Str().ToDate())
}

// --- the user guide: dates and times -------------------------------------------------

func Example_timeParse() {
	logs := ursus.Frame(
		ursus.Values("at", []string{"2026-03-08 06:30", "2026-03-08 07:45", "oops"}),
	)
	utc := ursus.Datetime(ursus.Micro, "UTC")
	show(logs.Select(
		ursus.Col("at"),
		ursus.Col("at").Str().Strptime(utc, "%Y-%m-%d %H:%M", false).Alias("instant"),
	))
	// Output:
	// shape: (3, 2)
	// ┌────────────────────┬─────────────────────────────┐
	// │ at                 │ instant                     │
	// │ String             │ Datetime(us, UTC)           │
	// ├────────────────────┼─────────────────────────────┤
	// │ "2026-03-08 06:30" │ 2026-03-08T06:30:00.000000Z │
	// │ "2026-03-08 07:45" │ 2026-03-08T07:45:00.000000Z │
	// │ "oops"             │ null                        │
	// └────────────────────┴─────────────────────────────┘
}

func Example_timeParts() {
	d := ursus.Col("day").Dt()
	show(days().Select(
		ursus.Col("day"),
		d.Year().Alias("year"),
		d.Month().Alias("month"),
		d.Weekday().Alias("weekday"),
		d.Week().Alias("week"),
		d.Quarter().Alias("quarter"),
		d.OrdinalDay().Alias("day_of_year"),
		d.IsLeapYear().Alias("leap"),
	))
	// Output:
	// shape: (3, 8)
	// ┌────────────┬───────┬───────┬─────────┬───────┬─────────┬─────────────┬───────┐
	// │ day        │ year  │ month │ weekday │ week  │ quarter │ day_of_year │ leap  │
	// │ Date       │ Int32 │ Int32 │ Int32   │ Int32 │ Int32   │ Int32       │ Bool  │
	// ├────────────┼───────┼───────┼─────────┼───────┼─────────┼─────────────┼───────┤
	// │ 2024-02-29 │ 2024  │ 2     │ 4       │ 9     │ 1       │ 60          │ true  │
	// │ 2026-01-03 │ 2026  │ 1     │ 6       │ 1     │ 1       │ 3           │ false │
	// │ 2026-12-31 │ 2026  │ 12    │ 4       │ 53    │ 4       │ 365         │ false │
	// └────────────┴───────┴───────┴─────────┴───────┴─────────┴─────────────┴───────┘
}

func Example_timeArithmetic() {
	invoices := ursus.Frame(
		ursus.Values("issued", []string{"2026-01-31", "2026-02-10"}),
		ursus.Values("paid", []string{"2026-02-14", "2026-02-11"}),
	).Select(ursus.Col("issued").Str().ToDate(), ursus.Col("paid").Str().ToDate())
	issued := ursus.Col("issued").Dt()
	show(invoices.Select(
		ursus.Col("issued"),
		ursus.Col("paid").Sub(ursus.Col("issued")).Dt().TotalDays().
			Alias("days_to_pay"),
		issued.OffsetBy(ursus.Every("1mo")).Alias("plus_a_month"),
		issued.MonthEnd().Alias("month_end"),
		issued.AddBusinessDays(5, ursus.RollForward).Alias("plus_5_business"),
	))
	// Output:
	// shape: (2, 5)
	// ┌────────────┬─────────────┬──────────────┬────────────┬─────────────────┐
	// │ issued     │ days_to_pay │ plus_a_month │ month_end  │ plus_5_business │
	// │ Date       │ Int64       │ Date         │ Date       │ Date            │
	// ├────────────┼─────────────┼──────────────┼────────────┼─────────────────┤
	// │ 2026-01-31 │ 14          │ 2026-02-28   │ 2026-01-31 │ 2026-02-09      │
	// │ 2026-02-10 │ 1           │ 2026-03-10   │ 2026-02-28 │ 2026-02-17      │
	// └────────────┴─────────────┴──────────────┴────────────┴─────────────────┘
}

func Example_timeTruncate() {
	date := ursus.Col("date").Dt()
	show(sales().Select(
		ursus.Col("date"),
		date.Truncate(ursus.Every("1w")).Alias("week"),
		date.Truncate(ursus.Every("1mo")).Alias("month"),
		date.Strftime("%a %d %b").Alias("label"),
	).Unique())
	// Output:
	// shape: (5, 4)
	// ┌────────────┬────────────┬────────────┬──────────────┐
	// │ date       │ week       │ month      │ label        │
	// │ Date       │ Date       │ Date       │ String       │
	// ├────────────┼────────────┼────────────┼──────────────┤
	// │ 2026-01-03 │ 2025-12-29 │ 2026-01-01 │ "Sat 03 Jan" │
	// │ 2026-01-04 │ 2025-12-29 │ 2026-01-01 │ "Sun 04 Jan" │
	// │ 2026-01-05 │ 2026-01-05 │ 2026-01-01 │ "Mon 05 Jan" │
	// │ 2026-01-06 │ 2026-01-05 │ 2026-01-01 │ "Tue 06 Jan" │
	// │ 2026-01-07 │ 2026-01-05 │ 2026-01-01 │ "Wed 07 Jan" │
	// └────────────┴────────────┴────────────┴──────────────┘
}

func Example_timeZones() {
	at := []time.Time{
		time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC),
	}
	instants := ursus.Frame(ursus.Values("utc", at)).
		Select(ursus.Col("utc").Cast(ursus.Datetime(ursus.Second, "UTC")))
	show(instants.Select(
		ursus.Col("utc"),
		ursus.Col("utc").Dt().ConvertTimeZone("America/New_York").Alias("new_york"),
		ursus.Col("utc").Dt().ReplaceTimeZone("Asia/Tokyo").Alias("same_wall_in_tokyo"),
	))
	// Output:
	// shape: (2, 3)
	// ┌──────────────────────┬───────────────────────────────┬───────────────────────────┐
	// │ utc                  │ new_york                      │ same_wall_in_tokyo        │
	// │ Datetime(s, UTC)     │ Datetime(s, America/New_York) │ Datetime(s, Asia/Tokyo)   │
	// ├──────────────────────┼───────────────────────────────┼───────────────────────────┤
	// │ 2026-03-08T06:30:00Z │ 2026-03-08T01:30:00-05:00     │ 2026-03-08T06:30:00+09:00 │
	// │ 2026-03-08T07:30:00Z │ 2026-03-08T03:30:00-04:00     │ 2026-03-08T07:30:00+09:00 │
	// └──────────────────────┴───────────────────────────────┴───────────────────────────┘
}

func Example_timeRanges() {
	from := ursus.Lit(time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC)).Cast(ursus.Date)
	to := ursus.Lit(time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)).Cast(ursus.Date)
	span := ursus.DateRange(from, to, ursus.Every("1d")).Alias("day")
	show(ursus.Frame().Select(span))
	// Output:
	// shape: (4, 1)
	// ┌────────────┐
	// │ day        │
	// │ Date       │
	// ├────────────┤
	// │ 2026-01-30 │
	// │ 2026-01-31 │
	// │ 2026-02-01 │
	// │ 2026-02-02 │
	// └────────────┘
}

func Example_timeUpsample() {
	show(prices().
		Filter(ursus.Col("ticker").Eq("ACME")).
		Upsample("date", ursus.Every("1d"), "ticker").
		WithColumns(ursus.Col("close").FillNull(ursus.FillForward).Alias("filled")))
	// Output:
	// shape: (8, 4)
	// ┌────────────┬────────┬─────────┬─────────┐
	// │ date       │ ticker │ close   │ filled  │
	// │ Date       │ String │ Float64 │ Float64 │
	// ├────────────┼────────┼─────────┼─────────┤
	// │ 2026-01-05 │ "ACME" │ 10      │ 10      │
	// │ 2026-01-06 │ "ACME" │ 11      │ 11      │
	// │ 2026-01-07 │ "ACME" │ 10.5    │ 10.5    │
	// │ 2026-01-08 │ "ACME" │ 12      │ 12      │
	// │ 2026-01-09 │ "ACME" │ 12      │ 12      │
	// │ 2026-01-10 │ "ACME" │ null    │ 12      │
	// │ 2026-01-11 │ "ACME" │ null    │ 12      │
	// │ 2026-01-12 │ "ACME" │ 13.5    │ 13.5    │
	// └────────────┴────────┴─────────┴─────────┘
}

func Example_timeDynamic() {
	show(prices().
		Filter(ursus.Col("ticker").Eq("ACME")).
		GroupByDynamic(ursus.Col("date"),
			ursus.DynamicOptions{Every: ursus.Every("1d")}).
		Agg(ursus.Len().Alias("rows"), ursus.Col("close").Mean().Alias("close")))
	// Output:
	// shape: (8, 3)
	// ┌────────────┬────────┬─────────┐
	// │ date       │ rows   │ close   │
	// │ Date       │ Uint64 │ Float64 │
	// ├────────────┼────────┼─────────┤
	// │ 2026-01-05 │ 1      │ 10      │
	// │ 2026-01-06 │ 1      │ 11      │
	// │ 2026-01-07 │ 1      │ 10.5    │
	// │ 2026-01-08 │ 1      │ 12      │
	// │ 2026-01-09 │ 1      │ 12      │
	// │ 2026-01-10 │ 0      │ null    │
	// │ 2026-01-11 │ 0      │ null    │
	// │ 2026-01-12 │ 1      │ 13.5    │
	// └────────────┴────────┴─────────┘
}

func Example_timeDynamicWeeks() {
	show(sales().
		GroupByDynamic(ursus.Col("date"), ursus.DynamicOptions{
			Every:   ursus.Every("1w"),
			GroupBy: []ursus.Expr{ursus.Col("region")},
		}).
		Agg(ursus.Col("qty").Sum().Alias("qty")).
		Sort(ursus.Asc(ursus.Col("region")), ursus.Asc(ursus.Col("date"))))
	// Output:
	// shape: (5, 3)
	// ┌────────────┬─────────┬────────┐
	// │ date       │ region  │ qty    │
	// │ Date       │ String  │ Int128 │
	// ├────────────┼─────────┼────────┤
	// │ 2026-01-05 │ "east"  │ 14     │
	// │ 2025-12-29 │ "north" │ 17     │
	// │ 2026-01-05 │ "north" │ 3      │
	// │ 2025-12-29 │ "south" │ 4      │
	// │ 2026-01-05 │ "south" │ 15     │
	// └────────────┴─────────┴────────┘
}

func Example_timeRolling() {
	show(prices().
		Filter(ursus.Col("ticker").Eq("ACME")).
		Rolling(ursus.Col("date"), ursus.RollingOptions{Period: ursus.Every("3d")}).
		Agg(ursus.Len().Alias("rows"), ursus.Col("close").Max().Alias("high_3d")))
	// Output:
	// shape: (6, 3)
	// ┌────────────┬────────┬─────────┐
	// │ date       │ rows   │ high_3d │
	// │ Date       │ Uint64 │ Float64 │
	// ├────────────┼────────┼─────────┤
	// │ 2026-01-05 │ 1      │ 10      │
	// │ 2026-01-06 │ 2      │ 11      │
	// │ 2026-01-07 │ 3      │ 11      │
	// │ 2026-01-08 │ 3      │ 12      │
	// │ 2026-01-09 │ 3      │ 12      │
	// │ 2026-01-12 │ 1      │ 13.5    │
	// └────────────┴────────┴─────────┘
}
