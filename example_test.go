package ursus_test

// Runnable examples. Each one appears on pkg.go.dev beside the symbol it names,
// and `go test` runs it and compares against the Output comment — so an example
// that stops being true stops the build, which is the only kind of documentation
// worth trusting.
//
// They build their data in memory rather than reading a file, so they run
// anywhere and there is nothing to download before the first one works.

import (
	"context"
	"fmt"
	"log"

	"github.com/advenn/ursus"
)

// exampleSales is the little frame the examples below work on.
func exampleSales() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("region", []string{"north", "south", "north", "east", "south"}),
		ursus.Values("product", []string{"apple", "apple", "pear", "pear", "fig"}),
		ursus.Values("qty", []int64{10, 4, 7, 2, 9}),
		ursus.Values("price", []float64{1.50, 1.50, 2.25, 2.25, 3.00}),
	)
}

// The shortest useful thing: build a frame and look at it.
func Example() {
	df, err := exampleSales().Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (5, 4)
	// ┌─────────┬─────────┬───────┬─────────┐
	// │ region  │ product │ qty   │ price   │
	// │ String  │ String  │ Int64 │ Float64 │
	// ├─────────┼─────────┼───────┼─────────┤
	// │ "north" │ "apple" │ 10    │ 1.5     │
	// │ "south" │ "apple" │ 4     │ 1.5     │
	// │ "north" │ "pear"  │ 7     │ 2.25    │
	// │ "east"  │ "pear"  │ 2     │ 2.25    │
	// │ "south" │ "fig"   │ 9     │ 3       │
	// └─────────┴─────────┴───────┴─────────┘
}

// Filter keeps rows; Select keeps columns. Neither runs until Collect.
func ExampleLazyFrame_Filter() {
	df, err := exampleSales().
		Filter(ursus.Col("qty").Gt(int64(5))).
		Select(ursus.Col("region"), ursus.Col("qty")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 2)
	// ┌─────────┬───────┐
	// │ region  │ qty   │
	// │ String  │ Int64 │
	// ├─────────┼───────┤
	// │ "north" │ 10    │
	// │ "north" │ 7     │
	// │ "south" │ 9     │
	// └─────────┴───────┘
}

// Expressions compose: a computed column is just another expression.
func ExampleLazyFrame_WithColumns() {
	df, err := exampleSales().
		WithColumns(ursus.Col("qty").Mul(ursus.Col("price")).Alias("revenue")).
		Select(ursus.Col("product"), ursus.Col("revenue")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (5, 2)
	// ┌─────────┬─────────┐
	// │ product │ revenue │
	// │ String  │ Float64 │
	// ├─────────┼─────────┤
	// │ "apple" │ 15      │
	// │ "apple" │ 6       │
	// │ "pear"  │ 15.75   │
	// │ "pear"  │ 4.5     │
	// │ "fig"   │ 27      │
	// └─────────┴─────────┘
}

// GroupBy takes the keys, Agg takes what to compute per group.
func ExampleLazyFrame_GroupBy() {
	df, err := exampleSales().
		GroupBy(ursus.Col("region")).
		Agg(
			ursus.Col("qty").Sum().Alias("total_qty"),
			ursus.Col("price").Mean().Alias("avg_price"),
			ursus.Len().Alias("orders"),
		).
		Sort(ursus.Asc(ursus.Col("region"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 4)
	// ┌─────────┬───────────┬───────────┬────────┐
	// │ region  │ total_qty │ avg_price │ orders │
	// │ String  │ Int128    │ Float64   │ Uint64 │
	// ├─────────┼───────────┼───────────┼────────┤
	// │ "east"  │ 2         │ 2.25      │ 1      │
	// │ "north" │ 17        │ 1.875     │ 2      │
	// │ "south" │ 13        │ 2.25      │ 2      │
	// └─────────┴───────────┴───────────┴────────┘
}

// GroupBy with NO keys aggregates the whole frame as one group. polars infers
// this from a bare select; ursus asks for it, so it cannot be confused with a
// row-wise select that happens to return one row.
func ExampleLazyFrame_GroupBy_wholeFrame() {
	df, err := exampleSales().
		GroupBy().
		Agg(
			ursus.Len().Alias("rows"),
			ursus.Col("qty").Sum().Alias("total_qty"),
		).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (1, 2)
	// ┌────────┬───────────┐
	// │ rows   │ total_qty │
	// │ Uint64 │ Int128    │
	// ├────────┼───────────┤
	// │ 5      │ 32        │
	// └────────┴───────────┘
}

// Joins take the key columns and, optionally, the kind. Inner is the default.
func ExampleLazyFrame_Join() {
	regions := ursus.Frame(
		ursus.Values("region", []string{"north", "south", "east"}),
		ursus.Values("manager", []string{"ada", "bob", "cy"}),
	)
	df, err := exampleSales().
		Join(regions, ursus.JoinOn(ursus.Col("region"))).
		Select(ursus.Col("product"), ursus.Col("manager")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (5, 2)
	// ┌─────────┬─────────┐
	// │ product │ manager │
	// │ String  │ String  │
	// ├─────────┼─────────┤
	// │ "apple" │ "ada"   │
	// │ "apple" │ "bob"   │
	// │ "pear"  │ "ada"   │
	// │ "pear"  │ "cy"    │
	// │ "fig"   │ "bob"   │
	// └─────────┴─────────┘
}

// Reading values back out. Column gives a typed series; At gives one cell and
// whether it was null.
func ExampleDataFrame_Column() {
	df, err := exampleSales().Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	// Column and At are GENERIC METHODS — Go 1.27's headline feature, and the
	// reason ursus can hand back typed data without a cast at the call site.
	qty, err := df.Column[int64]("qty")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("values:", qty.Values())

	v, ok, err := df.At[string](0, "region")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("row 0 region: %q present=%v\n", v, ok)
	// Output:
	// values: [10 4 7 2 9]
	// row 0 region: "north" present=true
}
