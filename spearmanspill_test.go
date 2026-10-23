package ursus_test

import (
	"os"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// TestAHotGroupsSpearmanSpillsItsPairs is step 339: CorrSpearman keeps every pair of
// a group, and over five hot keys, or none, refused past a limit. Now its pairs
// spill, sorted by y, are ranked by y as they merge back, and sorted again by x: its
// sums are exact, so each answer is the one without a limit to the bit. Over all-
// distinct integers, a column with nulls, a float with NaN, ±Inf and ±0, and long
// ties; keyed, maintaining order or not, and keyless; on one thread and four, under
// 8 KiB; and in a window over one partition and over the hot keys, and in a dynamic
// group under 32 KiB, where its batch of 512 rows alone holds 16 KiB.
func TestAHotGroupsSpearmanSpillsItsPairs(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)
	c := ursus.Col
	f := c("seq").Mod(int64(5)).Sub(int64(2)).Cast(dtype.Float64).
		Div(c("seq").Mod(int64(3)).Sub(int64(1)).Cast(dtype.Float64))
	sp := func(a, b ursus.Expr) ursus.Expr { return ursus.CorrSpearman(a, b) }
	aggs := []ursus.Expr{
		sp(c("seq"), c("opt")).Alias("so"),
		sp(f, c("k")).Alias("fk"),
		sp(c("opt"), c("seq").Mod(int64(7))).Alias("ties"),
		sp(c("k").Mod(int64(2)), c("seq")).Alias("two"),
		c("seq").Sum().Alias("sum"),
	}
	run := func(t *testing.T, name string, limit int64, q func() *ursus.LazyFrame, ordered bool) {
		t.Helper()
		want, err := q().Collect(t.Context(), ursus.WithBatchSize(512))
		if err != nil {
			t.Fatal(err)
		}
		threads := []int{1, 4}
		if raceDetector {
			// The root package's race run is near its limit: one thread there.
			threads = threads[:1]
		}
		for _, threads := range threads {
			var stats ursus.MemoryStats
			dir := t.TempDir()
			got, err := q().Collect(t.Context(), ursus.WithBatchSize(512),
				ursus.WithThreads(threads), ursus.WithVerify(),
				ursus.WithMemoryLimit(limit), ursus.WithSpillDir(dir),
				ursus.WithMemoryStats(&stats))
			if err != nil {
				t.Fatalf("%s on %d threads: %v", name, threads, err)
			}
			if stats.Spills == 0 {
				t.Errorf("%s on %d threads: nothing spilled under %d KiB", name, threads, limit>>10)
			}
			if ordered {
				ursustest.AssertFrameEqual(t, got, want)
			} else {
				ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
			}
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("%s on %d threads: %d entries left in the spill directory", name, threads, len(left))
			}
		}
	}
	run(t, "keyed", 8<<10, func() *ursus.LazyFrame { return ursus.Scan(src).GroupBy(c("grp")).Agg(aggs...) }, false)
	run(t, "ordered", 8<<10, func() *ursus.LazyFrame { return ursus.Scan(src).GroupBy(c("grp")).MaintainOrder().Agg(aggs...) }, true)
	run(t, "keyless", 8<<10, func() *ursus.LazyFrame { return ursus.Scan(src).GroupBy().Agg(aggs...) }, false)
	run(t, "over one partition", 8<<10, func() *ursus.LazyFrame {
		return ursus.Scan(src).Select(c("seq"), sp(c("seq"), c("opt")).Over().Alias("so"), sp(f, c("k")).Over().Alias("fk"))
	}, true)
	run(t, "over the hot keys", 8<<10, func() *ursus.LazyFrame {
		return ursus.Scan(src).Select(c("seq"), sp(c("seq"), c("opt")).Over(c("grp")).Alias("so"))
	}, true)
	run(t, "in a dynamic group", 32<<10, func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupByDynamic(c("seq"), ursus.DynamicOptions{Every: ursus.Every("5000i")}).
			Agg(sp(c("opt"), c("k")).Alias("ok"), sp(f, c("k")).Alias("fk"))
	}, true)
}
