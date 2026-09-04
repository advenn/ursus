package ursus_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source/memsrc"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// allNineteen builds one expression per AggOp.
//
// Nineteen is the whole enum, and the list is exhaustive on purpose: a replay sink
// that got any one of them wrong would be wrong only for SPILLED groups, so a
// spot-check on a resident group would pass.
func allNineteen() []ursus.Expr {
	flag := ursus.Col("opt").Gt(int64(4))
	return []ursus.Expr{
		ursus.Col("seq").Sum().Alias("sum"),
		ursus.Col("opt").Mean().Alias("mean"),
		ursus.Col("seq").Min().Alias("min"),
		ursus.Col("seq").Max().Alias("max"),
		ursus.Col("opt").Count().Alias("count"),
		ursus.Len().Alias("len"),
		ursus.Col("pad").NUnique().Alias("nunique"),
		ursus.Col("seq").First().Alias("first"),
		ursus.Col("seq").Last().Alias("last"),
		ursus.Col("opt").NullCount().Alias("nullcount"),
		flag.Any().Alias("any"),
		flag.AllTrue().Alias("all"),
		ursus.Col("opt").Product().Alias("product"),
		ursus.Col("seq").ArgMin().Alias("argmin"),
		ursus.Col("seq").ArgMax().Alias("argmax"),
		ursus.Col("opt").Var(1).Alias("var"),
		ursus.Col("opt").Std(1).Alias("std"),
		ursus.Col("opt").Median().Alias("median"),
		ursus.Col("opt").Quantile(0.9, ursus.InterpLinear).Alias("q90"),
	}
}

// TestSpillingHashAggMatchesInMemory is the point of the step: the same answer
// whether or not the aggregation went to disk.
//
// Compared with AssertFrameEqual, NOT df.String() — String truncates at ten rows,
// so a string comparison over thousands of groups would be checking ten of them
// and calling it equality.
//
// Floats are compared EXACTLY, with no tolerance. The never-split invariant means
// each group's values reach its accumulator in identical order spilled or not, so
// Sum, Mean, Var, Std and Product are bit-identical. A test that needed a tolerance
// to pass would have found the bug rather than worked around it.
func TestSpillingHashAggMatchesInMemory(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(allNineteen()...)
	}

	var unbounded ursus.MemoryStats
	want, err := q().Collect(t.Context(), ursus.WithBatchSize(512),
		ursus.WithVerify(), ursus.WithMemoryStats(&unbounded))
	if err != nil {
		t.Fatal(err)
	}
	if unbounded.Spills != 0 {
		t.Fatalf("an unbounded aggregation spilled %d times", unbounded.Spills)
	}
	// The group count must be far beyond any plausible resident set, or the test
	// silently degrades into "everything stayed in memory".
	if want.Height() < 1000 {
		t.Fatalf("%d groups is too few for a spill to mean anything", want.Height())
	}

	// The smallest limit is 64 KiB, not lower, and the reason is the point of
	// TestSpilledQuantileStillRefuses: median and q90 keep every value of every
	// RESIDENT group, so the post-freeze growth is O(rows in the resident set). Below
	// ~64 KiB this list refuses — correctly — and the refusal is covered there.
	for _, limit := range []int64{64 << 10, 256 << 10, 1 << 20} {
		var stats ursus.MemoryStats
		got, err := q().Collect(t.Context(),
			ursus.WithBatchSize(512), ursus.WithVerify(),
			ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		// Two, not one: a single partition cannot catch a bug that only shows when
		// two sub-aggregations have to agree — the same reason the accumulator merge
		// test folds in both directions.
		if stats.Spills < 2 {
			t.Fatalf("limit=%d spilled %d times, so it proves nothing", limit, stats.Spills)
		}
		ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
	}
}

// TestSpilledAggregateParametersSurvive: a replay sink that rebuilt its
// accumulators from the op alone would turn every Quantile(0.9) into a median and
// every Var(1) into a population variance — for SPILLED groups only, leaving
// resident groups right. Step 7 and step 11 each shipped that shape once.
func TestSpilledAggregateParametersSurvive(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupBy(ursus.Col("k")).MaintainOrder().Agg(
			ursus.Col("opt").Quantile(0.5, ursus.InterpLinear).Alias("p50"),
			ursus.Col("opt").Quantile(0.9, ursus.InterpLinear).Alias("p90"),
			ursus.Col("opt").Var(0).Alias("pop"),
			ursus.Col("opt").Var(1).Alias("samp"),
		)
	}
	want, err := q().Collect(t.Context(), ursus.WithBatchSize(512))
	if err != nil {
		t.Fatal(err)
	}
	var stats ursus.MemoryStats
	got, err := q().Collect(t.Context(), ursus.WithBatchSize(512),
		ursus.WithMemoryLimit(64<<10), ursus.WithSpillDir(t.TempDir()),
		ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills < 2 {
		t.Fatalf("spilled %d times", stats.Spills)
	}
	ursustest.AssertFrameEqual(t, got, want)

	// And the parameters actually differ somewhere, or a sink that ignored them
	// entirely would still pass the comparison above.
	differed := false
	for row := range got.Height() {
		a, aok, _ := got.At[float64](row, "p50")
		b, bok, _ := got.At[float64](row, "p90")
		if aok && bok && a != b {
			differed = true
			break
		}
	}
	if !differed {
		t.Error("no group had a different p50 and p90, so the parameters proved nothing")
	}
}

// TestSpilledGroupKeysUseGroupingEquality: the partitioner hashes the ENCODED key,
// which is the single authority on grouping equality — NaN groups with NaN and
// -0.0 with +0.0. Hashing raw bits instead would send the two NaNs to different
// partitions, aggregate them separately, and emit TWO rows where one belongs, each
// with a plausible sub-total and no error anywhere.
func TestSpilledGroupKeysUseGroupingEquality(t *testing.T) {
	const rows, batch = 8000, 256
	schema, err := dtype.NewSchema(dtype.NotNull("k", dtype.Float64))
	if err != nil {
		t.Fatal(err)
	}
	// Built as a MULTI-BATCH source rather than a Frame literal. The freeze is a
	// batch-boundary event, so a source that hands over its whole input in one batch
	// admits every key before it can freeze and never routes a row — the query would
	// pass while exercising nothing.
	var batches []*data.Batch
	for start := 0; start < rows; start += batch {
		n := min(batch, rows-start)
		vals := make([]float64, n)
		for i := range n {
			v := start + i
			// The special keys FIRST APPEAR IN THE SECOND HALF, which is the whole
			// point of the fixture. Residency is decided at first appearance, so a NaN
			// in row 0 is resident for the query's whole life and never reaches the
			// partitioner at all — an earlier version of this test scattered them from
			// row 0 and PASSED with the partitioner hashing raw bits, because the case
			// it claims to cover never ran.
			if v < rows/2 {
				vals[i] = float64((v*7919)%3000) + 4000
				continue
			}
			switch v % 4 {
			case 0:
				vals[i] = math.NaN()
			case 1:
				vals[i] = math.Copysign(0, -1)
			case 2:
				vals[i] = 0
			default:
				vals[i] = float64((v * 7919) % 3000)
			}
		}
		b, err := data.NewBatch(schema, []*data.Column{
			data.NewFixed("k", dtype.Float64, vals, bitmap.AllSet(n)),
		})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	src, err := memsrc.New(schema, batches...)
	if err != nil {
		t.Fatal(err)
	}

	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n"))
	}
	want, err := q().Collect(t.Context(), ursus.WithBatchSize(batch))
	if err != nil {
		t.Fatal(err)
	}
	// Grouping equality already collapsed the specials in the UNSPILLED answer: one
	// NaN group and one zero group, not one per bit pattern.
	nan, zero := groupSize(t, want, math.NaN()), groupSize(t, want, 0)
	if nan == 0 || zero == 0 {
		t.Fatalf("the fixture produced no NaN group (%d) or no zero group (%d)", nan, zero)
	}
	if zero != rows/8*2 {
		t.Fatalf("the zero group holds %d rows, so -0.0 and +0.0 did not collapse", zero)
	}

	var stats ursus.MemoryStats
	got, err := q().Collect(t.Context(), ursus.WithBatchSize(batch),
		ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(t.TempDir()),
		ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills < 2 {
		t.Fatalf("spilled %d times", stats.Spills)
	}
	if got.Height() != want.Height() {
		t.Fatalf("spilled group count = %d, want %d — a key landed in two partitions",
			got.Height(), want.Height())
	}
	// Named directly as well as through the frame comparison, so a failure says which
	// key split rather than only that some row differs.
	if n := groupSize(t, got, math.NaN()); n != nan {
		t.Errorf("the spilled NaN group holds %d rows, want %d — the two NaN spellings "+
			"were partitioned apart", n, nan)
	}
	if n := groupSize(t, got, 0); n != zero {
		t.Errorf("the spilled zero group holds %d rows, want %d — -0.0 and +0.0 were "+
			"partitioned apart", n, zero)
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
}

// groupSize totals the `n` column over every row whose key MATCHES BY GROUPING
// EQUALITY — NaN matches NaN, and -0.0 matches +0.0. Summing rather than reading
// one row is what makes a split key visible: two half-groups sum to the whole.
func groupSize(t testing.TB, df *ursus.DataFrame, key float64) uint64 {
	t.Helper()
	var total uint64
	for row := range df.Height() {
		k, ok, err := df.At[float64](row, "k")
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			continue
		}
		if k == key || (math.IsNaN(k) && math.IsNaN(key)) {
			n, _, err := df.At[uint64](row, "n")
			if err != nil {
				t.Fatal(err)
			}
			total += n
		}
	}
	return total
}

// TestGroupBySpillIsDeterministic: two runs of one query at one limit must be
// byte-identical. A hash seeded per process would break this and nothing else.
func TestGroupBySpillIsDeterministic(t *testing.T) {
	src := memFrame(t, 8000, 256, 2000)
	run := func() string {
		df, err := ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n")).
			Collect(t.Context(), ursus.WithBatchSize(256),
				ursus.WithMemoryLimit(32<<10), ursus.WithSpillDir(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		ks := int64Col(t, df, "k")
		var b strings.Builder
		for _, k := range ks {
			b.WriteString(strings.Repeat(" ", 0))
			b.WriteString(itoa64(k))
			b.WriteByte(',')
		}
		return b.String()
	}
	if a, b := run(), run(); a != b {
		t.Error("two runs of the same query at the same limit gave different group orders")
	}
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestGroupBySpillActuallyHappens: the query succeeding proves nothing. This
// asserts the counter AND that files existed on disk during the run, which the
// counter alone cannot show.
func TestGroupBySpillActuallyHappens(t *testing.T) {
	dir := t.TempDir()
	src := memFrame(t, 20000, 512, 4000)

	sawFiles := false
	for range ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n")).
		CollectBatches(t.Context(), ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(dir)) {
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(es) > 0 {
			sawFiles = true
		}
	}
	if !sawFiles {
		t.Error("no spill directory existed while the query ran")
	}
}

// TestSpillingHashAggIsBounded is the deliverable, as a measurement.
//
// Through SinkParquet rather than Collect: an aggregation's result is one row per
// group, and Collect would hold every one of them, which is exactly the thing the
// measurement is trying to exclude.
func TestSpillingHashAggIsBounded(t *testing.T) {
	src := memFrame(t, 40000, 512, 20000)
	out := filepath.Join(t.TempDir(), "agg.parquet")
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(
			// Sum, not Mean. An integer Sum outputs Int128, which SinkParquet refused
			// until step 14 — so this test had to measure a different aggregate than
			// the one a user would write. It measures the real one now.
			ursus.Col("seq").Sum().Alias("s"), ursus.Len().Alias("n"))
	}

	var unbounded ursus.MemoryStats
	if err := q().SinkParquet(t.Context(), out,
		ursus.WithBatchSize(512), ursus.WithMemoryStats(&unbounded)); err != nil {
		t.Fatal(err)
	}

	const limit = 128 << 10
	var stats ursus.MemoryStats
	if err := q().SinkParquet(t.Context(), out,
		ursus.WithBatchSize(512), ursus.WithMemoryLimit(limit),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats)); err != nil {
		t.Fatal(err)
	}

	if stats.Spills == 0 {
		t.Fatal("never spilled, so boundedness was not exercised")
	}
	if unbounded.Peak <= limit {
		t.Fatalf("the unbounded aggregation held only %d bytes, so a %d limit proves "+
			"nothing — the fixture is too small", unbounded.Peak, limit)
	}
	// The peak is NOT the limit, and pretending otherwise would measure the wrong
	// thing. Three terms overshoot it, all bounded: one batch (the budget is checked
	// after retention), the resident set at the moment of the freeze, and sixteen
	// partition write buffers. What is being claimed is "far below what holding
	// every group costs" — which is why the unbounded baseline above is asserted
	// first rather than assumed.
	if stats.Peak > unbounded.Peak/2 {
		t.Errorf("peak = %d against an unbounded peak of %d — the limit bought little",
			stats.Peak, unbounded.Peak)
	}

	df, err := ursus.ScanParquet(out).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() < 1000 {
		t.Errorf("wrote %d groups", df.Height())
	}
}

// TestGroupByOrderIsUnspecifiedWithoutMaintainOrder pins that the unordered path
// genuinely reorders — the negative assertion that stops the MaintainOrder test
// below from proving nothing.
//
// Asserting a difference is legitimate here precisely because the contract says
// the order is unspecified: plan.Aggregate's doc has said so since step 2, and
// this is the first step in which it is true.
func TestGroupByOrderIsUnspecifiedWithoutMaintainOrder(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n"))
	}
	want, err := q().Collect(t.Context(), ursus.WithBatchSize(512))
	if err != nil {
		t.Fatal(err)
	}
	got, err := q().Collect(t.Context(), ursus.WithBatchSize(512),
		ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
	if slices.Equal(int64Col(t, got, "k"), int64Col(t, want, "k")) {
		t.Fatal("the spilled and unspilled orders matched, so the MaintainOrder test " +
			"proves nothing")
	}
}

// TestGroupByMaintainOrderSurvivesSpilling: the exact first-appearance sequence,
// at four limits.
//
// On `k`, which is scattered by a multiplier coprime with its cardinality — NOT on
// `seq`, which is monotone and would pass even if the restoration sorted by key
// value instead of by first appearance.
func TestGroupByMaintainOrderSurvivesSpilling(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupBy(ursus.Col("k")).MaintainOrder().
			Agg(ursus.Col("seq").Sum().Alias("s"), ursus.Len().Alias("n"))
	}
	want, err := q().Collect(t.Context(), ursus.WithBatchSize(512))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture must not be accidentally sorted, or the test cannot distinguish
	// first-appearance order from key order.
	if slices.IsSorted(int64Col(t, want, "k")) {
		t.Fatal("the first-appearance order happens to be sorted; the fixture cannot " +
			"tell the two apart")
	}

	// 2 KiB is the DEPTH-2 case, and it is what pins the induction rather than the
	// base case: one level of 16-way partitioning leaves each bucket with more keys
	// than the budget holds, so a sub-sink spills its own sub-partitions. The
	// ordinals it restores come from the level-0 sink, two replays away.
	for _, limit := range []int64{2 << 10, 16 << 10, 64 << 10, 512 << 10, 0} {
		var stats ursus.MemoryStats
		got, err := q().Collect(t.Context(), ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		// More partition files than one level can open is the only public evidence
		// that the recursion ran at all.
		if limit == 2<<10 && stats.Spills <= 16 {
			t.Errorf("limit=2KiB opened %d partitions, so it never recursed and the "+
				"depth-2 case is not being exercised", stats.Spills)
		}
		if !slices.Equal(int64Col(t, got, "k"), int64Col(t, want, "k")) {
			t.Errorf("limit=%d: the group sequence changed", limit)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}

	// The composition the driver-order list does not name: Head after a group-by
	// takes the FIRST n groups, which is only meaningful under MaintainOrder.
	head, err := ursus.Scan(src).GroupBy(ursus.Col("k")).MaintainOrder().
		Agg(ursus.Len().Alias("n")).Head(10).
		Collect(t.Context(), ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(int64Col(t, head, "k"), int64Col(t, want, "k")[:10]) {
		t.Error("Head after an ordered group-by did not take the first ten groups")
	}
}

// TestGroupBySpillRecursesOnSkew: a partition that still does not fit is
// re-partitioned, and the DEPTH IS MIXED INTO THE HASH rather than selecting a
// different slice of one hash's bits.
//
// That is termination, not quality. Every key inside a partition already agrees in
// the bits the parent used, so re-hashing with the same function maps the whole
// partition onto one sub-partition, the recursion never narrows, and the query dies
// at maxSpillDepth on data that is not skewed at all.
//
// No other test reaches depth 2 — every limit large enough for the nineteen
// aggregates leaves each level-1 bucket small enough to fit — so without this the
// per-level seed is unexercised and the recursion is a base case with a comment.
func TestGroupBySpillRecursesOnSkew(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)
	q := func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupBy(ursus.Col("k")).
			Agg(ursus.Col("seq").Min().Alias("lo"), ursus.Len().Alias("n"))
	}
	want, err := q().Collect(t.Context(), ursus.WithBatchSize(512))
	if err != nil {
		t.Fatal(err)
	}

	var stats ursus.MemoryStats
	got, err := q().Collect(t.Context(), ursus.WithBatchSize(512),
		ursus.WithMemoryLimit(2<<10), ursus.WithSpillDir(t.TempDir()),
		ursus.WithMemoryStats(&stats))
	if err != nil {
		t.Fatal(err)
	}
	// One level opens at most sixteen files. Anything beyond that is a sub-sink
	// spilling its own partitions, which is the only public evidence the recursion
	// ran.
	if stats.Spills <= 16 {
		t.Fatalf("opened %d partitions, so nothing recursed", stats.Spills)
	}
	if got.Height() != want.Height() {
		t.Fatalf("%d groups after recursion, want %d", got.Height(), want.Height())
	}
	ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
}

// TestSpilledQuantileStillRefuses is the pair that tells the whole story: the
// freeze bounds CARDINALITY, never per-key state.
func TestSpilledQuantileStillRefuses(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)

	// Five groups over twenty thousand rows: every key is resident, nothing routes,
	// and a quantile keeps every value of every group.
	_, err := ursus.Scan(src).GroupBy(ursus.Col("grp")).
		Agg(ursus.Col("seq").Quantile(0.5, ursus.InterpLinear)).
		Collect(t.Context(), ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(t.TempDir()))
	if err == nil {
		t.Fatal("a quantile over five hot keys must still refuse")
	}
	if !errors.Is(err, uerr.ErrResource) {
		t.Fatalf("want a resource error, got %v", err)
	}
	for _, want := range []string{"group_by", "quantile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should name %q:\n%v", want, err)
		}
	}

	// The same key with a BOUNDED aggregate succeeds, and does not spill: five keys
	// fit, so there is nothing to partition.
	var stats ursus.MemoryStats
	if _, err := ursus.Scan(src).GroupBy(ursus.Col("grp")).
		Agg(ursus.Col("seq").Sum().Alias("s")).
		Collect(t.Context(), ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&stats)); err != nil {
		t.Fatalf("a bounded aggregate over five keys must fit: %v", err)
	}
	if stats.Spills != 0 {
		t.Errorf("five keys spilled %d times; there was nothing to partition",
			stats.Spills)
	}

	// And spilling DOES rescue a quantile when the cardinality is what is large:
	// each replayed partition's quantile state is bounded by that partition.
	if _, err := ursus.Scan(src).GroupBy(ursus.Col("k")).
		Agg(ursus.Col("opt").Quantile(0.5, ursus.InterpLinear).Alias("p")).
		Collect(t.Context(), ursus.WithBatchSize(512),
			ursus.WithMemoryLimit(256<<10), ursus.WithSpillDir(t.TempDir())); err != nil {
		t.Errorf("a quantile over many small groups should spill rather than refuse: %v", err)
	}
}

// TestGroupBySpillFilesAreCleanedUp on every exit path.
func TestGroupBySpillFilesAreCleanedUp(t *testing.T) {
	entries := func(dir string) int {
		t.Helper()
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return len(es)
	}
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		src := memFrame(t, 20000, 512, 4000)
		var stats ursus.MemoryStats
		if _, err := ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n")).
			Collect(t.Context(), ursus.WithBatchSize(512),
				ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(dir),
				ursus.WithMemoryStats(&stats)); err != nil {
			t.Fatal(err)
		}
		if stats.Spills == 0 {
			t.Fatal("never spilled")
		}
		if n := entries(dir); n != 0 {
			t.Errorf("%d entries left after success", n)
		}
	})

	// Two break points, not one. The resident groups are emitted FIRST, so breaking
	// on batch 0 leaves every partition file written but unvisited and no reader
	// open; breaking later lands inside a replay, with a spill reader and a live
	// sub-aggregation to unwind. The sort has only the second shape.
	for _, after := range []int{1, 6} {
		t.Run("early break after "+itoa64(int64(after)), func(t *testing.T) {
			dir := t.TempDir()
			src := memFrame(t, 20000, 512, 4000)
			seen := 0
			for range ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n")).
				CollectBatches(t.Context(), ursus.WithBatchSize(512),
					ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(dir)) {
				if seen++; seen >= after {
					break
				}
			}
			if seen < after {
				t.Fatalf("the query produced %d batches, so it never reached the break "+
					"point this case is named for", seen)
			}
			if n := entries(dir); n != 0 {
				t.Errorf("%d entries left after an early break", n)
			}
		})
	}

	t.Run("cancelled", func(t *testing.T) {
		dir := t.TempDir()
		src := memFrame(t, 20000, 512, 4000)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		n := 0
		for range ursus.Scan(src).GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n")).
			CollectBatches(ctx, ursus.WithBatchSize(512),
				ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(dir)) {
			n++
			cancel()
		}
		if got := entries(dir); got != 0 {
			t.Errorf("%d entries left after cancellation", got)
		}
	})

	t.Run("error", func(t *testing.T) {
		dir := t.TempDir()
		src := memFrame(t, 20000, 512, 4000)
		// A quantile over five hot keys refuses partway through, after partitions
		// for the high-cardinality key have been written.
		_, _ = ursus.Scan(src).GroupBy(ursus.Col("k")).
			Agg(ursus.Col("opt").Quantile(0.5, ursus.InterpLinear)).
			Collect(t.Context(), ursus.WithBatchSize(512),
				ursus.WithMemoryLimit(8<<10), ursus.WithSpillDir(dir))
		if n := entries(dir); n != 0 {
			t.Errorf("%d entries left after an error", n)
		}
	})
}

// TestDistinctIsAccounted: the seventh buffering operator, which had no account at
// all — its seen-set grew with distinct rows and was held across batches, exactly
// like the group table beside it, and neither spilled nor failed.
func TestDistinctIsAccounted(t *testing.T) {
	src := memFrame(t, 20000, 512, 20000)
	_, err := ursus.Scan(src).Unique("k").
		Collect(t.Context(), ursus.WithBatchSize(512), ursus.WithMemoryLimit(4<<10))
	if err == nil {
		t.Fatal("20,000 distinct keys against a 4KiB limit was accepted")
	}
	if !errors.Is(err, uerr.ErrResource) {
		t.Fatalf("want a resource error, got %v", err)
	}
	if !strings.Contains(err.Error(), "unique") {
		t.Errorf("the error must name the operator: %v", err)
	}
}

// TestZeroColumnBatchKeepsItsRows: a frame can legitimately have rows and no
// columns. The sort's gather inferred the row count from the columns and answered
// zero, so this lost every row.
func TestZeroColumnBatchKeepsItsRows(t *testing.T) {
	f := ursus.Frame(ursus.Values("v", []int64{3, 1, 2}))
	df, err := f.Select().Sort(ursus.Asc(ursus.Lit(int64(1)))).
		Collect(t.Context(), ursus.WithBatchSize(1))
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 {
		t.Errorf("height = %d, want 3", df.Height())
	}
	if df.Width() != 0 {
		t.Errorf("width = %d, want 0", df.Width())
	}
}
