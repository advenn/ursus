package ursus_test

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// wf is the window fixture: three partitions of different sizes, a null value, a
// tie, and a partition whose only value is null.
func wf() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("g", []string{"a", "b", "a", "b", "a", "c"}),
		ursus.ValuesNullable("v",
			[]int64{1, 10, 3, 20, 3, 0},
			[]bool{true, true, true, true, true, false}),
		ursus.Values("t", []int64{0, 1, 2, 3, 4, 5}), // a stable order key
	)
}

func u32(t *testing.T, df *ursus.DataFrame, name string) []uint32 {
	t.Helper()
	s, err := df.Column[uint32](name)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]uint32, df.Height())
	for i := range df.Height() {
		out[i], _ = s.Get(i)
	}
	return out
}

// TestWindowAcceptedWhereAggregateIsNot is the scoping change, stated directly.
//
// The same aggregate is illegal bare and legal under a window, in every
// row-preserving context — because a window preserves the frame's height and a bare
// aggregate does not. Before this step the seven rejectAggregate sites could not
// tell the two apart.
func TestWindowAcceptedWhereAggregateIsNot(t *testing.T) {
	cases := []struct {
		name string
		bare func() *ursus.LazyFrame
		win  func() *ursus.LazyFrame
	}{
		{"select",
			func() *ursus.LazyFrame { return wf().Select(ursus.Col("v").Sum()) },
			func() *ursus.LazyFrame {
				return wf().Select(ursus.Col("v").Sum().Over(ursus.Col("g")))
			}},
		{"with_columns",
			func() *ursus.LazyFrame { return wf().WithColumns(ursus.Col("v").Sum().Alias("s")) },
			func() *ursus.LazyFrame {
				return wf().WithColumns(ursus.Col("v").Sum().Over(ursus.Col("g")).Alias("s"))
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.bare().Collect(t.Context()); err == nil {
				t.Error("a bare aggregate must still be refused here")
			} else if !strings.Contains(err.Error(), "aggregate expression is not allowed") {
				t.Errorf("wrong refusal for the bare aggregate: %v", err)
			}
			df, err := c.win().Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("the same aggregate under a window must be accepted: %v", err)
			}
			if df.Height() != 6 {
				t.Errorf("height = %d, want 6 — a window preserves height", df.Height())
			}
		})
	}
}

// TestWindowRejectedInsideGroupByAgg: a window preserves height, so it is not an
// aggregate and cannot be one of GroupBy's outputs.
func TestWindowRejectedInsideGroupByAgg(t *testing.T) {
	_, err := wf().
		GroupBy(ursus.Col("g")).
		Agg(ursus.Col("v").Sum().Over(ursus.Col("g"))).
		Collect(t.Context())
	if err == nil {
		t.Fatal("a window inside GroupBy().Agg() must be refused")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}
}

// TestWindowNamesAfterItsValue pins the OutputName case.
//
// Leftmost-column-wins would name the result after the PARTITION key — a column
// that need not appear in the output at all.
func TestWindowNamesAfterItsValue(t *testing.T) {
	df, err := wf().Select(ursus.Col("v").Sum().Over(ursus.Col("g"))).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Schema().Names()[0]; got != "v" {
		t.Errorf("column name = %q, want %q — leftmost-column-wins would say \"g\"", got, "v")
	}
}

// TestWindowStringRendersSpec is the step-7 quantile bug, one node along.
//
// The planner deduplicates extracted windows by keying a map on String(). Two
// windows that render identically become ONE computation, so a String that hid the
// partition key would give every row the wrong partition's answer with no error.
func TestWindowStringRendersSpec(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "b", "b"}),
		ursus.Values("h", []string{"x", "y", "x", "y"}),
		ursus.Values("v", []int64{1, 2, 4, 8}),
	).Select(
		ursus.Col("v").Sum().Over(ursus.Col("g")).Alias("byG"),
		ursus.Col("v").Sum().Over(ursus.Col("h")).Alias("byH"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	byG, err := df.Column[ursus.Int128Value]("byG")
	if err != nil {
		t.Fatal(err)
	}
	byH, err := df.Column[ursus.Int128Value]("byH")
	if err != nil {
		t.Fatal(err)
	}
	// g partitions {a:[1,2], b:[4,8]} -> 3,3,12,12.
	// h partitions {x:[1,4], y:[2,8]} -> 5,10,5,10.
	wantG := []string{"3", "3", "12", "12"}
	wantH := []string{"5", "10", "5", "10"}
	for i := range 4 {
		g, _ := byG.Get(i)
		h, _ := byH.Get(i)
		if g.String() != wantG[i] || h.String() != wantH[i] {
			t.Fatalf("row %d: byG=%s byH=%s, want %s and %s — equal values in both "+
				"columns would mean the two windows collapsed into one temporary",
				i, g, h, wantG[i], wantH[i])
		}
	}
}

// TestWindowPushdownKeepsAggregatedColumn: the window's operands are ordinary
// children precisely so RootNames sees through them.
//
// If the aggregated child were hidden — the "nested scope body" treatment — the
// projection pushdown would report only the partition key as needed and prune the
// very column being aggregated out of the scan.
func TestWindowPushdownKeepsAggregatedColumn(t *testing.T) {
	plan, err := wf().
		Select(ursus.Col("v").Mean().Over(ursus.Col("g")).Alias("m")).
		Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Both the partition key and the aggregated column must survive; `t` must not,
	// because nothing reads it.
	for _, want := range []string{"g", "v"} {
		if !strings.Contains(plan, "projection: [g, v]") {
			t.Errorf("the scan must read the partition key AND the aggregated column "+
				"(missing %q):\n%s", want, plan)
			break
		}
	}
	if strings.Contains(plan, "projection: [g, v, t]") {
		t.Errorf("the scan should not read t, which no window mentions:\n%s", plan)
	}
}

// TestWindowIsAPushdownBarrier: pushing a filter below a window removes rows from
// the partitions, so every window value changes.
//
// The test compares against the same query with the filter applied afterwards by
// hand, which is what an unsound pushdown would silently produce.
func TestWindowIsAPushdownBarrier(t *testing.T) {
	got, err := wf().
		WithColumns(ursus.Col("v").Sum().Over(ursus.Col("g")).Alias("s")).
		Filter(ursus.Col("t").Ge(int64(2))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	// Partition "a" holds rows t=0,2,4 with v=1,3,3 — sum 7. The filter drops t=0
	// AFTER the window, so the surviving "a" rows must still say 7. A filter pushed
	// below would compute 6.
	sum, err := got.Column[ursus.Int128Value]("s")
	if err != nil {
		t.Fatal(err)
	}
	gc, err := got.Column[string]("g")
	if err != nil {
		t.Fatal(err)
	}
	for i := range got.Height() {
		k, _ := gc.Get(i)
		if k != "a" {
			continue
		}
		v, ok := sum.Get(i)
		if !ok || v.String() != "7" {
			t.Fatalf("partition \"a\" sums to %s (valid %v), want 7 — a 6 would mean "+
				"the filter was pushed below the window and shrank the partition",
				v, ok)
		}
	}
}

// TestWindowThroughMatch: All() inside a window puts a *Match under the new node,
// which panics the process without the substitute arm.
func TestWindowThroughMatch(t *testing.T) {
	df, err := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "b"}),
		ursus.Values("x", []int64{1, 2, 4}),
		ursus.Values("y", []int64{10, 20, 40}),
	).Select(
		ursus.Col("g"),
		ursus.ColRegex("^[xy]$").Sum().Over(ursus.Col("g")),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Width() != 3 {
		t.Fatalf("width = %d, want 3 — the selector should expand to x and y\n%s",
			df.Width(), df)
	}
}

// TestAllAggregatesAsWindows runs every aggregate through a window.
//
// The broadcast is one kernel.Take over the accumulator's result, so this is really
// a test that Take covers every type an aggregate can produce — Int128 from an
// integer sum, String from min and max, Bool from any and all_true, Uint64 from the
// counters and the arg positions.
func TestAllAggregatesAsWindows(t *testing.T) {
	f := ursus.Frame(
		ursus.Values("g", []string{"a", "b", "a", "b"}),
		ursus.ValuesNullable("n", []int64{1, 10, 3, 0}, []bool{true, true, true, false}),
		ursus.Values("s", []string{"p", "q", "r", "s"}),
		ursus.Values("b", []bool{true, false, true, true}),
	)
	g := ursus.Col("g")

	cases := []struct {
		name string
		e    ursus.Expr
	}{
		{"sum", ursus.Col("n").Sum().Over(g)},
		{"mean", ursus.Col("n").Mean().Over(g)},
		{"min", ursus.Col("n").Min().Over(g)},
		{"max", ursus.Col("n").Max().Over(g)},
		{"count", ursus.Col("n").Count().Over(g)},
		{"len", ursus.Col("n").Len().Over(g)},
		{"n_unique", ursus.Col("n").NUnique().Over(g)},
		{"first", ursus.Col("n").First().Over(g)},
		{"last", ursus.Col("n").Last().Over(g)},
		{"null_count", ursus.Col("n").NullCount().Over(g)},
		{"any", ursus.Col("b").Any().Over(g)},
		{"all_true", ursus.Col("b").AllTrue().Over(g)},
		{"var", ursus.Col("n").Var(0).Over(g)},
		{"std", ursus.Col("n").Std(0).Over(g)},
		{"product", ursus.Col("n").Product().Over(g)},
		{"arg_min", ursus.Col("n").ArgMin().Over(g)},
		{"arg_max", ursus.Col("n").ArgMax().Over(g)},
		{"median", ursus.Col("n").Median().Over(g)},
		{"quantile", ursus.Col("n").Quantile(0.9, ursus.InterpLinear).Over(g)},
		{"string min", ursus.Col("s").Min().Over(g)},
		{"string max", ursus.Col("s").Max().Over(g)},
		{"string first", ursus.Col("s").First().Over(g)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			df, err := f.Select(ursus.Col("g"), c.e.Alias("w")).
				Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("%s over a window: %v", c.name, err)
			}
			if df.Height() != 4 {
				t.Errorf("height = %d, want 4 — a window preserves height", df.Height())
			}
		})
	}
}

// TestWindowAggregateMatchesGroupBy is the differential that makes the broadcast
// real: the value on each row must equal what GroupBy would have computed for that
// row's group.
func TestWindowAggregateMatchesGroupBy(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	const n = 300
	keys := make([]string, n)
	vals := make([]float64, n)
	valid := make([]bool, n)
	for i := range n {
		keys[i] = string(rune('a' + rng.IntN(6)))
		vals[i] = rng.Float64() * 100
		valid[i] = rng.IntN(6) != 0
	}
	f := ursus.Frame(
		ursus.Values("g", keys),
		ursus.ValuesNullable("v", vals, valid),
	)

	win, err := f.Select(
		ursus.Col("g"),
		ursus.Col("v").Mean().Over(ursus.Col("g")).Alias("m"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	grp, err := f.GroupBy(ursus.Col("g")).
		Agg(ursus.Col("v").Mean().Alias("m")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]float64{}
	present := map[string]bool{}
	gk, _ := grp.Column[string]("g")
	gm, _ := grp.Column[float64]("m")
	for i := range grp.Height() {
		k, _ := gk.Get(i)
		v, ok := gm.Get(i)
		want[k], present[k] = v, ok
	}

	wk, _ := win.Column[string]("g")
	wm, _ := win.Column[float64]("m")
	for i := range win.Height() {
		k, _ := wk.Get(i)
		v, ok := wm.Get(i)
		if ok != present[k] {
			t.Fatalf("row %d group %q: validity %v, GroupBy says %v", i, k, ok, present[k])
		}
		if ok && math.Abs(v-want[k]) > 1e-12 {
			t.Fatalf("row %d group %q: window says %v, GroupBy says %v", i, k, v, want[k])
		}
	}
}

// TestRankMethods pins all five, over a partition with a tie and a null.
func TestRankMethods(t *testing.T) {
	// One partition, values [10, 20, 20, null]. Ascending, nulls first by default,
	// so the sorted order is null, 10, 20, 20.
	f := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a", "a"}),
		ursus.ValuesNullable("v", []int64{10, 20, 20, 0},
			[]bool{true, true, true, false}),
	)
	cases := []struct {
		method ursus.RankMethod
		want   []uint32 // in input order: 10, 20, 20, null
	}{
		{ursus.RankOrdinal, []uint32{2, 3, 4, 1}},
		{ursus.RankDense, []uint32{2, 3, 3, 1}},
		{ursus.RankMin, []uint32{2, 3, 3, 1}},
		{ursus.RankMax, []uint32{2, 4, 4, 1}},
	}
	for _, c := range cases {
		t.Run(c.method.String(), func(t *testing.T) {
			df, err := f.Select(
				ursus.Col("v").Rank(c.method, false).Over(ursus.Col("g")).Alias("r"),
			).Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatal(err)
			}
			if got := u32(t, df, "r"); !slices.Equal(got, c.want) {
				t.Errorf("rank(%s) = %v, want %v", c.method, got, c.want)
			}
		})
	}

	// Average returns Float64, which is the whole reason it is a separate method:
	// the mean of positions 3 and 4 is 3.5 and truncating it would make average
	// indistinguishable from min.
	df, err := f.Select(
		ursus.Col("v").Rank(ursus.RankAverage, false).Over(ursus.Col("g")).Alias("r"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	r, err := df.Column[float64]("r")
	if err != nil {
		t.Fatalf("RankAverage must be Float64: %v", err)
	}
	if v, _ := r.Get(1); v != 3.5 {
		t.Errorf("average rank of a tie at positions 3 and 4 = %v, want 3.5", v)
	}
}

// TestShiftPastPartitionBoundaries: a shift must null at the edge, never read the
// neighbouring partition's value.
func TestShiftPastPartitionBoundaries(t *testing.T) {
	f := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "b", "b"}),
		ursus.Values("v", []int64{1, 2, 3, 4}),
	)
	df, err := f.Select(
		ursus.Col("g"), ursus.Col("v"),
		ursus.Col("v").Shift(1).Over(ursus.Col("g")).Alias("lag"),
		ursus.Col("v").Shift(-1).Over(ursus.Col("g")).Alias("lead"),
		ursus.Col("v").ShiftFill(1, int64(-1)).Over(ursus.Col("g")).Alias("lagFill"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	lag, _ := df.Column[int64]("lag")
	lead, _ := df.Column[int64]("lead")
	fill, _ := df.Column[int64]("lagFill")

	// Row 2 is the first row of partition "b". Its lag must be NULL, not 2.
	if v, ok := lag.Get(2); ok {
		t.Errorf("lag at a partition start = %v, want null — it must not read "+
			"across the boundary", v)
	}
	if v, ok := lag.Get(1); !ok || v != 1 {
		t.Errorf("lag within a partition = %v (valid %v), want 1", v, ok)
	}
	// Row 1 is the last row of "a", so its lead is null.
	if v, ok := lead.Get(1); ok {
		t.Errorf("lead at a partition end = %v, want null", v)
	}
	// The fill replaces exactly the rows shifted in from outside.
	if v, ok := fill.Get(0); !ok || v != -1 {
		t.Errorf("filled lag = %v (valid %v), want -1", v, ok)
	}
	if v, ok := fill.Get(1); !ok || v != 1 {
		t.Errorf("a row with a real predecessor must keep it: %v (valid %v)", v, ok)
	}
}

// TestCumulativeNullSemantics: a null is skipped by the running value and preserved
// in place.
//
// cum_sum of [1, null, 3] is [1, null, 4]. Not [1, 1, 4], which would claim a value
// where there was none, and not [1, null, null], which would let one missing reading
// destroy the rest of the series.
func TestCumulativeNullSemantics(t *testing.T) {
	f := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "a"}),
		ursus.ValuesNullable("v", []int64{1, 0, 3}, []bool{true, false, true}),
	)
	df, err := f.Select(
		ursus.Col("v").CumSum(false).Over(ursus.Col("g")).Alias("cs"),
		ursus.Col("v").CumMax(false).Over(ursus.Col("g")).Alias("cm"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	rendered := df.String()
	if !strings.Contains(rendered, "null") {
		t.Errorf("the null row must stay null:\n%s", rendered)
	}
	cm, err := df.Column[int64]("cm")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := cm.Get(0); !ok || v != 1 {
		t.Errorf("cum_max[0] = %v (valid %v), want 1", v, ok)
	}
	if _, ok := cm.Get(1); ok {
		t.Error("cum_max at the null row must be null")
	}
	if v, ok := cm.Get(2); !ok || v != 3 {
		t.Errorf("cum_max[2] = %v (valid %v), want 3 — the null must not have "+
			"stopped the running value", v, ok)
	}
}

// TestCumSumMatchesSum: the last row of a running total must equal the total.
//
// They share an accumulator width for exactly this reason — a cum_sum that
// accumulated at a narrower type would diverge from sum only on large inputs.
func TestCumSumMatchesSum(t *testing.T) {
	const n = 200
	vals := make([]int64, n)
	for i := range n {
		vals[i] = int64(i * 1000)
	}
	f := ursus.Frame(
		ursus.Values("g", make([]string, n)), // one partition
		ursus.Values("v", vals),
	)
	df, err := f.Select(
		ursus.Col("v").Sum().Over(ursus.Col("g")).Alias("total"),
		ursus.Col("v").CumSum(false).Over(ursus.Col("g")).Alias("run"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := df.Schema().Field(0).Type, df.Schema().Field(1).Type; got != want {
		t.Errorf("sum is %s but cum_sum is %s; they must share a width", got, want)
	}
	// The last row's running total and the (constant) total must render the same.
	last := df.String()
	if strings.Count(last, "19900000") < 2 {
		t.Errorf("the final running total must equal the sum:\n%s", last)
	}
}

// TestIsUniqueAgreesWithDistinct: IsUnique partitions by the value itself, so it
// uses GROUPING equality — NaN equals NaN, and nulls group together.
//
// An IsUnique built on IEEE equality would disagree with Unique() about the same
// column, which is the trap n_unique and IsIn both record.
func TestIsUniqueAgreesWithDistinct(t *testing.T) {
	nan := math.NaN()
	f := ursus.Frame(ursus.Values("v", []float64{nan, nan, 1, 2, 2, 3}))

	df, err := f.Select(
		ursus.Col("v"),
		ursus.Col("v").IsUnique().Alias("u"),
		ursus.Col("v").IsDuplicated().Alias("d"),
		ursus.Col("v").IsFirstDistinct().Alias("first"),
		ursus.Col("v").IsLastDistinct().Alias("last"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := df.Column[bool]("u")
	d, _ := df.Column[bool]("d")
	first, _ := df.Column[bool]("first")
	last, _ := df.Column[bool]("last")

	// The two NaNs are duplicates of each other — IEEE equality would call them
	// unique, and then Unique() would disagree by collapsing them.
	for _, i := range []int{0, 1} {
		if v, ok := u.Get(i); !ok || v {
			t.Errorf("NaN row %d: IsUnique = %v, want false — the two NaNs are "+
				"duplicates under grouping equality", i, v)
		}
		if v, ok := d.Get(i); !ok || !v {
			t.Errorf("NaN row %d: IsDuplicated = %v, want true", i, v)
		}
	}
	if v, _ := u.Get(2); !v {
		t.Error("1 occurs once, so it is unique")
	}
	// First and last occurrence of the NaN run.
	if v, _ := first.Get(0); !v {
		t.Error("row 0 is the first NaN")
	}
	if v, _ := first.Get(1); v {
		t.Error("row 1 is not the first NaN")
	}
	if v, _ := last.Get(1); !v {
		t.Error("row 1 is the last NaN")
	}

	// The cross-check: Unique() collapses exactly the rows IsFirstDistinct marks.
	uniq, err := f.Unique().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for i := range df.Height() {
		if v, _ := first.Get(i); v {
			marked++
		}
	}
	if marked != uniq.Height() {
		t.Errorf("IsFirstDistinct marks %d rows but Unique() keeps %d; they must agree",
			marked, uniq.Height())
	}
}

func TestWindowRefusals(t *testing.T) {
	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
		want string
	}{
		{"window in a filter", func() *ursus.LazyFrame {
			return wf().Filter(ursus.Col("v").Sum().Over(ursus.Col("g")).Gt(int64(5)))
		}, "not supported in filter"},
		{"window in a sort key", func() *ursus.LazyFrame {
			return wf().Sort(ursus.Desc(ursus.Col("v").Sum().Over(ursus.Col("g"))))
		}, "not supported in sort"},
		{"nested windows", func() *ursus.LazyFrame {
			return wf().Select(
				ursus.Col("v").Sum().Over(ursus.Col("g")).Sum().Over(ursus.Col("g")))
		}, "may not contain another window"},
		{"non-window body", func() *ursus.LazyFrame {
			return wf().Select(ursus.Col("v").Over(ursus.Col("g")))
		}, "is not a window function"},
		{"partition by an unhashable value", func() *ursus.LazyFrame {
			return wf().Select(ursus.Col("v").Sum().Over(ursus.Col("v").Cast(ursus.Float64)))
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.lf().Collect(t.Context())
			if c.want == "" {
				return // only checking it does not panic or silently succeed wrongly
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should explain %q:\n%v", c.want, err)
			}
		})
	}
}

// TestWindowBatchSizeAndThreadsAreIndependent is the invariant that makes a window
// a Sink rather than a BatchOp.
//
// Inside a projectOp it would see one batch at a time and compute a partial answer,
// so WithBatchSize(1) would give one answer and WithBatchSize(8192) another.
func TestWindowBatchSizeAndThreadsAreIndependent(t *testing.T) {
	q := func() *ursus.LazyFrame {
		return wf().Select(
			ursus.Col("g"), ursus.Col("t"),
			ursus.Col("v").Sum().Over(ursus.Col("g")).Alias("s"),
			ursus.Col("v").Mean().Over(ursus.Col("g")).Alias("m"),
			ursus.Col("v").Rank(ursus.RankDense, true).Over(ursus.Col("g")).Alias("r"),
			ursus.Col("v").CumSum(false).Over(ursus.Col("g")).Alias("c"),
			ursus.Col("v").Shift(1).Over(ursus.Col("g")).Alias("p"),
			ursus.Col("v").IsUnique().Alias("u"),
		)
	}
	ref, err := q().Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := ref

	for _, bs := range []int{1, 2, 3, 5, 8192} {
		for _, th := range []int{1, 2, 4} {
			got, err := q().Collect(t.Context(),
				ursus.WithBatchSize(bs), ursus.WithThreads(th), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d threads %d: %v", bs, th, err)
			}
			// AssertFrameEqual, not String(): String renders ten rows, so a value
			// difference past row 10 would be invisible. This fixture is under
			// that today and nothing stops it growing.
			ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
		}
	}
}

// TestOrderedWindowMatchesPerPartitionSort is the differential for the one-sort
// trick.
//
// The permutation is built by putting the partition ids FIRST in a lexicographic
// comparator, so a single ArgSort yields something partition-major and ordered
// within each partition. This checks that against the obvious implementation:
// pull each partition out and sort it on its own.
func TestOrderedWindowMatchesPerPartitionSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 23))
	const n = 250
	keys := make([]string, n)
	vals := make([]int64, n)
	for i := range n {
		keys[i] = string(rune('a' + rng.IntN(7)))
		vals[i] = int64(rng.IntN(40)) // deliberately many ties
	}

	df, err := ursus.Frame(
		ursus.Values("g", keys),
		ursus.Values("v", vals),
	).Select(
		ursus.Col("v").Rank(ursus.RankOrdinal, false).Over(ursus.Col("g")).Alias("r"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	got := u32(t, df, "r")

	// The reference: for each partition, sort its row indices by (value, input
	// position) — which is what a STABLE sort within the partition gives — and
	// number them from 1.
	want := make([]uint32, n)
	byKey := map[string][]int{}
	for i := range n {
		byKey[keys[i]] = append(byKey[keys[i]], i)
	}
	for _, rows := range byKey {
		slices.SortStableFunc(rows, func(a, b int) int {
			switch {
			case vals[a] < vals[b]:
				return -1
			case vals[a] > vals[b]:
				return 1
			default:
				return 0 // stability keeps input order, as ArgSort does
			}
		})
		for pos, row := range rows {
			want[row] = uint32(pos + 1)
		}
	}

	if !slices.Equal(got, want) {
		for i := range n {
			if got[i] != want[i] {
				t.Fatalf("row %d (g=%q v=%d): rank %d, want %d — the partition-major "+
					"comparator disagrees with sorting the partition on its own",
					i, keys[i], vals[i], got[i], want[i])
			}
		}
	}
}
