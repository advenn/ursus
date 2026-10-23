package ursus_test

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestInterpolateBy is step 329: Polars' interpolate_by. Every answer is Polars
// 1.44.1's, to the bit.
func TestInterpolateBy(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	run := func(t *testing.T, lf *ursus.LazyFrame, e ursus.Expr) []any {
		t.Helper()
		got, err := lf.Select(e.Alias("v")).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]any, got.Height())
		for i := range out {
			if v, ok, _ := got.At[float64](i, "v"); ok {
				out[i] = v
			}
		}
		return out
	}
	same := func(t *testing.T, got, want []any) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%d rows, want %d", len(got), len(want))
		}
		for i := range want {
			g, w := got[i], want[i]
			switch {
			case g == nil || w == nil:
				if g != w {
					t.Errorf("row %d: %v, want %v", i, g, w)
				}
			case math.IsNaN(w.(float64)):
				if !math.IsNaN(g.(float64)) {
					t.Errorf("row %d: %v, want NaN", i, g)
				}
			case g != w:
				t.Errorf("row %d: %v, want %v", i, g, w)
			}
		}
	}

	t.Run("on the line", func(t *testing.T) {
		lf := ursus.Frame(f64s("x", 1.0, nil, nil, 4.0, nil, 10.0), ursus.Values("t", []int64{0, 1, 3, 4, 7, 10}),
			ursus.Values("u", []int64{0, 3, 1, 4, 7, 10}))
		same(t, run(t, lf, c("x").InterpolateBy(c("t"))), []any{1.0, 1.75, 3.25, 4.0, 7.0, 10.0})
		// by need not be sorted: the answers go back to the rows.
		same(t, run(t, lf, c("x").InterpolateBy(c("u"))), []any{1.0, 3.25, 1.75, 4.0, 7.0, 10.0})
		// Runs at either end stay null; a NaN is a value; equal by on both sides is a
		// zero span, and NaN.
		ends := ursus.Frame(f64s("x", nil, 1.0, nil, 3.0, nil), ursus.Values("t", []int64{0, 1, 2, 4, 5}))
		same(t, run(t, ends, c("x").InterpolateBy(c("t"))), []any{nil, 1.0, 1.6666666666666665, 3.0, nil})
		nan := ursus.Frame(f64s("x", 1.0, math.NaN(), nil, 3.0), ursus.Values("t", []int64{0, 1, 2, 4}))
		same(t, run(t, nan, c("x").InterpolateBy(c("t"))), []any{1.0, math.NaN(), math.NaN(), 3.0})
		flat := ursus.Frame(f64s("x", 1.0, nil, 3.0), ursus.Values("t", []int64{0, 0, 0}))
		same(t, run(t, flat, c("x").InterpolateBy(c("t"))), []any{1.0, math.NaN(), 3.0})
		dup := ursus.Frame(f64s("x", 1.0, nil, 3.0), ursus.Values("t", []int64{0, 0, 4}))
		same(t, run(t, dup, c("x").InterpolateBy(c("t"))), []any{1.0, 1.0, 3.0})
	})

	t.Run("forty rows, by unsorted", func(t *testing.T) {
		by := []int64{84, 214, 353, 325, 144, 245, 110, 243, 262, 93, 258, 270, 120, 1, 7, 189, 299, 219, 35, 74,
			384, 118, 119, 355, 21, 223, 378, 394, 208, 315, 226, 17, 168, 276, 254, 59, 332, 188, 11, 79}
		v := []any{-82.307221, -96.090097, nil, -19.731351, nil, -6.011597, -85.075333, nil, 60.582717, 89.861281,
			97.76383, nil, 11.788518, nil, nil, nil, -41.602541, 0.287819, nil, -46.859072, -68.211253, 96.47836, nil,
			nil, 4.434568, -36.271769, -77.458073, 25.299414, -33.524361, 85.94073, nil, nil, -65.196377, nil, nil,
			nil, 31.596048, nil, -67.382759, -97.147446}
		want := []any{-82.307221, -96.090097, -18.189528978260864, -19.731351, -26.703929499999997, -6.011597,
			-85.075333, -8.76252172727273, 60.582717, 89.861281, 97.76383, 38.48860716216217, 11.788518, nil, nil,
			-48.5685686, -41.602541, 0.287819, -9.114695396226413, -46.859072, -68.211253, 96.47836,
			54.133438999999996, -22.93101249999999, 4.434568, -36.271769, -77.458073, 25.299414, -33.524361,
			85.94073, -32.14538190909091, -24.292362799999992, -65.196377, 21.918024783783785, 65.83292938461538,
			-32.34200407547169, 31.596048, -49.360369, -67.382759, -97.147446}
		lf := ursus.Frame(f64s("x", v...), ursus.Values("t", by))
		same(t, run(t, lf, c("x").InterpolateBy(c("t"))), want)
	})

	t.Run("types", func(t *testing.T) {
		// Integers answer a Float64; a Float32 stays one, computed in float32.
		ints := ursus.Frame(i64s("x", 1, nil, 3), ursus.Values("t", []int64{0, 1, 4}))
		same(t, run(t, ints, c("x").InterpolateBy(c("t"))), []any{1.0, 1.5, 3.0})
		f32 := ursus.Frame(ursus.ValuesNullable("x", []float32{1.1, 0, 0, 7.3, 0, 2.9},
			[]bool{true, false, false, true, false, true}), ursus.Values("t", []float64{0, 0.3, 1.7, 2.2, 5, 6.1}))
		got, err := f32.Select(c("x").InterpolateBy(c("t"))).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.Values("x", []float32{1.1, 1.9454545974731445,
			5.890909194946289, 7.300000190734863, 4.141025543212891, 2.9000000953674316})))
		// Fourteen random Float32s by an unsorted integer: computed in float32, rows 4
		// and 7 are not what float64 rounded to float32 gives.
		rnd := ursus.Frame(ursus.ValuesNullable("x", []float32{-48.77217483520508, 0, -38.76011276245117,
			-10.716263771057129, 0, 18.38642692565918, -36.13090515136719, 0, -38.75564193725586, -26.819765090942383, 0,
			25.759248733520508, -35.2598762512207, 0}, []bool{true, false, true, true, false, true, true, false, true, true,
			false, true, true, false}),
			ursus.Values("t", []int64{47, 2, 42, 3, 8, 14, 34, 46, 28, 33, 26, 13, 37, 5}))
		got, err = rnd.Select(c("x").InterpolateBy(c("t"))).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, ursus.ValuesNullable("x", []float32{-48.77217483520508, 0,
			-38.76011276245117, -10.716263771057129, 7.521492958068848, 18.38642692565918, -36.13090515136719,
			-46.76976013183594, -38.75564193725586, -26.819765090942383, -30.59248924255371, 25.759248733520508,
			-35.2598762512207, -3.42116117477417}, []bool{true, false, true, true, true, true, true, true, true, true, true,
			true, true, true})))
		// A Date by counts days; a Datetime of big ticks is subtracted in integers.
		day := func(d int) time.Time { return time.Date(2020, 1, d, 0, 0, 0, 0, time.UTC) }
		dates := ursus.Frame(f64s("x", 1.0, nil, 3.0), ursus.Values("t", []time.Time{day(1), day(2), day(5)})).
			Select(c("x"), c("t").Cast(ursus.Date))
		same(t, run(t, dates, c("x").InterpolateBy(c("t"))), []any{1.0, 1.5, 3.0})
		t0 := int64(1_700_000_000_123_456_789)
		big := ursus.Frame(f64s("x", 3.25, nil, nil, -7.5),
			ursus.Values("t", []int64{t0, t0 + 123_457, t0 + 987_654_321, t0 + 1_000_000_007})).
			Select(c("x"), c("t").Cast(ursus.Datetime(ursus.Nano, "")))
		same(t, run(t, big, c("x").InterpolateBy(c("t"))), []any{3.25, 3.2486728372592903, -7.367283876429013, -7.5})
		// A NaN by sorts last, above every number.
		nanBy := ursus.Frame(f64s("x", 1.0, nil, 3.0, nil), f64s("t", 0.0, math.NaN(), 2.0, 1.0))
		same(t, run(t, nanBy, c("x").InterpolateBy(c("t"))), []any{1.0, nil, 3.0, 2.0})
		nanFirst := ursus.Frame(f64s("x", 5.0, nil, 3.0), f64s("t", math.NaN(), 0.0, 1.0))
		same(t, run(t, nanFirst, c("x").InterpolateBy(c("t"))), []any{5.0, nil, 3.0})

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
			if _, err := tc.lf.Select(c("x").InterpolateBy(c("t"))).Collect(ctx); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("per group", func(t *testing.T) {
		lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2, 2}), f64s("x", 1.0, nil, 3.0, 10.0, nil, 30.0),
			ursus.Values("t", []int64{0, 1, 4, 0, 3, 4}))
		same(t, run(t, lf, c("x").InterpolateBy(c("t")).Over(c("g"))), []any{1.0, 1.5, 3.0, 10.0, 25.0, 30.0})
		// by out of order within each group.
		mixed := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 1, 2, 2, 2, 2}),
			f64s("x", 3.0, 1.0, nil, nil, nil, 30.0, 10.0, 20.0), ursus.Values("t", []int64{4, 0, 1, 2, 3, 4, 0, 1}))
		same(t, run(t, mixed, c("x").InterpolateBy(c("t")).Over(c("g"))),
			[]any{3.0, 1.0, 1.5, 2.0, 26.666666666666668, 30.0, 10.0, 20.0})
	})
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
