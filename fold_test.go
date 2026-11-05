package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// foldFixture is the frame the fold group's recorded cases hold: a and b nullable
// integers, a float, text, and a List from the ranges lo to hi, [1, 2], null, [].
func foldFixture() *ursus.LazyFrame {
	c := ursus.Col
	return ursus.Frame(ursus.ValuesNullable("a", []int64{1, 2, 0}, []bool{true, true, false}),
		ursus.ValuesNullable("b", []int64{10, 0, 30}, []bool{true, false, true}),
		ursus.Values("f", []float64{0.5, 1.5, 2.5}),
		ursus.ValuesNullable("s", []string{"x", "y", ""}, []bool{true, true, false}),
		ursus.ValuesNullable("lo", []int64{1, 0, 5}, []bool{true, false, true}),
		ursus.Values("hi", []int64{3, 1, 5})).
		WithColumns(ursus.IntRanges(c("lo"), c("hi"), 1).Alias("l"))
}

func add(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }

// TestConcatList is F1 of v0.6.6-scope.md (step 266): each row's operands in one
// list. The answers are Polars 2.1.0's, recorded in the fold group of testdata/oracle
// and spelled in oracle_fold_test.go since step 497, which took this file's pasted
// values. What stays is the refusal: Polars casts a number to text beside a string.
func TestConcatList(t *testing.T) {
	c := ursus.Col
	if _, err := foldFixture().Select(ursus.ConcatList(c("a"), c("s"))).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "no common type") {
		t.Errorf("an integer beside a string: %v", err)
	}
}

// TestFolds: fold, reduce and their running forms, built when the query is planned,
// over a selection of several columns too; recorded in the fold group. What stays is
// the running forms' schema: a struct whose fields are named after the expressions
// folded in.
func TestFolds(t *testing.T) {
	c := ursus.Col
	df, err := foldFixture().Select(ursus.CumReduce(add, c("a", "b"))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s := df.Schema().String(); !strings.Contains(s, "cum_reduce") || !strings.Contains(s, "a: Int64") ||
		!strings.Contains(s, "b: Int64") {
		t.Errorf("cum_reduce's schema: %s", s)
	}
}

// TestFormat: Polars' format, recorded in the fold group; what stays is a count of
// arguments that is not the placeholders', refused.
func TestFormat(t *testing.T) {
	c := ursus.Col
	if _, err := foldFixture().Select(ursus.Format("{} {}", c("a"))).Collect(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "2 placeholders and 1 arguments") {
		t.Errorf("too few arguments: %v", err)
	}
}
