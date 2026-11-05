package ursus_test

// Step 171: Struct().WithFields sets fields of a struct: replacing those an
// expression names, in place, and adding the rest after; keeping the struct's own
// validity. Its answers are Polars 2.1.0's, recorded in the withfields group of
// testdata/oracle and spelled in oracle_withfields_test.go since step 499, which
// took this file's hand rows; what stays asserts the refusals.

import (
	"testing"

	"github.com/advenn/ursus"
)

func TestWithFieldsRefuses(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("a", []int64{1}), ursus.Values("b", []int64{2}))
	for name, e := range map[string]ursus.Expr{
		"not a struct": c("a").Struct().WithFields(c("b")),
		"one name twice": ursus.Struct(c("a")).Alias("s").Struct().WithFields(
			c("b").Alias("x"), c("a").Alias("x")),
	} {
		if _, err := frame.Select(e).Collect(t.Context()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestWithFieldsNamesAFieldAsPlanned: a Field read is named after its field, as
// Polars names it (step 275), so WithFields of p's "name" sets "name", itself, and q
// is age and name with no "p" (the oracle's withfields/names_a_field_as_planned).
// Until step 275 the read was named after its struct, p, and WithFields added a field
// p; Polars refuses q's field p, and so does this.
func TestWithFieldsNamesAFieldAsPlanned(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("age", []int64{30, 40}), ursus.Values("name", []string{"ann", "bo"}))
	p := c("p").Struct()
	q := frame.Select(ursus.Struct(c("age"), c("name")).Alias("p")).
		Select(p.WithFields(p.Field("name")).Alias("q"))
	if _, err := q.Select(c("q").Struct().Field("p")).Collect(t.Context()); err == nil {
		t.Error("q has no field p")
	}
}
