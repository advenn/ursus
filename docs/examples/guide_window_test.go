package examples_test

import (
	"context"
	"fmt"
	"log"

	"github.com/advenn/ursus"
)

// prices is the windows chapter's series: two tickers' closes over six trading days,
// a weekend between the fifth and the sixth.
func prices() *ursus.LazyFrame {
	return ursus.ScanCSV("testdata/prices.csv",
		ursus.WithSchemaOverrides(map[string]ursus.DataType{"date": ursus.Date}))
}

// --- the user guide: window functions --------------------------------------------

func Example_windowOver() {
	qty, region := ursus.Col("qty"), ursus.Col("region")
	df, err := sales().
		Select(
			ursus.Col("order_id"),
			region,
			qty,
			qty.Sum().Over(region).Alias("region_qty"),
			qty.Div(qty.Sum().Over(region)).Round(2).Alias("share"),
			qty.Sum().Over().Alias("all_qty"),
		).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (8, 6)
	// ┌──────────┬─────────┬───────┬────────────┬─────────┬─────────┐
	// │ order_id │ region  │ qty   │ region_qty │ share   │ all_qty │
	// │ Int64    │ String  │ Int64 │ Int128     │ Float64 │ Int128  │
	// ├──────────┼─────────┼───────┼────────────┼─────────┼─────────┤
	// │ 1        │ "north" │ 10    │ 20         │ 0.5     │ 53      │
	// │ 2        │ "south" │ 4     │ 19         │ 0.21    │ 53      │
	// │ 3        │ "north" │ 7     │ 20         │ 0.35    │ 53      │
	// │ 4        │ "east"  │ 2     │ 14         │ 0.14    │ 53      │
	// │ 5        │ "south" │ 9     │ 19         │ 0.47    │ 53      │
	// │ 6        │ "east"  │ 12    │ 14         │ 0.86    │ 53      │
	// │ 7        │ "north" │ 3     │ 20         │ 0.15    │ 53      │
	// │ 8        │ "south" │ 6     │ 19         │ 0.32    │ 53      │
	// └──────────┴─────────┴───────┴────────────┴─────────┴─────────┘
}

func Example_windowCumulative() {
	byTicker := ursus.WindowSpec{
		PartitionBy: []ursus.Expr{ursus.Col("ticker")},
		OrderBy:     []ursus.SortKey{ursus.Asc(ursus.Col("date"))},
	}
	df, err := prices().
		WithColumns(
			ursus.Col("close").CumMax(false).OverWith(byTicker).Alias("high"),
			ursus.Col("close").CumCount(false).OverWith(byTicker).Alias("day"),
		).
		Filter(ursus.Col("ticker").Eq("ACME")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (6, 5)
	// ┌────────────┬────────┬─────────┬─────────┬────────┐
	// │ date       │ ticker │ close   │ high    │ day    │
	// │ Date       │ String │ Float64 │ Float64 │ Uint32 │
	// ├────────────┼────────┼─────────┼─────────┼────────┤
	// │ 2026-01-05 │ "ACME" │ 10      │ 10      │ 1      │
	// │ 2026-01-06 │ "ACME" │ 11      │ 11      │ 2      │
	// │ 2026-01-07 │ "ACME" │ 10.5    │ 11      │ 3      │
	// │ 2026-01-08 │ "ACME" │ 12      │ 12      │ 4      │
	// │ 2026-01-09 │ "ACME" │ 12      │ 12      │ 5      │
	// │ 2026-01-12 │ "ACME" │ 13.5    │ 13.5    │ 6      │
	// └────────────┴────────┴─────────┴─────────┴────────┘
}

func Example_windowShift() {
	t := ursus.Col("ticker")
	df, err := prices().
		WithColumns(
			ursus.Col("close").Shift(1).Over(t).Alias("previous"),
			ursus.Col("close").Diff(1).Over(t).Alias("change"),
			ursus.Col("close").PctChange(1).Over(t).Round(3).Alias("pct"),
		).
		Filter(t.Eq("BOLT")).
		Drop("ticker").
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (6, 5)
	// ┌────────────┬─────────┬──────────┬─────────┬─────────┐
	// │ date       │ close   │ previous │ change  │ pct     │
	// │ Date       │ Float64 │ Float64  │ Float64 │ Float64 │
	// ├────────────┼─────────┼──────────┼─────────┼─────────┤
	// │ 2026-01-05 │ 20      │ null     │ null    │ null    │
	// │ 2026-01-06 │ 19      │ 20       │ -1      │ -0.05   │
	// │ 2026-01-07 │ 21      │ 19       │ 2       │ 0.105   │
	// │ 2026-01-08 │ 21      │ 21       │ 0       │ 0       │
	// │ 2026-01-09 │ 22      │ 21       │ 1       │ 0.048   │
	// │ 2026-01-12 │ 20      │ 22       │ -2      │ -0.091  │
	// └────────────┴─────────┴──────────┴─────────┴─────────┘
}

func Example_windowRank() {
	x := ursus.Col("points")
	df, err := ursus.Frame(
		ursus.Values("team", []string{"a", "b", "c", "d", "e"}),
		ursus.Values("points", []int64{7, 9, 7, 3, 9}),
	).WithColumns(
		x.Rank(ursus.RankOrdinal, true).Alias("ordinal"),
		x.Rank(ursus.RankMin, true).Alias("min"),
		x.Rank(ursus.RankMax, true).Alias("max"),
		x.Rank(ursus.RankDense, true).Alias("dense"),
		x.Rank(ursus.RankAverage, true).Alias("average"),
	).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (5, 7)
	// ┌────────┬────────┬─────────┬────────┬────────┬────────┬─────────┐
	// │ team   │ points │ ordinal │ min    │ max    │ dense  │ average │
	// │ String │ Int64  │ Uint32  │ Uint32 │ Uint32 │ Uint32 │ Float64 │
	// ├────────┼────────┼─────────┼────────┼────────┼────────┼─────────┤
	// │ "a"    │ 7      │ 3       │ 3      │ 4      │ 2      │ 3.5     │
	// │ "b"    │ 9      │ 1       │ 1      │ 2      │ 1      │ 1.5     │
	// │ "c"    │ 7      │ 4       │ 3      │ 4      │ 2      │ 3.5     │
	// │ "d"    │ 3      │ 5       │ 5      │ 5      │ 3      │ 5       │
	// │ "e"    │ 9      │ 2       │ 1      │ 2      │ 1      │ 1.5     │
	// └────────┴────────┴─────────┴────────┴────────┴────────┴─────────┘
}

func Example_windowRolling() {
	t, c := ursus.Col("ticker"), ursus.Col("close")
	df, err := prices().
		WithColumns(
			c.RollingMean(3).Over(t).Alias("mean3"),
			c.RollingMean(3, ursus.MinSamples(1)).Over(t).Alias("mean3_min1"),
			c.RollingMax(3, ursus.RollingCenter()).Over(t).Alias("max3_centred"),
		).
		Filter(t.Eq("ACME")).
		Drop("ticker").
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (6, 5)
	// ┌────────────┬─────────┬────────────────────┬────────────────────┬──────────────┐
	// │ date       │ close   │ mean3              │ mean3_min1         │ max3_centred │
	// │ Date       │ Float64 │ Float64            │ Float64            │ Float64      │
	// ├────────────┼─────────┼────────────────────┼────────────────────┼──────────────┤
	// │ 2026-01-05 │ 10      │ null               │ 10                 │ null         │
	// │ 2026-01-06 │ 11      │ null               │ 10.5               │ 11           │
	// │ 2026-01-07 │ 10.5    │ 10.5               │ 10.5               │ 12           │
	// │ 2026-01-08 │ 12      │ 11.166666666666666 │ 11.166666666666666 │ 12           │
	// │ 2026-01-09 │ 12      │ 11.5               │ 11.5               │ 13.5         │
	// │ 2026-01-12 │ 13.5    │ 12.5               │ 12.5               │ null         │
	// └────────────┴─────────┴────────────────────┴────────────────────┴──────────────┘
}

func Example_windowRollingBy() {
	t, c := ursus.Col("ticker"), ursus.Col("close")
	df, err := prices().
		WithColumns(
			c.RollingSum(3, ursus.MinSamples(1)).Over(t).Alias("last_3_rows"),
			c.RollingSumBy(ursus.Col("date"), ursus.Every("3d")).Over(t).
				Alias("last_3_days"),
		).
		Filter(t.Eq("ACME")).
		Drop("ticker").
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (6, 4)
	// ┌────────────┬─────────┬─────────────┬─────────────┐
	// │ date       │ close   │ last_3_rows │ last_3_days │
	// │ Date       │ Float64 │ Float64     │ Float64     │
	// ├────────────┼─────────┼─────────────┼─────────────┤
	// │ 2026-01-05 │ 10      │ 10          │ 10          │
	// │ 2026-01-06 │ 11      │ 21          │ 21          │
	// │ 2026-01-07 │ 10.5    │ 31.5        │ 31.5        │
	// │ 2026-01-08 │ 12      │ 33.5        │ 33.5        │
	// │ 2026-01-09 │ 12      │ 34.5        │ 34.5        │
	// │ 2026-01-12 │ 13.5    │ 37.5        │ 13.5        │
	// └────────────┴─────────┴─────────────┴─────────────┘
}

func Example_windowEwm() {
	t, c := ursus.Col("ticker"), ursus.Col("close")
	df, err := prices().
		WithColumns(
			c.EwmMean(ursus.EwmAlpha(0.5)).Over(t).Round(4).Alias("ewm"),
			c.EwmMean(ursus.EwmAlpha(0.5), ursus.EwmAdjust(false)).Over(t).Round(4).
				Alias("ewm_unadjusted"),
		).
		Filter(t.Eq("ACME")).
		Drop("ticker").
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (6, 4)
	// ┌────────────┬─────────┬─────────┬────────────────┐
	// │ date       │ close   │ ewm     │ ewm_unadjusted │
	// │ Date       │ Float64 │ Float64 │ Float64        │
	// ├────────────┼─────────┼─────────┼────────────────┤
	// │ 2026-01-05 │ 10      │ 10      │ 10             │
	// │ 2026-01-06 │ 11      │ 10.6667 │ 10.5           │
	// │ 2026-01-07 │ 10.5    │ 10.5714 │ 10.5           │
	// │ 2026-01-08 │ 12      │ 11.3333 │ 11.25          │
	// │ 2026-01-09 │ 12      │ 11.6774 │ 11.625         │
	// │ 2026-01-12 │ 13.5    │ 12.6032 │ 12.5625        │
	// └────────────┴─────────┴─────────┴────────────────┘
}
