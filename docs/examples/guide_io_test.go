package examples_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"

	"github.com/advenn/ursus"
)

// --- the user guide: CSV, Parquet and Arrow ---------------------------------------------

func Example_ioCSVOptions() {
	data := []byte("id;name;score\n1;Ada;90\n2;Ben;NA\n3;Cai;75\n")
	show(ursus.ScanCSVReader(data, "scores.csv",
		ursus.WithSeparator(';'),
		ursus.WithNullValues("NA"),
		ursus.WithSchemaOverrides(map[string]ursus.DataType{"score": ursus.Float64}),
	))
	// Output:
	// shape: (3, 3)
	// ┌───────┬────────┬─────────┐
	// │ id    │ name   │ score   │
	// │ Int64 │ String │ Float64 │
	// ├───────┼────────┼─────────┤
	// │ 1     │ "Ada"  │ 90      │
	// │ 2     │ "Ben"  │ null    │
	// │ 3     │ "Cai"  │ 75      │
	// └───────┴────────┴─────────┘
}

func Example_ioCSVGlob() {
	show(ursus.ScanCSVGlob("testdata/parts/revenue-*.csv").
		GroupBy(ursus.Col("month")).
		Agg(ursus.Col("revenue").Sum().Alias("revenue")).
		Sort(ursus.Asc(ursus.Col("month"))))
	// Output:
	// shape: (2, 2)
	// ┌───────────┬─────────┐
	// │ month     │ revenue │
	// │ String    │ Float64 │
	// ├───────────┼─────────┤
	// │ "2026-01" │ 86.25   │
	// │ "2026-02" │ 79.25   │
	// └───────────┴─────────┘
}

func Example_ioCSVWrite() {
	var out bytes.Buffer
	err := readings().Head(3).
		WriteCSV(context.Background(), &out, ursus.WithNullValue("NA"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(out.String())
	// Output:
	// hour,temp,backup
	// 0,10.0,9.0
	// 1,NA,11.0
	// 2,NA,NA
}

func Example_ioParquet() {
	ctx := context.Background()
	var file bytes.Buffer
	if err := sales().WriteParquet(ctx, &file, ursus.WithRowGroupRows(4)); err != nil {
		log.Fatal(err)
	}
	q := ursus.ScanParquetBytes(file.Bytes(), "sales.parquet").
		Filter(ursus.Col("order_id").Gt(6)).
		Select(ursus.Col("order_id"), ursus.Col("product"))
	plan, err := q.Explain(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(plan)
	show(q)
	// Output:
	// FILTER [(col("order_id") > lit(6))]
	//   PARQUET SCAN sales.parquet
	//     projection: [order_id, product] (2/6 cols)
	//     predicate: [(col("order_id") > lit(6))]
	//
	// shape: (2, 2)
	// ┌──────────┬─────────┐
	// │ order_id │ product │
	// │ Int64    │ String  │
	// ├──────────┼─────────┤
	// │ 7        │ "fig"   │
	// │ 8        │ "pear"  │
	// └──────────┴─────────┘
}

func Example_ioParquetFrom() {
	ctx := context.Background()
	var file bytes.Buffer
	if err := sales().WriteParquet(ctx, &file); err != nil {
		log.Fatal(err)
	}
	stored := file.Bytes() // standing in for an object in a bucket

	lf := ursus.ScanParquetFrom([]ursus.ParquetFile{{
		Name: "s3://shop/sales.parquet",
		Open: func(ctx context.Context) (io.ReaderAt, int64, error) {
			return bytes.NewReader(stored), int64(len(stored)), nil
		},
	}})
	n, err := lf.Filter(ursus.Col("region").Eq("south")).Count(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n, "orders from the south")
	// Output:
	// 3 orders from the south
}

func Example_ioArrow() {
	ctx := context.Background()
	df, err := sales().
		Select(ursus.Col("order_id"), ursus.Col("qty")).
		Head(3).
		Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	rec, err := df.Record() // the frame's memory, shared with arrow-go
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(rec.NumRows(), "rows,", rec.Schema().Field(1).Type)

	back := ursus.ScanArrowRecords(rec) // copies, so rec can be released
	rec.Release()
	show(back.WithColumns(ursus.Col("qty").Mul(2).Alias("double")))
	// Output:
	// 3 rows, int64
	// shape: (3, 3)
	// ┌──────────┬───────┬────────┐
	// │ order_id │ qty   │ double │
	// │ Int64    │ Int64 │ Int64  │
	// ├──────────┼───────┼────────┤
	// │ 1        │ 10    │ 20     │
	// │ 2        │ 4     │ 8      │
	// │ 3        │ 7     │ 14     │
	// └──────────┴───────┴────────┘
}
