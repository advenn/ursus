package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

type arrNsRow struct {
	A *[3]*int64
	T *[3]*string
	B *[3]*bool
	I int64
}

// arrNsFrame is step 342's frame, Polars': three Arrays of three, a null element in
// each, a null array, and a row's index.
func arrNsFrame() *ursus.LazyFrame {
	i := func(v int64) *int64 { return &v }
	s := func(v string) *string { return &v }
	b := func(v bool) *bool { return &v }
	return ursus.FromRows([]arrNsRow{
		{&[3]*int64{i(3), i(1), i(2)}, &[3]*string{s("b"), s("a"), s("b")}, &[3]*bool{b(true), b(false), b(true)}, 0},
		{&[3]*int64{i(4), nil, i(6)}, &[3]*string{s("x"), nil, s("y")}, &[3]*bool{nil, b(true), b(true)}, 1},
		{nil, nil, nil, 2},
		{&[3]*int64{i(2), i(2), i(1)}, &[3]*string{s("q"), s("q"), s("q")}, &[3]*bool{b(false), b(false), b(false)}, -1},
	})
}

// TestArrayNamespace is step 342: Polars' arr namespace. Every answer over this frame
// is Polars 2.1.0's, recorded in the arrns group of testdata/oracle and spelled in
// oracle_arrns_test.go since step 499, which took this file's hand table; what stays
// asserts the refusals.
func TestArrayNamespace(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	a, el := c("a").Arr(), ursus.Element
	lf := arrNsFrame().Select(c("A").Alias("a"), c("T").Alias("t"), c("B").Alias("b"), c("I").Alias("i"))
	for _, tc := range []struct {
		name string
		e    ursus.Expr
		want string
	}{
		// Polars: "`array.eval` is not allowed with non-length preserving expressions".
		{"eval of another length", a.Eval(el().Unique(), false), "changes each array's length"},
		{"eval of an aggregate", a.Eval(el().Sum(), false), "changes each array's length"},
		{"to_struct of two names", a.ToStruct("x", "y"), "names 2 fields of an Array of 3"},
		{"dot of another size", a.Dot(c("a").Arr().Head(2).List().ToArray(2)), "of one size"},
		{"dot of text", c("t").Arr().Dot(c("t")), "multiplies numbers"},
		{"a List's sum", c("a").Arr().ToList().Arr().Sum(), "requires an Array"},
		{"a List reversed", c("a").Arr().ToList().Arr().Reverse(), "requires an Array"},
	} {
		if _, err := lf.Select(tc.e.Alias("r")).Collect(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %q", tc.name, err, tc.want)
		}
	}
}
