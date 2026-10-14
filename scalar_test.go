package ursus_test

import (
	"bytes"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestAnAggregateInARowContextIsAScalar: since step 250 an aggregate in Select,
// WithColumns and Filter is one value for the frame, broadcast to every row, and a
// Select of aggregates and literals alone is one row, as in Polars. Each answer here
// is Polars 1.44.1's on the same frame, written as CSV by Polars itself. A window in
// a Filter is computed beneath it, SQL's QUALIFY.
func TestAnAggregateInARowContextIsAScalar(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2, 2}), ursus.Values("x", []float64{1, 4, 2, 8, 5}),
		ursus.Values("y", []float64{3, 1, 4, 1, 5}))
	cases := []struct {
		q      *ursus.LazyFrame
		polars string
	}{
		{lf.Select(c("x").Sum(), c("y")), "x,y\n20.0,3.0\n20.0,1.0\n20.0,4.0\n20.0,1.0\n20.0,5.0\n"},
		{lf.Select(c("x").Sum(), c("y").Mean().Alias("m")), "x,m\n20.0,2.8\n"},
		{lf.Select(c("x").Sum(), ursus.Lit(int64(7)).Alias("l")), "x,l\n20.0,7\n"},
		{lf.WithColumns(c("x").Sub(c("x").Mean()).Alias("cx")),
			"g,x,y,cx\n1,1.0,3.0,-3.0\n1,4.0,1.0,0.0\n2,2.0,4.0,-2.0\n2,8.0,1.0,4.0\n2,5.0,5.0,1.0\n"},
		{lf.Filter(c("x").Gt(c("x").Mean())), "g,x,y\n2,8.0,1.0\n2,5.0,5.0\n"},
		{lf.Filter(c("x").Sum().Over(c("g")).Gt(10)), "g,x,y\n2,2.0,4.0\n2,8.0,1.0\n2,5.0,5.0\n"},
		{lf.Select(c("x").Sum().Add(c("y").Max()).Mul(2).Alias("s")), "s\n50.0\n"},
		{lf.Select(c("x").Mul(c("x").Sum()).Alias("p")), "p\n20.0\n80.0\n40.0\n160.0\n100.0\n"},
		{lf.Select(ursus.All().Sum().Suffix("_s")), "g_s,x_s,y_s\n8,20.0,14.0\n"},
		{lf.Select(c("g"), c("x", "y").Mean()), "g,x,y\n1,4.0,2.8\n1,4.0,2.8\n2,4.0,2.8\n2,4.0,2.8\n2,4.0,2.8\n"},
		{lf.WithColumns(c("x").Div(c("x").Sum()).Alias("share")),
			"g,x,y,share\n1,1.0,3.0,0.05\n1,4.0,1.0,0.2\n2,2.0,4.0,0.1\n2,8.0,1.0,0.4\n2,5.0,5.0,0.25\n"},
		{lf.Select(c("x").Max().Sub(c("x").Min()).Alias("range"), c("y").Std(1).Alias("sd")),
			"range,sd\n7.0,1.7888543819998317\n"},
	}
	for i, tc := range cases {
		var got bytes.Buffer
		if err := tc.q.WriteCSV(t.Context(), &got); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got.String() != tc.polars {
			t.Errorf("case %d:\n got %q\nwant %q (Polars)", i, got.String(), tc.polars)
		}
	}
}

// TestABroadcastAggregateStreamsPastItsLimit: the broadcast is the aggregate over a
// window with no partition, so it streams past a memory limit as that window does
// (steps 197, 242): centring a column, a median broadcast, and a filter above the
// mean, under a tenth of the input, give the in-memory answer, spill, and leave the
// spill directory empty.
func TestABroadcastAggregateStreamsPastItsLimit(t *testing.T) {
	frame := sortedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	for name, q := range map[string]*ursus.LazyFrame{
		"centred and a median": frame.WithColumns(c("x").Sub(c("x").Mean()).Alias("cx"), c("v").Median().Alias("med")),
		"above the mean":       frame.Filter(c("x").Gt(c("x").Mean())),
	} {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
		if err != nil {
			t.Fatal(err)
		}
		var stats ursus.MemoryStats
		dir := t.TempDir()
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithMemoryLimit(limit),
			ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("%s under %d bytes: %v", name, limit, err)
		}
		if stats.Spills == 0 {
			t.Errorf("%s: nothing spilled", name)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if left, _ := os.ReadDir(dir); len(left) > 0 {
			t.Errorf("%s: %d entries left in the spill directory", name, len(left))
		}
	}
}

// TestAFilterIsAnAggregatesInput: Col("a").Filter(p).Agg() reads only the rows p is
// true on, in a group-by, a window, a Select, a WithColumns, a rolling group and a
// dynamic one (step 251). The answers are Polars 1.44.1's on the same frame but
// where ursus differs on purpose (feature-gaps §9): the sum of a group none of
// whose rows pass is null, where Polars gives 0, and n_unique does not count a null,
// where Polars counts it (group 3's u is 2 there). A chain of filters is every
// predicate over the frame's rows, AND-ed; Polars refuses a chain whose second
// predicate reads the whole column, and its form for the same, one filter of both,
// gives cc. A null predicate drops its row.
func TestAFilterIsAnAggregatesInput(t *testing.T) {
	c := ursus.Col
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ts := make([]time.Time, 8)
	for i := range ts {
		ts[i] = t0.Add(time.Duration(i) * time.Hour)
	}
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2, 3, 3, 3}),
		ursus.ValuesNullable("a", []float64{1, 4, 2, 8, 5, 3, 7, 6}, []bool{true, true, true, true, true, false, true, true}),
		ursus.ValuesNullable("b", []int64{1, -1, 3, -2, -5, 2, 0, 4}, []bool{true, true, true, true, true, true, false, true}),
		ursus.Values("ts", ts))
	p := c("b").Gt(0)
	cases := []struct {
		q    *ursus.LazyFrame
		want string
	}{
		{lf.GroupBy(c("g")).Agg(c("a").Filter(p).Sum().Alias("s"), c("a").Filter(p).Len().Alias("n"),
			c("a").Filter(p).First().Alias("f"), c("a").Filter(p).Last().Alias("l"), c("a").Filter(p).Mean().Alias("m"),
			c("a").Filter(p).NUnique().Alias("u"), c("a").Filter(p).Median().Alias("med"), c("a").Filter(p).Max().Alias("mx"),
			c("a").Filter(p).Filter(c("a").Gt(1.5)).Count().Alias("cc")).Sort(ursus.Asc(c("g"))),
			"g,s,n,f,l,m,u,med,mx,cc\n1,3.0,2,1.0,2.0,1.5,2,1.5,2.0,1\n2,,0,,,,0,,,0\n3,6.0,2,,6.0,6.0,1,6.0,6.0,1\n"},
		{lf.Select(c("g"), c("a").Filter(p).Sum().Over(c("g")).Alias("s")),
			"g,s\n1,3.0\n1,3.0\n1,3.0\n2,\n2,\n3,6.0\n3,6.0\n3,6.0\n"},
		{lf.Select(c("a").Filter(p).Sum().Alias("s"), c("a").Filter(c("g").Eq(3)).Len().Alias("n")), "s,n\n9.0,3\n"},
		{lf.Select(c("g"), c("a").Filter(p).Mean().Alias("pm")), "g,pm\n1,3.0\n1,3.0\n1,3.0\n2,3.0\n2,3.0\n3,3.0\n3,3.0\n3,3.0\n"},
		{lf.Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("3h")}).
			Agg(c("a").Filter(p).Sum().Alias("s"), c("a").Filter(p).Len().Alias("n")).Select(c("s", "n")),
			"s,n\n1.0,1\n1.0,1\n3.0,2\n2.0,1\n2.0,1\n,1\n,1\n6.0,2\n"},
		{lf.GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("2h"), GroupBy: []ursus.Expr{c("g")}}).
			Agg(c("a").Filter(p).Max().Alias("mx")).Select(c("g", "mx")),
			"g,mx\n1,1.0\n1,2.0\n2,\n2,\n3,\n3,6.0\n"},
	}
	for i, tc := range cases {
		var got bytes.Buffer
		if err := tc.q.WriteCSV(t.Context(), &got); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got.String() != tc.want {
			t.Errorf("case %d:\n got %q\nwant %q", i, got.String(), tc.want)
		}
	}

	// Anywhere but an aggregate's input a filter is refused, naming the frame's.
	for _, q := range []*ursus.LazyFrame{lf.Select(c("a").Filter(p)), lf.WithColumns(c("a").Filter(p).Add(1)),
		lf.GroupBy(c("g")).Agg(c("a").Filter(p).Add(1).Sum())} {
		_, err := q.Collect(t.Context())
		if err == nil || !strings.Contains(err.Error(), "changes the column's length") {
			t.Errorf("a filter that is not an aggregate's input should be refused: %v", err)
		}
	}
}

// TestAFilteredAggregateSpills: a filtered aggregate is fed through the same
// accumulators, so it spills and streams where they do: a filtered median and
// n_unique over a hot key, whose values spill (steps 220, 241), and a filtered sum
// broadcast over one partition (step 197), under a tenth of the input, give the
// in-memory answer and leave the spill directory empty.
func TestAFilteredAggregateSpills(t *testing.T) {
	frame := sortedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	p := c("v").Gt(0)
	for name, q := range map[string]*ursus.LazyFrame{
		"a hot key": frame.GroupBy(c("k")).Agg(c("x").Filter(p).Median().Alias("med"),
			c("x").Filter(p).NUnique().Alias("u"), c("x").Filter(p).Len().Alias("n")).Sort(ursus.Asc(c("k"))),
		"one partition": frame.WithColumns(c("x").Filter(p).Sum().Alias("s"), c("x").Filter(p).Median().Over().Alias("m")),
	} {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
		if err != nil {
			t.Fatal(err)
		}
		var stats ursus.MemoryStats
		dir := t.TempDir()
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithMemoryLimit(limit),
			ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("%s under %d bytes: %v", name, limit, err)
		}
		if stats.Spills == 0 {
			t.Errorf("%s: nothing spilled", name)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if left, _ := os.ReadDir(dir); len(left) > 0 {
			t.Errorf("%s: %d entries left in the spill directory", name, len(left))
		}
	}
}

// TestALengthChangingExpressionInAnAggregation: Sort, SortDesc, Head, Tail, Slice,
// Reverse, Unique, DropNulls and Get are Polars' expressions of the same name: in a
// group-by a list a group, and an aggregate over one a value a group (step 252).
// Every value here is Polars 1.44.1's on the same frame: the lists as Polars printed
// them, nulls first in a sort either way, Unique keeping first-seen order and the
// null, and the aggregates over them. An aggregate over a reshape is a scalar in
// Select and works in .Over and a rolling group. A reshape on its own outside an
// aggregation, a reshape of an aggregate, and one of a filter are refused.
func TestALengthChangingExpressionInAnAggregation(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 1, 2, 2}),
		ursus.ValuesNullable("a", []int64{3, 0, 1, 3, 5, 2}, []bool{true, false, true, true, true, true}))
	lists, err := lf.GroupBy(c("g")).Agg(c("a").Sort().Alias("sort"), c("a").SortDesc().Alias("sortd"),
		c("a").Head(2).Alias("head"), c("a").Tail(2).Alias("tail"), c("a").Slice(1, 2).Alias("slice"),
		c("a").Reverse().Alias("rev"), c("a").Unique().Alias("uniq"), c("a").DropNulls().Alias("dn")).
		Sort(ursus.Asc(c("g"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const polars = "shape: (2, 9)\n" +
		"┌───────┬─────────────────┬─────────────────┬─────────────┬─────────────┬─────────────┬─────────────────┬──────────────┬─────────────┐\n" +
		"│ g     │ sort            │ sortd           │ head        │ tail        │ slice       │ rev             │ uniq         │ dn          │\n" +
		"│ Int64 │ List(Int64)     │ List(Int64)     │ List(Int64) │ List(Int64) │ List(Int64) │ List(Int64)     │ List(Int64)  │ List(Int64) │\n" +
		"├───────┼─────────────────┼─────────────────┼─────────────┼─────────────┼─────────────┼─────────────────┼──────────────┼─────────────┤\n" +
		"│ 1     │ [null, 1, 3, 3] │ [null, 3, 3, 1] │ [3, null]   │ [1, 3]      │ [null, 1]   │ [3, 1, null, 3] │ [3, null, 1] │ [3, 1, 3]   │\n" +
		"│ 2     │ [2, 5]          │ [5, 2]          │ [5, 2]      │ [5, 2]      │ [2]         │ [2, 5]          │ [5, 2]       │ [5, 2]      │\n" +
		"└───────┴─────────────────┴─────────────────┴─────────────┴─────────────┴─────────────┴─────────────────┴──────────────┴─────────────┘\n"
	if got := lists.String(); got != polars {
		t.Errorf("the lists:\n%s\nwant (Polars' values)\n%s", got, polars)
	}

	cases := []struct {
		q    *ursus.LazyFrame
		want string
	}{
		{lf.GroupBy(c("g")).Agg(c("a").Get(1).Alias("get"),
			c("a").Sort().First().Alias("sort_first"), c("a").Head(2).Sum().Alias("head_sum"),
			c("a").Unique().Len().Alias("uniq_len"), c("a").DropNulls().Len().Alias("dn_len"),
			c("a").Sort().Last().Alias("sort_last"), c("a").Reverse().First().Alias("rev_first"),
			c("a").Unique().Sum().Alias("uniq_sum"), c("a").Head(2).Count().Alias("head_count")).Sort(ursus.Asc(c("g"))),
			"g,get,sort_first,head_sum,uniq_len,dn_len,sort_last,rev_first,uniq_sum,head_count\n1,,,3,3,3,3,3,4,1\n2,2,2,7,2,2,5,2,7,2\n"},
		{lf.Select(c("a").Sort().First().Alias("f"), c("a").Unique().Len().Alias("u"), c("a").Tail(3).Mean().Alias("m")),
			"f,u,m\n,5,3.3333333333333335\n"},
		// Chains: each reshape of the one before.
		{lf.GroupBy(c("g")).Agg(c("a").Sort().Head(2).Sum().Alias("s2"), c("a").DropNulls().Sort().First().Alias("f")).
			Sort(ursus.Asc(c("g"))), "g,s2,f\n1,1,1\n2,7,2\n"},
		{lf.Select(c("g"), c("a").Head(2).Sum().Over(c("g")).Alias("s")), "g,s\n1,3\n1,3\n1,3\n1,3\n2,7\n2,7\n"},
	}
	for i, tc := range cases {
		var got bytes.Buffer
		if err := tc.q.WriteCSV(t.Context(), &got); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got.String() != tc.want {
			t.Errorf("case %d:\n got %q\nwant %q", i, got.String(), tc.want)
		}
	}

	// A rolling group's windows, reshaped: the first two values of each 2-row window.
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ts := []time.Time{t0, t0.Add(time.Hour), t0.Add(2 * time.Hour), t0.Add(3 * time.Hour)}
	var got bytes.Buffer
	err = ursus.Frame(ursus.Values("ts", ts), ursus.Values("v", []int64{4, 1, 3, 2})).
		Rolling(c("ts"), ursus.RollingOptions{Period: ursus.Every("2h")}).
		Agg(c("v").Sort().First().Alias("lo"), c("v").Reverse().First().Alias("latest")).Select(c("lo", "latest")).
		WriteCSV(t.Context(), &got)
	if err != nil {
		t.Fatal(err)
	}
	if want := "lo,latest\n4,4\n1,1\n1,3\n2,2\n"; got.String() != want {
		t.Errorf("rolling:\n got %q\nwant %q", got.String(), want)
	}

	for _, q := range []*ursus.LazyFrame{lf.Select(c("a").Sort()), lf.GroupBy(c("g")).Agg(c("a").Sum().Sort()),
		lf.GroupBy(c("g")).Agg(c("a").Filter(c("a").Gt(1)).Sort())} {
		if _, err := q.Collect(t.Context()); err == nil {
			t.Error("a reshape outside an aggregation, of an aggregate, or of a filter should be refused")
		}
	}
}

// TestSortByGatherEveryAndDropNansInAnAggregation: E3's second half (step 253).
// SortBy and SortByDesc, GatherEvery and DropNans in a group-by, as lists, and
// median, std, var and quantile over a reshape, each Polars 1.44.1's answer on the
// same frame: sort_by stable and nulls first, gather_every from an offset, drop_nans
// keeping a null. One std is held to 1e-15 of Polars': both are its aggregate's
// answer, rounded along different paths.
func TestSortByGatherEveryAndDropNansInAnAggregation(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 1, 2, 2}),
		ursus.ValuesNullable("a", []int64{3, 0, 1, 3, 5, 2}, []bool{true, false, true, true, true, true}),
		ursus.Values("b", []int64{4, 3, 2, 1, 1, 2}),
		ursus.ValuesNullable("f", []float64{1.5, math.NaN(), 0, 2.5, math.NaN(), 4}, []bool{true, true, false, true, true, true}))
	q := lf.GroupBy(c("g")).Agg(
		c("a").SortBy(c("b")).Alias("sortby"), c("a").SortByDesc(c("b")).Alias("sortbyd"),
		c("a").GatherEvery(2, 0).Alias("ge"), c("a").GatherEvery(2, 1).Alias("ge1"), c("f").DropNans().Alias("dnan"),
		c("a").SortBy(c("b")).Last().Alias("sb_last"), c("a").Head(3).Median().Alias("hmed"),
		c("a").DropNulls().Std(1).Alias("sd"), c("a").Head(3).Var(0).Alias("v0"),
		c("a").Head(3).Quantile(0.25, ursus.InterpLinear).Alias("q25"), c("f").DropNans().Len().Alias("dnan_len"),
	).Sort(ursus.Asc(c("g")))
	lists, err := q.Select(c("sortby", "sortbyd", "ge", "ge1", "dnan")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Polars: sortby [[3, 1, None, 3], [5, 2]], sortbyd [[3, None, 1, 3], [2, 5]],
	// ge [[3, 1], [5]], ge1 [[None, 3], [2]], dnan [[1.5, None, 2.5], [4.0]].
	for _, want := range []string{"[3, 1, null, 3]", "[3, null, 1, 3]", "[3, 1]", "[null, 3]", "[1.5, null, 2.5]",
		"[5, 2]", "[2, 5]", "[5]", "[2]", "[4]"} {
		if !strings.Contains(lists.String(), want) {
			t.Errorf("the lists lack Polars' %s:\n%s", want, lists)
		}
	}
	var got bytes.Buffer
	if err := q.Select(c("g", "sb_last", "hmed", "v0", "q25", "dnan_len")).WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "g,sb_last,hmed,v0,q25,dnan_len\n1,3,2.0,1.0,1.5,3\n2,2,3.5,2.25,2.75,1\n"; got.String() != want {
		t.Errorf("got %q\nwant %q", got.String(), want)
	}
	sd, err := q.Select(c("sd")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := ursus.Frame(ursus.Values("sd", []float64{1.1547005383792515, 2.1213203435596424})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, sd, want, ursustest.WithTolerance(0, 1e-15))

	// Ties keep their order, either way: Polars gives [20, 40, 10, 30] and [10, 30, 20, 40].
	ties, err := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 1}), ursus.Values("a", []int64{10, 20, 30, 40}),
		ursus.Values("b", []int64{1, 0, 1, 0})).GroupBy(c("g")).
		Agg(c("a").SortBy(c("b")).Alias("s"), c("a").SortByDesc(c("b")).Alias("d")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s := ties.String(); !strings.Contains(s, "[20, 40, 10, 30]") || !strings.Contains(s, "[10, 30, 20, 40]") {
		t.Errorf("ties reordered:\n%s", s)
	}

	// Keys of another length than the values, after a reshape, are refused.
	if _, err := lf.GroupBy(c("g")).Agg(c("a").Head(2).SortBy(c("b"))).Collect(t.Context()); err == nil {
		t.Error("sort_by of a reshaped column should be refused")
	}
}
