package ursus_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// TestRollingMedianAndQuantileAnswerAsPolars: rolling median and quantile by rows
// and by time (step 227), against Polars 1.44.1: a null in the window, a NaN that
// sorts as the largest value, −0, MinSamples, a centred window, nearest and linear
// interpolation, an integer window, and windows by time where a null row still
// answers from its window.
func TestRollingMedianAndQuantileAnswerAsPolars(t *testing.T) {
	nan := math.NaN()
	ok := []bool{true, false, true, true, true, true, true, true, true, true}
	frame := ursus.Frame(
		ursus.ValuesNullable("v", []float64{3, 0, 1, 4, nan, 2, 2, math.Copysign(0, -1), 9, 5}, ok))
	c := ursus.Col
	got, err := frame.Select(
		c("v").RollingMedian(3).Alias("m3"),
		c("v").RollingMedian(3, ursus.MinSamples(1)).Alias("m3_1"),
		c("v").RollingQuantile(0.25, ursus.InterpNearest, 4, ursus.MinSamples(2)).Alias("q25n"),
		c("v").RollingQuantile(0.25, ursus.InterpLinear, 4, ursus.MinSamples(2)).Alias("q25l"),
		c("v").RollingMedian(4, ursus.MinSamples(1), ursus.RollingCenter()).Alias("mc"),
	).Collect(t.Context(), ursus.WithBatchSize(3))
	if err != nil {
		t.Fatal(err)
	}
	f := func(name string, v []float64, ok []bool) *ursus.Column {
		if ok == nil {
			return ursus.Values(name, v)
		}
		return ursus.ValuesNullable(name, v, ok)
	}
	want, err := ursus.Frame(
		f("m3", []float64{0, 0, 0, 0, 4, 4, 2, 2, 2, 5},
			[]bool{false, false, false, false, true, true, true, true, true, true}),
		f("m3_1", []float64{3, 3, 2, 2.5, 4, 4, 2, 2, 2, 5}, nil),
		f("q25n", []float64{0, 0, 1, 3, 4, 2, 2, 2, 2, 2},
			[]bool{false, false, true, true, true, true, true, true, true, true}),
		f("q25l", []float64{0, 0, 1.5, 2, 2.5, 1.75, 2, 1.5, 1.5, 1.5},
			[]bool{false, false, true, true, true, true, true, true, true, true}),
		f("mc", []float64{3, 2, 3, 4, 3, 3, 2, 2, 3.5, 5}, nil),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	// An Int32 window answers a Float64; Polars' rolling_quantile defaults to nearest.
	ints := ursus.Frame(ursus.ValuesNullable("i", []int32{5, 1, 0, 3, 3, 8},
		[]bool{true, true, false, true, true, true}))
	gotI, err := ints.Select(c("i").RollingMedian(2, ursus.MinSamples(1)).Alias("m"),
		c("i").RollingQuantile(0.5, ursus.InterpNearest, 2, ursus.MinSamples(1)).Alias("q")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantI, err := ursus.Frame(f("m", []float64{5, 3, 1, 3, 3, 5.5}, nil),
		f("q", []float64{5, 5, 1, 3, 3, 8}, nil)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, gotI, wantI)

	// By time: three minutes up to each row.
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var ts []time.Time
	for _, m := range []int{0, 1, 1, 3, 7, 8} {
		ts = append(ts, t0.Add(time.Duration(m)*time.Minute))
	}
	byT := ursus.Frame(ursus.Values("t", ts),
		ursus.ValuesNullable("v", []float64{4, 1, 0, 9, 2, 6}, []bool{true, true, false, true, true, true}))
	gotT, err := byT.Select(c("v").RollingMedianBy(c("t"), ursus.Every("3m")).Alias("m"),
		c("v").RollingQuantileBy(0.75, ursus.InterpHigher, c("t"), ursus.Every("3m")).Alias("q")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantT, err := ursus.Frame(f("m", []float64{4, 2.5, 2.5, 5, 2, 4}, nil),
		f("q", []float64{4, 4, 4, 9, 2, 6}, nil)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, gotT, wantT)

	// A quantile outside [0, 1] is refused, naming the function.
	_, err = frame.Select(c("v").RollingQuantile(1.5, ursus.InterpLinear, 3)).Collect(t.Context())
	if !errors.Is(err, uerr.ErrValue) || !strings.Contains(err.Error(), "rolling_quantile") {
		t.Errorf("a quantile of 1.5 should be refused, naming rolling_quantile: %v", err)
	}
}
