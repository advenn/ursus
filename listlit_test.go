package ursus_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

func listLitFixture() *ursus.LazyFrame {
	return ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2}),
		ursus.Values("a", []int64{10, 20, 30, 40, 50}),
		ursus.Values("i", []int64{2, 0, 1, 1, 5}),
		ursus.ValuesNullable("v", []int64{1, 3, 0, 2, 7}, []bool{true, true, false, true, true}))
}

// csvOf is a query's answer as CSV.
func csvOf(t *testing.T, q *ursus.LazyFrame) string {
	t.Helper()
	var got bytes.Buffer
	if err := q.WriteCSV(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	return got.String()
}

// TestListLiterals is E6 (step 257): a Go slice is one List value. Every answer is
// Polars 1.44.1's for pl.lit of the same list.
func TestListLiterals(t *testing.T) {
	c, lit := ursus.Col, ursus.Lit[[]int64]
	lf := listLitFixture()

	// Alone in Select, a row for each of the frame's, as a Select of literals alone
	// is here (step 250); Polars {'literal': [[1, 2]]} is one.
	one, err := lf.Select(lit([]int64{1, 2}).Alias("l")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if one.Height() != 5 || !strings.Contains(one.String(), "[1, 2]") {
		t.Errorf("a List literal alone:\n%s", one)
	}
	if got := lit([]int64{1, 2}).String(); !strings.Contains(got, "lit([1, 2])") {
		t.Errorf("a List literal reads %s", got)
	}

	// Beside the frame's columns, against them, and read by them. Polars: is_in
	// [T, F, None, T, F]; contains [T, F, F, T, F]; get [7, 5, 6, 6, None]; the float
	// list [T, F, F, F, F].
	want := "sum,in,has,get,fin\n3,true,true,7,true\n3,false,false,5,false\n" +
		"3,,false,6,false\n3,true,true,6,false\n3,false,false,,false\n"
	if got := csvOf(t, lf.WithColumns(lit([]int64{1, 2}).Alias("l")).Select(
		c("l").List().Sum().Alias("sum"),
		c("v").IsIn(lit([]int64{1, 2})).Alias("in"),
		lit([]int64{1, 2}).List().Contains(c("v")).Alias("has"),
		lit([]int64{5, 6, 7}).List().Get(c("i")).Alias("get"),
		c("a").IsIn(ursus.Lit([]float64{10, 30.5})).Alias("fin"),
	)); got != want {
		t.Errorf("beside the columns:\ngot  %q\nwant %q", got, want)
	}

	// An empty list, a list of strings, and of instants; Polars len [0], [['a', 'b']].
	at := time.Date(2024, 1, 2, 3, 0, 0, 0, time.UTC)
	got := csvOf(t, lf.Select(lit([]int64{}).List().Len().Alias("n"),
		ursus.Lit([]string{"a", "b"}).List().Join("|").Alias("s"),
		ursus.Lit([]time.Time{at}).List().First().Dt().Hour().Alias("h"),
		ursus.Lit([]time.Duration{time.Minute}).List().First().Dt().TotalSeconds().Alias("d")))
	if want := "n,s,h,d\n0,a|b,3,60\n"; !strings.HasPrefix(got, want) {
		t.Errorf("other element types:\ngot  %q\nwant %q", got, want)
	}
}

// TestGatherInAnAggregation is E3's last reshape (step 257): Gather by a List literal,
// the same for every group, or by a column, each group's own. Polars: [[20, 10],
// [50, 40]]; [[30], [50]]; [[30, 10, 20], [50, 40]]; sorted descending then the
// first two summed, [50, 90].
func TestGatherInAnAggregation(t *testing.T) {
	c, lit := ursus.Col, ursus.Lit[[]int64]
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 1, 2, 2}),
		ursus.Values("a", []int64{10, 20, 30, 40, 50}),
		ursus.Values("i", []int64{2, 0, 1, 1, 0}))
	q := lf.GroupBy(c("g")).Agg(
		c("a").Gather(lit([]int64{1, 0})).Alias("lit"),
		c("a").Gather(lit([]int64{-1})).Alias("neg"),
		c("a").Gather(c("i")).Alias("col"),
		c("a").SortDesc().Gather(lit([]int64{0, 1})).Sum().Alias("top2"),
	).Sort(ursus.Asc(c("g")))
	df, err := q.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[20, 10]", "[50, 40]", "[30]", "[50]", "[30, 10, 20]"} {
		if !strings.Contains(df.String(), want) {
			t.Errorf("the lists lack Polars' %s:\n%s", want, df)
		}
	}
	if got, want := csvOf(t, q.Select(c("g", "top2"))), "g,top2\n1,50\n2,90\n"; got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}

	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: gather indices are out of bounds.
		"an index out of range":  {lf.GroupBy(c("g")).Agg(c("a").Gather(lit([]int64{0, 2}))), "out of range"},
		"one index":              {lf.GroupBy(c("g")).Agg(c("a").Gather(ursus.Lit(1))), "List of them"},
		"outside an aggregation": {lf.Select(c("a").Gather(lit([]int64{0}))), "not there yet"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
