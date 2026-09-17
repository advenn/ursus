package kernel_test

// Concat and Take over SLICED List columns.
//
// concatList used to shift each part's end offsets by the previous parts' whole child
// lengths and concatenate the whole children. That is right only for a List whose
// child is exactly its window, which is what NewList builds and what Column.Slice does
// not. Every Collect ends in kernel.Concat, so this ran on the most ordinary path in the
// engine and never on a sliced List in a test.
//
// Two failures, and they need different assertions:
//
//   - the VALUES were wrong — rows read other rows' elements — which renderRow sees;
//   - the child GREW by a whole copy per part, which no value comparison can see at
//     all, because the offsets can be internally consistent over a child that is far
//     too big. TestConcatOfSlicedListsHoldsOnlyItsWindow asserts the length directly.

import (
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// intLists builds a List(Int64) column from Go slices; a nil row is a null list.
func intLists(name string, rows [][]int64) *data.Column {
	var elems []int64
	offs := []int32{0}
	valid := bitmap.NewBuilder(len(rows))
	for _, r := range rows {
		elems = append(elems, r...)
		offs = append(offs, int32(len(elems)))
		valid.Append(r != nil)
	}
	child := data.NewFixed("item", dtype.Int64, elems, bitmap.AllSet(len(elems)))
	return data.NewList(name, offs, child, valid.Finish())
}

// fixtureRows: a non-empty head, an empty row, a null row and a non-empty tail, so a
// slice anywhere inside has elements on both sides that must not leak in.
var fixtureRows = [][]int64{{5, 6}, {1, 1, 2}, {}, nil, {9, 3}, {7}, {4, 8, 4}}

func renderAll(t *testing.T, c *data.Column) string {
	t.Helper()
	parts := make([]string, c.Len())
	for i := range c.Len() {
		if !c.IsValid(i) {
			parts[i] = "null"
			continue
		}
		s, err := renderRow(c, i)
		if err != nil {
			t.Fatal(err)
		}
		parts[i] = s
	}
	return strings.Join(parts, " | ")
}

// splitAt cuts a column into consecutive slices at the given row boundaries, the way
// a memory source cuts a stored batch at a batch size.
func splitAt(c *data.Column, cuts ...int) []*data.Column {
	var parts []*data.Column
	prev := 0
	for _, cut := range append(cuts, c.Len()) {
		parts = append(parts, c.Slice(prev, cut-prev))
		prev = cut
	}
	return parts
}

var cutPlans = [][]int{
	{1},          // a one-row head
	{2, 4},       // three parts, the middle one empty and null
	{1, 2, 3, 4}, // every row but the last on its own
	{3},
	{6},
}

func TestConcatOfSlicedListsIsTheOriginal(t *testing.T) {
	full := intLists("l", fixtureRows)
	want := renderAll(t, full)

	for _, cuts := range cutPlans {
		got, err := kernel.ConcatColumns("l", splitAt(full, cuts...))
		if err != nil {
			t.Fatalf("cuts %v: %v", cuts, err)
		}
		if s := renderAll(t, got); s != want {
			t.Errorf("concatenating slices cut at %v:\n got:  %s\n want: %s", cuts, s, want)
		}
	}
}

// TestConcatOfSlicedListsHoldsOnlyItsWindow is the half no value comparison can see.
func TestConcatOfSlicedListsHoldsOnlyItsWindow(t *testing.T) {
	full := intLists("l", fixtureRows)
	elems := full.Child().Len()

	for _, cuts := range cutPlans {
		got, err := kernel.ConcatColumns("l", splitAt(full, cuts...))
		if err != nil {
			t.Fatalf("cuts %v: %v", cuts, err)
		}
		if n := got.Child().Len(); n != elems {
			t.Errorf("concatenating %d slices produced a child of %d elements, want "+
				"%d — each part contributed more than its own window",
				len(cuts)+1, n, elems)
		}
	}
}

// TestTakeFromASlicedList: Take reads absolute ranges and gathers from the whole
// child, which is correct — this pins that it stays so.
func TestTakeFromASlicedList(t *testing.T) {
	full := intLists("l", fixtureRows)
	const k, m = 2, 4
	sliced := full.Slice(k, m)

	sel := []int32{3, 0, 2, kernel.NullIndex, 1}
	got, err := kernel.Take(sliced, sel)
	if err != nil {
		t.Fatal(err)
	}
	shifted := make([]int32, len(sel))
	for i, s := range sel {
		if s == kernel.NullIndex {
			shifted[i] = s
			continue
		}
		shifted[i] = s + k
	}
	want, err := kernel.Take(full, shifted)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := renderAll(t, got), renderAll(t, want); g != w {
		t.Errorf("Take from a slice:\n got:  %s\n want: %s", g, w)
	}
}

// TestConcatOfSlicedStructsHoldingLists reaches concatList TRANSITIVELY: concatStruct
// concatenates field by field, so a List field inside a Struct takes the same path
// with nothing at the call site saying so.
func TestConcatOfSlicedStructsHoldingLists(t *testing.T) {
	lists := intLists("tags", fixtureRows)
	ids := data.NewFixed("id", dtype.Int64, []int64{0, 1, 2, 3, 4, 5, 6}, bitmap.AllSet(7))
	full := data.NewStruct("s", []*data.Column{ids, lists}, bitmap.AllSet(7))

	want := renderAll(t, lists)
	for _, cuts := range cutPlans {
		got, err := kernel.ConcatColumns("s", splitAt(full, cuts...))
		if err != nil {
			t.Fatalf("cuts %v: %v", cuts, err)
		}
		field, ok := got.Field("tags")
		if !ok {
			t.Fatal("the struct lost its List field")
		}
		if s := renderAll(t, field); s != want {
			t.Errorf("the List field of a concatenated struct, cut at %v:\n got:  %s\n want: %s",
				cuts, s, want)
		}
	}
}

// TestConcatOfSlicedNestedLists is the recursive case. The child of a sliced
// List(List(Int64)) is itself a List with absolute offsets, so concatList's windowed
// child slice must rebase correctly one level down as well.
func TestConcatOfSlicedNestedLists(t *testing.T) {
	inner := intLists("item", [][]int64{{1}, {2, 3}, {}, {4, 5, 6}, {7}, {8, 9}})
	full := data.NewList("ll", []int32{0, 2, 2, 5, 6}, inner, bitmap.AllSet(4))

	want := renderNested(t, full)
	for _, cuts := range [][]int{{1}, {2}, {1, 3}} {
		got, err := kernel.ConcatColumns("ll", splitAt(full, cuts...))
		if err != nil {
			t.Fatalf("cuts %v: %v", cuts, err)
		}
		if s := renderNested(t, got); s != want {
			t.Errorf("nested concat cut at %v:\n got:  %s\n want: %s", cuts, s, want)
		}
		if n, w := got.Child().Child().Len(), full.Child().Child().Len(); n != w {
			t.Errorf("nested concat cut at %v has %d leaf elements, want %d", cuts, n, w)
		}
	}
}

// renderNested renders List(List(Int64)) through absolute Get ranges at both levels,
// which is correct on a sliced column at either level.
func renderNested(t *testing.T, c *data.Column) string {
	t.Helper()
	outer := c.Lists()
	inner := outer.Child()
	var rows []string
	for i := range c.Len() {
		lo, hi, _ := outer.Get(i)
		var elems []string
		for j := lo; j < hi; j++ {
			s, err := renderRow(inner, int(j))
			if err != nil {
				t.Fatal(err)
			}
			elems = append(elems, s)
		}
		rows = append(rows, "["+strings.Join(elems, ",")+"]")
	}
	return strings.Join(rows, " | ")
}
