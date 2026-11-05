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
// several names, a regex or "*", a column a field. Every answer over structFrame is
// Polars 2.1.0's, recorded in the structfields group of testdata/oracle and spelled
// in oracle_structfields_test.go since step 499, which took this file's hand table;
// what stays asserts the refusals.
func TestStructFieldOfSeveralNames(t *testing.T) {
	c := ursus.Col
	s := c("s").Struct()
	lf := structFrame()
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
