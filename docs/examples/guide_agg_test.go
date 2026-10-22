package examples_test

import (
	"context"
	"fmt"
	"log"

	"github.com/advenn/ursus"
)

// --- the user guide: aggregation and group-by ---------------------------------------

func Example_aggStatistics() {
	df, err := sales().
		GroupBy(ursus.Col("region")).
		Agg(
			ursus.Col("qty").Sum().Alias("sum"),
			ursus.Col("qty").Mean().Alias("mean"),
			ursus.Col("qty").Median().Alias("median"),
			ursus.Col("qty").Min().Alias("min"),
			ursus.Col("qty").Max().Alias("max"),
			ursus.Col("qty").Std(1).Round(2).Alias("std"),
		).
		Sort(ursus.Asc(ursus.Col("region"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 7)
	// ┌─────────┬────────┬───────────────────┬─────────┬───────┬───────┬─────────┐
	// │ region  │ sum    │ mean              │ median  │ min   │ max   │ std     │
	// │ String  │ Int128 │ Float64           │ Float64 │ Int64 │ Int64 │ Float64 │
	// ├─────────┼────────┼───────────────────┼─────────┼───────┼───────┼─────────┤
	// │ "east"  │ 14     │ 7                 │ 7       │ 2     │ 12    │ 7.07    │
	// │ "north" │ 20     │ 6.666666666666667 │ 7       │ 3     │ 10    │ 3.51    │
	// │ "south" │ 19     │ 6.333333333333333 │ 6       │ 4     │ 9     │ 2.52    │
	// └─────────┴────────┴───────────────────┴─────────┴───────┴───────┴─────────┘
}

func Example_aggCounting() {
	df, err := sales().
		GroupBy(ursus.Col("product")).
		Agg(
			ursus.Len().Alias("orders"),
			ursus.Col("region").NUnique().Alias("regions"),
			ursus.Col("region").First().Alias("first"),
			ursus.Col("region").Last().Alias("last"),
		).
		Sort(ursus.Asc(ursus.Col("product"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 5)
	// ┌─────────┬────────┬─────────┬─────────┬─────────┐
	// │ product │ orders │ regions │ first   │ last    │
	// │ String  │ Uint64 │ Uint64  │ String  │ String  │
	// ├─────────┼────────┼─────────┼─────────┼─────────┤
	// │ "apple" │ 3      │ 3       │ "north" │ "east"  │
	// │ "fig"   │ 2      │ 2       │ "south" │ "north" │
	// │ "pear"  │ 3      │ 3       │ "north" │ "south" │
	// └─────────┴────────┴─────────┴─────────┴─────────┘
}

func Example_aggByExpression() {
	df, err := sales().
		GroupBy(ursus.Col("qty").Ge(8).Alias("bulk")).
		Agg(ursus.Len().Alias("orders"), ursus.Col("qty").Sum().Alias("qty")).
		Sort(ursus.Asc(ursus.Col("bulk"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (2, 3)
	// ┌───────┬────────┬────────┐
	// │ bulk  │ orders │ qty    │
	// │ Bool  │ Uint64 │ Int128 │
	// ├───────┼────────┼────────┤
	// │ false │ 5      │ 22     │
	// │ true  │ 3      │ 31     │
	// └───────┴────────┴────────┘
}

func Example_aggSeveralKeys() {
	df, err := sales().
		GroupBy(ursus.Col("region"), ursus.Col("product")).
		Agg(ursus.Col("qty").Sum().Alias("qty")).
		Sort(ursus.Asc(ursus.Col("region")), ursus.Asc(ursus.Col("product"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (8, 3)
	// ┌─────────┬─────────┬────────┐
	// │ region  │ product │ qty    │
	// │ String  │ String  │ Int128 │
	// ├─────────┼─────────┼────────┤
	// │ "east"  │ "apple" │ 12     │
	// │ "east"  │ "pear"  │ 2      │
	// │ "north" │ "apple" │ 10     │
	// │ "north" │ "fig"   │ 3      │
	// │ "north" │ "pear"  │ 7      │
	// │ "south" │ "apple" │ 4      │
	// │ "south" │ "fig"   │ 9      │
	// │ "south" │ "pear"  │ 6      │
	// └─────────┴─────────┴────────┘
}

func Example_aggConditional() {
	df, err := sales().
		GroupBy(ursus.Col("region")).
		Agg(
			ursus.Col("qty").Filter(ursus.Col("product").Eq("apple")).Sum().
				Alias("apple_qty"),
			ursus.Col("qty").Ge(8).Sum().Alias("bulk_orders"),
			ursus.Col("product").MaxBy(ursus.Col("qty")).Alias("biggest"),
		).
		Sort(ursus.Asc(ursus.Col("region"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 4)
	// ┌─────────┬───────────┬─────────────┬─────────┐
	// │ region  │ apple_qty │ bulk_orders │ biggest │
	// │ String  │ Int128    │ Int128      │ String  │
	// ├─────────┼───────────┼─────────────┼─────────┤
	// │ "east"  │ 12        │ 1           │ "apple" │
	// │ "north" │ 10        │ 1           │ "apple" │
	// │ "south" │ 4         │ 1           │ "fig"   │
	// └─────────┴───────────┴─────────────┴─────────┘
}

func Example_aggLists() {
	df, err := sales().
		GroupBy(ursus.Col("region")).
		Agg(
			ursus.Col("order_id").Implode().Alias("orders"),
			ursus.Col("product").Unique().Sort().Alias("products"),
		).
		Sort(ursus.Asc(ursus.Col("region"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌─────────┬─────────────┬──────────────────────────┐
	// │ region  │ orders      │ products                 │
	// │ String  │ List(Int64) │ List(String)             │
	// ├─────────┼─────────────┼──────────────────────────┤
	// │ "east"  │ [4, 6]      │ ["apple", "pear"]        │
	// │ "north" │ [1, 3, 7]   │ ["apple", "fig", "pear"] │
	// │ "south" │ [2, 5, 8]   │ ["apple", "fig", "pear"] │
	// └─────────┴─────────────┴──────────────────────────┘
}

func Example_aggFirstNonNull() {
	temps, read := []float64{0, 21.5, 22.0, 0}, []bool{false, true, true, false}
	readings := ursus.Frame(
		ursus.Values("sensor", []string{"a", "a", "a", "b"}),
		ursus.ValuesNullable("temp", temps, read),
	)
	g := readings.GroupBy(ursus.Col("sensor")).MaintainOrder()
	show(g.Agg(
		ursus.Col("temp").First().Alias("first"),
		ursus.Col("temp").FirstNonNull().Alias("first_seen"),
		ursus.Col("temp").LastNonNull().Alias("last_seen"),
	))
	show(g.All())
	// Output:
	// shape: (2, 4)
	// ┌────────┬─────────┬────────────┬───────────┐
	// │ sensor │ first   │ first_seen │ last_seen │
	// │ String │ Float64 │ Float64    │ Float64   │
	// ├────────┼─────────┼────────────┼───────────┤
	// │ "a"    │ null    │ 21.5       │ 22        │
	// │ "b"    │ null    │ null       │ null      │
	// └────────┴─────────┴────────────┴───────────┘
	//
	// shape: (2, 2)
	// ┌────────┬──────────────────┐
	// │ sensor │ temp             │
	// │ String │ List(Float64)    │
	// ├────────┼──────────────────┤
	// │ "a"    │ [null, 21.5, 22] │
	// │ "b"    │ [null]           │
	// └────────┴──────────────────┘
}

func Example_aggHaving() {
	show(sales().
		GroupBy(ursus.Col("region")).
		MaintainOrder().
		Having(ursus.Len().Gt(2)).
		Agg(ursus.Col("qty").Sum().Alias("qty")))
	// Output:
	// shape: (2, 2)
	// ┌─────────┬────────┐
	// │ region  │ qty    │
	// │ String  │ Int128 │
	// ├─────────┼────────┤
	// │ "north" │ 20     │
	// │ "south" │ 19     │
	// └─────────┴────────┘
}

func Example_aggHeadTail() {
	show(sales().
		Select(ursus.Col("region"), ursus.Col("product"), ursus.Col("qty")).
		GroupBy(ursus.Col("region")).
		Tail(1))
	// Output:
	// shape: (3, 3)
	// ┌─────────┬─────────┬───────┐
	// │ region  │ product │ qty   │
	// │ String  │ String  │ Int64 │
	// ├─────────┼─────────┼───────┤
	// │ "north" │ "fig"   │ 3     │
	// │ "south" │ "pear"  │ 6     │
	// │ "east"  │ "apple" │ 12    │
	// └─────────┴─────────┴───────┘
}

func Example_aggShortcut() {
	df, err := sales().
		Select(ursus.Col("region"), ursus.Col("qty"), ursus.Col("price")).
		GroupBy(ursus.Col("region")).
		Max().
		Sort(ursus.Asc(ursus.Col("region"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌─────────┬───────┬─────────┐
	// │ region  │ qty   │ price   │
	// │ String  │ Int64 │ Float64 │
	// ├─────────┼───────┼─────────┤
	// │ "east"  │ 12    │ 2.25    │
	// │ "north" │ 10    │ 3       │
	// │ "south" │ 9     │ 3       │
	// └─────────┴───────┴─────────┘
}

func Example_aggMaintainOrder() {
	df, err := sales().
		GroupBy(ursus.Col("product")).
		MaintainOrder().
		Agg(ursus.Col("qty").Sum().Alias("qty")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 2)
	// ┌─────────┬────────┐
	// │ product │ qty    │
	// │ String  │ Int128 │
	// ├─────────┼────────┤
	// │ "apple" │ 26     │
	// │ "pear"  │ 15     │
	// │ "fig"   │ 12     │
	// └─────────┴────────┘
}

func Example_aggWholeFrame() {
	df, err := sales().
		Select(
			ursus.Len().Alias("orders"),
			ursus.Col("qty").Sum().Alias("qty"),
			ursus.Col("qty").Mul(ursus.Col("price")).Sum().Alias("revenue"),
			ursus.Col("date").Max().Alias("last_day"),
		).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (1, 4)
	// ┌────────┬────────┬─────────┬────────────┐
	// │ orders │ qty    │ revenue │ last_day   │
	// │ Uint64 │ Int128 │ Float64 │ Date       │
	// ├────────┼────────┼─────────┼────────────┤
	// │ 8      │ 53     │ 108.75  │ 2026-01-07 │
	// └────────┴────────┴─────────┴────────────┘
}

func Example_aggValueCounts() {
	df, err := sales().ValueCounts(ursus.Col("product")).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 2)
	// ┌─────────┬────────┐
	// │ product │ count  │
	// │ String  │ Uint64 │
	// ├─────────┼────────┤
	// │ "apple" │ 3      │
	// │ "pear"  │ 3      │
	// │ "fig"   │ 2      │
	// └─────────┴────────┘
}

func Example_aggDescribe() {
	df, err := sales().
		Select(ursus.Col("qty"), ursus.Col("price")).
		Describe(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (9, 3)
	// ┌──────────────┬───────────────────┬────────────────────┐
	// │ statistic    │ qty               │ price              │
	// │ String       │ Float64           │ Float64            │
	// ├──────────────┼───────────────────┼────────────────────┤
	// │ "count"      │ 8                 │ 8                  │
	// │ "null_count" │ 0                 │ 0                  │
	// │ "mean"       │ 6.625             │ 2.15625            │
	// │ "std"        │ 3.543101950067402 │ 0.6258922202972101 │
	// │ "min"        │ 2                 │ 1.5                │
	// │ "25%"        │ 4                 │ 1.5                │
	// │ "50%"        │ 7                 │ 2.25               │
	// │ "75%"        │ 9                 │ 2.25               │
	// │ "max"        │ 12                │ 3                  │
	// └──────────────┴───────────────────┴────────────────────┘
}

func Example_aggFrame() {
	lines := sales().Select(ursus.Col("region"), ursus.Col("qty"), ursus.Col("price"))
	show(lines.Mean())
	show(lines.Max())
	// Output:
	// shape: (1, 3)
	// ┌────────┬─────────┬─────────┐
	// │ region │ qty     │ price   │
	// │ String │ Float64 │ Float64 │
	// ├────────┼─────────┼─────────┤
	// │ null   │ 6.625   │ 2.15625 │
	// └────────┴─────────┴─────────┘
	//
	// shape: (1, 3)
	// ┌─────────┬───────┬─────────┐
	// │ region  │ qty   │ price   │
	// │ String  │ Int64 │ Float64 │
	// ├─────────┼───────┼─────────┤
	// │ "south" │ 12    │ 3       │
	// └─────────┴───────┴─────────┘
}
