package ursus_test

// Runnable examples. Each one appears on pkg.go.dev beside the symbol it names,
// and `go test` runs it and compares against the Output comment — so an example
// that stops being true stops the build, which is the only kind of documentation
// worth trusting.
//
// They build their data in memory rather than reading a file, so they run
// anywhere and there is nothing to download before the first one works.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

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

// Arrow both ways. Record shares a frame's memory with arrow-go; ScanArrowRecords
// copies records in, so the record can be released straight away.
func ExampleScanArrowRecords() {
	ctx := context.Background()
	big, err := exampleSales().Filter(ursus.Col("qty").Gt(5)).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	rec, err := big.Record()
	if err != nil {
		log.Fatal(err)
	}

	lf := ursus.ScanArrowRecords(rec)
	rec.Release()

	df, err := lf.Select(ursus.Col("region"), ursus.Col("qty")).Collect(ctx)
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

// --- new in 0.3 --------------------------------------------------------------------

// Read Parquet from wherever it lives. Open returns an io.ReaderAt and its size —
// here over bytes in memory, standing in for a ranged GET against an object store.
// ursus reads the footer, then only the column chunks the query needs.
func ExampleScanParquetFrom() {
	ctx := context.Background()
	var file bytes.Buffer
	if err := exampleSales().WriteParquet(ctx, &file); err != nil {
		log.Fatal(err)
	}

	lf := ursus.ScanParquetFrom([]ursus.ParquetFile{{
		Name: "s3://shop/sales.parquet",
		Open: func(ctx context.Context) (io.ReaderAt, int64, error) {
			return bytes.NewReader(file.Bytes()), int64(file.Len()), nil
		},
	}})
	n, err := lf.Filter(ursus.Col("product").Eq("pear")).Count(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n, "pear sales")
	// Output:
	// 2 pear sales
}

// A Go function over each value, taking part in the plan like any expression.
// Nulls pass through without calling it.
func ExampleExpr_MapElements() {
	df, err := exampleSales().Select(
		ursus.Col("product"),
		ursus.Col("product").MapElements("shout", ursus.String,
			func(s string) (string, error) { return strings.ToUpper(s) + "!", nil }).Alias("loud"),
	).Head(3).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 2)
	// ┌─────────┬──────────┐
	// │ product │ loud     │
	// │ String  │ String   │
	// ├─────────┼──────────┤
	// │ "apple" │ "APPLE!" │
	// │ "apple" │ "APPLE!" │
	// │ "pear"  │ "PEAR!"  │
	// └─────────┴──────────┘
}

// Split builds a List column; the .list namespace asks questions of each list, and
// Explode gives every element a row of its own.
func ExampleLazyFrame_Explode() {
	lf := ursus.Frame(
		ursus.Values("order", []int64{1, 2}),
		ursus.Values("items", []string{"apple,pear", "fig"}),
	).WithColumns(ursus.Col("items").Str().Split(","))

	df, err := lf.WithColumns(ursus.Col("items").List().Len().Alias("n")).
		Explode("items").Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌───────┬─────────┬────────┐
	// │ order │ items   │ n      │
	// │ Int64 │ String  │ Uint32 │
	// ├───────┼─────────┼────────┤
	// │ 1     │ "apple" │ 2      │
	// │ 1     │ "pear"  │ 2      │
	// │ 2     │ "fig"   │ 1      │
	// └───────┴─────────┴────────┘
}

// A join on a condition rather than equal keys: each sale with the price band it
// falls in.
func ExampleLazyFrame_JoinWhere() {
	bands := ursus.Frame(
		ursus.Values("band", []string{"cheap", "dear"}),
		ursus.Values("lo", []float64{0, 2}),
		ursus.Values("hi", []float64{2, 10}),
	)
	df, err := exampleSales().Select(ursus.Col("product"), ursus.Col("price")).
		JoinWhere(bands,
			ursus.Col("price").Ge(ursus.Col("lo")),
			ursus.Col("price").Lt(ursus.Col("hi"))).
		Select(ursus.Col("product"), ursus.Col("price"), ursus.Col("band")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (5, 3)
	// ┌─────────┬─────────┬─────────┐
	// │ product │ price   │ band    │
	// │ String  │ Float64 │ String  │
	// ├─────────┼─────────┼─────────┤
	// │ "apple" │ 1.5     │ "cheap" │
	// │ "apple" │ 1.5     │ "cheap" │
	// │ "pear"  │ 2.25    │ "dear"  │
	// │ "pear"  │ 2.25    │ "dear"  │
	// │ "fig"   │ 3       │ "dear"  │
	// └─────────┴─────────┴─────────┘
}

// Decimal arithmetic is exact: + - * keep every digit and say so in the type, and
// a result past 38 digits is refused rather than rounded.
func ExampleDecimal() {
	df, err := exampleSales().Select(
		ursus.Col("product"),
		ursus.Col("price").Cast(ursus.Decimal(10, 2)).Mul(ursus.Col("qty")).Alias("revenue"),
	).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (5, 2)
	// ┌─────────┬────────────────┐
	// │ product │ revenue        │
	// │ String  │ Decimal(29, 2) │
	// ├─────────┼────────────────┤
	// │ "apple" │ 15.00          │
	// │ "apple" │ 6.00           │
	// │ "pear"  │ 15.75          │
	// │ "pear"  │ 4.50           │
	// │ "fig"   │ 27.00          │
	// └─────────┴────────────────┘
}

// An Enum is built from String, and sorts by its categories rather than by its
// text.
func ExampleEnum() {
	size := ursus.Enum("small", "medium", "large")
	df, err := ursus.Frame(ursus.Values("size", []string{"large", "small", "medium"})).
		Select(ursus.Col("size").Cast(size)).
		Sort(ursus.Asc(ursus.Col("size"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 1)
	// ┌────────────────────────────┐
	// │ size                       │
	// │ Enum(small, medium, large) │
	// ├────────────────────────────┤
	// │ "small"                    │
	// │ "medium"                   │
	// │ "large"                    │
	// └────────────────────────────┘
}

// Under a memory limit, sort, group-by, join, unique and partitioned windows spill
// to disk and give the answer they give in memory; an operator that cannot spill
// fails with an error naming itself.
func ExampleWithMemoryLimit() {
	keys := make([]int64, 40000)
	for i := range keys {
		keys[i] = int64(i * 7919 % 20000) // each of 20000 keys, twice
	}
	var stats ursus.MemoryStats
	n, err := ursus.Frame(ursus.Values("k", keys)).Unique("k").
		Count(context.Background(), ursus.WithBatchSize(1024),
			ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(os.TempDir()),
			ursus.WithMemoryStats(&stats))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n, "distinct keys; spilled:", stats.Spills > 0)
	// Output:
	// 20000 distinct keys; spilled: true
}

// A List column writes to Parquet and reads back, in the standard encoding PyArrow
// and Polars read too.
func ExampleLazyFrame_WriteParquet() {
	ctx := context.Background()
	orders := ursus.Frame(
		ursus.Values("order", []int64{1, 2}),
		ursus.Values("items", []string{"apple,pear", "fig"}),
	).WithColumns(ursus.Col("items").Str().Split(","))

	var file bytes.Buffer
	if err := orders.WriteParquet(ctx, &file); err != nil {
		log.Fatal(err)
	}
	df, err := ursus.ScanParquetBytes(file.Bytes(), "orders.parquet").Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (2, 2)
	// ┌───────┬───────────────────┐
	// │ order │ items             │
	// │ Int64 │ List(String)      │
	// ├───────┼───────────────────┤
	// │ 1     │ ["apple", "pear"] │
	// │ 2     │ ["fig"]           │
	// └───────┴───────────────────┘
}
