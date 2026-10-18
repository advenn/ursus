package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// structFrame is Polars' frame: s, a struct of a [1, 3, None], ab [2, None, None], b
// ["x", "y", None] and c [1.5, None, None], beside k [1, 2, 3].
func structFrame() *ursus.LazyFrame {
	c := ursus.Col
	return ursus.Frame(
		ursus.ValuesNullable("a", []int64{1, 3, 0}, []bool{true, true, false}),
		ursus.ValuesNullable("ab", []int64{2, 0, 0}, []bool{true, false, false}),
		ursus.ValuesNullable("b", []string{"x", "y", ""}, []bool{true, true, false}),
		ursus.ValuesNullable("c", []float64{1.5, 0, 0}, []bool{true, false, false}),
		ursus.Values("k", []int64{1, 2, 3})).
		Select(ursus.Struct(c("a"), c("ab"), c("b"), c("c")).Alias("s"), c("k"))
}

// TestStructFieldOfSeveralNames is T of v0.6.6-scope.md (step 275): struct.field of
// several names, a regex or "*", a column a field. Every answer is Polars 1.44.1's.
func TestStructFieldOfSeveralNames(t *testing.T) {
	c := ursus.Col
	s := c("s").Struct()
	lf := structFrame()
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		// In the struct's order, whatever the names'.
		{"two", lf.Select(s.Field("b", "a")), "a,b\n1,x\n3,y\n,\n"},
		{"one", lf.Select(s.Field("b")), "b\nx\ny\n\n"},
		{"a regex", lf.Select(s.Field("^a.*$")), "a,ab\n1,2\n3,\n,\n"},
		{"a regex inside", lf.Select(s.Field("^.b$")), "ab\n2\n\n\n"},
		{"every field", lf.Select(s.Field("*")), "a,ab,b,c\n1,2,x,1.5\n3,,y,\n,,,\n"},
		{"a name and a regex", lf.Select(s.Field("c", "^a.*$")), "a,ab,c\n1,2,1.5\n3,,\n,,\n"},
		{"a name twice", lf.Select(s.Field("a", "a")), "a\n1\n3\n\n"},
		{"a regex and its match", lf.Select(s.Field("^a.*$", "a")), "a,ab\n1,2\n3,\n,\n"},
		{"a regex of none", lf.Select(c("k"), s.Field("^z.*$")), "k\n1\n2\n3\n"},
		{"an aggregate each", lf.Select(s.Field("a", "ab").Sum()), "a,ab\n4,2\n"},
		{"arithmetic each", lf.Select(s.Field("a", "ab").Mul(2)), "a,ab\n2,4\n6,\n,\n"},
		{"a suffix each", lf.Select(s.Field("a", "ab").Suffix("_x")), "a_x,ab_x\n1,2\n3,\n,\n"},
		// The suffix was undone by the field's name above it, for unnest too, until
		// step 275.
		{"unnest's suffix", lf.Select(s.Unnest().Suffix("_x")), "a_x,ab_x,b_x,c_x\n1,2,x,1.5\n3,,y,\n,,,\n"},
		// One field is named after it, not after its struct, as Polars names it.
		{"one field's name", lf.Select(s.Field("c")), "c\n1.5\n\n\n"},
		{"WithColumns", lf.WithColumns(s.Field("a", "b")).Select(c("k"), c("a"), c("b")), "k,a,b\n1,1,x\n2,3,y\n3,,\n"},
		{"a group's first", lf.GroupBy(c("k")).Agg(s.Field("a", "ab").First()).Sort(ursus.Asc(c("k"))),
			"k,a,ab\n1,1,2\n2,3,\n3,,\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
	// Over a fold's struct, which Expand builds before it writes the fields out.
	sum := func(acc, x ursus.Expr) ursus.Expr { return acc.Add(x) }
	ab := ursus.Frame(ursus.Values("a", []int64{1, 2}), ursus.Values("b", []int64{10, 20}))
	if got, want := csvOf(t, ab.Select(ursus.CumFold(0, sum, c("a"), c("b")).Struct().Field("a", "b"))),
		"a,b\n1,11\n2,22\n"; got != want {
		t.Errorf("a fold's fields:\ngot  %q\nwant %q", got, want)
	}
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// Polars: `"zz" not found`; `duplicate output name 'z'`; `not supported for dtype i64`.
		"a field it has not": {lf.Select(s.Field("a", "zz")), `no field "zz"`},
		"one alias for two":  {lf.Select(s.Field("a", "ab").Alias("z")), "would all be named"},
		"of an integer":      {lf.Select(c("k").Struct().Field("a", "b")), "Struct"},
		// Only ^...$ is a regex: "^a" is a name, as Polars reads it.
		"an open pattern": {lf.Select(s.Field("a", "^a")), `no field "^a"`},
		"a bad regex":     {lf.Select(s.Field("^(a$")), "not a regex"},
		"no names":        {lf.Select(s.Field()), "at least one name"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
