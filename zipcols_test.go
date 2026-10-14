package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestLengthChangingExpressionsInWithColumns is E4's second half (step 259): a
// column of the frame's height beside its columns. Every answer is Polars 1.44.1's.
func TestLengthChangingExpressionsInWithColumns(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.ValuesNullable("a", []int64{3, 1, 0, 2, 5}, []bool{true, true, false, true, true}),
		ursus.Values("b", []int64{10, 20, 30, 40, 50}))
	a, b := c("a"), c("b")
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"a sort beside", lf.WithColumns(a.Sort().Alias("s")), "a,b,s\n3,10,\n1,20,1\n,30,2\n2,40,3\n5,50,5\n"},
		{"a sort in place", lf.WithColumns(a.Sort()), "a,b\n,10\n1,20\n2,30\n3,40\n5,50\n"},
		{"a reverse plus a column", lf.WithColumns(a.Reverse().Add(b).Alias("r")),
			"a,b,r\n3,10,15\n1,20,22\n,30,\n2,40,41\n5,50,53\n"},
		{"a sort and a sum", lf.WithColumns(a.Sort().Alias("s"), b.Sum().Alias("t")),
			"a,b,s,t\n3,10,,150\n1,20,1,150\n,30,2,150\n2,40,3,150\n5,50,5,150\n"},
		{"a filter that keeps every row", lf.WithColumns(a.Filter(b.Gt(0)).Alias("f")),
			"a,b,f\n3,10,3\n1,20,1\n,30,\n2,40,2\n5,50,5\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestExplodeIsAnExpression: Explode is a List's elements, an empty or a null list one
// null (step 259). Polars: [1, 2, None, None, 3]; plus one, [2, 3, None, None, 4];
// summed, 6; in an aggregation by id % 2, [[1, 2, None], [None, 3]].
func TestExplodeIsAnExpression(t *testing.T) {
	c := ursus.Col
	lf := ursus.ScanParquet(writeListFile(t, []int64{1, 2, 3, 4},
		[][]int64{{1, 2}, {}, nil, {3}}, []bool{true, true, false, true}))
	if got, want := csvOf(t, lf.Select(c("tags").Explode().Alias("e"), c("tags").Explode().Add(1).Alias("p"))),
		"e,p\n1,2\n2,3\n,\n,\n3,4\n"; got != want {
		t.Errorf("in Select:\ngot  %q\nwant %q", got, want)
	}
	if got, want := csvOf(t, lf.Select(c("tags").Explode().Sum())), "tags\n6\n"; got != want {
		t.Errorf("summed:\ngot  %q\nwant %q", got, want)
	}
	df, err := lf.GroupBy(c("id").Mod(2).Alias("odd")).Agg(c("tags").Explode()).
		Sort(ursus.Desc(c("odd"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[1, 2, null]", "[null, 3]"} {
		if !strings.Contains(df.String(), want) {
			t.Errorf("in an aggregation, the lists lack Polars' %s:\n%s", want, df)
		}
	}
	// Polars returns a column that is not a List as it was; here it is refused.
	if _, err := lf.Select(c("id").Explode()).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), `"id" is Int64, not a List`) {
		t.Errorf("exploding an integer: %v", err)
	}
}
