package ursus_test

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// TestModeInAnAggregation is step 340: a group's Mode is an aggregate of its own, an
// entry a distinct value, where it was the list kernel over the group's values
// imploded. Its answers are Polars 2.1.0's since step 500, recorded in the modeagg
// group of testdata/oracle and spelled in oracle_modeagg_test.go, which took this
// file's table. What stays asserts the plan and the first-seen order on four threads.
func TestModeInAnAggregation(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(
		ursus.Values("g", []int64{1, 1, 1, 1, 2, 2, 2, 3, 3, 3, 3}),
		ursus.ValuesNullable("x", []float64{3, 0, 0, 3, math.Copysign(0, -1), 0, 2, math.NaN(), 1, math.NaN(), 1},
			[]bool{true, false, false, true, true, true, true, true, true, true, true}))
	// The aggregate, not an implode.
	plan, err := lf.GroupBy(c("g")).Agg(c("x").Mode(true)).Explain(t.Context())
	if err != nil || !strings.Contains(plan, "mode(maintain_order=true)") || strings.Contains(plan, "implode") {
		t.Errorf("the plan of a mode by g:\n%s\n%v", plan, err)
	}
	// The order the values first appear is the rows', on four threads too: each value
	// first in a batch of its own, and every value as frequent.
	const batches = 40
	vs := make([]int64, 0, batches*64)
	for b := range batches {
		for i := range 64 {
			vs = append(vs, int64((b+i/32)%batches))
		}
	}
	firsts := ursus.Frame(ursus.Values("v", vs)).GroupBy().Agg(c("v").Mode(true).Alias("m"))
	for _, threads := range []int{1, 4} {
		got, err := firsts.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithThreads(threads))
		if err != nil {
			t.Fatal(err)
		}
		rows, _ := got.ToMaps()
		if s := fmt.Sprint(rows[0]["m"]); !strings.HasPrefix(s, "[0 1 2 3 4 5 6 7 8 9 10 ") {
			t.Errorf("first-seen order on %d threads: %s", threads, s)
		}
	}
}

// TestAHotGroupsModeSpillsItsCounts is step 340: a mode over five hot keys, and over
// none, kept every value of a group, imploded, and refused past a limit. Now it keeps
// an entry a distinct value, which a few values a group keep under the limit, and
// past it the entries spill, sorted by group and value, and are read back twice:
// once for each group's modes, once for their values. Each answer is the one without
// a limit: over 997 integers and 1999 strings with ties of the most rows, all-
// distinct integers, every one a mode, integers and text with nulls, and a float with
// NaN, ±Inf and ±0; keyed, maintaining order or not, and keyless; on one thread and four,
// under 8 KiB; and in a dynamic group under 32 KiB.
func TestAHotGroupsModeSpillsItsCounts(t *testing.T) {
	src := memFrame(t, 20000, 512, 4000)
	c := ursus.Col
	f := c("seq").Mod(int64(5)).Sub(int64(2)).Cast(dtype.Float64).
		Div(c("seq").Mod(int64(3)).Sub(int64(1)).Cast(dtype.Float64))
	aggs := []ursus.Expr{
		c("seq").Mod(int64(997)).Mode(true).Alias("m997"),
		c("seq").Mod(int64(997)).Mode(false).Alias("s997"),
		c("seq").Mod(int64(1999)).Cast(ursus.String).Mode(true).First().Alias("t1999"),
		c("seq").Mode(false).List().Len().Alias("all"),
		c("opt").Mode(true).Alias("opt"),
		c("opt").Cast(ursus.String).Mode(true).Alias("topt"),
		f.Mode(true).Alias("f"),
		f.Mode(false).Alias("fs"),
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
	run(t, "in a dynamic group", 32<<10, func() *ursus.LazyFrame {
		return ursus.Scan(src).GroupByDynamic(c("seq"), ursus.DynamicOptions{Every: ursus.Every("5000i")}).
			Agg(c("seq").Mod(int64(997)).Mode(true).Alias("m997"), f.Mode(false).Alias("f"))
	}, true)
}
