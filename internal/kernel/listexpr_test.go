package kernel_test

import (
	"fmt"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// boolText is a Bool column as text, "null" for a null.
func boolText(c *data.Column) []string {
	out := make([]string, c.Len())
	for i := range out {
		switch {
		case !c.IsValid(i):
			out[i] = "null"
		case c.Bools().Get(i):
			out[i] = "true"
		default:
			out[i] = "false"
		}
	}
	return out
}

// TestOneListAgainstEveryRow: a list one row long, a literal, is looked in by every
// row, and past a few elements through a hash set built once. Both ways answer as
// the scan does, for is_in and list.contains (step 254).
func TestOneListAgainstEveryRow(t *testing.T) {
	long := make([]int64, 40)
	for i := range long {
		long[i] = int64(i * 3)
	}
	x := i64("x", []int64{0, 3, 4, 117, 9, 1}, []bool{true, true, true, true, false, true})
	for _, tc := range []struct {
		list    []int64
		in, has string
	}{
		// list.contains finds a null value only among nulls, and these lists have none.
		{long[:5], "[true true false false null false]", "[true true false false false false]"},
		{long, "[true true false true null false]", "[true true false true false false]"},
	} {
		list := intLists("l", [][]int64{tc.list})
		in, err := kernel.HorizontalCall(expr.FnIsInExpr, "x", dtype.Bool, []*data.Column{x, list})
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(boolText(in)); got != tc.in {
			t.Errorf("is_in a list of %d: got %s, want %s", len(tc.list), got, tc.in)
		}
		has, err := kernel.HorizontalCall(expr.FnListContainsExpr, "l", dtype.Bool, []*data.Column{list, x})
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(boolText(has)); got != tc.has {
			t.Errorf("list.contains in a list of %d: got %s, want %s", len(tc.list), got, tc.has)
		}
	}
}
