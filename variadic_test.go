package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestVariadicOverASelection (step 302): a selection among a variadic function's
// operands is its columns, one operand each, as Polars writes them out, so the
// function answers one column, named after the first. Before, the whole function
// expanded once per column, and MaxHorizontal(All()) gave each column itself. The
// answers are Polars 2.1.0's, recorded in the variadic group of testdata/oracle and
// spelled in oracle_variadic_test.go since step 497, which took this file's pasted
// values. What stays: HashRows over a selection is one column named after the first,
// its numbers ursus's own; and a selection of nothing is refused.
func TestVariadicOverASelection(t *testing.T) {
	lf := ursus.Frame(
		ursus.ValuesNullable("a", []int64{1, 0}, []bool{true, false}),
		ursus.ValuesNullable("b", []int64{0, 2}, []bool{false, true}),
	)
	df, err := lf.Select(ursus.HashRows(0)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if names := df.Schema().Names(); len(names) != 1 || names[0] != "a" {
		t.Errorf("hash_rows(all) is the columns %v, want one named a", names)
	}
	for i := range df.Height() {
		if _, ok, _ := df.At[uint64](i, "a"); !ok {
			t.Errorf("hash_rows(all) row %d is null", i)
		}
	}
	// Polars: expected at least 1 input in sum_horizontal.
	_, err = lf.Select(ursus.SumHorizontal(ursus.ColDType(ursus.Float64))).Collect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "sum_horizontal of no expression") {
		t.Errorf("a selection of nothing: got %v", err)
	}
}

// TestVariadicInAListsBody (step 302): a variadic function or a fold in List().Eval's
// or CumulativeEval's body is built over its expressions as written, since the body
// is lowered when it is written and no schema reaches it after. The gate found every
// variadic there unbuilt once they waited for one; a fold there never was built
// (step 266). Recorded in the variadic group; what stays is a fold in
// CumulativeEval, which Polars refuses ("not allowed with non-scalar output", its
// function opaque to it) and ursus answers, built ahead into a value a prefix: the
// same as the sum written out (feature-gaps.md §9); and the refusals.
func TestVariadicInAListsBody(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	add := func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }
	frame := ursus.Frame(ursus.Values("g", []int64{1, 1, 2}), ursus.Values("x", []int64{1, 2, 3}))
	lists := frame.GroupBy(c("g")).Agg(c("x").Implode()).Sort(ursus.Asc(c("g")))
	got := csvOf(t, frame.Select(c("x").CumulativeEval(ursus.Fold(0, add, el().Sum(), el().Max()))))
	want := csvOf(t, frame.Select(c("x").CumulativeEval(el().Sum().Add(el().Max()))))
	if got != want {
		t.Errorf("a fold in cumulative_eval:\ngot  %q\nwant %q, the sum written out", got, want)
	}
	for _, tc := range []struct {
		name string
		body ursus.Expr
		want string
	}{
		// Polars: named columns are not allowed in `eval` functions.
		{"a selection", ursus.SumHorizontal(ursus.All()), "list.eval reads all(), and only Element() is read inside list.eval"},
		// Polars: multiple fields with name '' found.
		{"cum_sum_horizontal", ursus.CumSumHorizontal(el(), el()), "cum_sum inside a list's body is not supported"},
	} {
		_, err := lists.Select(c("x").List().Eval(tc.body)).Collect(t.Context())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error with %q", tc.name, err, tc.want)
		}
	}
}

// TestFormatOfAVariadic (step 302): Format is named after its first argument when it
// is written, and a variadic function not yet built is named as the function will
// be, after its first operand (the variadic group records the names). Format expands
// per column, as Polars' format does, where ConcatStr takes a selection as its
// operands: Polars formats each column of a selection, named after it; here each is
// named after the first argument when the format is written, so a selection is
// refused (feature-gaps.md §1), but not taken as one column of them all, as
// ConcatStr's.
func TestFormatOfAVariadic(t *testing.T) {
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("b", []int64{3, 4}))
	_, err := lf.Select(ursus.Format("<{}>", ursus.All())).Collect(t.Context())
	if err == nil || !strings.Contains(err.Error(), `would all be named "literal"`) {
		t.Errorf("a format of all(): got %v", err)
	}
}
