package ursus_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/ursustest"
)

// lists64 reads a List(Int64) column as strings, with "null" for a null ELEMENT,
// plus each row's own validity.
//
// Elements and the list itself are reported separately because they are different
// things: [null] is a one-element list, and a null list has no elements at all.
func lists64(t *testing.T, df *ursus.DataFrame, name string) ([][]string, []bool) {
	t.Helper()
	c, found := df.Batch().ByName(name)
	if !found {
		t.Fatalf("no column %q in\n%s", name, df)
	}
	if c.DType().ID() != dtype.TypeList {
		t.Fatalf("%s is %s, not a List", name, c.DType())
	}
	out := make([][]string, 0, df.Height())
	valid := make([]bool, 0, df.Height())
	for row := range df.Height() {
		vals, ok := listCellStrings(t, c, row)
		out = append(out, vals)
		valid = append(valid, ok)
	}
	return out, valid
}

func listCellStrings(t *testing.T, c *data.Column, row int) ([]string, bool) {
	t.Helper()
	child, err := data.TypedColumn[int64](c.Lists().Child())
	if err != nil {
		t.Fatal(err)
	}
	start, end, ok := c.Lists().Get(row)
	vals := []string{}
	for e := start; e < end; e++ {
		if v, present := child.Get(int(e)); present {
			vals = append(vals, itoa(v))
		} else {
			vals = append(vals, "null")
		}
	}
	return vals, ok
}

// listString renders one row of a kernel-level List(Int64) column, for the Merge
// test, which works below the frame layer.
func listString(c *data.Column, row int) string {
	acc := c.Lists()
	start, end, _ := acc.Get(row)
	child := acc.Child()
	parts := make([]string, 0, end-start)
	for e := start; e < end; e++ {
		vals, _ := data.Values[int64](child)
		if child.IsValid(int(e)) {
			parts = append(parts, itoa(vals[e]))
		} else {
			parts = append(parts, "null")
		}
	}
	return strings.Join(parts, ",")
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// implodeFrame carries the three cases that matter: a group with a null among real
// values, a group that is ENTIRELY null, and a singleton. Values are chosen so
// element ORDER is observable — a shuffled group would show.
func implodeFrame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("k", []string{"a", "b", "a", "c", "b", "a"}),
		ursus.ValuesNullable("v",
			[]int64{10, 0, 20, 99, 0, 30},
			[]bool{true, false, true, true, false, true}),
	)
}

// TestImplodeKeepsNulls is the one place implode deviates from every other
// aggregate, and the highest-value check in the file.
//
// Every other accumulator skips nulls — the sum of an all-null group is null, not
// zero. Implode is a SELECTION, so a null takes a slot. Group "b" is entirely null
// and must come back as a list OF nulls: not the null list, and not the empty one.
// A fixture without nulls cannot see any of this, and a length check alone cannot
// separate the three states.
func TestImplodeKeepsNulls(t *testing.T) {
	df, err := implodeFrame().
		GroupBy(ursus.Col("k")).
		Agg(
			ursus.Col("v").Implode().Alias("all"),
			ursus.Col("v").Implode().List().Len().Alias("n"),
			ursus.Col("v").Implode().List().DropNulls().List().Len().Alias("nn"),
		).
		Sort(ursus.Asc(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	got, valid := lists64(t, df, "all")
	want := [][]string{
		{"10", "20", "30"}, // a: order preserved
		{"null", "null"},   // b: a list OF nulls
		{"99"},             // c
	}
	for i, w := range want {
		if !valid[i] {
			t.Errorf("row %d: the list itself is null; a group always has rows", i)
			continue
		}
		if strings.Join(got[i], ",") != strings.Join(w, ",") {
			t.Errorf("row %d: got [%s], want [%s]",
				i, strings.Join(got[i], ","), strings.Join(w, ","))
		}
	}

	// len counts the nulls, drop_nulls then len does not. Group b is 2 and 0.
	for i, w := range []struct{ n, nn uint32 }{{3, 3}, {2, 0}, {1, 1}} {
		n, _, err := df.At[uint32](i, "n")
		if err != nil {
			t.Fatal(err)
		}
		nn, _, err := df.At[uint32](i, "nn")
		if err != nil {
			t.Fatal(err)
		}
		if n != w.n || nn != w.nn {
			t.Errorf("row %d: len=%d drop_nulls.len=%d, want %d and %d", i, n, nn, w.n, w.nn)
		}
	}
}

// TestImplodeIsBatchSizeInvariant. The accumulator retains one column per batch and
// concatenates them in Finish, shifting each batch's row indices by the rows already
// held. A group whose rows straddle a batch boundary is what exercises that shift.
func TestImplodeIsBatchSizeInvariant(t *testing.T) {
	query := func() *ursus.LazyFrame {
		return implodeFrame().
			GroupBy(ursus.Col("k")).
			Agg(ursus.Col("v").Implode().Alias("all")).
			Sort(ursus.Asc(ursus.Col("k")))
	}
	want, err := query().Collect(t.Context(), ursus.WithBatchSize(8192))
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 2, 3, 7, 8192} {
		for _, threads := range []int{1, 2, 4} {
			got, err := query().Collect(t.Context(),
				ursus.WithBatchSize(size), ursus.WithThreads(threads), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d, %d threads: %v", size, threads, err)
			}
			ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
		}
	}
}

// TestImplodeElementOrderIsStable is the order-dependence check.
//
// A list's element order is its data. The parallel aggregation driver dispatches
// batches round-robin, so if implode were not declared order-dependent the elements
// would arrive interleaved and the answer would vary run to run. Enough rows and
// batches that a shuffle would show, run repeatedly because the failure is a race
// rather than a certainty.
func TestImplodeElementOrderIsStable(t *testing.T) {
	const n = 2000
	ks := make([]string, n)
	vs := make([]int64, n)
	for i := range n {
		ks[i] = []string{"a", "b", "c", "d"}[i%4]
		vs[i] = int64(i)
	}
	lf := ursus.Frame(ursus.Values("k", ks), ursus.Values("v", vs)).
		GroupBy(ursus.Col("k")).
		Agg(ursus.Col("v").Implode().Alias("all")).
		Sort(ursus.Asc(ursus.Col("k")))

	for attempt := range 5 {
		df, err := lf.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithThreads(8))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := lists64(t, df, "all")
		// Group "a" took rows 0, 4, 8, ... in input order.
		for i, v := range got[0] {
			if v != itoa(int64(i*4)) {
				t.Fatalf("attempt %d: element %d of group a is %s, want %d — "+
					"the input order has been shuffled", attempt, i, v, i*4)
			}
		}
	}
}

// TestImplodeEveryPayloadShape is the Take-based design's central claim: it needs no
// per-type arm. Enum is the one a typed-slice design gets wrong, because
// data.NewList derives its element type from the child and has no WithDType hook.
func TestImplodeEveryPayloadShape(t *testing.T) {
	lf := ursus.Frame(
		ursus.Values("k", []string{"a", "a", "b"}),
		ursus.Values("i", []int64{1, 2, 3}),
		ursus.Values("f", []float64{1.5, 2.5, 3.5}),
		ursus.Values("s", []string{"x", "y", "z"}),
		ursus.Values("b", []bool{true, false, true}),
	)
	df, err := lf.GroupBy(ursus.Col("k")).Agg(
		ursus.Col("i").Implode().Alias("li"),
		ursus.Col("f").Implode().Alias("lf"),
		ursus.Col("s").Implode().Alias("ls"),
		ursus.Col("b").Implode().Alias("lb"),
		// A list OF lists: implode is defined for nested types because Take and
		// NewList are recursive, not because it has an arm for them.
		ursus.Col("s").Str().Split("").Implode().Alias("ll"),
	).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		want dtype.DataType
	}{
		{"li", dtype.List(dtype.Int64)},
		{"lf", dtype.List(dtype.Float64)},
		{"ls", dtype.List(dtype.String)},
		{"lb", dtype.List(dtype.Bool)},
		{"ll", dtype.List(dtype.List(dtype.String))},
	} {
		f, found := df.Batch().ByName(c.name)
		if !found {
			t.Fatalf("no column %q", c.name)
		}
		if f.DType() != c.want {
			t.Errorf("%s is %s, want %s", c.name, f.DType(), c.want)
		}
	}
}

// TestImplodeEmptyGlobalAggregate — a global implode over no rows is ONE row
// holding the empty list, not zero rows and not a null.
func TestImplodeEmptyGlobalAggregate(t *testing.T) {
	df, err := implodeFrame().
		Filter(ursus.Col("k").Eq("nope")).
		GroupBy().
		Agg(ursus.Col("v").Implode().Alias("all")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1 {
		t.Fatalf("got %d rows, want 1:\n%s", df.Height(), df)
	}
	got, valid := lists64(t, df, "all")
	if !valid[0] {
		t.Error("the empty global aggregate should be an EMPTY list, not a null one")
	}
	if len(got[0]) != 0 {
		t.Errorf("got %v, want an empty list", got[0])
	}
}

// TestImplodeFeedsTheListNamespace is the point of the step: the fourteen list
// functions, available per group without one of them being reimplemented.
func TestImplodeFeedsTheListNamespace(t *testing.T) {
	df, err := implodeFrame().
		GroupBy(ursus.Col("k")).
		Agg(
			ursus.Col("v").Implode().List().Sort().List().Head(2).Alias("smallest2"),
			ursus.Col("v").Implode().List().Reverse().List().Get(0).Alias("last"),
			// Integer sums accumulate at 128 bits, so the cast is what lets this
			// read back as an Int64 rather than what the aggregate produces.
			ursus.Col("v").Implode().List().DropNulls().List().Sum().
				Cast(ursus.Int64).Alias("total"),
			ursus.Col("v").Implode().List().Unique().List().Len().Alias("distinct"),
		).
		Sort(ursus.Asc(ursus.Col("k"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}

	// Group a is [10, 20, 30]: reversed-first is 30, sum is 60, three distinct.
	if v, _, err := df.At[int64](0, "last"); err != nil {
		t.Fatal(err)
	} else if v != 30 {
		t.Errorf("last of group a = %d, want 30", v)
	}
	if v, ok, err := df.At[int64](0, "total"); err != nil {
		t.Fatal(err)
	} else if !ok || v != 60 {
		t.Errorf("sum of group a = %v (ok=%v), want 60", v, ok)
	}
	// Group b is [null, null]: dropping the nulls leaves nothing, so the sum of an
	// empty list is null rather than zero.
	if _, ok, err := df.At[int64](1, "total"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("the sum of an empty list should be null")
	}
}

// TestImplodeOverBroadcasts — the window form, which is free because
// finishAggregate is Finish then Take, and Take has had a List arm since step 29.
func TestImplodeOverBroadcasts(t *testing.T) {
	df, err := implodeFrame().
		WithColumns(ursus.Col("v").Implode().Over(ursus.Col("k")).Alias("peers")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 6 {
		t.Fatalf("a window must preserve height: got %d, want 6\n%s", df.Height(), df)
	}
	got, _ := lists64(t, df, "peers")
	// Rows 0, 2 and 5 are group a and must all carry the same list.
	for _, i := range []int{0, 2, 5} {
		if strings.Join(got[i], ",") != "10,20,30" {
			t.Errorf("row %d: peers = [%s], want [10,20,30]", i, strings.Join(got[i], ","))
		}
	}
}

// TestImplodeMergeFoldsBothWays tests Merge DIRECTLY, because nothing calls it.
//
// Implode is order-dependent, so the parallel driver runs one worker; spilling
// partitions by key, so one sink sees a whole group. Merge is therefore unreachable
// in production — which is exactly the state steps 13, 17 and 39 each found a Merge
// in, and each time it was wrong. The Accumulator doc's rule applies: "a Merge
// written later against forgotten invariants is a Merge that is wrong."
//
// The precondition is argExtremumAcc's: `other` saw a strictly LATER portion, so its
// elements append after this one's.
func TestImplodeMergeFoldsBothWays(t *testing.T) {
	bind, err := expr.ResolveAggBinding(expr.AggImplode, dtype.Int64)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(vals []int64, groups []int32) kernel.Accumulator {
		t.Helper()
		acc, err := kernel.NewAccumulator(expr.AggImplode, dtype.Int64, bind, expr.AggParams{})
		if err != nil {
			t.Fatal(err)
		}
		acc.Reserve(2)
		col := data.NewFixed("v", dtype.Int64, vals, bitmap.AllSet(len(vals)))
		if err := acc.AddBatch(groups, col); err != nil {
			t.Fatal(err)
		}
		return acc
	}

	// first saw rows 1,2,3; second saw 4,5 — a strictly later portion.
	first := mk([]int64{1, 2, 3}, []int32{0, 1, 0})
	second := mk([]int64{4, 5}, []int32{0, 1})

	if err := first.Merge(second, nil); err != nil {
		t.Fatal(err)
	}
	out, err := first.Finish("all", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := listString(out, 0); got != "1,3,4" {
		t.Errorf("group 0 = [%s], want [1,3,4] — later elements append after earlier", got)
	}
	if got := listString(out, 1); got != "2,5" {
		t.Errorf("group 1 = [%s], want [2,5]", got)
	}

	// And under a remap, where the second accumulator numbered its groups
	// independently: its group 0 is this one's group 1 and vice versa.
	third := mk([]int64{7, 8, 9}, []int32{0, 1, 0})
	fourth := mk([]int64{60, 70}, []int32{0, 1})
	if err := third.Merge(fourth, []int32{1, 0}); err != nil {
		t.Fatal(err)
	}
	out2, err := third.Finish("all", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := listString(out2, 0); got != "7,9,70" {
		t.Errorf("remapped group 0 = [%s], want [7,9,70]", got)
	}
	if got := listString(out2, 1); got != "8,60" {
		t.Errorf("remapped group 1 = [%s], want [8,60]", got)
	}
}

// TestImplodeRefusesPastTheBudget. Implode's state is O(rows), so a spilling
// group-by cannot bound it: partitioning divides the key space and one key's values
// all belong to that key. The refusal must name the situation.
// The fixture is FEW ROWS WITH BIG VALUES on purpose. With many small rows the
// per-group index slices alone exceed any limit, so the refusal fires whether or not
// NBytes counts the retained columns — and the tooth for that omission does not
// bite. Two thousand indices are 8 KiB; two thousand 512-byte strings are a
// megabyte, and only the second crosses the limit below.
func TestImplodeRefusesPastTheBudget(t *testing.T) {
	const n = 2000
	ks := make([]string, n)
	vs := make([]string, n)
	for i := range n {
		ks[i] = "one-hot-key"
		vs[i] = strings.Repeat("x", 512)
	}
	_, err := ursus.Frame(ursus.Values("k", ks), ursus.Values("v", vs)).
		GroupBy(ursus.Col("k")).
		Agg(ursus.Col("v").Implode().Alias("all")).
		Collect(t.Context(), ursus.WithBatchSize(256),
			ursus.WithMemoryLimit(128<<10), ursus.WithSpillDir(t.TempDir()))
	if err == nil {
		t.Fatal("imploding a megabyte into one key under a 128 KiB limit should refuse")
	}
	if !strings.Contains(err.Error(), "memory limit") {
		t.Errorf("the error should name the limit: %v", err)
	}
}

// TestWindowInsideAggIsRefused. Until step 46 this resolved cleanly, printed a plan
// in Explain, and died at Collect with a message announcing itself as an ursus bug —
// while hashAggSink's own comment claimed the check existed.
func TestWindowInsideAggIsRefused(t *testing.T) {
	lf := implodeFrame().
		GroupBy(ursus.Col("k")).
		Agg(ursus.Col("v").CumSum(false).Sum().Alias("bad"))

	// Explain must fail too, or the refusal is one only Collect notices.
	if _, err := lf.Explain(t.Context()); err == nil {
		t.Error("Explain should not print a plan that cannot run")
	}

	_, err := lf.Collect(t.Context())
	if err == nil {
		t.Fatal("a window inside Agg() should be refused")
	}
	if strings.Contains(strings.ToLower(err.Error()), "ursus bug") ||
		strings.Contains(err.Error(), "should have been extracted") {
		t.Errorf("a user mistake must not be reported as an internal error: %v", err)
	}
	if !strings.Contains(err.Error(), "window") {
		t.Errorf("the error should name what is wrong: %v", err)
	}
}

// TestOverWithOrderByOverAnAggregateIsRefused. The first OverWith coverage in the
// repository: the field was accepted and silently discarded, which is worse than
// refusing it, because the user asked for something and got no answer either way.
func TestOverWithOrderByOverAnAggregateIsRefused(t *testing.T) {
	_, err := implodeFrame().
		WithColumns(ursus.Col("v").Implode().OverWith(ursus.WindowSpec{
			PartitionBy: []ursus.Expr{ursus.Col("k")},
			OrderBy:     []ursus.SortKey{ursus.Asc(ursus.Col("v"))},
		}).Alias("peers")).
		Collect(t.Context())
	if err == nil {
		t.Fatal("an ordering over an aggregate is not honoured and must not be ignored")
	}
	if !strings.Contains(err.Error(), "ordering") {
		t.Errorf("the error should say what was not honoured: %v", err)
	}

	// Without the ordering the same window is fine.
	if _, err := implodeFrame().
		WithColumns(ursus.Col("v").Implode().OverWith(ursus.WindowSpec{
			PartitionBy: []ursus.Expr{ursus.Col("k")},
		}).Alias("peers")).
		Collect(t.Context(), ursus.WithVerify()); err != nil {
		t.Fatalf("no ordering, so it should work: %v", err)
	}
}

// TestMapJoinRefusalPointsAtImplode. The strategy stays refused, but the reason
// changed twice and is now the true one: it is a synonym for Implode().Over().
func TestMapJoinRefusalPointsAtImplode(t *testing.T) {
	_, err := implodeFrame().
		WithColumns(ursus.Col("v").Sum().OverWith(ursus.WindowSpec{
			PartitionBy: []ursus.Expr{ursus.Col("k")},
			Mapping:     ursus.MapJoin,
		}).Alias("x")).
		Collect(t.Context())
	if err == nil {
		t.Fatal("MapJoin is still refused")
	}
	if !strings.Contains(err.Error(), "Implode") {
		t.Errorf("the refusal should name the thing that does this: %v", err)
	}
}
