package ursus_test

// Step 172: Interpolate's nearest method and rolling windows labelled at their middle
// row. Their answers are Polars 2.1.0's since step 500, recorded in the window172
// group of testdata/oracle and spelled in oracle_window172_test.go, which took this
// file's tables. What stays is the cell renderer the root package's other tests share.

import (
	"testing"

	"github.com/advenn/ursus"
)

// rendered172 renders a Float64 or Int64 column with nulls as "∅".
func rendered172(t *testing.T, df *ursus.DataFrame, name string) string {
	t.Helper()
	text, err := df.Select(t.Context(), ursus.Col(name).Cast(ursus.String))
	if err != nil {
		t.Fatal(err)
	}
	col, err := text.Column[string](name)
	if err != nil {
		t.Fatal(err)
	}
	out := ""
	for i := range col.Len() {
		v, ok := col.Get(i)
		if !ok {
			v = "∅"
		}
		out += v + " "
	}
	return out
}
