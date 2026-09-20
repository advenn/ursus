package kernel_test

// Casting a List by casting its elements.
//
// dtype.CanCast has promised List -> List all along and the kernel refused it — the
// fourth plan-accepts / kernel-rejects divergence unary.go records. It hid longest
// because neither cast instrument could reach it: the contract matrix had no List
// column and no List cast target, and TestCanCastAgreesWithTheKernel names List as
// an unsamplable source.
//
// The risk that matters is a SLICED List. Offsets are absolute and the child stays
// whole, so a slice has offs[0] > 0 and a child holding elements belonging to rows
// the column does not have. Keeping those offsets over a wholly-cast child would
// still INDEX correctly — that much was checked rather than assumed — so the reason
// to gather is strictness: see the third test, where casting in place refuses a
// slice because of a value in a row outside it.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// intList builds [[1,2],[3],[],[4,5,6]] as a List(Int64).
func intList(t *testing.T) *data.Column {
	t.Helper()
	child := data.NewFixed("item", dtype.Int64,
		[]int64{1, 2, 3, 4, 5, 6}, bitmap.AllSet(6))
	return data.NewList("l", []int32{0, 2, 3, 3, 6}, child, bitmap.AllSet(4))
}

func rowsOf(t *testing.T, c *data.Column) [][]float64 {
	t.Helper()
	acc := c.Lists()
	vals, err := data.Values[float64](acc.Child())
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]float64, c.Len())
	for i := range c.Len() {
		start, end, ok := acc.Get(i)
		if !ok {
			out[i] = nil
			continue
		}
		out[i] = append([]float64{}, vals[start:end]...)
	}
	return out
}

func TestCastListConvertsItsElements(t *testing.T) {
	got, err := kernel.Cast("o", dtype.List(dtype.Float64), false, intList(t))
	if err != nil {
		t.Fatalf("casting List(Int64) to List(Float64): %v", err)
	}
	if got.DType() != dtype.List(dtype.Float64) {
		t.Fatalf("type is %s", got.DType())
	}
	want := [][]float64{{1, 2}, {3}, {}, {4, 5, 6}}
	rows := rowsOf(t, got)
	if len(rows) != len(want) {
		t.Fatalf("%d rows, want %d", len(rows), len(want))
	}
	for i := range want {
		if len(rows[i]) != len(want[i]) {
			t.Errorf("row %d is %v, want %v", i, rows[i], want[i])
			continue
		}
		for j := range want[i] {
			if rows[i][j] != want[i][j] {
				t.Errorf("row %d is %v, want %v", i, rows[i], want[i])
				break
			}
		}
	}
}

// TestCastListOfASlicedColumn is the arm that a child-in-place cast gets wrong.
func TestCastListOfASlicedColumn(t *testing.T) {
	full := intList(t)
	// Rows 2 and 3 only: offs[0] is 3, and the child still holds 1,2,3 in front.
	sliced := full.Slice(2, 2)

	got, err := kernel.Cast("o", dtype.List(dtype.Float64), false, sliced)
	if err != nil {
		t.Fatalf("casting a sliced List: %v", err)
	}
	rows := rowsOf(t, got)
	want := [][]float64{{}, {4, 5, 6}}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	for i := range want {
		if len(rows[i]) != len(want[i]) {
			t.Fatalf("row %d is %v, want %v — an element from a row outside the "+
				"slice leaked in", i, rows[i], want[i])
		}
		for j := range want[i] {
			if rows[i][j] != want[i][j] {
				t.Errorf("row %d is %v, want %v", i, rows[i], want[i])
			}
		}
	}
}

// TestCastListStrictIgnoresElementsOutsideTheSlice is the arm that decides HOW the
// cast is written. Keeping the absolute offsets and casting the whole child in place
// indexes correctly — the offsets still line up — so the shape of the answer is not
// what is at stake. A STRICT cast is: it fails on a value the target cannot hold,
// and the whole child contains values belonging to rows this column does not have.
//
// Row 0 of the full list holds 999, which is not an Int8. The slice does not contain
// it, so casting the slice must succeed.
func TestCastListStrictIgnoresElementsOutsideTheSlice(t *testing.T) {
	child := data.NewFixed("item", dtype.Int64,
		[]int64{999, 1, 2, 3}, bitmap.AllSet(4))
	full := data.NewList("l", []int32{0, 1, 4}, child, bitmap.AllSet(2))

	// The full column must still refuse: 999 is genuinely in it.
	if _, err := kernel.Cast("o", dtype.List(dtype.Int8), true, full); err == nil {
		t.Error("a strict cast accepted 999 as an Int8")
	}

	sliced := full.Slice(1, 1) // row 1 only: [1, 2, 3]
	got, err := kernel.Cast("o", dtype.List(dtype.Int8), true, sliced)
	if err != nil {
		t.Fatalf("a strict cast of a slice failed on an element outside it: %v", err)
	}
	acc := got.Lists()
	start, end, ok := acc.Get(0)
	if !ok || end-start != 3 {
		t.Fatalf("row 0 spans [%d,%d), want 3 elements", start, end)
	}
}
