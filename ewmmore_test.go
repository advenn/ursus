package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// Tier 2 of v0.6.6-scope.md (step 285): ewm_sum, ewm_mean_by and ewm_sum_by. Their
// answers are Polars 2.1.0's since step 500, recorded in the ewmmore group of
// testdata/oracle (the 300-row fingerprints whole) and spelled in
// oracle_ewmmore_test.go, which took this file's tables. What stays asserts the name,
// the plan, the refusals, and the stream past a memory limit.

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

func TestEwmMeanAndSumBy(t *testing.T) {
	c := ursus.Col
	x, tt := c("x"), c("t")
	lf := ewmFrame()
	naive := func(name string) ursus.Expr { return c(name).Cast(ursus.Datetime(ursus.Micro, "")) }
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
		"a value that is text": {ursus.Frame(ursus.Values("x", []string{"a"}), ursus.Values("t", []int64{18262 * 86_400_000_000})).
			WithColumns(naive("t")).Select(x.EwmSumBy(tt, ursus.Every("1d"))), "not defined"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
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
