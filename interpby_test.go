package ursus_test

import (
	"math"
	"os"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestInterpolateBy is step 329: Polars' interpolate_by. Its answers are Polars 2.1.0's,
// to the bit, recorded in the interpby group of testdata/oracle and spelled in
// oracle_interpby_test.go since step 498, which took this file's tables. What stays
// asserts the refusals.
func TestInterpolateBy(t *testing.T) {
	c := ursus.Col
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		want string
	}{
		{"a null by", ursus.Frame(f64s("x", 1.0, nil, 3.0), i64s("t", 0, nil, 4)), "null"},
		{"text", ursus.Frame(strs("x", "a", nil, "c"), ursus.Values("t", []int64{0, 1, 4})), "not defined for String"},
		{"a Boolean by", ursus.Frame(f64s("x", 1.0, nil, 3.0), ursus.Values("t", []bool{true, false, true})),
			"by is a Date"},
	} {
		if _, err := tc.lf.Select(c("x").InterpolateBy(c("t"))).Collect(t.Context()); err == nil ||
			!strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.want)
		}
	}
}

// TestInterpolateByStreamsPastItsLimit: interpolate_by over one huge partition
// (step 329), in a sorted pass by its by, under a 16 KiB limit, frame-equal to the
// answer without one, on one thread and on four: a by unsorted with runs of one value,
// gaps longer than a batch, an integer by, a float by and a Float32 value beside
// another function, and the spill directory empty after.
func TestInterpolateByStreamsPastItsLimit(t *testing.T) {
	const n = 6000
	v, vok, ts, xs, f32 := make([]float64, n), make([]bool, n), make([]int64, n), make([]float64, n), make([]float32, n)
	for i := range n {
		v[i], vok[i] = float64(i%977)-400, i%300 >= 150 || i%7 != 0
		if i%1000 < 130 {
			vok[i] = false // gaps of 130, longer than two batches
		}
		ts[i] = int64((i * 7919) % 2003) // unsorted, each value about three times
		xs[i] = float64(i%641) / 8
		f32[i] = float32(math.Sin(float64(i)) * 40) // not quarters: float32's arithmetic shows
	}
	frame := ursus.Frame(ursus.ValuesNullable("v", v, vok), ursus.Values("ts", ts), ursus.Values("x", xs),
		ursus.ValuesNullable("f", f32, vok))
	c := ursus.Col
	lf := frame.WithColumns(c("v").InterpolateBy(c("ts")).Alias("by_ts"), c("v").InterpolateBy(c("x")).Alias("by_x"),
		c("f").InterpolateBy(c("ts")).Alias("f_ts"), c("v").CumSum(false).Alias("cs"))
	want, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	for _, threads := range []int{1, 4} {
		dir := t.TempDir()
		var st ursus.MemoryStats
		got, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
			ursus.WithSpillDir(dir), ursus.WithMemoryStats(&st), ursus.WithThreads(threads))
		if err != nil {
			t.Fatalf("%d threads under a 16 KiB limit: %v", threads, err)
		}
		if st.Spills == 0 {
			t.Fatalf("%d threads answered without spilling, so it proves nothing", threads)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if left, _ := os.ReadDir(dir); len(left) > 0 {
			t.Errorf("%d threads left %d files in the spill directory", threads, len(left))
		}
	}
}
