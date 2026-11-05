package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// M5 of v0.6.6-scope.md (step 283): round's modes, truncate, round_sig_figs, cot and
// the one-sided clips. Their answers are Polars 2.1.0's, recorded in the numodds
// group of testdata/oracle and spelled in oracle_numodds_test.go since step 496,
// which took this file's 118 hand-typed lines of values. What stays asserts the plan
// and the refusals.

func TestRoundModesAndTruncate(t *testing.T) {
	x := ursus.Col("x")
	floats := ursus.Frame(ursus.Values("x", []float64{0.5, 1.5}))
	text := ursus.Frame(ursus.Values("x", []string{"a"}))
	// The default's plan is the one it always had; a mode is the call's argument.
	if got := x.Round(2, ursus.RoundHalfAwayFromZero).String(); got != x.Round(2).String() {
		t.Errorf("an explicit default changed the plan: %s", got)
	}
	if got := x.Round(2, ursus.RoundHalfToEven).String(); !strings.Contains(got, "half_to_even") {
		t.Errorf("the mode is not in the plan: %s", got)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		"a mode that is none":   {floats.Select(x.Round(1, "up")), "not a rounding mode"},
		"two modes":             {floats.Select(x.Round(1, ursus.RoundHalfToEven, ursus.RoundHalfToEven)), "one mode"},
		"truncate to -1":        {floats.Select(x.Truncate(-1)), "must not be negative"},
		"truncate of text":      {text.Select(x.Truncate(0)), "requires a number"},
		"round to even of text": {text.Select(x.Round(0, ursus.RoundHalfToEven)), "numeric"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

func TestRoundSigFigs(t *testing.T) {
	x := ursus.Col("x")
	ints := ursus.Frame(ursus.Values("x", []int64{123456, -987}))
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "digits must be an integer >= 1".
		"0 figures": {ints.Select(x.RoundSigFigs(0)), "at least 1"},
		// Polars: "round_sig_figs can only be used on numeric types".
		"text":       {ursus.Frame(ursus.Values("x", []string{"a"})).Select(x.RoundSigFigs(2)), "requires a number"},
		"a Boolean":  {ursus.Frame(ursus.Values("x", []bool{true})).Select(x.RoundSigFigs(2)), "requires a number"},
		"a duration": {ints.Select(x.Cast(ursus.Duration(ursus.Micro)).RoundSigFigs(2)), "requires a number"},
		// Polars rounds a Decimal's digits; Round refuses one here, and so does this.
		"a Decimal": {ints.Select(x.Cast(ursus.Decimal(38, 2)).RoundSigFigs(2)), "requires a number other than a Decimal"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

func TestCot(t *testing.T) {
	// Polars: "trigonometry operation not supported for dtype `str`".
	if _, err := ursus.Frame(ursus.Values("x", []string{"a"})).Select(ursus.Col("x").Cot()).Collect(t.Context()); err == nil {
		t.Error("cot of text: no error")
	}
}
