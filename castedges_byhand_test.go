package ursus_test

// Three temporal edges: audit.md's S18–S20.
//
// S18: a strict Int64 -> Time cast folded a count past a day into one, so 90000
// seconds became 01:00 — Polars refuses it, and makes it null when lossy. S19: a
// Duration cast to a coarser unit floored, -1.5s to -2s, while ursus's own
// TotalSeconds truncates it to -1s, as Polars does both. S20: Abs and Neg of the
// minimum Duration wrapped to itself, where every other overflowing temporal
// operation has been refused since step 61.
//
// The answers are Polars 2.1.0's, recorded in the castedges_byhand group of
// testdata/oracle and spelled in oracle_castedges_byhand_test.go since step 497,
// which took this file's hand-typed values. What stays asserts the refusals, and the
// unit Polars has not: seconds, a Duration(s) truncating and a Datetime(s) flooring,
// held to Go's own integer division over seeded ticks.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

func TestCastEdgesByHand(t *testing.T) {
	c := ursus.Col
	refused := func(lf *ursus.LazyFrame, words ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := lf.Collect(t.Context())
			return refusal(df, err, ursus.ErrValue, words...)
		}
	}
	ints := func(v ...int64) *ursus.LazyFrame { return ursus.Frame(ursus.Values("x", v)) }
	minDur := ursus.Frame(ursus.Values("x", []time.Duration{time.Duration(math.MinInt64), time.Second}))

	// Seeded milliseconds either side of a second, the hand cases among them.
	rng := rand.New(rand.NewPCG(497, 18))
	ms := []int64{-1500, 1500, -2000, -1, 0, 1, 999, -999, 1000, -1000}
	for range 200 {
		ms = append(ms, rng.Int64N(20_000_001)-10_000_000)
	}
	// toSeconds casts the ms column to a unit of seconds and reads the ticks back.
	toSeconds := func(to dtype.DataType, from dtype.DataType) []int64 {
		df, err := ints(ms...).Select(c("x").Cast(from).Cast(to).Cast(ursus.Int64)).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		col, err := df.Column[int64]("x")
		if err != nil {
			t.Fatal(err)
		}
		out := make([]int64, df.Height())
		for i := range out {
			out[i], _ = col.Get(i)
		}
		return out
	}

	cases := []ioCase{
		// --- S18 ---
		{"strict Int64 -> Time past a day is refused", refused(
			ints(90000*int64(time.Second)).Select(c("x").Cast(dtype.Time(dtype.Nano))), "time of day")},

		// --- S19: the second is a unit Polars has not ---
		{"Duration(ms) -> Duration(s) truncates toward zero, as Go's division does", func(t *testing.T) string {
			got := toSeconds(dtype.Duration(dtype.Second), dtype.Duration(dtype.Milli))
			want := make([]int64, len(ms))
			for i, v := range ms {
				want[i] = v / 1000
			}
			if !slices.Equal(got, want) {
				return fmt.Sprintf("%v, want %v", got, want)
			}
			return ""
		}},
		{"Datetime(ms) -> Datetime(s) floors", func(t *testing.T) string {
			got := toSeconds(dtype.Datetime(dtype.Second, ""), dtype.Datetime(dtype.Milli, ""))
			want := make([]int64, len(ms))
			for i, v := range ms {
				want[i] = int64(math.Floor(float64(v) / 1000))
			}
			if !slices.Equal(got, want) {
				return fmt.Sprintf("%v, want %v", got, want)
			}
			return ""
		}},

		// --- S20 ---
		{"Abs of the minimum Duration is refused", refused(minDur.Select(c("x").Abs()), "abs")},
		{"Neg of the minimum Duration is refused", refused(minDur.Select(c("x").Neg()), "neg")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if wrong := tc.run(t); wrong != "" {
				t.Error(wrong)
			}
		})
	}
}
