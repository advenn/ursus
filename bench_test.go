package ursus_test

import (
	"strconv"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
)

// Engine benchmarks.
//
// Before step 5 the repository had exactly three benchmarks, all in
// internal/arrowx, all about the Phase-0 arrow-go bet rather than about queries.
// Nothing could answer "did that change make anything faster?" — which is a
// problem for any optimization, and a disqualifying one for parallelism, whose
// entire justification is a number.
//
// These are deliberately end-to-end through the public API: what a user's query
// costs, not what one kernel costs in isolation.

// benchFrame builds an in-memory source of `rows` rows in batches of `batch`,
// large enough that per-batch overhead is not what is being measured.
//
// It returns a *memsrc.Source rather than a LazyFrame so each benchmark iteration
// can build a fresh plan over the same data without rebuilding the data.
func benchFrame(b *testing.B, rows, batch int) *memsrc.Source {
	b.Helper()

	schema, err := dtype.NewSchema(
		dtype.Field{Name: "id", Type: dtype.Int64},
		dtype.Field{Name: "qty", Type: dtype.Int64},
		dtype.Field{Name: "price", Type: dtype.Float64},
		dtype.Field{Name: "region", Type: dtype.String},
	)
	if err != nil {
		b.Fatal(err)
	}

	regions := []string{"eu", "us", "apac", "latam"}
	var batches []*data.Batch
	for start := 0; start < rows; start += batch {
		n := min(batch, rows-start)
		ids := make([]int64, n)
		qty := make([]int64, n)
		price := make([]float64, n)
		reg := make([]string, n)
		for i := range n {
			v := start + i
			ids[i] = int64(v)
			qty[i] = int64(v % 100)
			price[i] = float64(v%1000) * 1.5
			reg[i] = regions[v%len(regions)]
		}
		bt, err := data.NewBatch(schema, []*data.Column{
			data.NewFixed("id", dtype.Int64, ids, bitmap.AllSet(n)),
			data.NewFixed("qty", dtype.Int64, qty, bitmap.AllSet(n)),
			data.NewFixed("price", dtype.Float64, price, bitmap.AllSet(n)),
			data.NewString("region", reg, bitmap.AllSet(n)),
		})
		if err != nil {
			b.Fatal(err)
		}
		batches = append(batches, bt)
	}

	src, err := memsrc.New(schema, batches...)
	if err != nil {
		b.Fatal(err)
	}
	return src
}

const (
	benchRows  = 1 << 20 // ~1M rows: enough that a thread actually has work to do
	benchBatch = 8192
)

// BenchmarkPipeline is the shape parallelism targets: a scan feeding a chain of
// stateless BatchOps. Everything here is embarrassingly parallel in principle.
func BenchmarkPipeline(b *testing.B) {
	src := benchFrame(b, benchRows, benchBatch)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		n, err := ursus.Scan(src).
			Filter(ursus.Col("qty").Gt(50)).
			WithColumns(ursus.Col("price").Mul(ursus.Col("qty")).Alias("total")).
			Select(ursus.Col("id"), ursus.Col("total")).
			Count(b.Context())
		if err != nil {
			b.Fatal(err)
		}
		if n == 0 {
			b.Fatal("empty result: the benchmark is measuring nothing")
		}
	}
}

// BenchmarkFilterOnly isolates the cheapest useful pipeline, where per-batch
// overhead is the largest fraction and parallelism has the least to hide behind.
func BenchmarkFilterOnly(b *testing.B) {
	src := benchFrame(b, benchRows, benchBatch)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if _, err := ursus.Scan(src).
			Filter(ursus.Col("qty").Gt(50)).
			Count(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkProjectArithmetic is evaluator-bound rather than scan-bound.
func BenchmarkProjectArithmetic(b *testing.B) {
	src := benchFrame(b, benchRows, benchBatch)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if _, err := ursus.Scan(src).
			Select(ursus.Col("price").Mul(ursus.Col("qty")).Add(ursus.Col("price")).Alias("t")).
			Count(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCollect measures the materialising path, which is where exec.Collect's
// 2x peak lives.
func BenchmarkCollect(b *testing.B) {
	src := benchFrame(b, benchRows/8, benchBatch)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		df, err := ursus.Scan(src).
			Filter(ursus.Col("qty").Gt(90)).
			Select(ursus.Col("id"), ursus.Col("price")).
			Collect(b.Context())
		if err != nil {
			b.Fatal(err)
		}
		if df.Height() == 0 {
			b.Fatal("empty result")
		}
	}
}

// --- pipeline breakers, for context ------------------------------------------------
//
// These stay SERIAL in step 5. They are benchmarked anyway, so that the cost of
// parallelising only the pipeline is visible: a query dominated by its aggregate
// should show little improvement, and that is a fact worth being able to state
// rather than guess.

func BenchmarkGroupBy(b *testing.B) {
	src := benchFrame(b, benchRows/4, benchBatch)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if _, err := ursus.Scan(src).
			GroupBy(ursus.Col("region")).
			Agg(ursus.Col("price").Sum().Alias("total"), ursus.Len().Alias("n")).
			Collect(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSort(b *testing.B) {
	src := benchFrame(b, benchRows/16, benchBatch)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if _, err := ursus.Scan(src).
			Sort(ursus.Desc(ursus.Col("price"))).
			Collect(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTopK is BenchmarkSort with a Head, which is the branch step 9 turned
// on and never measured: limit pushdown reaches the Sort, the sink takes the
// ArgTopK path, and the cost should be O(n log k) rather than O(n log n).
//
// It sits next to BenchmarkSort deliberately — the pair is the measurement, not
// either number alone.
func BenchmarkTopK(b *testing.B) {
	src := benchFrame(b, benchRows/16, benchBatch)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if _, err := ursus.Scan(src).
			Sort(ursus.Desc(ursus.Col("price"))).
			Head(100).
			Collect(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSortSpilling is the same sort under a budget far below the input, so
// it goes to disk and merges back. The interesting number is the RATIO to
// BenchmarkSort: what bounded memory costs when it is not needed.
func BenchmarkSortSpilling(b *testing.B) {
	src := benchFrame(b, benchRows/16, benchBatch)
	dir := b.TempDir()
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if err := ursus.Scan(src).
			Sort(ursus.Desc(ursus.Col("price"))).
			SinkCSV(b.Context(), dir+"/out.csv",
				ursus.WithMemoryLimit(4<<20),
				ursus.WithSpillDir(dir)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGroupByHighCardinality is BenchmarkGroupBy with a key that is unique
// per row rather than one of four regions.
//
// Four regions is O(1) groups, so BenchmarkGroupBy measures the accumulator inner
// loop and nothing else — it would show the same number whether or not the group
// table could spill. This one measures the group table.
//
// Through SinkCSV rather than Collect, so the two spilling variants below can be
// compared against it without the harness holding every group.
func BenchmarkGroupByHighCardinality(b *testing.B) {
	src := benchFrame(b, benchRows/16, benchBatch)
	dir := b.TempDir()
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if err := ursus.Scan(src).
			GroupBy(ursus.Col("id")).
			Agg(ursus.Col("price").Sum().Alias("total"), ursus.Len().Alias("n")).
			SinkCSV(b.Context(), dir+"/out.csv"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGroupBySpilling is the same aggregation under a budget far below its
// group table, so the keys are radix-partitioned to disk and replayed.
//
// The interesting number is the RATIO to BenchmarkGroupByHighCardinality — the
// pair is the measurement, exactly as BenchmarkSortSpilling beside BenchmarkSort
// was. It is what decides whether anyone turns the limit on.
func BenchmarkGroupBySpilling(b *testing.B) {
	src := benchFrame(b, benchRows/16, benchBatch)
	dir := b.TempDir()
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if err := ursus.Scan(src).
			GroupBy(ursus.Col("id")).
			Agg(ursus.Col("price").Sum().Alias("total"), ursus.Len().Alias("n")).
			SinkCSV(b.Context(), dir+"/out.csv",
				ursus.WithMemoryLimit(1<<20),
				ursus.WithSpillDir(dir)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGroupBySpillingOrdered turns MaintainOrder's documented cost — which
// nodes_agg.go states as "it costs time and memory", with no number — into a
// figure.
//
// Two costs, and the benchmark shows their sum: an int64 ordinal column written
// into every spill partition, and a materialise-plus-sort of the whole result at
// the end, because first appearance interleaves resident and spilled groups and
// therefore cannot stream.
func BenchmarkGroupBySpillingOrdered(b *testing.B) {
	src := benchFrame(b, benchRows/16, benchBatch)
	dir := b.TempDir()
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if err := ursus.Scan(src).
			GroupBy(ursus.Col("id")).MaintainOrder().
			Agg(ursus.Col("price").Sum().Alias("total"), ursus.Len().Alias("n")).
			SinkCSV(b.Context(), dir+"/out.csv",
				ursus.WithMemoryLimit(1<<20),
				ursus.WithSpillDir(dir)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInnerJoin(b *testing.B) {
	left := benchFrame(b, benchRows/16, benchBatch)
	right := benchFrame(b, benchRows/64, benchBatch)
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if _, err := ursus.Scan(left).
			Join(ursus.Scan(right), ursus.JoinOn(ursus.Col("id"))).
			Count(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJoinSpilling is BenchmarkInnerJoin under a budget far below its build
// side, so the keys are partitioned to disk and the buckets replayed.
//
// The interesting number is the RATIO to BenchmarkInnerJoin — the pair is the
// measurement, exactly as BenchmarkSortSpilling beside BenchmarkSort and
// BenchmarkGroupBySpilling beside BenchmarkGroupByHighCardinality. It is what
// decides whether anyone turns the limit on.
//
// Through Count rather than Collect: a join's output can be larger than either
// input, and holding it would measure the harness.
func BenchmarkJoinSpilling(b *testing.B) {
	left := benchFrame(b, benchRows/16, benchBatch)
	right := benchFrame(b, benchRows/64, benchBatch)
	dir := b.TempDir()
	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		if _, err := ursus.Scan(left).
			Join(ursus.Scan(right), ursus.JoinOn(ursus.Col("id"))).
			Count(b.Context(),
				ursus.WithMemoryLimit(256<<10),
				ursus.WithSpillDir(dir)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPipelineThreads is the number the whole step rests on.
//
// Reported as a sub-benchmark per thread count so `benchstat` can compare them
// directly, and so a claim of "N times faster" is a measurement rather than an
// assertion.
func BenchmarkPipelineThreads(b *testing.B) {
	src := benchFrame(b, benchRows, benchBatch)

	for _, threads := range []int{1, 2, 4, 8} {
		b.Run("threads="+strconv.Itoa(threads), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ursus.Scan(src).
					Filter(ursus.Col("qty").Gt(50)).
					WithColumns(ursus.Col("price").Mul(ursus.Col("qty")).Alias("total")).
					Select(ursus.Col("id"), ursus.Col("total")).
					Count(b.Context(), ursus.WithThreads(threads)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkGroupByThreads shows what parallelising ONLY the pipeline buys on a
// query whose cost is dominated by a serial breaker. A small improvement here is
// the expected and honest result, not a disappointment.
func BenchmarkGroupByThreads(b *testing.B) {
	src := benchFrame(b, benchRows/2, benchBatch)

	for _, threads := range []int{1, 8} {
		b.Run("threads="+strconv.Itoa(threads), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ursus.Scan(src).
					Filter(ursus.Col("qty").Gt(10)).
					GroupBy(ursus.Col("region")).
					Agg(ursus.Col("price").Sum().Alias("t")).
					Collect(b.Context(), ursus.WithThreads(threads)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
