package ursus_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestMetaNamespace is step 326: Polars' meta namespace, Expr.Meta(). Every answer
// is Polars 1.44.1's.
func TestMetaNamespace(t *testing.T) {
	c := ursus.Col
	z := c("a").Add(c("b").Mul(c("a"))).Alias("z")
	when := ursus.When(c("c").Gt(1)).Then(c("a")).Otherwise(c("b"))

	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want []string
	}{
		{"a sum of products", z, []string{"a", "b", "a"}},
		{"over a window", c("a").Sum().Over(c("g")), []string{"a", "g"}},
		{"sorted by another", c("a").SortBy(c("b")), []string{"a", "b"}},
		{"a conditional, its branches first", when, []string{"a", "b", "c"}},
		{"two names", c("a", "b"), nil},
		{"all", ursus.All(), nil},
		{"a pattern", ursus.ColRegex("^a.*$"), nil},
		{"a literal", ursus.Lit(1), nil},
	} {
		if got := tc.e.Meta().RootNames(); !slices.Equal(got, tc.want) {
			t.Errorf("root names of %s: %q, want %q", tc.name, got, tc.want)
		}
	}

	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		{"an alias", z, "z"},
		{"over a window", c("a").Sum().Over(c("g")), "a"},
		{"a suffix", c("a").Name().Suffix("_x"), "a_x"},
		{"a count", ursus.Len(), "len"},
		{"a conditional", when, "a"},
		{"a literal", ursus.Lit(1), "literal"},
	} {
		if got, err := tc.e.Meta().OutputName(); err != nil || got != tc.want {
			t.Errorf("output name of %s: %q, %v, want %q", tc.name, got, err, tc.want)
		}
	}
	for _, e := range []ursus.Expr{c("a", "b"), ursus.All()} {
		if _, err := e.Meta().OutputName(); err == nil || !strings.Contains(err.Error(), "unable to find root column name") {
			t.Errorf("the output name of %s: %v", e, err)
		}
	}

	flags := []struct {
		name string
		got  bool
		want bool
	}{
		{"a column is a column", c("a").Meta().IsColumn(), true},
		{"an aliased one is not", c("a").Alias("b").Meta().IsColumn(), false},
		{"a sum is not", c("a").Add(1).Meta().IsColumn(), false},
		{"a type's columns are not", ursus.ColDType(ursus.Int64).Meta().IsColumn(), false},
		{"two names are several", c("a", "b").Meta().HasMultipleOutputs(), true},
		{"all is several", ursus.All().Meta().HasMultipleOutputs(), true},
		{"a pattern is several", ursus.ColRegex("^a$").Meta().HasMultipleOutputs(), true},
		{"a type's are several", ursus.ColDType(ursus.Int64).Meta().HasMultipleOutputs(), true},
		{"a sum of two is several", c("a", "b").Sum().Meta().HasMultipleOutputs(), true},
		{"one column is one", c("a").Meta().HasMultipleOutputs(), false},
		{"a pattern is a pattern", ursus.ColRegex("^a$").Meta().IsRegexProjection(), true},
		// Col never reads a name as a pattern, where Polars' col("^a$") does.
		{"an anchored name is a name", c("^a$").Meta().IsRegexProjection(), false},
		{"a name is not", c("a").Meta().IsRegexProjection(), false},
		{"a column selects", c("a").Meta().IsColumnSelection(false), true},
		{"all selects", ursus.All().Meta().IsColumnSelection(false), true},
		{"exclude selects", ursus.Exclude("a").Meta().IsColumnSelection(false), true},
		{"an aliased column does not", c("a").Alias("q").Meta().IsColumnSelection(false), false},
		{"an aliased column does, aliasing allowed", c("a").Alias("q").Meta().IsColumnSelection(true), true},
		{"a sum does not", c("a").Add(1).Meta().IsColumnSelection(true), false},
		{"a literal is one", ursus.Lit(1).Meta().IsLiteral(false), true},
		{"an aliased one is not", ursus.Lit(1).Alias("x").Meta().IsLiteral(false), false},
		{"an aliased one is, aliasing allowed", ursus.Lit(1).Alias("x").Meta().IsLiteral(true), true},
		{"equal aliases", c("a").Alias("x").Meta().Eq(c("a").Alias("x")), true},
		{"other aliases", c("a").Alias("x").Meta().Eq(c("a").Alias("y")), false},
		{"an integer and a float", ursus.Lit(int64(1)).Meta().Eq(ursus.Lit(1.0)), false},
		{"no aliases", z.Meta().UndoAliases().Meta().Eq(c("a").Add(c("b").Mul(c("a")))), true},
		{"no aliases inside, nor a suffix", c("a").Alias("q").Mul(2).Name().Suffix("_x").Meta().UndoAliases().
			Meta().Eq(c("a").Mul(2)), true},
		{"no keep", c("a").Alias("q").Name().Keep().Meta().UndoAliases().Meta().Eq(c("a")), true},
	}
	for _, f := range flags {
		if f.got != f.want {
			t.Errorf("%s: %v, want %v", f.name, f.got, f.want)
		}
	}

	// pop is the last input first, as Polars gives it.
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want []ursus.Expr
	}{
		{"a sum", c("a").Add(c("b")), []ursus.Expr{c("b"), c("a")}},
		{"an aggregate", c("a").Sum(), []ursus.Expr{c("a")}},
		{"over a window", c("a").Sum().Over(c("g")), []ursus.Expr{c("g"), c("a").Sum()}},
		{"a column", c("a"), nil},
	} {
		got := tc.e.Meta().Pop()
		ok := len(got) == len(tc.want)
		for i := range got {
			ok = ok && got[i].Meta().Eq(tc.want[i])
		}
		if !ok {
			t.Errorf("pop of %s: %v, want %v", tc.name, got, tc.want)
		}
	}

	got := c("a").Add(c("b").Mul(2)).Alias("z").Meta().TreeFormat()
	want := "alias(\"z\")\n└── binary: +\n    ├── " + c("a").String() + "\n    └── binary: *\n        ├── " +
		c("b").String() + "\n        └── " + ursus.Lit(2).String() + "\n"
	if got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
}
