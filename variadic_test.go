package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestVariadicOverASelection (step 302): a selection among a variadic function's
// operands is its columns, one operand each, as Polars writes them out, so the
// function answers one column, named after the first. Before, the whole function
// expanded once per column, and MaxHorizontal(All()) gave each column itself. Every
// answer is Polars 1.44.1's.
func TestVariadicOverASelection(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(
		ursus.ValuesNullable("a", []int64{1, 0}, []bool{true, false}),
		ursus.ValuesNullable("b", []int64{0, 2}, []bool{false, true}),
	)
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"max_horizontal(all)", ursus.MaxHorizontal(ursus.All()), "a\n1\n2\n"},
		{"min_horizontal(all)", ursus.MinHorizontal(ursus.All()), "a\n1\n2\n"},
		{"sum_horizontal(col(a, b))", ursus.SumHorizontal(c("a", "b")), "a\n1\n2\n"},
		{"mean_horizontal(all)", ursus.MeanHorizontal(ursus.All()), "a\n1.0\n2.0\n"},
		{"any_horizontal(all > 1)", ursus.AnyHorizontal(ursus.All().Gt(1)), "a\n\ntrue\n"},
		{"all_horizontal(all > 0)", ursus.AllHorizontal(ursus.All().Gt(0)), "a\n\n\n"},
		{"coalesce(all, -1)", ursus.Coalesce(ursus.All(), ursus.Lit(int64(-1))), "a\n1\n2\n"},
		{"concat_str(all)", ursus.ConcatStr("-", ursus.All().Cast(ursus.String).FillNullWith("_")), "a\n1-_\n_-2\n"},
		{"struct(all), json", ursus.Struct(ursus.All()).Struct().JsonEncode(),
			"a\n\"{\"\"a\"\":1,\"\"b\"\":null}\"\n\"{\"\"a\"\":null,\"\"b\"\":2}\"\n"},
		{"concat_list(all), its length", ursus.ConcatList(ursus.All()).List().Len(), "a\n2\n2\n"},
		{"hash_rows(all) as one column", ursus.HashRows(0).IsNull(), "a\nfalse\nfalse\n"},
	} {
		q := lf.Select(tc.e)
		if got := csvOf(t, q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// List().Concat is ConcatList of its receiver first, as Polars' list.concat is
	// concat_list: over a selection, one column.
	lists := ursus.Frame(ursus.Values("x", []int64{1, 2}), ursus.Values("y", []int64{3, 4})).
		Select(ursus.ConcatList(c("x")).Alias("a"), ursus.ConcatList(c("y")).Alias("b"))
	concat := ursus.All().List().Concat(ursus.Lit([]int64{9})).List().Eval(ursus.Element().Cast(ursus.String)).List().Join(",")
	if got := csvOf(t, lists.Select(concat)); got != "a\n\"1,3,9\"\n\"2,4,9\"\n" {
		t.Errorf("all().list.concat([9]): %q", got)
	}
	// A selection outside the function still expands per column, and the function
	// inside it is built first: all() - sum_horizontal(all()), as Polars'.
	if got := csvOf(t, lf.Select(ursus.All().Sub(ursus.SumHorizontal(ursus.All())))); got != "a,b\n0,\n,0\n" {
		t.Errorf("all - sum_horizontal(all): %q", got)
	}
	// Polars: expected at least 1 input in sum_horizontal.
	_, err := lf.Select(ursus.SumHorizontal(ursus.ColDType(ursus.Float64))).Collect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "sum_horizontal of no expression") {
		t.Errorf("a selection of nothing: got %v", err)
	}
}

// TestVariadicInAListsBody (step 302): a variadic function or a fold in List().Eval's
// or CumulativeEval's body is built over its expressions as written, since the body
// is lowered when it is written and no schema reaches it after. The gate found every
// variadic there unbuilt once they waited for one; a fold there never was built
// (step 266). Every answer is Polars 1.44.1's, but where a comment says otherwise.
func TestVariadicInAListsBody(t *testing.T) {
	c, el := ursus.Col, ursus.Element
	add := func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }
	text := func(e ursus.Expr) ursus.Expr { return e.Cast(ursus.String) }
	frame := ursus.Frame(ursus.Values("g", []int64{1, 1, 2}), ursus.Values("x", []int64{1, 2, 3}))
	lists := frame.GroupBy(c("g")).Agg(c("x").Implode()).Sort(ursus.Asc(c("g")))
	each := func(body ursus.Expr) *ursus.LazyFrame { return lists.Select(c("x").List().Eval(body).Explode()) }
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"concat_str", lists.Select(c("x").List().Eval(ursus.ConcatStr(":", text(el()), text(el()))).List().Join("|")),
			"x\n1:1|2:2\n3:3\n"},
		{"sum_horizontal", each(ursus.SumHorizontal(el(), el())), "x\n2\n4\n6\n"},
		{"coalesce", each(ursus.Coalesce(ursus.When(el().Gt(1)).Then(el()).Otherwise(ursus.Null(ursus.Int64)), el().Mul(10))),
			"x\n10\n2\n3\n"},
		{"struct", each(ursus.Struct(el().Alias("a"), el().Mul(2).Alias("b"))).Select(c("x").Struct().JsonEncode()),
			"x\n\"{\"\"a\"\":1,\"\"b\"\":2}\"\n\"{\"\"a\"\":2,\"\"b\"\":4}\"\n\"{\"\"a\"\":3,\"\"b\"\":6}\"\n"},
		{"fold", each(ursus.Fold(0, add, el(), el())), "x\n2\n4\n6\n"},
		{"in a group-by", frame.GroupBy(c("g")).Agg(c("x").Implode().List().Eval(ursus.ConcatStr(":", text(el()), text(el()))).
			List().Join("|")).Sort(ursus.Asc(c("g"))).Select(c("x")), "x\n1:1|2:2\n3:3\n"},
		{"cumulative_eval", frame.Select(c("x").CumulativeEval(ursus.MaxHorizontal(el().Sum(), el().Max()))), "x\n1\n3\n6\n"},
		// Polars refuses a fold in cumulative_eval, "not allowed with non-scalar
		// output", its function being opaque to it; built ahead, this one is a value
		// a prefix.
		{"a fold in cumulative_eval", frame.Select(c("x").CumulativeEval(ursus.Fold(0, add, el().Sum(), el().Max()))),
			"x\n2\n5\n9\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
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
// be, after its first operand. Format expands per column, as Polars' format does,
// where ConcatStr takes a selection as its operands. Every name is Polars 1.44.1's.
func TestFormatOfAVariadic(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("b", []int64{3, 4}))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"a literal first", ursus.Format("<{}>", ursus.ConcatStr("-", ursus.Lit("x"), c("a").Cast(ursus.String))),
			"literal\n<x-1>\n<x-2>\n"},
		{"a column first", ursus.Format("<{}>", ursus.Coalesce(c("b"), c("a"))), "b\n<3>\n<4>\n"},
	} {
		if got := csvOf(t, lf.Select(tc.e)); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// Polars formats each column of a selection, named after it. Here each is named
	// after the first argument when the format is written, so a selection is refused
	// (feature-gaps.md §1), but not taken as one column of them all, as ConcatStr's.
	_, err := lf.Select(ursus.Format("<{}>", ursus.All())).Collect(t.Context())
	if err == nil || !strings.Contains(err.Error(), `would all be named "literal"`) {
		t.Errorf("a format of all(): got %v", err)
	}
}
