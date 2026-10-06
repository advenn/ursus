package ursus_test

// Three temporal edges, by hand: audit.md's S18–S20.
//
// S18: a strict Int64 -> Time cast folded a count past a day into one, so 90000
// seconds became 01:00 — Polars refuses it, and makes it null when lossy. S19: a
// Duration cast to a coarser unit floored, -1.5s to -2s, while ursus's own
// TotalSeconds truncates it to -1s, as Polars does both. S20: Abs and Neg of the
// minimum Duration wrapped to itself, where every other overflowing temporal
// operation has been refused since step 61.

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// knownCastEdgeDefects names each case that answers wrongly today, with what it
// answers.
var knownCastEdgeDefects = map[string]string{}

func TestCastEdgesByHand(t *testing.T) {
	c := ursus.Col
	// ticks reads column name back as Int64 text, null as ∅.
	ticks := func(lf *ursus.LazyFrame, name string, want ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := lf.Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			_, got := textOf(t, df, name)
			if !slices.Equal(got, want) {
				return fmt.Sprintf("%v, want %v", got, want)
			}
			return ""
		}
	}
	refused := func(lf *ursus.LazyFrame, words ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := lf.Collect(t.Context())
			return refusal(df, err, ursus.ErrValue, words...)
		}
	}
	day := int64(24 * time.Hour)
	ints := func(v ...int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	durs := func(unit dtype.TimeUnit, v ...int64) *ursus.LazyFrame {
		return ints(v...).Select(c("x").Cast(dtype.Duration(unit)))
	}
	minDur := ursus.Frame(ursus.Values("x", []time.Duration{time.Duration(math.MinInt64), time.Second}))

	cases := []ioCase{
		// --- S18 ---
		{"strict Int64 -> Time past a day is refused", refused(
			ints(90000*int64(time.Second)).Select(c("x").Cast(dtype.Time(dtype.Nano))), "time of day")},
		{"lossy Int64 -> Time outside a day is null", ticks(
			ints(90000*int64(time.Second), -1, 3600*int64(time.Second)).
				Select(c("x").CastLossy(dtype.Time(dtype.Nano)).Cast(ursus.Int64)),
			"x", null, null, fmt.Sprint(3600*int64(time.Second)))},
		{"control: Int64 -> Time inside a day casts", ticks(
			ints(0, day-1).Select(c("x").Cast(dtype.Time(dtype.Nano)).Cast(ursus.Int64)),
			"x", "0", fmt.Sprint(day-1))},
		{"control: Datetime -> Time takes the time of day", ticks(
			ursus.Frame(ursus.Values("x", []time.Time{time.Date(2024, 3, 5, 1, 0, 0, 0, time.UTC)})).
				Select(c("x").Cast(dtype.Time(dtype.Nano)).Cast(ursus.Int64)),
			"x", fmt.Sprint(int64(time.Hour)))},

		// --- S19 ---
		{"Duration(ms) -> Duration(s) truncates", ticks(
			durs(dtype.Milli, -1500, 1500, -2000).Select(c("x").Cast(dtype.Duration(dtype.Second)).Cast(ursus.Int64)),
			"x", "-1", "1", "-2")},
		{"Duration(ns) -> Duration(us) truncates", ticks(
			durs(dtype.Nano, -1500, 999).Select(c("x").Cast(dtype.Duration(dtype.Micro)).Cast(ursus.Int64)),
			"x", "-1", "0")},
		{"control: a coarser Datetime still floors", ticks(
			ints(-1500).Select(c("x").Cast(dtype.Datetime(dtype.Milli, "")).
				Cast(dtype.Datetime(dtype.Second, "")).Cast(ursus.Int64)),
			"x", "-2")},

		// --- S20 ---
		{"Abs of the minimum Duration is refused", refused(minDur.Select(c("x").Abs()), "abs")},
		{"Neg of the minimum Duration is refused", refused(minDur.Select(c("x").Neg()), "neg")},
		{"control: Abs and Neg of an ordinary Duration", ticks(
			ursus.Frame(ursus.Values("x", []time.Duration{-time.Second})).
				Select(c("x").Abs().Cast(ursus.Int64).Alias("a")), "a", fmt.Sprint(int64(time.Second)))},
		// Integer arithmetic wraps, in ursus as in Polars; only temporal arithmetic
		// is checked.
		{"control: Abs of the minimum Int64 wraps", ticks(
			ints(math.MinInt64).Select(c("x").Abs()), "x", fmt.Sprint(int64(math.MinInt64)))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownCastEdgeDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownCastEdgeDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownCastEdgeDefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownCastEdgeDefects names %q, which is not a case", name)
		}
	}
}
