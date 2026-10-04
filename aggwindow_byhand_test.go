package ursus_test

// Aggregations and windows, against answers worked out by hand: audit.md §7.
//
// Each case is a silent wrong answer, a crash or a refusal that the audit measured,
// answered under the rule this step chose for it — most of them the answer Polars
// and DuckDB already agree on, and where they differ, ursus's own rule: an integer
// mean is exact because an integer Sum is, and a variance is never negative.

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// knownAggDefects names each case that answers wrongly today, with what it answers.
var knownAggDefects = map[string]string{
	"A13 Product of Int64 [3, 0, -5]":    "-0",
	"A13 GroupBy().Agg() of nothing":     "shape (0, 0)",
	"A14 GroupByDynamic with Closed(99)": "answered as ClosedLeft",
	"A14 IsBetween with Closed(99)":      "answered as ClosedLeft",
}

type aggCase struct {
	name   string
	got    *ursus.LazyFrame
	want   *ursus.LazyFrame              // the hand answer, or
	check  func(*ursus.DataFrame) string // a property of it, or nil with
	refuse []string                      // a value refusal naming these
}

func TestAggregationsAndWindowsByHand(t *testing.T) {
	c := ursus.Col
	f64 := func(name string, v ...float64) *ursus.LazyFrame { return ursus.Frame(ursus.Values(name, v)) }
	one := func(lf *ursus.LazyFrame, e ursus.Expr) *ursus.LazyFrame { return lf.GroupBy().Agg(e) }
	inf := math.Inf(1)
	const big = 1<<53 + 1

	cases := []aggCase{
		// --- A1: ShiftFill's fill meets the column at a common type ---
		{name: "A1 Int64 ShiftFill(1, 1.5)",
			got:  ursus.Frame(ursus.Values("v", []int64{1, 2, 3})).Select(c("v").ShiftFill(1, 1.5)),
			want: f64("v", 1.5, 1, 2)},
		{name: "A1 Int32 ShiftFill(1, 0)",
			got:  ursus.Frame(ursus.Values("v", []int32{1, 2, 3})).Select(c("v").ShiftFill(1, 0)),
			want: ursus.Frame(ursus.Values("v", []int32{0, 1, 2}))},
		{name: "control: Int64 ShiftFill(1, int64(-1))",
			got:  ursus.Frame(ursus.Values("v", []int64{1, 2, 3})).Select(c("v").ShiftFill(1, int64(-1))),
			want: ursus.Frame(ursus.Values("v", []int64{-1, 1, 2}))},

		// --- A2: a quantile at an exact rank is that value; nothing overflows ---
		{name: "A2 Quantile(0) of [-inf, 1, 2]",
			got:  one(f64("v", -inf, 1, 2), c("v").Quantile(0, ursus.InterpLinear)),
			want: f64("v", -inf)},
		{name: "A2 Quantile(1) of [1, 2, inf]",
			got:  one(f64("v", 1, 2, inf), c("v").Quantile(1, ursus.InterpLinear)),
			want: f64("v", inf)},
		{name: "A2 Median of [-1.7e308, 1.7e308]",
			got:  one(f64("v", -1.7e308, 1.7e308), c("v").Median()),
			want: f64("v", 0)},
		{name: "A2 midpoint of [1e308, 1.5e308]",
			got:  one(f64("v", 1e308, 1.5e308), c("v").Quantile(0.5, ursus.InterpMidpoint)),
			want: f64("v", 1.25e308)},
		{name: "A2 midpoint of [1.7e308, 1.7e308]",
			got:  one(f64("v", 1.7e308, 1.7e308), c("v").Quantile(0.5, ursus.InterpMidpoint)),
			want: f64("v", 1.7e308)},
		{name: "control: Median of [1.7e308, 1.7e308]",
			got:  one(f64("v", 1.7e308, 1.7e308), c("v").Median()),
			want: f64("v", 1.7e308)},

		// --- A3: PctChange computes in float, not at the column's width ---
		{name: "A3 PctChange of UInt8 [3, 1, 255, 0]",
			got: ursus.Frame(ursus.Values("v", []uint8{3, 1, 255, 0})).Select(c("v").PctChange(1)),
			want: ursus.Frame(ursus.ValuesNullable("v", []float64{0, -2.0 / 3, 254, -1},
				[]bool{false, true, true, true}))},
		{name: "A3 PctChange of Int8 [100, -100, 50]",
			got: ursus.Frame(ursus.Values("v", []int8{100, -100, 50})).Select(c("v").PctChange(1)),
			want: ursus.Frame(ursus.ValuesNullable("v", []float64{0, -2, -1.5},
				[]bool{false, true, true}))},

		// --- A4: an integer mean divides an exact sum ---
		// (2^53+1 + 2^53+2)/2 = 2^53+1.5, whose nearest float64 is 2^53+2. Summed in
		// float64, 2^53+1 is already 2^53 and the mean comes out 2^53.
		{name: "A4 group mean of [2^53+1, 2^53+2]",
			got:  one(ursus.Frame(ursus.Values("v", []int64{big, big + 1})), c("v").Mean()),
			want: f64("v", 1<<53+2)},
		{name: "A4 window mean of [2^53+1, 2^53+2]",
			got: ursus.Frame(ursus.Values("g", []int64{1, 1}), ursus.Values("v", []int64{big, big + 1})).
				Select(c("v").Mean().Over(c("g"))),
			want: f64("v", 1<<53+2, 1<<53+2)},
		{name: "A4 list mean of [2^53+1, 2^53+2]",
			got: ursus.Frame(ursus.Values("g", []int64{1, 1}), ursus.Values("v", []int64{big, big + 1})).
				GroupBy(c("g")).Agg(c("v").Implode()).Select(c("v").List().Mean()),
			want: f64("v", 1<<53+2)},

		// --- A5: a variance that overflows is +Inf, never negative ---
		{name: "A5 Var(0) of [1e308, -1e308]",
			got:  one(f64("v", 1e308, -1e308), c("v").Var(0)),
			want: f64("v", inf)},
		{name: "A5 Std(0) of [1e308, -1e308]",
			got:  one(f64("v", 1e308, -1e308), c("v").Std(0)),
			want: f64("v", inf)},
		{name: "control: Var(0) of [1e308, 1e308]",
			got:  one(f64("v", 1e308, 1e308), c("v").Var(0)),
			want: f64("v", 0)},

		// --- A10: Sum of a Bool counts its trues ---
		{name: "A10 Sum of [true, false, true, null]",
			got: one(ursus.Frame(ursus.ValuesNullable("v", []bool{true, false, true, false},
				[]bool{true, true, true, false})), c("v").Sum()),
			want: ursus.Frame(ursus.Values("v", []int64{2})).Select(c("v").Cast(ursus.Int128))},
		{name: "A10 grouped Sum of a Bool",
			got: ursus.Frame(ursus.Values("g", []int64{1, 2, 1, 2}), ursus.Values("v", []bool{true, true, true, false})).
				GroupBy(c("g")).Agg(c("v").Sum()).Sort(ursus.Asc(c("g"))),
			want: ursus.Frame(ursus.Values("g", []int64{1, 2}), ursus.Values("v", []int64{2, 1})).
				Select(c("g"), c("v").Cast(ursus.Int128))},
		{name: "A10 CumSum of [true, null, true, false]",
			got: ursus.Frame(ursus.ValuesNullable("v", []bool{true, false, true, false},
				[]bool{true, false, true, true})).Select(c("v").CumSum(false)),
			want: ursus.Frame(ursus.ValuesNullable("v", []int64{1, 0, 2, 2}, []bool{true, false, true, true})).
				Select(c("v").Cast(ursus.Int128))},

		// --- A13 ---
		{name: "A13 Product of Int64 [3, 0, -5]",
			got: one(ursus.Frame(ursus.Values("v", []int64{3, 0, -5})), c("v").Product()),
			check: func(df *ursus.DataFrame) string {
				v, ok, err := df.At[float64](0, "v")
				if err != nil || !ok || v != 0 || math.Signbit(v) {
					return fmt.Sprintf("product = %v (sign bit %v), want +0", v, math.Signbit(v))
				}
				return ""
			}},
		{name: "A13 GroupBy().Agg() of nothing",
			got: ursus.Frame(ursus.Values("v", []int64{1, 2, 3})).GroupBy().Agg(),
			check: func(df *ursus.DataFrame) string {
				if h, w := df.Shape(); h != 1 || w != 0 {
					return fmt.Sprintf("shape (%d, %d), want (1, 0): a global aggregate is one row", h, w)
				}
				return ""
			}},

		// --- A14: an undeclared Closed is refused ---
		{name: "A14 GroupByDynamic with Closed(99)",
			got: hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:05:00").
				GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("1h"), Closed: ursus.Closed(99)}).
				Agg(ursus.Len()),
			refuse: []string{"Closed"}},
		{name: "A14 IsBetween with Closed(99)",
			got:    ursus.Frame(ursus.Values("v", []int64{1, 2, 3})).Select(c("v").IsBetween(1, 3, ursus.Closed(99))),
			refuse: []string{"Closed"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := aggWrong(t, tc)
			why, known := knownAggDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownAggDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownAggDefects {
		if !slices.ContainsFunc(cases, func(c aggCase) bool { return c.name == name }) {
			t.Errorf("knownAggDefects names %q, which is not a case", name)
		}
	}
}

func aggWrong(t *testing.T, tc aggCase) string {
	df, err := tc.got.Collect(t.Context())
	if tc.refuse != nil {
		switch {
		case err == nil:
			return fmt.Sprintf("answered %s; want a refusal", strings.Join(strings.Fields(df.String()), " "))
		case !errors.Is(err, ursus.ErrValue):
			return fmt.Sprintf("a %s error, want a value error: %v", kindOf(err), err)
		}
		for _, w := range tc.refuse {
			if !strings.Contains(err.Error(), w) {
				return fmt.Sprintf("the refusal does not say %q: %v", w, err)
			}
		}
		return ""
	}
	if err != nil {
		if errors.Is(err, ursus.ErrInternal) {
			return "ErrInternal: " + err.Error()
		}
		return err.Error()
	}
	if tc.check != nil {
		return tc.check(df)
	}
	want, err := tc.want.Collect(t.Context())
	if err != nil {
		t.Fatalf("the hand answer: %v", err)
	}
	return framesDiffer(t, df, want)
}
