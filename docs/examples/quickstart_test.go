package examples_test

import (
	"context"
	"fmt"
	"log"

	"github.com/advenn/ursus"
)

// The home page's query: a CSV scanned, filtered, grouped and sorted.
func Example_home() {
	df, err := ursus.ScanCSV("testdata/sales.csv").
		Filter(ursus.Col("qty").Gt(int64(3))).
		GroupBy(ursus.Col("region")).
		Agg(
			ursus.Col("qty").Mul(ursus.Col("price")).Sum().Alias("revenue"),
			ursus.Len().Alias("orders"),
		).
		Sort(ursus.Desc(ursus.Col("revenue"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌─────────┬─────────┬────────┐
	// │ region  │ revenue │ orders │
	// │ String  │ Float64 │ Uint64 │
	// ├─────────┼─────────┼────────┤
	// │ "south" │ 46.5    │ 3      │
	// │ "north" │ 30.75   │ 2      │
	// │ "east"  │ 18      │ 1      │
	// └─────────┴─────────┴────────┘
}

func Example_quickstartFrame() {
	df, err := ursus.Frame(
		ursus.Values("city", []string{"Oslo", "Lima", "Pune"}),
		ursus.Values("population", []int64{709_000, 10_151_000, 7_764_000}),
		ursus.Values("coastal", []bool{true, true, false}),
	).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌────────┬────────────┬─────────┐
	// │ city   │ population │ coastal │
	// │ String │ Int64      │ Bool    │
	// ├────────┼────────────┼─────────┤
	// │ "Oslo" │ 709000     │ true    │
	// │ "Lima" │ 10151000   │ true    │
	// │ "Pune" │ 7764000    │ false   │
	// └────────┴────────────┴─────────┘
}

func Example_quickstartScan() {
	df, err := ursus.ScanCSV("testdata/sales.csv",
		ursus.WithSchemaOverrides(map[string]ursus.DataType{"date": ursus.Date}),
	).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (8, 6)
	// ┌──────────┬────────────┬─────────┬─────────┬───────┬─────────┐
	// │ order_id │ date       │ region  │ product │ qty   │ price   │
	// │ Int64    │ Date       │ String  │ String  │ Int64 │ Float64 │
	// ├──────────┼────────────┼─────────┼─────────┼───────┼─────────┤
	// │ 1        │ 2026-01-03 │ "north" │ "apple" │ 10    │ 1.5     │
	// │ 2        │ 2026-01-03 │ "south" │ "apple" │ 4     │ 1.5     │
	// │ 3        │ 2026-01-04 │ "north" │ "pear"  │ 7     │ 2.25    │
	// │ 4        │ 2026-01-05 │ "east"  │ "pear"  │ 2     │ 2.25    │
	// │ 5        │ 2026-01-05 │ "south" │ "fig"   │ 9     │ 3       │
	// │ 6        │ 2026-01-06 │ "east"  │ "apple" │ 12    │ 1.5     │
	// │ 7        │ 2026-01-07 │ "north" │ "fig"   │ 3     │ 3       │
	// │ 8        │ 2026-01-07 │ "south" │ "pear"  │ 6     │ 2.25    │
	// └──────────┴────────────┴─────────┴─────────┴───────┴─────────┘
}

func Example_quickstartSelect() {
	df, err := ursus.ScanCSV("testdata/sales.csv").
		Filter(ursus.Col("region").Eq("north")).
		Select(ursus.Col("product"), ursus.Col("qty")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 2)
	// ┌─────────┬───────┐
	// │ product │ qty   │
	// │ String  │ Int64 │
	// ├─────────┼───────┤
	// │ "apple" │ 10    │
	// │ "pear"  │ 7     │
	// │ "fig"   │ 3     │
	// └─────────┴───────┘
}

func Example_quickstartWithColumns() {
	df, err := ursus.ScanCSV("testdata/sales.csv").
		WithColumns(
			ursus.Col("qty").Mul(ursus.Col("price")).Alias("revenue"),
			ursus.When(ursus.Col("qty").Ge(int64(8))).
				Then("bulk").
				Otherwise("single").
				Alias("kind"),
		).
		Select(ursus.Col("order_id"), ursus.Col("revenue"), ursus.Col("kind")).
		Head(4).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (4, 3)
	// ┌──────────┬─────────┬──────────┐
	// │ order_id │ revenue │ kind     │
	// │ Int64    │ Float64 │ String   │
	// ├──────────┼─────────┼──────────┤
	// │ 1        │ 15      │ "bulk"   │
	// │ 2        │ 6       │ "single" │
	// │ 3        │ 15.75   │ "single" │
	// │ 4        │ 4.5     │ "single" │
	// └──────────┴─────────┴──────────┘
}

func Example_quickstartGroupBy() {
	df, err := ursus.ScanCSV("testdata/sales.csv").
		GroupBy(ursus.Col("product")).
		Agg(
			ursus.Col("qty").Sum().Alias("qty"),
			ursus.Col("price").Max().Alias("price"),
			ursus.Col("region").NUnique().Alias("regions"),
		).
		Sort(ursus.Asc(ursus.Col("product"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 4)
	// ┌─────────┬────────┬─────────┬─────────┐
	// │ product │ qty    │ price   │ regions │
	// │ String  │ Int128 │ Float64 │ Uint64  │
	// ├─────────┼────────┼─────────┼─────────┤
	// │ "apple" │ 26     │ 1.5     │ 3       │
	// │ "fig"   │ 12     │ 3       │ 2       │
	// │ "pear"  │ 15     │ 2.25    │ 3       │
	// └─────────┴────────┴─────────┴─────────┘
}

func Example_quickstartJoin() {
	managers := ursus.Frame(
		ursus.Values("region", []string{"north", "south"}),
		ursus.Values("manager", []string{"Ada", "Grace"}),
	)
	df, err := ursus.ScanCSV("testdata/sales.csv").
		Join(managers,
			ursus.JoinOn(ursus.Col("region")),
			ursus.JoinHow(ursus.JoinLeft)).
		Select(ursus.Col("order_id"), ursus.Col("region"), ursus.Col("manager")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (8, 3)
	// ┌──────────┬─────────┬─────────┐
	// │ order_id │ region  │ manager │
	// │ Int64    │ String  │ String  │
	// ├──────────┼─────────┼─────────┤
	// │ 1        │ "north" │ "Ada"   │
	// │ 2        │ "south" │ "Grace" │
	// │ 3        │ "north" │ "Ada"   │
	// │ 4        │ "east"  │ null    │
	// │ 5        │ "south" │ "Grace" │
	// │ 6        │ "east"  │ null    │
	// │ 7        │ "north" │ "Ada"   │
	// │ 8        │ "south" │ "Grace" │
	// └──────────┴─────────┴─────────┘
}

func Example_quickstartRows() {
	type sale struct {
		ID      int64  `ursus:"order_id"`
		Product string `ursus:"product"`
		Qty     int64  `ursus:"qty"`
	}
	df, err := ursus.ScanCSV("testdata/sales.csv").
		Filter(ursus.Col("qty").Ge(int64(9))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	sales, err := df.Rows[sale]()
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range sales {
		fmt.Printf("%+v\n", s)
	}
	// Output:
	// {ID:1 Product:apple Qty:10}
	// {ID:5 Product:fig Qty:9}
	// {ID:6 Product:apple Qty:12}
}

func Example_quickstartExplain() {
	plan, err := ursus.ScanCSV("testdata/sales.csv").
		Filter(ursus.Col("qty").Gt(int64(3))).
		Select(ursus.Col("product"), ursus.Col("qty")).
		Explain(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(plan)
	// Output:
	// FILTER [(col("qty") > lit(3))]
	//   CSV SCAN testdata/sales.csv
	//     projection: [product, qty] (2/6 cols)
}
