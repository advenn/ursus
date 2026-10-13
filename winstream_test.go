package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestUnpartitionedWindowsStreamPastTheirLimit: step 197, v0.6.1 item O2. A window
// with no partition_by is one partition, which partitioning to disk cannot divide,
// so past its limit it refused. Its rows now go to one file and are read back a
// batch at a time:
//
//   - aggregates broadcast to every row, of an integer, a float and a string;
//   - cumulative functions carrying their last answer across batches, a sum
//     widening an Int32 to Int128, a max over strings, a count, and a count
//     reversed, which counts down from the file's rows (step 221);
//   - ewm functions carrying their running state (step 221): a mean, a variance
//     ignoring nulls across runs of them longer than a batch, and an unadjusted,
//     biased standard deviation wanting five values;
//   - shifts back and forward, with a fill, and a diff, which is a shift;
//   - forward fills with and without a limit, and a backward fill with one, over
//     runs of nulls longer than a batch;
//   - rolling windows of rows, centred and with a minimum count, and a rolling
//     median and quantile (step 227).
//
// 20,000 rows in batches of 64 under a 16 KiB limit, frame-equal to the answer
// without a limit.
func TestUnpartitionedWindowsStreamPastTheirLimit(t *testing.T) {
	const n = 20_000
	v, vok := make([]int64, n), make([]bool, n)
	f, i32, s := make([]float64, n), make([]int32, n), make([]string, n)
	for i := range n {
		v[i], vok[i] = int64(i%977)-400, i%300 >= 150 || i%7 != 0 // runs of nulls, some long
		if i%1000 < 130 {
			vok[i] = false // runs of 130, longer than two batches
		}
		f[i] = float64(i%61) / 4
		i32[i] = int32(i * 7919)
		s[i] = []string{"pear", "apple", "zebra", "fig"}[i*31%4]
	}
	frame := ursus.Frame(ursus.ValuesNullable("v", v, vok), ursus.Values("f", f),
		ursus.Values("i32", i32), ursus.Values("s", s))
	c := ursus.Col
	lf := frame.WithColumns(
		c("v").Sum().Over().Alias("sum"), c("f").Mean().Over().Alias("mean"), c("s").Max().Over().Alias("smax"),
		c("v").CumSum(false).Alias("cs"), c("i32").CumSum(false).Alias("cs32"),
		c("f").CumMax(false).Alias("cmax"), c("s").CumMin(false).Alias("smin"), c("v").CumCount(false).Alias("cc"),
		c("v").CumCount(true).Alias("ccr"),
		c("f").EwmMean(ursus.EwmAlpha(0.3)).Alias("em"),
		c("v").EwmVar(ursus.EwmSpan(10), ursus.IgnoreNulls()).Alias("ev"),
		c("v").EwmStd(ursus.EwmCom(2), ursus.EwmAdjust(false), ursus.Biased(), ursus.MinSamples(5)).Alias("es"),
		c("v").Shift(2).Alias("lag2"), c("v").ShiftFill(-3, int64(-1)).Alias("lead3"), c("v").Diff(1).Alias("diff"),
		c("v").ForwardFill(0).Alias("ff"), c("v").ForwardFill(2).Alias("ff2"), c("v").BackwardFill(3).Alias("bf3"),
		c("f").RollingSum(5).Alias("rs"), c("f").RollingMean(4, ursus.RollingCenter()).Alias("rm"),
		c("f").RollingMin(6, ursus.MinSamples(2)).Alias("rmin"),
		c("f").RollingMedian(5).Alias("rmed"),
		c("v").RollingQuantile(0.9, ursus.InterpNearest, 7, ursus.RollingCenter(), ursus.MinSamples(2)).Alias("rq"),
	)
	want, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	var st ursus.MemoryStats
	got, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatalf("under a 16 KiB limit: %v", err)
	}
	if st.Spills == 0 {
		t.Fatal("answered without spilling, so it proves nothing")
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// TestUnpartitionedWindowsThatNeedTheWholeFrameStillRefuse: what step 197 leaves
// refusing, each a function whose answer for a batch needs an unbounded part of the
// frame: a rolling window by time, which names the window and says it has no
// partition. An ewm mean was here until step 221, a reversed cumulative sum and a
// backward fill with no limit until step 236, a rank until step 237, and an
// interpolation until step 238.
func TestUnpartitionedWindowsThatNeedTheWholeFrameStillRefuse(t *testing.T) {
	const n = 4000
	v, vok, ts := make([]int64, n), make([]bool, n), make([]int64, n)
	for i := range n {
		v[i], vok[i], ts[i] = int64(i), i%5 != 0, int64(i)*1_000_000
	}
	frame := ursus.Frame(ursus.ValuesNullable("v", v, vok), ursus.Values("ts", ts)).
		WithColumns(ursus.Col("ts").Cast(ursus.Datetime(ursus.Micro, "")).Alias("ts"))
	c := ursus.Col
	for name, e := range map[string]ursus.Expr{
		"a rolling window by time": c("v").RollingSumBy(c("ts"), ursus.Every("1s")),
	} {
		_, err := frame.WithColumns(e.Alias("w")).Collect(t.Context(), ursus.WithBatchSize(64),
			ursus.WithMemoryLimit(16<<10), ursus.WithSpillDir(t.TempDir()))
		if err == nil {
			t.Errorf("%s over the whole frame answered under 16 KiB", name)
			continue
		}
		if !strings.Contains(err.Error(), "over") || !strings.Contains(err.Error(), "no partition") {
			t.Errorf("%s: the refusal should name the window and its one partition: %v", name, err)
		}
	}
}
