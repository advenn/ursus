package ursus_test

// Step 172: Interpolate's nearest method and rolling windows labelled at their middle
// row. The expected answers are Polars 1.44.1's, from the bench's own environment:
//
//	pl.Series([...]).interpolate(method="nearest")
//	pl.Series([1., 2., 3., 4., 5., 6.]).rolling_sum(w, center=True[, min_samples=1])

import (
	"math"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// nanNulls is vals with NaN standing for a null, as ValuesNullable wants them.
func nanNulls(name string, vals []float64) *ursus.Column {
	ok := make([]bool, len(vals))
	for i, v := range vals {
		ok[i] = !math.IsNaN(v)
	}
	return ursus.ValuesNullable(name, vals, ok)
}

// rendered172 renders a Float64 or Int64 column with nulls as "∅".
func rendered172(t *testing.T, df *ursus.DataFrame, name string) string {
	t.Helper()
	text, err := df.Select(t.Context(), ursus.Col(name).Cast(ursus.String))
	if err != nil {
		t.Fatal(err)
	}
	col, err := text.Column[string](name)
	if err != nil {
		t.Fatal(err)
	}
	out := ""
	for i := range col.Len() {
		v, ok := col.Get(i)
		if !ok {
			v = "∅"
		}
		out += v + " "
	}
	return out
}

func TestInterpolateNearestAnswersAsPolars(t *testing.T) {
	c := ursus.Col
	nan := math.NaN()
	for _, tc := range []struct {
		in   []int64
		ok   []bool
		want string
	}{
		{[]int64{1, 0, 0, 7, 0, 10, 0, 0, 0, 20, 0},
			[]bool{true, false, false, true, false, true, false, false, false, true, false},
			"1 1 7 7 10 10 10 20 20 20 ∅ "},
		{[]int64{1, 0, 4}, []bool{true, false, true}, "1 4 4 "},
		{[]int64{0, 2, 0}, []bool{false, true, false}, "∅ 2 ∅ "},
	} {
		df, err := ursus.Frame(ursus.ValuesNullable("v", tc.in, tc.ok)).
			Select(c("v").Interpolate(ursus.InterpolateNearest())).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if df.Schema().Field(0).Type != dtype.Int64 {
			t.Fatalf("nearest gave %s, want Int64 kept", df.Schema().Field(0).Type)
		}
		if got := rendered172(t, df, "v"); got != tc.want {
			t.Errorf("nearest of %v: %q, want %q", tc.in, got, tc.want)
		}
	}
	df, err := ursus.Frame(nanNulls("v", []float64{1.5, nan, nan, nan, 4.5})).
		Select(c("v").Interpolate(ursus.InterpolateNearest())).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := rendered172(t, df, "v"); got != "1.5 1.5 4.5 4.5 4.5 " {
		t.Errorf("nearest of floats: %q", got)
	}

	// An instant keeps its type: nearer the later day on a tie.
	day := func(d int) time.Time { return time.Date(2024, 1, d, 0, 0, 0, 0, time.UTC) }
	ts := ursus.ValuesNullable("t", []time.Time{day(1), {}, day(3)}, []bool{true, false, true})
	df, err = ursus.Frame(ts).Select(c("t").Cast(dtype.Date).Interpolate(ursus.InterpolateNearest())).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if df.Schema().Field(0).Type != dtype.Date || rendered172(t, df, "t") != "2024-01-01 2024-01-03 2024-01-03 " {
		t.Errorf("nearest of dates: %s %q", df.Schema().Field(0).Type, rendered172(t, df, "t"))
	}
}

func TestRollingCenterAnswersAsPolars(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("v", []float64{1, 2, 3, 4, 5, 6}))
	for _, tc := range []struct {
		w             int
		want, partial string
	}{
		{2, "∅ 3 5 7 9 11 ", "1 3 5 7 9 11 "},
		{3, "∅ 6 9 12 15 ∅ ", "3 6 9 12 15 11 "},
		{4, "∅ ∅ 10 14 18 ∅ ", "3 6 10 14 18 15 "},
	} {
		df, err := frame.Select(
			c("v").RollingSum(tc.w, ursus.RollingCenter()).Alias("full"),
			c("v").RollingSum(tc.w, ursus.RollingCenter(), ursus.MinSamples(1)).Alias("partial"),
		).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got := rendered172(t, df, "full"); got != tc.want {
			t.Errorf("w=%d: %q, want %q", tc.w, got, tc.want)
		}
		if got := rendered172(t, df, "partial"); got != tc.partial {
			t.Errorf("w=%d, min_samples 1: %q, want %q", tc.w, got, tc.partial)
		}
	}
}

// TestRollingCenterIsPerPartition: each partition is centered in its own rows, and a
// centered and an uncentered window of one size in one Select are two answers.
func TestRollingCenterIsPerPartition(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(
		ursus.Values("g", []string{"a", "b", "a", "b", "a", "b", "a"}),
		ursus.Values("v", []float64{1, 10, 2, 20, 3, 30, 4}),
	)
	df, err := frame.Select(
		c("v").RollingMax(3, ursus.RollingCenter(), ursus.MinSamples(1)).Over(c("g")).Alias("centered"),
		c("v").RollingMax(3, ursus.MinSamples(1)).Over(c("g")).Alias("trailing"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// a: 1 2 3 4 → centered 2 3 4 4, trailing 1 2 3 4; b: 10 20 30 → 20 30 30, 10 20 30.
	want := map[string]string{
		"centered": "2 20 3 30 4 30 4 ",
		"trailing": "1 10 2 20 3 30 4 ",
	}
	for name, w := range want {
		if got := rendered172(t, df, name); got != w {
			t.Errorf("%s: %q, want %q", name, got, w)
		}
	}
}

// TestLinearAndNearestAreTwoAnswers: in one Select, Interpolate and
// Interpolate(InterpolateNearest()) are two window functions, not one shared.
func TestLinearAndNearestAreTwoAnswers(t *testing.T) {
	c := ursus.Col
	df, err := ursus.Frame(ursus.ValuesNullable("v", []int64{1, 0, 0, 7}, []bool{true, false, false, true})).
		Select(c("v").Interpolate().Alias("linear"), c("v").Interpolate(ursus.InterpolateNearest()).Alias("nearest")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if l, n := rendered172(t, df, "linear"), rendered172(t, df, "nearest"); l != "1 3 5 7 " || n != "1 1 7 7 " {
		t.Fatalf("linear %q, nearest %q", l, n)
	}
}
