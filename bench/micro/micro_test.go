package micro

import (
	"testing"

	"github.com/advenn/ursus"
)

// ---------------------------------------------------------------------- IO --
//
// The root bench_test.go builds every fixture in memory, so nothing there
// measures a file. These four do, and they are the ones a real workload spends
// most of its time in.

func BenchmarkScanParquet(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanParquet(f.parquet))
	}
}

// BenchmarkScanParquetProjected reads 2 of 7 columns. The ratio against
// BenchmarkScanParquet is how much projection pushdown is worth.
func BenchmarkScanParquetProjected(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanParquet(f.parquet).
			Select(ursus.Col("id"), ursus.Col("price")))
	}
}

// BenchmarkScanParquetWide reads 2 of 32 columns. Same question, asked where
// the answer matters more.
func BenchmarkScanParquetWide(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanParquet(f.wide).
			Select(ursus.Col("c0"), ursus.Col("c17")))
	}
}

// BenchmarkScanParquetPruned filters on `id`, which is the row index and so is
// monotonic in file order. Every row group but the first is out of range, so
// statistics can skip them without decoding a value.
//
// Pair it with BenchmarkScanParquetPrunedMiss, which asks for the same number of
// rows through a column with no clustering: the difference between the two is
// the whole value of row-group pruning, and it is zero when the data does not
// cooperate.
func BenchmarkScanParquetPruned(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanParquet(f.parquet).
			Filter(ursus.Col("id").Lt(int64(16384))))
	}
}

func BenchmarkScanParquetPrunedMiss(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanParquet(f.parquet).
			Filter(ursus.Col("key").Lt(int64(16))))
	}
}

func BenchmarkScanCSV(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanCSV(f.csv))
	}
}

// BenchmarkScanCSVTypedSchema skips inference by declaring the schema. The
// difference against BenchmarkScanCSV is what the inference pass costs, and
// nothing else: the declared types are exactly what inference would produce,
// including `ts` as a String, because ursus's CSV inference does not recognise
// dates. Declaring it as a Datetime instead would add timestamp parsing to the
// measurement and turn the comparison into something else.
func BenchmarkScanCSVTypedSchema(b *testing.B) {
	f := load(b)
	schema := ursus.MustSchema(
		ursus.Of("id", ursus.Int64),
		ursus.Of("qty", ursus.Int64),
		ursus.Of("price", ursus.Float64),
		ursus.Of("region", ursus.String),
		ursus.Of("key", ursus.Int64),
		ursus.Of("ts", ursus.String),
		ursus.Of("label", ursus.String),
	)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanCSV(f.csv, ursus.WithSchema(schema)))
	}
}

func BenchmarkSinkParquet(b *testing.B) {
	f := load(b)
	dir := b.TempDir()
	b.ReportAllocs()
	for b.Loop() {
		if err := ursus.ScanParquet(f.parquet).
			Select(ursus.Col("id"), ursus.Col("price"), ursus.Col("region")).
			SinkParquet(b.Context(), dir+"/out.parquet"); err != nil {
			b.Fatal(err)
		}
	}
}

// ------------------------------------------------------------ batch sizing --
//
// WithBatchSize is a knob with no measured guidance anywhere in the repo. The
// sweep says where the vectorisation win flattens out and where per-batch
// overhead starts to dominate.

func BenchmarkBatchSize(b *testing.B) {
	f := load(b)
	for _, size := range []int{512, 2048, 8192, 32768, 131072} {
		b.Run("batch="+itoa(size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				consume(b, ursus.ScanParquet(f.parquet).
					Filter(ursus.Col("qty").Gt(int64(50))).
					Select(ursus.Col("price").Mul(ursus.Col("qty")).Alias("total")),
					ursus.WithBatchSize(size))
			}
		})
	}
}

// ------------------------------------------------------------- collection --

// BenchmarkCollectBatches streams the result instead of materialising it. The
// ratio against BenchmarkCollectAll is what holding the whole answer costs.
func BenchmarkCollectBatches(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		total := 0
		for df, err := range ursus.ScanParquet(f.parquet).
			Filter(ursus.Col("qty").Gt(int64(50))).
			CollectBatches(b.Context()) {
			if err != nil {
				b.Fatal(err)
			}
			total += df.Height()
		}
		if total == 0 {
			b.Fatal("filter matched nothing; the fixture is wrong")
		}
	}
}

func BenchmarkCollectAll(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		collect(b, ursus.ScanParquet(f.parquet).
			Filter(ursus.Col("qty").Gt(int64(50))))
	}
}

// ------------------------------------------------------------ group tables --
//
// Two cardinalities, so the accumulator loop and the group table are separable:
// 4 groups fit in registers, 16k do not fit in L2.

func BenchmarkGroupByLowCardinality(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		collect(b, ursus.ScanParquet(f.parquet).
			GroupBy(ursus.Col("region")).
			Agg(
				ursus.Col("price").Sum().Alias("revenue"),
				ursus.Len().Alias("n"),
			))
	}
}

func BenchmarkGroupByHighCardinality(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		collect(b, ursus.ScanParquet(f.parquet).
			GroupBy(ursus.Col("key")).
			Agg(
				ursus.Col("price").Sum().Alias("revenue"),
				ursus.Len().Alias("n"),
			))
	}
}

// BenchmarkGroupByStringKey groups on a high-cardinality string, which is where
// hashing and key storage cost more than the aggregation does.
func BenchmarkGroupByStringKey(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		collect(b, ursus.ScanParquet(f.parquet).
			GroupBy(ursus.Col("label")).
			Agg(ursus.Col("price").Sum().Alias("revenue")))
	}
}

// BenchmarkGroupByThreads is the one number the design docs lean on hardest:
// step-5-as-built.md records 2.97x from 1 to 8 threads for the pipeline.
//
// The aggregate is no longer a serial breaker: step 17 wired parallel aggregation
// up, and aggBreaker hands out one sink per thread whenever aggWorkers' two gates
// pass — no MaintainOrder or order-dependent aggregate, and no memory limit. This
// query trips neither, so it runs on all of them.
func BenchmarkGroupByThreads(b *testing.B) {
	f := load(b)
	for _, threads := range []int{1, 2, 4, 8} {
		b.Run("threads="+itoa(threads), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				collect(b, ursus.ScanParquet(f.parquet).
					Filter(ursus.Col("qty").Gt(int64(20))).
					GroupBy(ursus.Col("key")).
					Agg(ursus.Col("price").Sum().Alias("revenue")),
					ursus.WithThreads(threads))
			}
		})
	}
}

// ----------------------------------------------------------------- windows --
//
// Not benchmarked anywhere in the repo, and the operator refuses to spill, so
// its memory behaviour is worth watching as much as its speed.

func BenchmarkWindowMean(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		collect(b, ursus.ScanParquet(f.parquet).
			Select(
				ursus.Col("price").
					Sub(ursus.Col("price").Mean().Over(ursus.Col("region"))).
					Alias("delta"),
			))
	}
}

func BenchmarkWindowRank(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		collect(b, ursus.ScanParquet(f.parquet).
			Select(
				ursus.Col("price").Rank(ursus.RankDense, true).
					Over(ursus.Col("region")).
					Alias("rank"),
			))
	}
}

// --------------------------------------------------------- string kernels --

func BenchmarkStringContains(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanParquet(f.parquet).
			Filter(ursus.Col("label").Str().Contains("apac", true)))
	}
}

func BenchmarkStringRegex(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		consume(b, ursus.ScanParquet(f.parquet).
			Filter(ursus.Col("label").Str().Contains(`sku-(eu|us)-\d{3}$`, false)))
	}
}

func BenchmarkStringSliceUpper(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		collect(b, ursus.ScanParquet(f.parquet).
			Select(ursus.Col("label").Str().Slice(0, 6).Str().ToUpper().Alias("prefix")))
	}
}

// -------------------------------------------------------------- temporal --

func BenchmarkDatetimeExtract(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		collect(b, ursus.ScanParquet(f.parquet).
			GroupBy(ursus.Col("ts").Dt().Month().Alias("month")).
			Agg(ursus.Col("price").Sum().Alias("revenue")))
	}
}

// ------------------------------------------------------------------ joins --

func BenchmarkJoinInner(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		right := ursus.ScanParquet(f.parquet).
			Select(ursus.Col("key"), ursus.Col("region")).
			Unique("key")
		consume(b, ursus.ScanParquet(f.parquet).
			Select(ursus.Col("key"), ursus.Col("price")).
			Join(right, ursus.JoinOn(ursus.Col("key"))))
	}
}

// BenchmarkJoinAsOf is not measured anywhere in the repo, and as-of is the join
// people reach for ursus to get.
func BenchmarkJoinAsOf(b *testing.B) {
	f := load(b)
	b.ReportAllocs()
	for b.Loop() {
		left := ursus.ScanParquet(f.parquet).
			Select(ursus.Col("ts"), ursus.Col("region"), ursus.Col("price")).
			Sort(ursus.Asc(ursus.Col("ts")))
		right := ursus.ScanParquet(f.parquet).
			Select(ursus.Col("ts"), ursus.Col("qty")).
			Unique("ts").
			Sort(ursus.Asc(ursus.Col("ts")))
		consume(b, left.JoinAsOf(right,
			ursus.AsOfOn(ursus.Col("ts")),
			ursus.AsOfStrategyOpt(ursus.AsOfBackward),
		))
	}
}

// ----------------------------------------------------------------- spilling --
//
// Each of these pairs with an unbounded twin above. Read them as ratios: the
// ratio is what bounded memory costs when it is not actually needed, which is
// the number that decides whether a memory limit is safe to leave switched on.

func BenchmarkSortUnbounded(b *testing.B) {
	f := load(b)
	dir := b.TempDir()
	b.ReportAllocs()
	for b.Loop() {
		if err := ursus.ScanParquet(f.parquet).
			Select(ursus.Col("id"), ursus.Col("price")).
			Sort(ursus.Desc(ursus.Col("price"))).
			SinkParquet(b.Context(), dir+"/sorted.parquet"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSortSpilling(b *testing.B) {
	f := load(b)
	dir := b.TempDir()
	b.ReportAllocs()
	var stats ursus.MemoryStats
	for b.Loop() {
		if err := ursus.ScanParquet(f.parquet).
			Select(ursus.Col("id"), ursus.Col("price")).
			Sort(ursus.Desc(ursus.Col("price"))).
			SinkParquet(b.Context(), dir+"/sorted.parquet",
				ursus.WithMemoryLimit(4<<20),
				ursus.WithSpillDir(dir),
				ursus.WithMemoryStats(&stats)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(stats.Spills), "spills")
	b.ReportMetric(float64(stats.Peak)/(1<<20), "peakMiB")
}

func BenchmarkGroupBySpilling(b *testing.B) {
	f := load(b)
	dir := b.TempDir()
	b.ReportAllocs()
	var stats ursus.MemoryStats
	for b.Loop() {
		// 64 KiB, not 1 MiB: 16k groups of one float is ~128 KiB of
		// accumulator, so a megabyte ceiling never binds and the benchmark
		// would report a spilling path that never spilled.
		if err := ursus.ScanParquet(f.parquet).
			GroupBy(ursus.Col("key")).
			Agg(ursus.Col("price").Sum().Alias("revenue")).
			SinkParquet(b.Context(), dir+"/grouped.parquet",
				ursus.WithMemoryLimit(64<<10),
				ursus.WithSpillDir(dir),
				ursus.WithMemoryStats(&stats)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(stats.Spills), "spills")
	b.ReportMetric(float64(stats.Peak)/(1<<20), "peakMiB")
}
