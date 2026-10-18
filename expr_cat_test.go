package ursus_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// fruit is Polars' Enum(["apple", "banana", "éclair", ""]) column s of banana, a
// null, éclair, apple, "" and banana, beside g, the groups 0, 1, 0, 1, 0, 1.
func fruit() *ursus.LazyFrame {
	c := ursus.Col
	return ursus.Frame(ursus.ValuesNullable("s", []string{"banana", "", "éclair", "apple", "", "banana"},
		[]bool{true, false, true, true, true, true}), ursus.Values("g", []int64{0, 1, 0, 1, 0, 1})).
		Select(c("s").Cast(ursus.Enum("apple", "banana", "éclair", "")), c("g"))
}

// TestCatMethods is C of v0.6.6-scope.md (step 273): the categorical namespace on an
// Enum. Every answer is Polars 1.44.1's.
func TestCatMethods(t *testing.T) {
	c := ursus.Col
	s := c("s").Cat()
	lf := fruit()
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"starts_with", s.StartsWith("ba"), "true\n\nfalse\nfalse\nfalse\ntrue"},
		{"ends_with", s.EndsWith("ir"), "false\n\ntrue\nfalse\nfalse\nfalse"},
		{"an empty prefix", s.StartsWith(""), "true\n\ntrue\ntrue\ntrue\ntrue"},
		{"len_bytes", s.LenBytes(), "6\n\n7\n5\n0\n6"},
		{"len_chars", s.LenChars(), "6\n\n6\n5\n0\n6"},
		{"slice", s.Slice(1, 3), "ana\n\ncla\nppl\n\"\"\nana"},
		{"slice from the end", s.Slice(-2), "na\n\nir\nle\n\"\"\nna"},
		{"slice to the end", s.Slice(2), "nana\n\nlair\nple\n\"\"\nnana"},
		{"physical", s.Physical(), "1\n\n2\n0\n3\n1"},
	} {
		if got, want := csvOf(t, lf.Select(tc.e.Alias("r"))), "r\n"+tc.want+"\n"; got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, want)
		}
	}
	// The types: UInt32 lengths, String slices, and the indices at Polars' width.
	for _, tc := range []struct {
		e    ursus.Expr
		want ursus.DataType
	}{
		{s.LenBytes(), ursus.Uint32}, {s.Slice(0, 1), ursus.String}, {s.Physical(), ursus.Uint8},
	} {
		got, err := lf.Select(tc.e).Collect(t.Context())
		if err != nil || got.Schema().Field(0).Type != tc.want {
			t.Errorf("%s: %v, %v, want %s", tc.e, got, err, tc.want)
		}
	}
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
func TestGetCategories(t *testing.T) {
	c := ursus.Col
	lf := fruit()
	if got, want := csvOf(t, lf.Select(c("s").Cat().GetCategories())), "s\napple\nbanana\néclair\n\"\"\n"; got != want {
		t.Errorf("a selection:\ngot  %q\nwant %q", got, want)
	}
	if got := csvOf(t, lf.Select(c("s").Cat().GetCategories().Len())); got != "s\n4\n" {
		t.Errorf("its length: %q", got)
	}
	agg := lf.GroupBy(c("g")).Agg(c("s").Cat().GetCategories().List().Join("|")).Sort(ursus.Asc(c("g")))
	if got, want := csvOf(t, agg), "g,s\n0,apple|banana|éclair|\n1,apple|banana|éclair|\n"; got != want {
		t.Errorf("an aggregation:\ngot  %q\nwant %q", got, want)
	}
	// In WithColumns beside a frame of its length, of an aliased receiver, its length
	// a group, and Physical in a Filter.
	four := fruit().Head(4)
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"WithColumns", four.WithColumns(c("s").Cat().GetCategories().Alias("c")).Select(c("c")),
			"c\napple\nbanana\néclair\n\"\"\n"},
		{"an aliased receiver", four.Select(c("s").Alias("t").Cat().GetCategories()), "t\napple\nbanana\néclair\n\"\"\n"},
		{"its length a group", four.GroupBy(c("g")).Agg(c("s").Cat().GetCategories().Len()).Sort(ursus.Asc(c("g"))),
			"g,s\n0,4\n1,4\n"},
		{"Physical in a Filter", four.Filter(c("s").Cat().Physical().Eq(1)), "s,g\nbanana,0\n"},
		// One value for the frame: all its rows kept, or none.
		{"a Filter by their count", four.Filter(c("s").Cat().GetCategories().Len().Gt(3)).Select(ursus.Len()),
			"len\n4\n"},
		{"a Filter by too many", four.Filter(c("s").Cat().GetCategories().Len().Gt(4)).Select(ursus.Len()),
			"len\n0\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
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
