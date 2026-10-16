package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

func foldFixture() *ursus.LazyFrame {
	c := ursus.Col
	return ursus.Frame(ursus.ValuesNullable("a", []int64{1, 2, 0}, []bool{true, true, false}),
		ursus.ValuesNullable("b", []int64{10, 0, 30}, []bool{true, false, true}),
		ursus.Values("f", []float64{0.5, 1.5, 2.5}),
		ursus.ValuesNullable("s", []string{"x", "y", ""}, []bool{true, true, false}),
		ursus.ValuesNullable("lo", []int64{1, 0, 5}, []bool{true, false, true}),
		ursus.Values("hi", []int64{3, 1, 5})).
		WithColumns(ursus.IntRanges(c("lo"), c("hi"), 1).Alias("l")) // [1, 2], null, []
}

func add(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }

// TestConcatList is F1 of v0.6.6-scope.md (step 266): each row's operands in one
// list. Polars: [[1, 10], [2, None], [None, 30]]; [[1, 2, 1], None, [None]]; [[1.0,
// 0.5], [2.0, 1.5], [None, 2.5]]; [[7, 1], [7, 2], [7, None]]; [[1, 2, 1, 2], None,
// []].
func TestConcatList(t *testing.T) {
	c := ursus.Col
	lf := foldFixture()
	for name, tc := range map[string]struct {
		e    ursus.Expr
		want []string
	}{
		"two values":           {ursus.ConcatList(c("a"), c("b")), []string{"[1, 10]", "[2, null]", "[null, 30]"}},
		"a list and a value":   {ursus.ConcatList(c("l"), c("a")), []string{"[1, 2, 1]", "null", "[null]"}},
		"an integer and float": {ursus.ConcatList(c("a"), c("f")), []string{"[1, 0.5]", "[2, 1.5]", "[null, 2.5]"}},
		"a literal":            {ursus.ConcatList(ursus.Lit(7), c("a")), []string{"[7, 1]", "[7, 2]", "[7, null]"}},
		"two lists":            {ursus.ConcatList(c("l"), c("l")), []string{"[1, 2, 1, 2]", "null", "[]"}},
	} {
		df, err := lf.Select(tc.e).Collect(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(df.String(), w) {
				t.Errorf("%s lacks Polars' %s:\n%s", name, w, df)
			}
		}
	}
	// Polars casts the number to text beside a string; here it is refused.
	if _, err := lf.Select(ursus.ConcatList(c("a"), c("s"))).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "no common type") {
		t.Errorf("an integer beside a string: %v", err)
	}
}

// TestFolds: fold, reduce and their running forms, built when the query is planned,
// over a selection of several columns too. Every answer is Polars 1.44.1's: fold
// [11, None, None] named after its accumulator; reduce named after its first
// expression; cum_fold and cum_reduce {a: 1, b: 11}, {a: 2, b: None}, {None, None};
// cum_sum_horizontal of a and f {1.0, 1.5}, {2.0, 3.5}, {None, None}, both Float64.
func TestFolds(t *testing.T) {
	c := ursus.Col
	lf := foldFixture()
	mul := func(acc, x ursus.Expr) ursus.Expr { return acc.Mul(x) }
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"fold", lf.Select(ursus.Fold(0, add, c("a", "b"))), "literal\n11\n\n\n"},
		{"fold from a column", lf.Select(ursus.Fold(c("f"), mul, c("a"), c("b"))), "f\n5.0\n\n\n"},
		{"fold over every column", lf.Select(c("a", "b")).Select(ursus.Fold(0, add, ursus.All())), "literal\n11\n\n\n"},
		{"reduce", lf.Select(ursus.Reduce(add, c("a", "b"))), "a\n11\n\n\n"},
		{"cum_fold", lf.Select(ursus.CumFold(0, add, c("a", "b"))).
			Select(c("cum_fold").Struct().Field("a"), c("cum_fold").Struct().Field("b").Alias("b")), "cum_fold,b\n1,11\n2,\n,\n"},
		{"cum_reduce", lf.Select(ursus.CumReduce(add, c("a", "b"))).
			Select(c("cum_reduce").Struct().Field("a"), c("cum_reduce").Struct().Field("b").Alias("b")), "cum_reduce,b\n1,11\n2,\n,\n"},
		{"cum_sum_horizontal", lf.Select(ursus.CumSumHorizontal(c("a"), c("f"))).
			Select(c("cum_sum").Struct().Field("a"), c("cum_sum").Struct().Field("f").Alias("f")), "cum_sum,f\n1.0,1.5\n2.0,3.5\n,\n"},
		{"in an aggregation", lf.Select(ursus.Fold(0, add, c("a").Sum(), c("b").Sum())), "literal\n43\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// A struct's fields are named after the expressions folded in.
	df, err := lf.Select(ursus.CumReduce(add, c("a", "b"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s := df.Schema().String(); !strings.Contains(s, "cum_reduce") || !strings.Contains(s, "a: Int64") ||
		!strings.Contains(s, "b: Int64") {
		t.Errorf("cum_reduce's schema: %s", s)
	}
}

// TestFormat: Polars' answers ["1 and x", "2 and y", None] named after the first
// argument; ["n=0.5!", "n=1.5!", "n=2.5!"]; ["{} 1", "{} 2", None]; and a count of
// arguments that is not the placeholders' refused.
func TestFormat(t *testing.T) {
	c := ursus.Col
	lf := foldFixture()
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"two placeholders", lf.Select(ursus.Format("{} and {}", c("a"), c("s"))), "a\n1 and x\n2 and y\n\n"},
		{"text around one", lf.Select(ursus.Format("n={}!", c("f"))), "f\nn=0.5!\nn=1.5!\nn=2.5!\n"},
		{"escaped braces", lf.Select(ursus.Format("{{}} {}", c("a"))), "a\n{} 1\n{} 2\n\n"},
		{"no placeholder", lf.Select(c("a"), ursus.Format("plain")), "a,literal\n1,plain\n2,plain\n,plain\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	if _, err := lf.Select(ursus.Format("{} {}", c("a"))).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "2 placeholders and 1 arguments") {
		t.Errorf("too few arguments: %v", err)
	}
}
