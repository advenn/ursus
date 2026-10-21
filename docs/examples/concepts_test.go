package examples_test

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"

	"github.com/advenn/ursus"
)

// sales is the CSV the concept pages read, its date column a Date.
func sales() *ursus.LazyFrame {
	return ursus.ScanCSV("testdata/sales.csv",
		ursus.WithSchemaOverrides(map[string]ursus.DataType{"date": ursus.Date}))
}

// --- lazy and eager -----------------------------------------------------------

func Example_lazyDeferredError() {
	lf := sales().
		Select(ursus.Col("prodcut")). // a typo: nothing has run, so nothing fails yet
		Filter(ursus.Col("qty").Gt(int64(3)))

	_, err := lf.Collect(context.Background())
	fmt.Println(err)
	// Output:
	// ursus: select: unknown column "prodcut"
	//   did you mean: "product"?
	//   available: order_id, date, region, product, qty, price
	//   at Project
}

func Example_lazyExplainUnoptimized() {
	q := sales().
		Filter(ursus.Col("qty").Gt(int64(3))).
		Select(ursus.Col("product"), ursus.Col("qty"))

	written, err := q.Explain(context.Background(), ursus.Optimized(false))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(written)
	// Output:
	// PROJECT [col("product"), col("qty")]
	//   FILTER [(col("qty") > lit(3))]
	//     CSV SCAN testdata/sales.csv
	//       projection: * (6 cols)
}

func Example_lazyBatches() {
	ctx := context.Background()
	for batch, err := range sales().CollectBatches(ctx, ursus.WithBatchSize(3)) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("a batch of", batch.Height(), "rows")
	}
	// Output:
	// a batch of 3 rows
	// a batch of 3 rows
	// a batch of 2 rows
}

func Example_eager() {
	ctx := context.Background()
	df, err := sales().Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	big, err := df.Filter(ctx, ursus.Col("qty").Ge(int64(9)))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(big.Shape())
	// Output:
	// 3 6
}

// --- expressions ----------------------------------------------------------------

func Example_exprContexts() {
	ctx := context.Background()
	revenue := ursus.Col("qty").Mul(ursus.Col("price"))

	perRow, err := sales().
		Select(ursus.Col("order_id"), revenue.Alias("revenue")).
		Head(3).
		Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	total, err := sales().Select(revenue.Sum().Alias("revenue")).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	byRegion, err := sales().GroupBy(ursus.Col("region")).
		Agg(revenue.Sum().Alias("revenue")).
		Sort(ursus.Asc(ursus.Col("region"))).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(perRow)
	fmt.Println(total)
	fmt.Println(byRegion)
	// Output:
	// shape: (3, 2)
	// ┌──────────┬─────────┐
	// │ order_id │ revenue │
	// │ Int64    │ Float64 │
	// ├──────────┼─────────┤
	// │ 1        │ 15      │
	// │ 2        │ 6       │
	// │ 3        │ 15.75   │
	// └──────────┴─────────┘
	//
	// shape: (1, 1)
	// ┌─────────┐
	// │ revenue │
	// │ Float64 │
	// ├─────────┤
	// │ 108.75  │
	// └─────────┘
	//
	// shape: (3, 2)
	// ┌─────────┬─────────┐
	// │ region  │ revenue │
	// │ String  │ Float64 │
	// ├─────────┼─────────┤
	// │ "east"  │ 22.5    │
	// │ "north" │ 39.75   │
	// │ "south" │ 46.5    │
	// └─────────┴─────────┘
}

func Example_exprOver() {
	df, err := sales().
		Select(
			ursus.Col("order_id"),
			ursus.Col("region"),
			ursus.Col("qty"),
			ursus.Col("qty").Sum().Over(ursus.Col("region")).Alias("region_qty"),
			ursus.Col("qty").Rank(ursus.RankDense, true).
				Over(ursus.Col("region")).Alias("rank"),
		).
		Sort(ursus.Asc(ursus.Col("region")), ursus.Asc(ursus.Col("rank"))).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (8, 5)
	// ┌──────────┬─────────┬───────┬────────────┬────────┐
	// │ order_id │ region  │ qty   │ region_qty │ rank   │
	// │ Int64    │ String  │ Int64 │ Int128     │ Uint32 │
	// ├──────────┼─────────┼───────┼────────────┼────────┤
	// │ 6        │ "east"  │ 12    │ 14         │ 1      │
	// │ 4        │ "east"  │ 2     │ 14         │ 2      │
	// │ 1        │ "north" │ 10    │ 20         │ 1      │
	// │ 3        │ "north" │ 7     │ 20         │ 2      │
	// │ 7        │ "north" │ 3     │ 20         │ 3      │
	// │ 5        │ "south" │ 9     │ 19         │ 1      │
	// │ 8        │ "south" │ 6     │ 19         │ 2      │
	// │ 2        │ "south" │ 4     │ 19         │ 3      │
	// └──────────┴─────────┴───────┴────────────┴────────┘
}

func Example_exprMany() {
	df, err := sales().
		Select(
			ursus.Col("product"),
			ursus.Col("qty", "price").Mul(int64(2)).Suffix("_x2"),
		).
		Head(3).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌─────────┬────────┬──────────┐
	// │ product │ qty_x2 │ price_x2 │
	// │ String  │ Int64  │ Float64  │
	// ├─────────┼────────┼──────────┤
	// │ "apple" │ 20     │ 3        │
	// │ "apple" │ 8      │ 3        │
	// │ "pear"  │ 14     │ 4.5      │
	// └─────────┴────────┴──────────┘
}

func Example_exprNamespaces() {
	df, err := sales().
		Select(
			ursus.Col("product").Str().ToUpper().Alias("product"),
			ursus.Col("date").Dt().Weekday().Alias("weekday"),
			ursus.Col("product").Str().Contains("p", true).Alias("has_p"),
		).
		Head(3).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌─────────┬─────────┬───────┐
	// │ product │ weekday │ has_p │
	// │ String  │ Int32   │ Bool  │
	// ├─────────┼─────────┼───────┤
	// │ "APPLE" │ 6       │ true  │
	// │ "APPLE" │ 6       │ true  │
	// │ "PEAR"  │ 7       │ true  │
	// └─────────┴─────────┴───────┘
}

// --- types and nulls --------------------------------------------------------------

func Example_typesSchema() {
	df, err := sales().Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df.Schema())
	// Output:
	// {order_id: Int64, date: Date, region: String, product: String, qty: Int64, price: Float64}
}

func Example_typesNulls() {
	df, err := ursus.Frame(
		ursus.ValuesNullable("score", []float64{4, 0, 8}, []bool{true, false, true}),
	).Select(
		ursus.Len().Alias("rows"),
		ursus.Col("score").Count().Alias("count"),
		ursus.Col("score").NullCount().Alias("nulls"),
		ursus.Col("score").Mean().Alias("mean"),
		ursus.Col("score").FillNullWith(0.0).Mean().Alias("mean_as_zero"),
	).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (1, 5)
	// ┌────────┬────────┬────────┬─────────┬──────────────┐
	// │ rows   │ count  │ nulls  │ mean    │ mean_as_zero │
	// │ Uint64 │ Uint64 │ Uint64 │ Float64 │ Float64      │
	// ├────────┼────────┼────────┼─────────┼──────────────┤
	// │ 3      │ 2      │ 1      │ 6       │ 4            │
	// └────────┴────────┴────────┴─────────┴──────────────┘
}

func Example_typesNaN() {
	df, err := ursus.Frame(
		ursus.ValuesNullable("x",
			[]float64{1, math.NaN(), 0},
			[]bool{true, true, false}),
	).Select(
		ursus.Col("x"),
		ursus.Col("x").IsNull().Alias("is_null"),
		ursus.Col("x").IsNan().Alias("is_nan"),
	).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌─────────┬─────────┬────────┐
	// │ x       │ is_null │ is_nan │
	// │ Float64 │ Bool    │ Bool   │
	// ├─────────┼─────────┼────────┤
	// │ 1       │ false   │ false  │
	// │ NaN     │ false   │ true   │
	// │ null    │ true    │ null   │
	// └─────────┴─────────┴────────┘
}

func Example_typesCast() {
	ctx := context.Background()
	text := ursus.Frame(ursus.Values("n", []string{"12", "7", "seven"}))

	_, err := text.Select(ursus.Col("n").Cast(ursus.Int64)).Collect(ctx)
	fmt.Println(err)

	df, err := text.Select(ursus.Col("n").CastLossy(ursus.Int64)).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// ursus: cast: cannot parse "seven" as Int64 at row 2
	//   use CastLossy to turn unparseable values into nulls instead
	// shape: (3, 1)
	// ┌───────┐
	// │ n     │
	// │ Int64 │
	// ├───────┤
	// │ 12    │
	// │ 7     │
	// │ null  │
	// └───────┘
}

func Example_typesFrameCast() {
	lines := sales().
		Select(ursus.Col("order_id"), ursus.Col("qty"), ursus.Col("price")).
		Head(2)
	show(lines.Cast(map[string]ursus.DataType{
		"qty":   ursus.Int32,
		"price": ursus.Float32,
	}))
	show(lines.CastTypes(map[ursus.DataType]ursus.DataType{ursus.Int64: ursus.Int16}))
	show(lines.Select(ursus.Col("price").Truncate(0).Cast(ursus.Int64)))
	// Output:
	// shape: (2, 3)
	// ┌──────────┬───────┬─────────┐
	// │ order_id │ qty   │ price   │
	// │ Int64    │ Int32 │ Float32 │
	// ├──────────┼───────┼─────────┤
	// │ 1        │ 10    │ 1.5     │
	// │ 2        │ 4     │ 1.5     │
	// └──────────┴───────┴─────────┘
	//
	// shape: (2, 3)
	// ┌──────────┬───────┬─────────┐
	// │ order_id │ qty   │ price   │
	// │ Int16    │ Int16 │ Float64 │
	// ├──────────┼───────┼─────────┤
	// │ 1        │ 10    │ 1.5     │
	// │ 2        │ 4     │ 1.5     │
	// └──────────┴───────┴─────────┘
	//
	// shape: (2, 1)
	// ┌───────┐
	// │ price │
	// │ Int64 │
	// ├───────┤
	// │ 1     │
	// │ 1     │
	// └───────┘
}

func Example_typesMatchToSchema() {
	want := ursus.MustSchema(
		ursus.Of("order_id", ursus.Int64),
		ursus.Of("qty", ursus.Int64),
		ursus.Of("discount", ursus.Float64),
	)
	drifted := sales().Head(2).Select(
		ursus.Col("qty").Cast(ursus.Int32), ursus.Col("order_id"), ursus.Col("product"))

	_, err := drifted.
		MatchToSchema(want, ursus.MatchOptions{}).
		Collect(context.Background())
	fmt.Println(err)

	show(drifted.MatchToSchema(want, ursus.MatchOptions{
		MissingColumns: ursus.MatchInsert,
		ExtraColumns:   ursus.MatchIgnore,
		IntegerCast:    ursus.MatchUpcast,
	}))
	// Output:
	// ursus: match_to_schema: qty is Int32, and the schema's is Int64
	//   MatchOptions{IntegerCast: ursus.MatchUpcast} widens it
	// shape: (2, 3)
	// ┌──────────┬───────┬──────────┐
	// │ order_id │ qty   │ discount │
	// │ Int64    │ Int64 │ Float64  │
	// ├──────────┼───────┼──────────┤
	// │ 1        │ 10    │ null     │
	// │ 2        │ 4     │ null     │
	// └──────────┴───────┴──────────┘
}

// --- memory ----------------------------------------------------------------------------

func Example_memorySpill() {
	keys := make([]int64, 100_000)
	for i := range keys {
		keys[i] = int64(i * 7919 % 25_000)
	}
	var stats ursus.MemoryStats
	df, err := ursus.Frame(ursus.Values("k", keys)).
		GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n")).
		Select(ursus.Len().Alias("groups"), ursus.Col("n").Max().Alias("largest")).
		Collect(context.Background(),
			ursus.WithMemoryLimit(256<<10),
			ursus.WithSpillDir(os.TempDir()),
			ursus.WithMemoryStats(&stats))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	fmt.Println("spilled:", stats.Spills > 0)
	// Output:
	// shape: (1, 2)
	// ┌────────┬─────────┐
	// │ groups │ largest │
	// │ Uint64 │ Uint64  │
	// ├────────┼─────────┤
	// │ 25000  │ 4       │
	// └────────┴─────────┘
	//
	// spilled: true
}

func Example_memoryRefusal() {
	keys := make([]int64, 100_000)
	_, err := ursus.Frame(ursus.Values("k", keys)).
		Select(ursus.Col("k").Implode()).
		Collect(context.Background(),
			ursus.WithMemoryLimit(64<<10),
			ursus.WithSpillDir(os.TempDir()))
	fmt.Println(err)
	// Output:
	// ursus: group_by: memory limit of 64.0KiB exceeded: the query is holding 105.2KiB, 105.2KiB of it in this operator
	//   group_by holds one entry per distinct key, and implode holds every VALUE as its answer — which partitioning to disk cannot divide, because it divides across keys (quantile, median and n_unique spill their values); raise the limit with WithMemoryLimit
	//   sort, group_by, join, unique, over, reverse and tail spill to disk; join_asof, merge_sorted, hstack, rolling and group_by_dynamic stream their input; an over computing a rolling window by time one of whose periods holds more than the limit fails rather than exceed it, and so does implode over one group larger than it
}

// Compiled, not run: it reads files the tests do not have.
func Example_memorySink() {
	ctx := context.Background()
	err := ursus.ScanParquet("events/*.parquet").
		Sort(ursus.Desc(ursus.Col("ts"))).
		SinkParquet(ctx, "sorted.parquet",
			ursus.WithMemoryLimit(512<<20),
			ursus.WithSpillDir("/var/tmp/ursus"))
	if err != nil {
		log.Fatal(err)
	}
}
