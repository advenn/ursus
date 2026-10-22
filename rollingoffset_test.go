package ursus_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestRollingOffsetAsPolars (step 313): a rolling group's Offset is where each row's
// window starts, as Polars' offset is, (t + offset, t + offset + period], minus the
// period by default; NoOffset is Polars' "0d". A window whose offset is the period's
// length behind its row ends at the row, a month measured as 28 days. Each window is
// listed by its rows' values, joined by "|". Every answer is Polars 1.44.1's.
func TestRollingOffsetAsPolars(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	ds := []time.Time{at(2024, 1, 31, 0), at(2024, 2, 28, 0), at(2024, 2, 29, 0), at(2024, 3, 1, 0),
		at(2024, 3, 29, 0), at(2024, 3, 30, 0), at(2024, 3, 31, 0)}
	days := ursus.Frame(ursus.Values("t", ds), i64s("v", 1, 2, 3, 4, 5, 6, 7)).WithColumns(c("t").Cast(ursus.Date))
	ints := ursus.Frame(i64s("t", 1, 3, 5, 6), i64s("v", 1, 2, 3, 4))
	every := ursus.Every
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		o    ursus.RollingOptions
		want []string
	}{
		{"1mo", days, ursus.RollingOptions{Period: every("1mo")}, []string{"1", "1|2", "1|2|3", "2|3|4", "4|5", "4|5|6", "4|5|6|7"}},
		// Minus the period, given: the default, ending at the row, not at
		// February 29 plus a month.
		{"1mo, offset -1mo", days, ursus.RollingOptions{Period: every("1mo"), Offset: every("-1mo")},
			[]string{"1", "1|2", "1|2|3", "2|3|4", "4|5", "4|5|6", "4|5|6|7"}},
		{"1mo, no offset", days, ursus.RollingOptions{Period: every("1mo"), Offset: ursus.NoOffset()},
			[]string{"2|3", "3|4", "4|5", "5|6|7", "6|7", "7", ""}},
		{"1mo, offset -2d", days, ursus.RollingOptions{Period: every("1mo"), Offset: every("-2d")},
			[]string{"1|2|3", "2|3|4", "2|3|4", "3|4", "5|6|7", "5|6|7", "6|7"}},
		// 28 days is a month to Polars, so the window ends at its row.
		{"1mo, offset -28d", days, ursus.RollingOptions{Period: every("1mo"), Offset: every("-28d")},
			[]string{"1", "2", "2|3", "2|3|4", "5", "5|6", "5|6|7"}},
		{"1mo, offset -4w", days, ursus.RollingOptions{Period: every("1mo"), Offset: every("-4w")},
			[]string{"1", "2", "2|3", "2|3|4", "5", "5|6", "5|6|7"}},
		{"1mo, offset -40d, wholly behind", days, ursus.RollingOptions{Period: every("1mo"), Offset: every("-40d")},
			[]string{"", "1", "1", "1", "2|3|4", "2|3|4", "2|3|4"}},
		{"3d, offset -1d", days, ursus.RollingOptions{Period: every("3d"), Offset: every("-1d")},
			[]string{"1", "2|3|4", "3|4", "4", "5|6|7", "6|7", "7"}},
		{"3d, offset 1d, closed left", days, ursus.RollingOptions{Period: every("3d"), Offset: every("1d"), Closed: ursus.ClosedLeft},
			[]string{"", "3|4", "4", "", "6|7", "7", ""}},
		{"1mo, closed both", days, ursus.RollingOptions{Period: every("1mo"), Closed: ursus.ClosedBoth},
			[]string{"1", "1|2", "1|2|3", "2|3|4", "3|4|5", "3|4|5|6", "3|4|5|6|7"}},
		{"2i", ints, ursus.RollingOptions{Period: every("2i")}, []string{"1", "2", "3", "3|4"}},
		{"2i, no offset", ints, ursus.RollingOptions{Period: every("2i"), Offset: ursus.NoOffset()}, []string{"2", "3", "4", ""}},
		{"2i, offset -3i", ints, ursus.RollingOptions{Period: every("2i"), Offset: every("-3i")}, []string{"", "1", "2", "3"}},
		{"2i, offset 1i", ints, ursus.RollingOptions{Period: every("2i"), Offset: every("1i")}, []string{"2", "3|4", "", ""}},
		{"2i, offset -2i", ints, ursus.RollingOptions{Period: every("2i"), Offset: every("-2i")}, []string{"1", "2", "3", "3|4"}},
		{"2i, offset -1i", ints, ursus.RollingOptions{Period: every("2i"), Offset: every("-1i")}, []string{"1", "2", "3|4", "4"}},
	} {
		if got := windowMembers(ctx, t, tc.lf.Rolling(c("t"), tc.o)); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}

	// An offset of none is shown, as the default is not.
	for _, tc := range []struct {
		o    ursus.RollingOptions
		want string
	}{
		{ursus.RollingOptions{Period: every("2i"), Offset: ursus.NoOffset()}, "offset=0s"},
		{ursus.RollingOptions{Period: every("2i"), Offset: every("-1i")}, "offset=-1i"},
	} {
		plan, err := ints.Rolling(c("t"), tc.o).Agg(ursus.Len()).Explain(ctx)
		if err != nil || !strings.Contains(plan, tc.want) {
			t.Errorf("the plan says nothing of %q: %v\n%s", tc.want, err, plan)
		}
	}
	if plan, err := ints.Rolling(c("t"), ursus.RollingOptions{Period: every("2i")}).Agg(ursus.Len()).Explain(ctx); err != nil ||
		strings.Contains(plan, "offset=") {
		t.Errorf("the default offset is shown: %v\n%s", err, plan)
	}
}

// windowMembers is each of a rolling group's windows as its rows' values, joined by
// "|", in the rows' order.
func windowMembers(ctx context.Context, t *testing.T, g *ursus.GroupBy) []string {
	t.Helper()
	df, err := g.Agg(ursus.Col("v").Implode().List().Eval(ursus.Element().Cast(ursus.String)).List().Join("|").Alias("vs")).
		Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	col, err := df.Column[string]("vs")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, col.Len())
	for i := range out {
		out[i], _ = col.Get(i)
	}
	return out
}
