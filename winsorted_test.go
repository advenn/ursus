package ursus_test

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// sortedFrame is n rows for windows in orders of their own: v an integer with runs of
// nulls longer than two batches, f a float with nulls, NaN, −0 and +0 among few values, x
// an integer whose value mod 7 ties across every batch, s a string, k a key that is
// 0 for the first 12,000 rows and one of 500 others after.
func sortedFrame(chunks, rows int) *ursus.LazyFrame {
	var frames []*ursus.LazyFrame
	for ch := range chunks {
		k, v, x := make([]int64, rows), make([]int64, rows), make([]int64, rows)
		vok, fok := make([]bool, rows), make([]bool, rows)
		f := make([]float64, rows)
		s := make([]string, rows)
		for j := range rows {
			i := ch*rows + j
			if i >= 12_000 && i%2 == 1 {
				k[j] = int64(1 + i%500)
			}
			v[j], vok[j] = int64(i%977)-400, i%1000 >= 130 && i%7 != 0
			x[j] = int64(i * 7919 % 100_003)
			f[j] = []float64{1.5, math.NaN(), math.Copysign(0, -1), 0, -2, 3.25}[i*31%6]
			fok[j] = i%11 != 0
			s[j] = fmt.Sprintf("s%03d", i*13%211)
		}
		frames = append(frames, ursus.Frame(ursus.Values("k", k), ursus.ValuesNullable("v", v, vok),
			ursus.Values("x", x), ursus.ValuesNullable("f", f, fok), ursus.Values("s", s)))
	}
	return ursus.Concat(frames)
}

// sortedWindows is four distinct orders, two of them the same keys in opposite
// directions, a function in the file's order and an aggregate, partitioned by part.
func sortedWindows(part ...ursus.Expr) []ursus.Expr {
	c := ursus.Col
	byF := ursus.WindowSpec{PartitionBy: part,
		OrderBy: []ursus.SortKey{ursus.Desc(c("f")).NullsLast(), ursus.Asc(c("x"))}}
	byFAsc := ursus.WindowSpec{PartitionBy: part,
		OrderBy: []ursus.SortKey{ursus.Asc(c("f")), ursus.Asc(c("x"))}}
	byFDesc := ursus.WindowSpec{PartitionBy: part,
		OrderBy: []ursus.SortKey{ursus.Desc(c("f")), ursus.Asc(c("x"))}}
	byX := ursus.WindowSpec{PartitionBy: part, OrderBy: []ursus.SortKey{ursus.Asc(c("x").Mod(7))}}
	return []ursus.Expr{
		c("v").CumSum(false).OverWith(byF).Alias("cs_f"),
		c("v").Shift(2).OverWith(byF).Alias("lag_f"),
		c("v").CumCount(false).OverWith(byF).Alias("cc_f"),
		c("v").ForwardFill(0).OverWith(byF).Alias("ff_f"),
		c("f").RollingMean(4, ursus.MinSamples(1)).OverWith(byF).Alias("rm_f"),
		c("s").CumMax(false).OverWith(byF).Alias("smax_f"),
		c("x").EwmMean(ursus.EwmAlpha(0.3)).OverWith(byF).Alias("ewm_f"),
		c("v").CumSum(false).OverWith(byFAsc).Alias("cs_fasc"),
		c("v").CumSum(false).OverWith(byFDesc).Alias("cs_fdesc"),
		c("v").CumSum(false).OverWith(byX).Alias("cs_x"),
		c("v").ShiftFill(-3, int64(-1)).OverWith(byX).Alias("lead_x"),
		c("v").CumCount(true).OverWith(byX).Alias("ccr_x"),
		c("v").CumSum(false).Over(part...).Alias("cs_file"),
		// Backward (step 236): in the file's order, and in an order of their own.
		c("v").CumSum(true).Over(part...).Alias("csr_file"),
		c("s").CumMin(true).Over(part...).Alias("sminr_file"),
		c("v").BackwardFill(0).Over(part...).Alias("bf_file"),
		c("v").CumProd(true).OverWith(byX).Alias("cpr_x"),
		c("x").CumMax(true).OverWith(byF).Alias("cmaxr_f"),
		c("v").BackwardFill(0).OverWith(byF).Alias("bf_f"),
		// Rank, in its own order (step 237), whatever the window's order_by.
		c("f").Rank(ursus.RankOrdinal, false).OverWith(byX).Alias("r_ord"),
		c("f").Rank(ursus.RankDense, true).Over(part...).Alias("r_dense"),
		c("v").Rank(ursus.RankMin, false).Over(part...).Alias("r_min"),
		c("v").Rank(ursus.RankMax, true).Over(part...).Alias("r_max"),
		c("f").Rank(ursus.RankAverage, false).Over(part...).Alias("r_avg"),
		c("s").Rank(ursus.RankMax, false).Over(part...).Alias("r_smax"),
		// Interpolation (step 238), linear and nearest, with an order or none, over
		// runs of nulls longer than two batches and nulls before the first value.
		c("v").Interpolate().Over(part...).Alias("ip_lin"),
		c("v").Interpolate(ursus.InterpolateNearest()).Over(part...).Alias("ip_near"),
		c("f").Cast(ursus.Float32).Interpolate().OverWith(byX).Alias("ip_f32"),
		c("v").Interpolate(ursus.InterpolateNearest()).OverWith(byF).Alias("ip_near_f"),
		c("x").Sum().Over(part...).Alias("total"),
	}
}

// TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit: windows with an order_by over one
// huge partition (step 235), with no partition_by and over one hot key, where they
// refused. Four orders — a float descending with nulls last, NaN and −0 among its few
// values and an integer to break its ties; the same ascending, nulls first, and
// descending, nulls first; and x mod 7, an expression whose seven values tie across
// every batch — a function in the file's order, and an aggregate.
// Cumulative sums, a shift, cum_count either way, a forward fill over runs of nulls
// longer than two batches, a rolling mean, a cumulative max of strings, an ewm and a
// shift forward with a fill. And, walked backward (step 236), reversed cumulative
// sums, minima of strings, products and maxima, and backward fills with no limit,
// over runs of nulls longer than two batches, in the file's order and in an order of
// their own. And rank (step 237), every method, both directions, over a float with
// nulls, NaN and ±0, an integer with long runs of nulls, and strings with many ties.
// Under a tenth of the input, on one thread and four, the answer is the in-memory
// one, row for row, and the spill directory is empty after. And interpolation (step
// 238), linear and nearest, of an integer and a Float32, in the file's order and in
// orders of their own.
func TestAWindowInAnOrderOfItsOwnStreamsPastItsLimit(t *testing.T) {
	frame := sortedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	shapes := map[string]*ursus.LazyFrame{
		"no partition_by": frame.WithColumns(sortedWindows()...),
		"one hot key":     frame.WithColumns(sortedWindows(c("k"))...),
	}
	threadCounts := []int{1, 4}
	if raceDetector {
		// Under the race detector this takes a minute and a half, and the root
		// package's runs have ten. The hot key on four threads drives the sorted pass,
		// the hot key's split and the parallel scan; the rest runs without it.
		delete(shapes, "no partition_by")
		threadCounts = []int{4}
	}
	for name, q := range shapes {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
		if err != nil {
			t.Fatal(err)
		}
		for _, threads := range threadCounts {
			var stats ursus.MemoryStats
			dir := t.TempDir()
			got, err := q.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithThreads(threads),
				ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
			if err != nil {
				t.Fatalf("%s on %d threads, under %d bytes: %v", name, threads, limit, err)
			}
			if stats.Spills == 0 {
				t.Fatalf("%s on %d threads: nothing spilled", name, threads)
			}
			ursustest.AssertFrameEqual(t, got, want)
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("%s on %d threads: %d entries left in the spill directory", name, threads, len(left))
			}
		}
	}
}

// TestASortedPassStaysNearItsLimit: a window with no partition_by that ranks,
// interpolates and sums in an order of its own, under a tenth of its input, peaks
// within twice the limit, and leaves no file behind after an early break (step 239).
// In batches of 64 rows: a merge holds a batch of each of up to sixteen runs a level,
// and the bound is for a limit well above sixteen batches, as a real one is. At
// 8,192-row batches of a few narrow columns that is a few megabytes.
func TestASortedPassStaysNearItsLimit(t *testing.T) {
	frame := sortedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	q := frame.WithColumns(
		c("f").Rank(ursus.RankAverage, false).Over().Alias("r"),
		c("v").Interpolate().Over().Alias("ip"),
		c("v").CumSum(false).OverWith(ursus.WindowSpec{OrderBy: []ursus.SortKey{ursus.Asc(c("x"))}}).Alias("cs"),
	)
	var stats ursus.MemoryStats
	if _, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(limit),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats)); err != nil {
		t.Fatal(err)
	}
	t.Logf("limit %d, peak %d (%.2fx), spills %d", limit, stats.Peak, float64(stats.Peak)/float64(limit), stats.Spills)
	if stats.Peak > 2*limit {
		t.Errorf("peaked at %d, over twice the limit of %d", stats.Peak, limit)
	}
	if stats.Spills <= sortFanInForTest {
		t.Errorf("%d spills: too few for a sort to merge its runs a level up", stats.Spills)
	}
	// Its sorts spill no run under a sixteenth of the limit: 135 files here, where a
	// run at every batch the query is over made 389.
	if stats.Spills > 250 {
		t.Errorf("%d spills: the pass's sorts spill runs smaller than their minimum", stats.Spills)
	}

	// Stopped after its first batch, it leaves nothing behind.
	dir := t.TempDir()
	for _, err := range q.CollectBatches(t.Context(), ursus.WithBatchSize(64),
		ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir)) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if left, _ := os.ReadDir(dir); len(left) > 0 {
		t.Errorf("%d entries left in the spill directory after an early break", len(left))
	}
}

// sortFanInForTest is the sort's fan-in, physical.sortFanIn.
const sortFanInForTest = 16

// TestAWindowWithNoPartitionBesideAKeyedOneStreams: windows with no partition_by
// beside keyed ones (step 240), where they refused: a cumulative sum in the file's
// order, a rank, an interpolation in an order of its own, and since step 246 a
// rolling sum by time, with a sum over k — whose key 0 holds most of the rows — and a
// max over k mod 7. Under a tenth of the input, on one thread and four, the answer is
// the in-memory one, and the spill directory is empty after.
func TestAWindowWithNoPartitionBesideAKeyedOneStreams(t *testing.T) {
	frame := sortedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	byX := ursus.WindowSpec{OrderBy: []ursus.SortKey{ursus.Asc(c("x"))}}
	q := frame.WithColumns(
		c("v").CumSum(false).Over().Alias("cs"),
		c("f").Rank(ursus.RankAverage, true).Over().Alias("r"),
		c("v").Interpolate().OverWith(byX).Alias("ip"),
		c("v").RollingSumBy(c("x").Cast(ursus.Datetime(ursus.Micro, "")), ursus.Every("10us")).Over().Alias("rs"),
		c("v").Sum().Over(c("k")).Alias("ks"),
		c("x").Max().Over(c("k").Mod(7)).Alias("km"),
	)
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
	if err != nil {
		t.Fatal(err)
	}
	for _, threads := range []int{1, 4} {
		var stats ursus.MemoryStats
		dir := t.TempDir()
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithThreads(threads),
			ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("on %d threads, under %d bytes: %v", threads, limit, err)
		}
		if stats.Spills == 0 {
			t.Fatalf("on %d threads: nothing spilled", threads)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if left, _ := os.ReadDir(dir); len(left) > 0 {
			t.Errorf("on %d threads: %d entries left in the spill directory", threads, len(left))
		}
	}
}

// TestAGrowingWindowAggregateSpillsItsValues: over one huge partition, with no
// partition_by and over a hot key, a median, a quantile of a float with nulls, NaN and
// ±0 among its values, and n_unique of an integer whose every value is distinct, of
// that float, whose NaN and ±0 count once each, and of strings spill their values
// (step 242), where they refused; beside a sum and a cumulative sum. Under a tenth of
// the input, on one thread and four, the answer is the in-memory one, the peak is at
// most twice the limit, and the spill directory is empty after. Implode, whose answer
// is every value, still refuses.
//
// The batch is 64 rows, as TestASortedPassStaysNearItsLimit's is: the merge of the
// spilled values holds a batch of every run, which at 500 rows is about a tenth of
// this limit, and of a real one far less. With no partition_by the peak is under
// twice the limit at 500 rows too, as the sink lets go of its values once they are
// on disk, before a merge reads them back; over a hot key, the other keys' sink
// still holds their rows then.
func TestAGrowingWindowAggregateSpillsItsValues(t *testing.T) {
	frame := sortedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	aggs := func(part ...ursus.Expr) []ursus.Expr {
		return []ursus.Expr{
			c("v").Median().Over(part...).Alias("med"),
			c("f").Quantile(0.9, ursus.InterpLinear).Over(part...).Alias("q90"),
			c("x").NUnique().Over(part...).Alias("nx"),
			c("f").NUnique().Over(part...).Alias("nf"),
			c("s").NUnique().Over(part...).Alias("ns"),
			c("x").Sum().Over(part...).Alias("total"),
			c("v").CumSum(false).Over(part...).Alias("cs"),
		}
	}
	keyless := frame.WithColumns(aggs()...)
	shapes := map[string]*ursus.LazyFrame{
		"no partition_by": keyless,
		"one hot key":     frame.WithColumns(aggs(c("k"))...),
	}
	threadCounts := []int{1, 4}
	if raceDetector {
		// The root package's race run is near its ten minutes: the hot key on four
		// threads drives the spill, the hot key's split and the parallel scan.
		delete(shapes, "no partition_by")
		threadCounts = []int{4}
	}
	for name, q := range shapes {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
		if err != nil {
			t.Fatal(err)
		}
		for _, threads := range threadCounts {
			var stats ursus.MemoryStats
			dir := t.TempDir()
			got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithThreads(threads),
				ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
			if err != nil {
				t.Fatalf("%s on %d threads, under %d bytes: %v", name, threads, limit, err)
			}
			if stats.Spills == 0 {
				t.Fatalf("%s on %d threads: nothing spilled", name, threads)
			}
			if stats.Peak > 2*limit {
				t.Errorf("%s on %d threads: peak %d, %.2fx the limit of %d", name, threads,
					stats.Peak, float64(stats.Peak)/float64(limit), limit)
			}
			ursustest.AssertFrameEqual(t, got, want)
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("%s on %d threads: %d entries left in the spill directory", name, threads, len(left))
			}
		}
	}

	if !raceDetector {
		var stats ursus.MemoryStats
		if _, err := keyless.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithMemoryLimit(limit),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats)); err != nil {
			t.Fatal(err)
		}
		if stats.Peak > 2*limit {
			t.Errorf("no partition_by at 500 rows a batch: peak %d, %.2fx the limit of %d", stats.Peak,
				float64(stats.Peak)/float64(limit), limit)
		}
	}

	_, err = frame.WithColumns(c("v").Implode().Over().Alias("all")).Collect(t.Context(),
		ursus.WithBatchSize(500), ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "implode") {
		t.Errorf("implode over one partition past its limit should refuse, naming implode: %v", err)
	}
}

// timedFrame is n rows a third of a second apart in threes, with a run of 1,200 rows
// at one instant, longer than two batches; by null every 23rd row; v an integer with
// nulls, f a float whose sums round, s a string, x an integer to order by, and k a key
// that is 0 for the first 12,000 rows and one of 500 others after.
func timedFrame(chunks, rows int) *ursus.LazyFrame {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var frames []*ursus.LazyFrame
	for ch := range chunks {
		ts := make([]time.Time, rows)
		tok, vok := make([]bool, rows), make([]bool, rows)
		k, v, x := make([]int64, rows), make([]int64, rows), make([]int64, rows)
		f := make([]float64, rows)
		s := make([]string, rows)
		for j := range rows {
			i := ch*rows + j
			at := i / 3
			if i >= 9000 && i < 10200 {
				at = 3000
			}
			ts[j], tok[j] = t0.Add(time.Duration(at)*time.Second), i%23 != 0
			if i >= 12_000 && i%2 == 1 {
				k[j] = int64(1 + i%500)
			}
			v[j], vok[j] = int64(i%977)-400, i%11 != 0
			x[j] = int64(i * 7919 % 100_003)
			f[j] = float64(i%101)*0.1 + 1.0/3
			s[j] = fmt.Sprintf("s%03d", i*13%211)
		}
		frames = append(frames, ursus.Frame(ursus.ValuesNullable("ts", ts, tok), ursus.Values("k", k),
			ursus.ValuesNullable("v", v, vok), ursus.Values("f", f), ursus.Values("s", s), ursus.Values("x", x)))
	}
	return ursus.Concat(frames)
}

// TestARollingWindowByTimeStreamsPastItsLimit: rolling windows by time over one huge
// partition (step 246), with no partition_by and over one hot key, where they
// refused. A mean and a sum of a float whose sums round, closed both ways; an
// integer's sum, an Int128, and its std wanting three values; a string's max closed
// on the left; a median; a quantile closed on neither side; and a float's std in an
// order_by of its own, which orders its ties, and so how Welford's state rounds. By a Datetime in runs of three, with a run
// of 1,200 rows at one instant and nulls. Under a tenth of the input, on one thread
// and four, the answer is the in-memory one, to the bit, it spills, and the spill
// directory is empty after. A period holding more rows than the limit refuses,
// naming it.
func TestARollingWindowByTimeStreamsPastItsLimit(t *testing.T) {
	frame := timedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	windows := func(part ...ursus.Expr) []ursus.Expr {
		byX := ursus.WindowSpec{PartitionBy: part, OrderBy: []ursus.SortKey{ursus.Desc(c("x"))}}
		return []ursus.Expr{
			c("f").RollingMeanBy(c("ts"), ursus.Every("90s")).Over(part...).Alias("fmean"),
			c("f").RollingSumBy(c("ts"), ursus.Every("5m"), ursus.WindowClosed(ursus.ClosedBoth)).Over(part...).Alias("fsum"),
			c("v").RollingSumBy(c("ts"), ursus.Every("4m")).Over(part...).Alias("vsum"),
			c("v").RollingStdBy(c("ts"), ursus.Every("4m"), ursus.MinSamples(3)).Over(part...).Alias("vstd"),
			c("s").RollingMaxBy(c("ts"), ursus.Every("3m"), ursus.WindowClosed(ursus.ClosedLeft)).Over(part...).Alias("smax"),
			c("f").RollingMedianBy(c("ts"), ursus.Every("2m")).Over(part...).Alias("fmed"),
			c("v").RollingQuantileBy(0.9, ursus.InterpNearest, c("ts"), ursus.Every("7m"),
				ursus.WindowClosed(ursus.ClosedNone)).Over(part...).Alias("vq"),
			c("f").RollingStdBy(c("ts"), ursus.Every("5m")).OverWith(byX).Alias("fstd_x"),
			c("v").CumSum(false).Over(part...).Alias("cs"),
			c("x").Sum().Over(part...).Alias("total"),
		}
	}
	shapes := map[string]*ursus.LazyFrame{
		"no partition_by": frame.WithColumns(windows()...),
		"one hot key":     frame.WithColumns(windows(c("k"))...),
	}
	threadCounts := []int{1, 4}
	if raceDetector {
		// The root package's race run is near its limit: the hot key on four threads
		// drives the pass, the split and the parallel scan.
		delete(shapes, "no partition_by")
		threadCounts = []int{4}
	}
	for name, q := range shapes {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
		if err != nil {
			t.Fatal(err)
		}
		for _, threads := range threadCounts {
			var stats ursus.MemoryStats
			dir := t.TempDir()
			got, err := q.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithThreads(threads),
				ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
			if err != nil {
				t.Fatalf("%s on %d threads, under %d bytes: %v", name, threads, limit, err)
			}
			if stats.Spills == 0 {
				t.Fatalf("%s on %d threads: nothing spilled", name, threads)
			}
			ursustest.AssertFrameEqual(t, got, want)
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("%s on %d threads: %d entries left in the spill directory", name, threads, len(left))
			}
		}
	}

	dir := t.TempDir()
	_, err = frame.WithColumns(c("f").RollingMedianBy(c("ts"), ursus.Every("3h")).Over().Alias("m")).
		Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir))
	if !errors.Is(err, ursus.ErrResource) || !strings.Contains(err.Error(), "period of 3h") {
		t.Errorf("a period holding every row under a tenth of them should refuse, naming it: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) > 0 {
		t.Errorf("%d entries left in the spill directory after the refusal", len(left))
	}
}
