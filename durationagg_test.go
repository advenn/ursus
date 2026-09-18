package ursus_test

// sum and mean over a Duration, which accumulated in float64.
//
// The values here are ordinary: 2^53 nanoseconds is 104 days, which any "total time
// per user" query reaches. What made the defect invisible is that a Duration renders
// as a span whatever its bits say, so a lost tick and a sign flip both print as
// plausible answers.

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

const p53 = int64(1) << 53

func durations(name string, vals ...time.Duration) *ursus.LazyFrame {
	return ursus.Frame(ursus.Values(name, vals))
}

// TestDurationSumIsExact: the smallest question — one row — which sum answered one
// tick short while min and max beside it answered right.
func TestDurationSumIsExact(t *testing.T) {
	got, err := durations("d", time.Duration(p53+1)).GroupBy().Agg(
		ursus.Col("d").Sum().Alias("sum"),
		ursus.Col("d").Mean().Alias("mean"),
		ursus.Col("d").Min().Alias("min"),
		ursus.Col("d").Max().Alias("max"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(
		ursus.Values("sum", []time.Duration{time.Duration(p53 + 1)}),
		ursus.Values("mean", []time.Duration{time.Duration(p53 + 1)}),
		ursus.Values("min", []time.Duration{time.Duration(p53 + 1)}),
		ursus.Values("max", []time.Duration{time.Duration(p53 + 1)}),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// TestDurationSumDoesNotLoseIncrements: every +1 onto 2^53 lands on a tie and rounds
// back in float64, so a million nanoseconds could disappear from a total.
func TestDurationSumDoesNotLoseIncrements(t *testing.T) {
	vals := []time.Duration{time.Duration(p53)}
	for range 100 {
		vals = append(vals, 1)
	}
	for _, threads := range []int{1, 4} {
		got, err := durations("d", vals...).GroupBy().
			Agg(ursus.Col("d").Sum().Alias("sum")).
			Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(7))
		if err != nil {
			t.Fatalf("%d threads: %v", threads, err)
		}
		s, err := got.Column[int64]("sum")
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := s.Get(0); v != p53+100 {
			t.Errorf("%d threads: sum = %d, want %d", threads, v, p53+100)
		}
	}
}

// TestPartialSumsMayLeaveInt64 is the property the 128-bit accumulator buys, and the
// reason the narrowing happens once at the end rather than as it goes.
func TestPartialSumsMayLeaveInt64(t *testing.T) {
	const max = time.Duration(math.MaxInt64)
	for _, threads := range []int{1, 4} {
		got, err := durations("d", max, max, -max, -max).GroupBy().
			Agg(ursus.Col("d").Sum().Alias("sum")).
			Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(1))
		if err != nil {
			t.Fatalf("%d threads: a total of zero must not be refused: %v", threads, err)
		}
		s, err := got.Column[int64]("sum")
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := s.Get(0); v != 0 {
			t.Errorf("%d threads: sum = %d, want 0", threads, v)
		}
	}
}

// TestDurationSumRefusesRatherThanWrapping. Col("d").Add(Col("d")) has refused on
// this data since step 61; before this step sum wrapped it to a negative span, so the
// two paths contradicted each other over the same values.
func TestDurationSumRefusesRatherThanWrapping(t *testing.T) {
	const max = time.Duration(math.MaxInt64)
	_, err := durations("d", max, max).GroupBy().
		Agg(ursus.Col("d").Sum().Alias("sum")).
		Collect(t.Context(), ursus.WithThreads(1))

	var ue *uerr.Error
	if !errors.As(err, &ue) || !errors.Is(&uerr.Error{Kind: ue.Kind}, uerr.ErrValue) {
		t.Fatalf("want a KindValue refusal, got %v", err)
	}
	if ue.Op != "sum" {
		t.Errorf("op is %q, want \"sum\" so a caller can tell this from an arithmetic refusal", ue.Op)
	}
	for _, want := range []string{"18446744073709551614", "overflows Duration(ns)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say %q: %v", want, err)
		}
	}
}

// TestDurationMeanIsExactPastInt64: the mean of values that each fit always fits,
// even when their sum does not — which is why mean carries the exact sum rather than
// borrowing sum's refusal.
func TestDurationMeanIsExactPastInt64(t *testing.T) {
	const max = time.Duration(math.MaxInt64)
	got, err := durations("d", max, max, max).GroupBy().
		Agg(ursus.Col("d").Mean().Alias("mean")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m, err := got.Column[int64]("mean")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := m.Get(0); v != math.MaxInt64 {
		t.Errorf("mean = %d, want %d", v, int64(math.MaxInt64))
	}
}

// TestDurationMeanTruncatesTowardZero states the rounding rather than letting it be
// whatever the arithmetic happens to do. A floor would answer -2 for the first case.
//
// The fixture is asserted to be negative: with only positive values the two rules
// agree and this test would pass against either.
func TestDurationMeanTruncatesTowardZero(t *testing.T) {
	for _, c := range []struct {
		vals []time.Duration
		want int64
	}{
		{[]time.Duration{-1, -2}, -1},
		{[]time.Duration{1, 2}, 1},
		{[]time.Duration{-1, -1, -1}, -1},
		{[]time.Duration{-5, 2}, -1},
	} {
		var sum time.Duration
		for _, v := range c.vals {
			sum += v
		}
		if c.want < 0 && sum >= 0 {
			t.Fatalf("%v: the fixture must have a negative mean, or truncation and "+
				"flooring agree and this proves nothing", c.vals)
		}
		got, err := durations("d", c.vals...).GroupBy().
			Agg(ursus.Col("d").Mean().Alias("mean")).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		m, err := got.Column[int64]("mean")
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := m.Get(0); v != c.want {
			t.Errorf("mean%v = %d, want %d (truncating toward zero)", c.vals, v, c.want)
		}
	}
}

// TestDurationSumAgreesWithArithmetic: sum(d) and d + d must answer the same way on
// the same data, which is the whole reason the aggregate refuses.
func TestDurationSumAgreesWithArithmetic(t *testing.T) {
	const max = time.Duration(math.MaxInt64)

	_, sumErr := durations("d", max, max).GroupBy().
		Agg(ursus.Col("d").Sum()).Collect(t.Context(), ursus.WithThreads(1))
	_, addErr := durations("d", max).
		Select(ursus.Col("d").Add(ursus.Col("d"))).Collect(t.Context(), ursus.WithThreads(1))

	if (sumErr == nil) != (addErr == nil) {
		t.Errorf("sum and + disagree about the same data:\n  sum: %v\n  add: %v", sumErr, addErr)
	}
}
