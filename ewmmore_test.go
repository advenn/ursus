package ursus_test

import (
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// Tier 2 of v0.6.6-scope.md (step 285): ewm_sum, ewm_mean_by and ewm_sum_by. Every
// answer is Polars 1.44.1's, compared by its bits.

// ewmFrame is Polars' probe frame: x with nulls, its integers, g, and t, naive
// datetimes in microseconds, 2020-01-01, 02, 04, 04, 07, 08, 10 and 11.
func ewmFrame() *ursus.LazyFrame {
	days := []int64{18262, 18263, 18265, 18265, 18268, 18269, 18271, 18272}
	us := make([]int64, len(days))
	for i, d := range days {
		us[i] = d * 86_400_000_000
	}
	ok := []bool{true, true, false, true, true, false, false, true}
	return ursus.Frame(
		ursus.ValuesNullable("x", []float64{1, 2, 0, 4, 8, 0, 0, 3}, ok),
		ursus.ValuesNullable("i", []int64{1, 2, 0, 4, 8, 0, 0, 3}, ok),
		ursus.Values("g", []int64{1, 1, 1, 1, 2, 2, 2, 2}),
		ursus.Values("t", us)).
		WithColumns(ursus.Col("t").Cast(ursus.Datetime(ursus.Micro, "")))
}

func TestEwmSum(t *testing.T) {
	c := ursus.Col
	x := c("x")
	nan := math.NaN()
	lf := ewmFrame()
	half := ursus.EwmAlpha(0.5)
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"alpha 0.5", lf.Select(x.EwmSum(half)), []any{1.0, 2.5, nil, 4.625, 10.3125, nil, nil, 4.2890625}},
		{"nulls ignored", lf.Select(x.EwmSum(half, ursus.IgnoreNulls())), []any{1.0, 2.5, nil, 5.25, 10.625, nil, nil, 8.3125}},
		{"from 2", lf.Select(x.EwmSum(half, ursus.MinSamples(2))), []any{nil, 2.5, nil, 4.625, 10.3125, nil, nil, 4.2890625}},
		{"a span of 3", lf.Select(x.EwmSum(ursus.EwmSpan(3))), []any{1.0, 2.5, nil, 4.625, 10.3125, nil, nil, 4.2890625}},
		{"integers, a com of 1", lf.Select(c("i").EwmSum(ursus.EwmCom(1))), []any{1.0, 2.5, nil, 4.625, 10.3125, nil, nil, 4.2890625}},
		{"over g", lf.Select(x.EwmSum(half).Over(c("g"))), []any{1.0, 2.5, nil, 4.625, 8.0, nil, nil, 4.0}},
		{"NaN and Inf", ursus.Frame(ursus.Values("x", []float64{1, nan, 2, math.Inf(1)})).Select(x.EwmSum(half)),
			[]any{1.0, nan, nan, nan}},
		{"leading nulls", ursus.Frame(ursus.ValuesNullable("x", []float64{0, 0, 1, 2}, []bool{false, false, true, true})).
			Select(x.EwmSum(half)), []any{nil, nil, 1.0, 2.5}},
		{"leading nulls ignored", ursus.Frame(ursus.ValuesNullable("x", []float64{0, 0, 1, 2}, []bool{false, false, true, true})).
			Select(x.EwmSum(half, ursus.IgnoreNulls())), []any{nil, nil, 1.0, 2.5}},
	} {
		if got := typedCells[float64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	// Summed in float32, as Polars sums a Float32.
	if got, want := typedCells[float32](t, lf.Select(x.Cast(ursus.Float32).EwmSum(ursus.EwmAlpha(0.3)))),
		[]any{float32(1), float32(2.700000047683716), nil, float32(5.322999954223633), float32(11.726099967956543), nil, nil,
			float32(7.02205228805542)}; !sameCells(got, want) {
		t.Errorf("a Float32:\ngot  %v\nwant %v", got, want)
	}
}

func TestEwmMeanAndSumBy(t *testing.T) {
	c := ursus.Col
	x, tt := c("x"), c("t")
	nan := math.NaN()
	lf := ewmFrame()
	days := func(d ...int64) []int64 {
		for i := range d {
			d[i] = (18262 + d[i]) * 86_400_000_000
		}
		return d
	}
	naive := func(name string) ursus.Expr { return c(name).Cast(ursus.Datetime(ursus.Micro, "")) }
	unsorted := ursus.Frame(ursus.Values("x", []float64{1, 2, 3}), ursus.Values("t", days(2, 0, 1))).WithColumns(naive("t"))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"mean, 2d", lf.Select(x.EwmMeanBy(tt, ursus.Every("2d"))),
			[]any{1.0, 1.2928932188134525, nil, 2.646446609406726, 6.107233047033631, nil, nil, 3.776808261758408}},
		{"mean of integers, 1d", lf.Select(c("i").EwmMeanBy(tt, ursus.Every("1d"))),
			[]any{1.0, 1.5, nil, 3.375, 7.421875, nil, nil, 3.2763671875}},
		{"mean over g", lf.Select(x.EwmMeanBy(tt, ursus.Every("2d")).Over(c("g"))),
			[]any{1.0, 1.2928932188134525, nil, 2.646446609406726, 8.0, nil, nil, 4.25}},
		{"by a date", lf.Select(x.EwmMeanBy(tt.Dt().Date(), ursus.Every("1d"))),
			[]any{1.0, 1.5, nil, 3.375, 7.421875, nil, nil, 3.2763671875}},
		{"by a date, 36h", lf.Select(x.EwmMeanBy(tt.Dt().Date(), ursus.Every("36h"))),
			[]any{1.0, 1.3700394750525633, nil, 2.9562994740159003, 6.739074868503975, nil, nil, 3.588867391745133}},
		{"by milliseconds, 1500us truncated", lf.Select(x.EwmMeanBy(tt.Cast(ursus.Datetime(ursus.Milli, "")), ursus.Every("1500us"))),
			[]any{1.0, 2.0, nil, 4.0, 8.0, nil, nil, 3.0}},
		{"by nanoseconds, 31h17m3s", lf.Select(x.EwmMeanBy(tt.Cast(ursus.Datetime(ursus.Nano, "")), ursus.Every("31h17m3s"))),
			[]any{1.0, 1.4124275071520311, nil, 3.1066627610804836, 7.007365156142701, nil, nil, 3.47764445707091}},
		{"zoned, 24h", lf.Select(x.EwmMeanBy(tt.Dt().ReplaceTimeZone("Europe/Dublin"), ursus.Every("24h"))),
			[]any{1.0, 1.5, nil, 3.375, 7.421875, nil, nil, 3.2763671875}},
		// Every refuses "0d", which spans no time; the zero Interval is Polars' "0d".
		{"a half-life of 0", lf.Select(x.EwmMeanBy(tt, ursus.Interval{})), []any{1.0, 2.0, nil, 4.0, 8.0, nil, nil, 3.0}},
		{"unsorted", unsorted.Select(x.EwmMeanBy(tt, ursus.Every("1d"))), []any{1.75, 2.0, 2.5}},
		{"a null time", ursus.Frame(ursus.Values("x", []float64{1, 2, 3}), ursus.ValuesNullable("t", days(0, 0, 1), []bool{true, false, true})).
			WithColumns(naive("t")).Select(x.EwmMeanBy(tt, ursus.Every("1d"))), []any{1.0, nil, 2.0}},
		{"a NaN", ursus.Frame(ursus.Values("x", []float64{1, nan, 3}), ursus.Values("t", days(0, 1, 3))).
			WithColumns(naive("t")).Select(x.EwmMeanBy(tt, ursus.Every("2d"))), []any{1.0, nan, nan}},
		// Two at one instant: the second weighs nothing, and equals the answer.
		{"one instant", ursus.Frame(ursus.Values("x", []float64{1, 5, 3}), ursus.Values("t", days(0, 1, 1))).
			WithColumns(naive("t")).Select(x.EwmMeanBy(tt, ursus.Every("1d"))), []any{1.0, 3.0, 3.0}},
		// A value equal to the last answer is the answer, as Polars returns it: weighed,
		// d·x + (1−d)·x would be 123.45599999999999.
		{"a constant", ursus.Frame(ursus.Values("x", []float64{123.456, 123.456, 123.456}), ursus.Values("t", days(0, 1, 3))).
			WithColumns(naive("t")).Select(x.EwmMeanBy(tt, ursus.Every("31h17m3s"))), []any{123.456, 123.456, 123.456}},
		{"sum, 2d", lf.Select(x.EwmSumBy(tt, ursus.Every("2d"))),
			[]any{1.0, 2.7071067811865475, nil, 5.353553390593274, 9.892766952966369, nil, nil, 5.473191738241592}},
	} {
		if got := typedCells[float64](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []any
	}{
		{"a Float32's mean, in float32", lf.Select(x.Cast(ursus.Float32).EwmMeanBy(tt, ursus.Every("36h"))),
			[]any{float32(1), float32(1.370039463043213), nil, float32(2.9562995433807373), float32(6.73907470703125), nil, nil,
				float32(3.588867425918579)}},
		{"a Float32's sum", lf.Select(x.Cast(ursus.Float32).EwmSumBy(tt, ursus.Every("36h"))),
			[]any{float32(1), float32(2.629960536956787), nil, float32(5.043700695037842), float32(9.26092529296875), nil, nil,
				float32(4.4585041999816895)}},
	} {
		if got := typedCells[float32](t, tc.q); !sameCells(got, tc.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", tc.name, got, tc.want)
		}
	}
	if s, err := lf.Select(x.EwmMeanBy(tt, ursus.Every("1d"))).CollectSchema(t.Context()); err != nil || s.Field(0).Name != "x" {
		t.Errorf("ewm_mean_by's name: %v, %v", s, err)
	}
	if got := x.EwmMeanBy(tt, ursus.Every("2d")).String(); !strings.Contains(got, "ewm_mean_by(") || !strings.Contains(got, "half_life 2d") {
		t.Errorf("ewm_mean_by's plan: %s", got)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "expected `half_life` to be a constant duration ..., got 1mo".
		"a month":      {lf.Select(x.EwmMeanBy(tt, ursus.Every("1mo"))), "not a constant duration"},
		"a day, zoned": {lf.Select(x.EwmMeanBy(tt.Dt().ReplaceTimeZone("Europe/Dublin"), ursus.Every("1d"))), "not a constant duration"},
		"negative":     {lf.Select(x.EwmMeanBy(tt, ursus.Every("-1d"))), "must not be negative"},
		"by a Time": {lf.Select(x.EwmMeanBy(tt.Dt().Time(), ursus.Every("1h"))),
			"by a Date, a Datetime or an integer of 32 or 64 bits, got Time"},
		// An integer by counts by "i" (step 312); Polars: must be a parsed integer.
		"an integer by, a half-life of time": {lf.Select(x.EwmMeanBy(c("g"), ursus.Every("1h"))),
			"half-life=1h is an interval of time"},
		"a value that is text": {ursus.Frame(ursus.Values("x", []string{"a"}), ursus.Values("t", days(0))).WithColumns(naive("t")).
			Select(x.EwmSumBy(tt, ursus.Every("1d"))), "not defined"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestEwmFingerprints: 300 rows of sevenths, a null every 37th, at times 1 to 17
// hours and a few minutes apart, against Polars' answers by a fingerprint of their
// bits: the sum of each answer's bits, a float32's 32, and how many are null. Each
// decay's 0.5^(Δt/half-life) counts, and each float32 rounding.
func TestEwmFingerprints(t *testing.T) {
	const n = 300
	xs, xok, ts := make([]float64, n), make([]bool, n), make([]int64, n)
	var at int64
	for i := range n {
		xs[i], xok[i] = float64(i*7919%1000)/7-50, i%37 != 5
		at += int64(i*31%17+1)*3_600_000_000 + int64(i%7)*60_000_000
		ts[i] = at
	}
	lf := ursus.Frame(ursus.ValuesNullable("x", xs, xok), ursus.Values("t", ts)).
		WithColumns(ursus.Col("t").Cast(ursus.Datetime(ursus.Micro, "")))
	x, tt := ursus.Col("x"), ursus.Col("t")
	x32 := x.Cast(ursus.Float32)
	for _, tc := range []struct {
		name  string
		e     ursus.Expr
		f32   bool
		sum   uint64
		nulls int
	}{
		{"mean by 5h", x.EwmMeanBy(tt, ursus.Every("5h")), false, 4704840752413525591, 8},
		{"sum by 31h17m3s", x.EwmSumBy(tt, ursus.Every("31h17m3s")), false, 7084447438426112445, 8},
		{"a Float32's mean by 5h", x32.EwmMeanBy(tt, ursus.Every("5h")), true, 528454491499, 8},
		{"a Float32's sum by 7h", x32.EwmSumBy(tt, ursus.Every("7h")), true, 519572069017, 8},
		{"a Float32's ewm_sum", x32.EwmSum(ursus.EwmAlpha(0.3)), true, 454315174649, 8},
		{"ewm_sum, nulls ignored", x.EwmSum(ursus.EwmAlpha(0.1), ursus.IgnoreNulls()), false, 17727293377543501082, 8},
	} {
		var cells []any
		if tc.f32 {
			cells = typedCells[float32](t, lf.Select(tc.e))
		} else {
			cells = typedCells[float64](t, lf.Select(tc.e))
		}
		var sum uint64
		nulls := 0
		for _, v := range cells {
			switch v := v.(type) {
			case nil:
				nulls++
			case float32:
				sum += uint64(math.Float32bits(v))
			case float64:
				sum += math.Float64bits(v)
			}
		}
		if sum != tc.sum || nulls != tc.nulls {
			t.Errorf("%s: bits sum to %d with %d nulls, Polars' to %d with %d", tc.name, sum, nulls, tc.sum, tc.nulls)
		}
	}
}

// TestEwmMoreStreamPastTheirLimit: an ewm_sum carries its running sum across batches,
// as the ewm functions before it do (step 221), and an ewm by time walks a sorted
// pass in by's order, carrying its last time and answer, as a rolling window by time
// does (step 246); over a key the partitions spill. Each carries its state exactly,
// so the answers equal those without a limit to the bit. 20,000 rows, by out of
// order, in batches of 64 under a 16 KiB limit.
func TestEwmMoreStreamPastTheirLimit(t *testing.T) {
	const n = 20_000
	v, vok, ts, k := make([]float64, n), make([]bool, n), make([]int64, n), make([]int64, n)
	for i := range n {
		v[i], vok[i] = float64(i%977)/7-40, i%300 >= 150 || i%7 != 0
		ts[i] = int64(i*7919%n) * 3_600_000_000 // hours, shuffled
		k[i] = int64(i % 3)
	}
	frame := ursus.Frame(ursus.ValuesNullable("v", v, vok), ursus.Values("t", ts), ursus.Values("k", k)).
		WithColumns(ursus.Col("t").Cast(ursus.Datetime(ursus.Micro, "")))
	c := ursus.Col
	for name, cols := range map[string][]ursus.Expr{
		"the whole frame": {
			c("v").EwmSum(ursus.EwmAlpha(0.1)).Alias("s"),
			c("v").EwmSum(ursus.EwmSpan(30), ursus.IgnoreNulls(), ursus.MinSamples(3)).Alias("si"),
			c("v").EwmMeanBy(c("t"), ursus.Every("5h")).Alias("mb"),
			c("v").EwmSumBy(c("t"), ursus.Every("90m")).Alias("sb"),
		},
		"over a key": {
			c("v").EwmSum(ursus.EwmAlpha(0.1)).Over(c("k")).Alias("s"),
			c("v").EwmMeanBy(c("t"), ursus.Every("5h")).Over(c("k")).Alias("mb"),
		},
	} {
		lf := frame.WithColumns(cols...)
		want, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
		if err != nil {
			t.Fatal(err)
		}
		var st ursus.MemoryStats
		got, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		if err != nil {
			t.Fatalf("%s, under a 16 KiB limit: %v", name, err)
		}
		if st.Spills == 0 {
			t.Fatalf("%s answered without spilling, so it proves nothing", name)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}
}
