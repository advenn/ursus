package ursus_test

// Step 148: an Over whose body is not one aggregate or ordered function hands its
// keys down to the windowed parts of that body — audit A8's Diff, PctChange and
// FillNull(FillMean) inside .Over(g), and a body such as (x - x.Mean()).Over(g).

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/advenn/ursus"
)

// a8Frame: g, x and t, with each group's rows interleaved, so a window that ignored
// its partition, or its order, would be visibly wrong.
func a8Frame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("g", []string{"a", "a", "b", "a", "b"}),
		ursus.Values("x", []int64{1, 4, 10, 9, 15}),
		ursus.Values("t", []int64{3, 1, 2, 2, 1}),
	)
}

// numbers reads column name of df, an Int64 or a Float64, as text: 4 significant
// digits, "null" for a null.
func numbers(t *testing.T, df *ursus.DataFrame, name string) []string {
	t.Helper()
	out := make([]string, df.Height())
	for i := range out {
		v, ok, err := df.At[float64](i, name)
		if err != nil {
			iv, iok, ierr := df.At[int64](i, name)
			if ierr != nil {
				t.Fatalf("%s: %v", name, err)
			}
			v, ok = float64(iv), iok
		}
		out[i] = "null"
		if ok {
			out[i] = strconv.FormatFloat(v, 'g', 4, 64)
		}
	}
	return out
}

func TestTheSugarInsideOver(t *testing.T) {
	c := ursus.Col
	g := c("g")
	byT := ursus.WindowSpec{PartitionBy: []ursus.Expr{g}, OrderBy: []ursus.SortKey{ursus.Asc(c("t"))}}
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want []string
	}{
		// a is rows 0, 1 and 3, b rows 2 and 4.
		{"Diff", c("x").Diff(1).Over(g), []string{"null", "3", "null", "5", "5"}},
		{"PctChange", c("x").PctChange(1).Over(g), []string{"null", "3", "null", "1.25", "0.5"}},
		// In t's order, a is rows 1, 3, 0 and b rows 4, 2.
		{"Diff in an order", c("x").Diff(1).OverWith(byT), []string{"-8", "null", "-5", "5", "null"}},
		{"Shift, written out", c("x").Sub(c("x").Shift(1)).Over(g), []string{"null", "3", "null", "5", "5"}},
		// a's mean is 14/3 and b's 12.5.
		{"demeaned", c("x").Sub(c("x").Mean()).Over(g), []string{"-3.667", "-0.6667", "-2.5", "4.333", "2.5"}},
		{"a running share", c("x").Cast(ursus.Float64).CumSum(false).Div(c("x").Cast(ursus.Float64).Sum()).Over(g),
			[]string{"0.07143", "0.3571", "0.4", "1", "1"}},
		{"an aggregate cast", c("x").Sum().Cast(ursus.Float64).Over(g), []string{"14", "14", "25", "14", "25"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df := collect(t, a8Frame().Select(tc.e.Alias("r")))
			if got := numbers(t, df, "r"); !slices.Equal(got, tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestFillNullByAGroupsAggregate(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a", "b", "b"}),
		ursus.ValuesNullable("y", []int64{1, 0, 3, 0, 10}, []bool{true, false, true, false, true}),
	)
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want []string
	}{
		{"mean, per group", c("y").FillNull(ursus.FillMean).Over(c("g")), []string{"1", "2", "3", "10", "10"}},
		{"min, per group", c("y").FillNull(ursus.FillMin).Over(c("g")), []string{"1", "1", "3", "10", "10"}},
		{"max, per group", c("y").FillNull(ursus.FillMax).Over(c("g")), []string{"1", "3", "3", "10", "10"}},
		// Without Over, the frame's: (1+3+10)/3.
		{"mean, the frame's", c("y").FillNull(ursus.FillMean), []string{"1", "4.667", "3", "4.667", "10"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df := collect(t, lf.Select(tc.e.Alias("r")))
			if got := numbers(t, df, "r"); !slices.Equal(got, tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestWindowsTheUserScopedStayRefused(t *testing.T) {
	c := ursus.Col
	g := c("g")
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		// The inner Over is the user's, and its scope is theirs.
		{"a user's window inside another", c("x").Mean().Over(c("t")).Sub(c("x")).Over(g),
			"may not contain another window"},
		// The CumSum would need the Shift computed below it.
		{"a window in an ordered function's operand", c("x").Diff(1).CumSum(false).Over(g),
			"may not contain another window"},
		{"nothing windowed", c("x").Add(1).Over(g), "is not a window function"},
		// An ordered window over an aggregate is refused, handed down or not.
		{"an order over an aggregate", c("x").Sub(c("x").Mean()).OverWith(ursus.WindowSpec{
			PartitionBy: []ursus.Expr{g}, OrderBy: []ursus.SortKey{ursus.Asc(c("t"))}}), "mean"},
	} {
		t.Run(tc.name, func(t *testing.T) { assertUserError(t, a8Frame().Select(tc.e), tc.want) })
	}
}

// TestTheSugarInsideOverAgreesWithItsSpelling: Diff and PctChange inside Over equal
// what they expand to, written with each window placed by hand, over random groups.
func TestTheSugarInsideOverAgreesWithItsSpelling(t *testing.T) {
	c := ursus.Col
	n := 2_000
	gs := make([]int64, n)
	xs := make([]float64, n)
	for i := range n {
		gs[i] = int64((i * 31) % 7)
		xs[i] = math.Floor(float64((i*7919)%1000)) / 10
	}
	lf := ursus.Frame(ursus.Values("g", gs), ursus.Values("x", xs))
	g := c("g")
	df := collect(t, lf.Select(
		c("x").Diff(2).Over(g).Alias("diff"),
		c("x").Sub(c("x").Shift(2).Over(g)).Alias("diff_by_hand"),
		c("x").PctChange(1).Over(g).Alias("pct"),
		c("x").Sub(c("x").Shift(1).Over(g)).Div(c("x").Shift(1).Over(g)).Alias("pct_by_hand"),
	))
	for _, pair := range [][2]string{{"diff", "diff_by_hand"}, {"pct", "pct_by_hand"}} {
		a, b := numbers(t, df, pair[0]), numbers(t, df, pair[1])
		if !slices.Equal(a, b) {
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("%s row %d: %s, by hand %s", pair[0], i, a[i], b[i])
				}
			}
		}
	}
	_ = fmt.Sprint
}
