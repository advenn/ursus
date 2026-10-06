package ursus_test

// A filter whose only cast cannot fail is pushed into a join side, by hand.
//
// Step 78 stopped pushing a FALLIBLE conjunct below a guard or into a join side,
// and counted every strict cast as fallible unless totalCast called it total —
// which knew no temporal cast at all. PDS-H q7 compares a Date column with
// `Lit(time).Cast(ursus.Date)`: a Datetime(ns) literal narrowed to days, which can
// never fail. Its date filter therefore stayed above five joins, and q7 went from
// 2.4 s to 3.9 s and from 0.60 GB to 1.68 GB between step 40's report and 0.3.

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
)

// knownCastPushDefects names each case that answers wrongly today, with what it
// answers.
var knownCastPushDefects = map[string]string{
	"Datetime(ns) literal cast to Date: pushed into the join side": "stays above the join",
	"Datetime(us) column cast to Date: pushed":                     "stays above the join",
	"Datetime(ns) cast to a coarser Datetime(ms): pushed":          "stays above the join",
	"Datetime cast to Time: pushed":                                "stays above the join",
}

// filterBelowJoin reports whether lf's optimized plan has its FILTER beneath its
// JOIN — inside a side — rather than above it.
func filterBelowJoin(t *testing.T, lf *ursus.LazyFrame) (bool, string) {
	t.Helper()
	plan, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f, j := strings.Index(plan, "FILTER"), strings.Index(plan, "JOIN")
	if f < 0 || j < 0 {
		t.Fatalf("no FILTER or no JOIN in the plan:\n%s", plan)
	}
	return f > j, plan
}

func TestCastPushByHand(t *testing.T) {
	c := ursus.Col
	day := func(d int) time.Time { return time.Date(1995, 1, d, 0, 0, 0, 0, time.UTC) }
	left := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("k", []int64{1, 2, 3}),
			ursus.Values("ts", []time.Time{day(1), day(5), day(9)}),
		).WithColumns(c("ts").Cast(ursus.Date).Alias("d"))
	}
	right := ursus.Frame(ursus.Values("k", []int64{1, 2, 3}), ursus.Values("v", []int64{10, 20, 30}))
	joined := func(pred ursus.Expr) *ursus.LazyFrame {
		return left().Join(right, ursus.JoinOn(c("k"))).Filter(pred)
	}
	pushed := func(pred ursus.Expr) func(*testing.T) string {
		return func(t *testing.T) string {
			if below, plan := filterBelowJoin(t, joined(pred)); !below {
				return "stays above the join:\n" + plan
			}
			return ""
		}
	}
	stays := func(pred ursus.Expr) func(*testing.T) string {
		return func(t *testing.T) string {
			if below, plan := filterBelowJoin(t, joined(pred)); below {
				return "pushed below the join, where it can fail on rows the join removes:\n" + plan
			}
			return ""
		}
	}

	cases := []ioCase{
		{"Datetime(ns) literal cast to Date: pushed into the join side",
			pushed(c("d").Ge(ursus.Lit(day(4)).Cast(ursus.Date)))},
		{"Datetime(us) column cast to Date: pushed",
			pushed(c("ts").Cast(dtype.Datetime(dtype.Micro, "")).Cast(ursus.Date).Ge(ursus.Lit(day(4)).Cast(ursus.Date)))},
		{"Datetime(ns) cast to a coarser Datetime(ms): pushed",
			pushed(c("ts").Cast(dtype.Datetime(dtype.Milli, "")).Ge(ursus.Lit(day(4)).Cast(dtype.Datetime(dtype.Milli, ""))))},
		{"Datetime cast to Time: pushed",
			pushed(c("ts").Cast(dtype.Time(dtype.Nano)).Ge(ursus.Lit(int64(0)).Cast(dtype.Time(dtype.Nano))))},
		{"control: a comparison with no cast is pushed", pushed(c("ts").Ge(ursus.Lit(day(4))))},

		// Casts that CAN fail stay where they were written, as step 78 decided.
		{"control: Date to Datetime(ns) can overflow, and stays", stays(
			c("d").Cast(dtype.Datetime(dtype.Nano, "UTC")).Ge(ursus.Lit(day(4))))},
		{"control: Datetime(s) to Date can overflow, and stays", stays(
			c("ts").Cast(dtype.Datetime(dtype.Second, "")).Cast(ursus.Date).Ge(ursus.Lit(day(4)).Cast(ursus.Date)))},
		{"control: Int64 to Time can fall outside the day, and stays", stays(
			c("k").Cast(dtype.Time(dtype.Nano)).Ge(ursus.Lit(int64(0)).Cast(dtype.Time(dtype.Nano))))},

		{"the pushed filter answers as written", func(t *testing.T) string {
			df, err := joined(c("d").Ge(ursus.Lit(day(4)).Cast(ursus.Date))).
				Sort(ursus.Asc(c("k"))).Select(c("k"), c("v")).Collect(t.Context())
			if err != nil {
				return err.Error()
			}
			_, got := textOf(t, df, "v")
			if !slices.Equal(got, []string{"20", "30"}) {
				return fmt.Sprintf("%v, want [20 30]", got)
			}
			return ""
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownCastPushDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownCastPushDefects (%s)", why)
			case known:
				t.Logf("known defect (%s)", why)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownCastPushDefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownCastPushDefects names %q, which is not a case", name)
		}
	}
}
