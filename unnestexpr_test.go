package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// TestUnnestIsAnExpression is E4's last stub (step 260): Unnest is a struct's fields,
// a column each named after its field, beside other columns in Select, after the
// frame's in WithColumns, and under an operation each field alone. Every answer is
// Polars 1.44.1's for struct.unnest().
func TestUnnestIsAnExpression(t *testing.T) {
	c := ursus.Col
	lf := ursus.ScanParquet(structFixture(t))
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"alone", lf.Select(c("person").Unnest()), "age,city\n30,NY\n,SF\n,\n,\n"},
		{"before a column", lf.Select(c("person").Unnest(), c("id")), "age,city,id\n30,NY,1\n,SF,2\n,,3\n,,4\n"},
		{"after a column", lf.Select(c("id"), c("person").Unnest()), "id,age,city\n1,30,NY\n2,,SF\n3,,\n4,,\n"},
		{"in WithColumns", lf.WithColumns(c("person").Unnest()).Drop("person"),
			"id,age,city\n1,30,NY\n2,,SF\n3,,\n4,,\n"},
		{"the struct's namespace", lf.Select(c("person").Struct().Unnest()), "age,city\n30,NY\n,SF\n,\n,\n"},
	} {
		if got := csvOf(t, tc.q); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}

	// Each field alone under an operation, and under an aggregate. Polars: plus one
	// p [2, None, 4], r [3.5, None, None]; summed p 4, r 2.5.
	pts := ursus.Frame(ursus.ValuesNullable("p", []int64{1, 0, 3}, []bool{true, false, true}),
		ursus.ValuesNullable("r", []float64{2.5, 0, 0}, []bool{true, false, false})).
		Select(ursus.Struct(c("p"), c("r")).Alias("s"))
	if got, want := csvOf(t, pts.Select(c("s").Unnest().Add(1))), "p,r\n2,3.5\n,\n4,\n"; got != want {
		t.Errorf("plus one:\ngot  %q\nwant %q", got, want)
	}
	if got, want := csvOf(t, pts.Select(c("s").Unnest().Sum())), "p,r\n4,2.5\n"; got != want {
		t.Errorf("summed:\ngot  %q\nwant %q", got, want)
	}

	for name, tc := range map[string]struct {
		e    ursus.Expr
		want string
	}{
		"not a struct":     {c("id").Unnest(), "requires a Struct column"},
		"two structs":      {c("person").Unnest().Add(c("person").Alias("q").Unnest()), "more than one"},
		"with a selection": {c("person").Unnest().Add(c("id", "id")), "more than one"},
	} {
		if _, err := lf.Select(tc.e).Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, tc.want)
		}
	}
}
