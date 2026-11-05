package ursus_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// fruits are the categories of fruit's Enum, in their order.
var fruits = []string{"apple", "banana", "éclair", ""}

// fruit is Polars' Enum(["apple", "banana", "éclair", ""]) column s of banana, a
// null, éclair, apple, "" and banana, beside g, the groups 0, 1, 0, 1, 0, 1.
func fruit() *ursus.LazyFrame {
	c := ursus.Col
	return ursus.Frame(ursus.ValuesNullable("s", []string{"banana", "", "éclair", "apple", "", "banana"},
		[]bool{true, false, true, true, true, true}), ursus.Values("g", []int64{0, 1, 0, 1, 0, 1})).
		Select(c("s").Cast(ursus.Enum(fruits...)), c("g"))
}

// The categorical namespace on an Enum, C of v0.6.6-scope.md (step 273): every
// answer over fruit is Polars 2.1.0's, recorded in the expr_cat group of
// testdata/oracle and spelled in oracle_expr_cat_test.go since step 499, which took
// this file's hand table. GetCategories, which Polars 2 removed, is held to the
// type's own category list below.

// TestCatPhysicalWidth: the index's type grows with the categories, Uint8 to 255,
// Uint16 to 65,535 and Uint32 past, as Polars' does; the last category's index is
// the count less one.
func TestCatPhysicalWidth(t *testing.T) {
	c := ursus.Col
	for _, tc := range []struct {
		n    int
		want ursus.DataType
	}{{255, ursus.Uint8}, {256, ursus.Uint16}, {65_535, ursus.Uint16}, {65_536, ursus.Uint32}} {
		cats := make([]string, tc.n)
		for i := range cats {
			cats[i] = "c" + strconv.Itoa(i)
		}
		q := ursus.Frame(ursus.Values("s", []string{cats[tc.n-1]})).Select(c("s").Cast(ursus.Enum(cats...)).Cat().Physical())
		got, err := q.Collect(t.Context())
		if err != nil || got.Schema().Field(0).Type != tc.want {
			t.Errorf("physical of %d categories: %v, %v, want %s", tc.n, got, err, tc.want)
		}
		if v := csvOf(t, q); v != "s\n"+strconv.Itoa(tc.n-1)+"\n" {
			t.Errorf("the last of %d categories: %q", tc.n, v)
		}
	}
}

// TestGetCategories: the categories, a column of their own length in a selection,
// refused beside a column of another length, and a list a group in an aggregation.
// Polars 2 removed get_categories (the Enum's dtype.categories is its answer), so
// every expected frame here is built from the type's own list, fruits.
func TestGetCategories(t *testing.T) {
	c := ursus.Col
	lf := fruit()
	same := func(t *testing.T, q, want *ursus.LazyFrame) {
		t.Helper()
		got, err := q.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		w, err := want.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, w)
	}
	cats := ursus.Enum(fruits...).Categories()
	joined := strings.Join(cats, "|")
	count := uint32(len(cats)) // an expression's Len is a Uint32
	same(t, lf.Select(c("s").Cat().GetCategories()), ursus.Frame(ursus.Values("s", cats)))
	same(t, lf.Select(c("s").Cat().GetCategories().Len()), ursus.Frame(ursus.Values("s", []uint32{count})))
	same(t, lf.GroupBy(c("g")).Agg(c("s").Cat().GetCategories().List().Join("|")).Sort(ursus.Asc(c("g"))),
		ursus.Frame(ursus.Values("g", []int64{0, 1}), ursus.Values("s", []string{joined, joined})))
	// In WithColumns beside a frame of its length, of an aliased receiver, its length
	// a group, and a Filter by their count: one value for the frame, all its rows
	// kept, or none.
	four := fruit().Head(4)
	same(t, four.WithColumns(c("s").Cat().GetCategories().Alias("c")).Select(c("c")), ursus.Frame(ursus.Values("c", cats)))
	same(t, four.Select(c("s").Alias("t").Cat().GetCategories()), ursus.Frame(ursus.Values("t", cats)))
	same(t, four.GroupBy(c("g")).Agg(c("s").Cat().GetCategories().Len()).Sort(ursus.Asc(c("g"))),
		ursus.Frame(ursus.Values("g", []int64{0, 1}), ursus.Values("s", []uint32{count, count})))
	same(t, four.Filter(c("s").Cat().GetCategories().Len().Gt(count-1)).Select(ursus.Len()),
		ursus.Frame(ursus.Values("len", []uint64{4})))
	same(t, four.Filter(c("s").Cat().GetCategories().Len().Gt(count)).Select(ursus.Len()),
		ursus.Frame(ursus.Values("len", []uint64{0})))
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: "Series length 4 doesn't match the DataFrame height of 6".
		"beside a column":     {lf.Select(c("s").Cat().GetCategories(), c("g")), "length"},
		"of a String":         {lf.Select(c("s").Cast(ursus.String).Cat().GetCategories()), "requires an Enum"},
		"a String's physical": {lf.Select(c("s").Cast(ursus.String).Cat().Physical()), "requires an Enum"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}

// TestStrOfAnEnum: Str() reads an Enum as its text, where Polars' str refuses one; so
// do the string functions of an expression, whose operands may be Enums too.
func TestStrOfAnEnum(t *testing.T) {
	c := ursus.Col
	lf := fruit()
	text := lf.Select(c("s").Cast(ursus.String), c("g"))
	for _, e := range []func(ursus.Expr) ursus.Expr{
		func(x ursus.Expr) ursus.Expr { return x.Str().ToUpper() },
		func(x ursus.Expr) ursus.Expr { return x.Str().Contains("an", true) },
		func(x ursus.Expr) ursus.Expr { return x.Str().ReplaceAll("a", "4", true) },
		func(x ursus.Expr) ursus.Expr { return x.Str().LenChars() },
		func(x ursus.Expr) ursus.Expr { return x.Str().Split("a").List().Len() },
		func(x ursus.Expr) ursus.Expr { return x.Str().StartsWith(c("s").Str().Head(1)) },
		func(x ursus.Expr) ursus.Expr { return x.Str().JsonPathMatch("$") },
	} {
		if got, want := csvOf(t, lf.Select(e(c("s")))), csvOf(t, text.Select(e(c("s")))); got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", e(c("s")), got, want)
		}
	}
}
