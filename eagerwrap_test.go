package ursus_test

import (
	"context"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestEagerWrappersAnswerAsTheirLazyMethods: each eager method of step 228 answers
// as its lazy method collected, over a frame whose answers differ from method to
// method, so a wrapper calling the wrong one is caught.
func TestEagerWrappersAnswerAsTheirLazyMethods(t *testing.T) {
	ctx := t.Context()
	lf := ursus.Frame(
		ursus.ValuesNullable("k", []int64{3, 1, 4, 1, 5, 9}, []bool{true, true, true, true, true, false}),
		ursus.ValuesNullable("f", []float64{1.5, 0, 2.5, 3.5, 4.5, 5.5}, []bool{true, false, true, true, true, true}),
		ursus.Values("s", []string{"a", "b", "c", "d", "e", "f"}),
	).WithColumns(ursus.Col("f").Div(ursus.Col("f").Sub(2.5)).Alias("nan"))
	df, err := lf.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ursus.Frame(ursus.Values("k", []int64{1, 4, 7}), ursus.Values("o", []string{"x", "y", "z"})).
		Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	right, err := ursus.Frame(ursus.Values("r", []int64{10, 20, 30, 40, 50, 60})).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lists, err := ursus.Frame(ursus.Values("g", []int64{1, 1, 2})).GroupBy(ursus.Col("g")).
		Agg(ursus.Col("g").Implode().Alias("l")).Sort(ursus.Asc(ursus.Col("g"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	structs, err := df.Select(ctx, ursus.Struct(ursus.Col("k"), ursus.Col("s")).Alias("st"))
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := df.Sort(ctx, ursus.Asc(ursus.Col("k")))
	if err != nil {
		t.Fatal(err)
	}
	k := ursus.Desc(ursus.Col("k"))
	cases := map[string]struct {
		eager func(context.Context) (*ursus.DataFrame, error)
		lazy  *ursus.LazyFrame
	}{
		"Slice":        {func(c context.Context) (*ursus.DataFrame, error) { return df.Slice(c, 1, 3) }, lf.Slice(1, 3)},
		"Limit":        {func(c context.Context) (*ursus.DataFrame, error) { return df.Limit(c, 2) }, lf.Limit(2)},
		"DropNulls":    {func(c context.Context) (*ursus.DataFrame, error) { return df.DropNulls(c, "k") }, lf.DropNulls("k")},
		"FillNull":     {func(c context.Context) (*ursus.DataFrame, error) { return df.FillNull(c, 0, "k") }, lf.FillNull(0, "k")},
		"FillNan":      {func(c context.Context) (*ursus.DataFrame, error) { return df.FillNan(c, 7.0) }, lf.FillNan(7.0)},
		"TopK":         {func(c context.Context) (*ursus.DataFrame, error) { return df.TopK(c, 2, k) }, lf.TopK(2, k)},
		"BottomK":      {func(c context.Context) (*ursus.DataFrame, error) { return df.BottomK(c, 2, k) }, lf.BottomK(2, k)},
		"HStack":       {func(c context.Context) (*ursus.DataFrame, error) { return df.HStack(c, right) }, lf.HStack(right.Lazy())},
		"VStack":       {func(c context.Context) (*ursus.DataFrame, error) { return df.VStack(c, df) }, lf.VStack(lf)},
		"MergeSorted":  {func(c context.Context) (*ursus.DataFrame, error) { return sorted.MergeSorted(c, sorted, "k") }, sorted.Lazy().MergeSorted(sorted.Lazy(), "k")},
		"Explode":      {func(c context.Context) (*ursus.DataFrame, error) { return lists.Explode(c, "l") }, lists.Lazy().Explode("l")},
		"Unnest":       {func(c context.Context) (*ursus.DataFrame, error) { return structs.Unnest(c, "st") }, structs.Lazy().Unnest("st")},
		"WithRowIndex": {func(c context.Context) (*ursus.DataFrame, error) { return df.WithRowIndex(c, "i", 5) }, lf.WithRowIndex("i", 5)},
		"Remove":       {func(c context.Context) (*ursus.DataFrame, error) { return df.Remove(c, ursus.Col("k").Gt(2)) }, lf.Remove(ursus.Col("k").Gt(2))},
		"GroupBy": {func(c context.Context) (*ursus.DataFrame, error) {
			return df.GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n")).Collect(c)
		}, lf.GroupBy(ursus.Col("k")).Agg(ursus.Len().Alias("n"))},
	}
	for name, cs := range cases {
		got, err := cs.eager(ctx)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want, err := cs.lazy.Collect(ctx)
		if err != nil {
			t.Errorf("%s, lazily: %v", name, err)
			continue
		}
		ursustest.AssertFrameEqual(t, got, want, ursustest.IgnoreRowOrder())
	}

	asof, err := sorted.DropNulls(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	gotA, err := asof.JoinAsOf(ctx, other, ursus.AsOfOn(ursus.Col("k")))
	if err != nil {
		t.Fatal(err)
	}
	wantA, err := asof.Lazy().JoinAsOf(other.Lazy(), ursus.AsOfOn(ursus.Col("k"))).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, gotA, wantA)
}
