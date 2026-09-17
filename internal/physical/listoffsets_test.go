package physical

// Every `.list` function, on a SLICED List.
//
// The List invariant is stated on data.ListAccessor.Window: offsets are absolute
// indices into the child, and a derived column — any Slice — keeps them absolute and
// its child whole. Two kernels did not honour that, and nothing noticed, because every
// List batch-size test read from Parquet, which builds a fresh column per batch with
// offsets from zero. A sliced List never reached a list function in a test.
//
// So this does exactly that, for all of them. For each function f and each window
// [k, k+m), it asserts
//
//	f(full.Slice(k, m))  ==  f(full).Slice(k, m)
//
// — computing on the slice must equal slicing the computation. The function list is
// DERIVED from the enum through allCallFns, so a list function added tomorrow is swept
// the day it is named, and the arguments come from contractArgSets beside it.
//
// # The oracle is not ursus
//
// Both columns are exported through arrowout and compared with arrow-go's
// array.Equal. That is an independent implementation of "these two arrays hold the
// same values", which reads offsets as absolute exactly as Arrow specifies — so it
// cannot share a blind spot with the kernels under test the way an ursus renderer
// could.

import (
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// slicedListBatch is the fixture, and its SHAPE is the point.
//
//	row  l
//	0    [5, 6]          before every window, non-empty: absorption is visible
//	1    [1, 1, 2]       a duplicate, for unique
//	2    []              empty
//	3    null            a null list — same offsets as empty, different answer
//	4    [9, null, 3]    a null ELEMENT, for len, drop_nulls and sort
//	5    [7]
//	6    [4, 8, 4]       after most windows: a tail that must not leak in either
//
// Every value is distinct enough that a sum or a min cannot be right by coincidence.
func slicedListBatch(t *testing.T) *data.Batch {
	t.Helper()

	elems := []int64{5, 6, 1, 1, 2, 9, 0, 3, 7, 4, 8, 4}
	ev := bitmap.NewBuilder(len(elems))
	for i := range elems {
		ev.Append(i != 6) // the null element inside row 4
	}
	child := data.NewFixed("item", dtype.Int64, elems, ev.Finish())

	rv := bitmap.NewBuilder(7)
	for i := range 7 {
		rv.Append(i != 3) // row 3 is a null list
	}
	offs := []int32{0, 2, 5, 5, 5, 8, 9, 12}
	col := data.NewList("l", offs, child, rv.Finish())

	schema, err := dtype.NewSchema(dtype.Of("l", col.DType()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(schema, []*data.Column{col})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// windows covers a start in the middle, a start past the null and empty rows, a
// single trailing row, and the whole column as the control.
var listWindows = []struct{ k, m int }{
	{1, 3},
	{2, 4},
	{3, 3},
	{4, 2},
	{6, 1},
	{0, 7}, // control: no slicing at all
}

func TestEveryListFunctionHonoursASlicedList(t *testing.T) {
	full := slicedListBatch(t)
	ctx := context.Background()

	var compared, narrowWindows int
	reached := map[expr.CallFn]bool{}

	for _, fn := range allCallFns() {
		if !fn.IsList() {
			continue
		}
		for _, set := range contractArgSets(fn) {
			args := []expr.Node{&expr.Col{Name: "l"}}
			for _, a := range set.args {
				args = append(args, callLit(a))
			}
			node := &expr.Call{Fn: fn, Args: args}

			whole, err := Eval(ctx, node, full)
			if err != nil {
				t.Fatalf("%s over the unsliced column: %v", fn, err)
			}

			for _, w := range listWindows {
				label := fn.String() + set.label
				sliced := full.Slice(w.k, w.m)

				// Narrower than its child on EITHER side. A head alone is not the
				// property: listReduce absorbed the tail as well, so a slice at row 0
				// that stops short of the end exercises the defect just as well.
				acc := sliced.Column(0).Lists()
				if lo, hi := acc.Window(); lo > 0 || int(hi) < acc.Child().Len() {
					narrowWindows++
				}

				got, err := Eval(ctx, node, sliced)
				if err != nil {
					t.Errorf("%s over rows [%d,%d): %v", label, w.k, w.k+w.m, err)
					continue
				}
				want := whole.Slice(w.k, w.m)

				ga, err := arrowout.Column(got)
				if err != nil {
					t.Fatalf("%s: exporting the result: %v", label, err)
				}
				wa, err := arrowout.Column(want)
				if err != nil {
					ga.Release()
					t.Fatalf("%s: exporting the reference: %v", label, err)
				}
				if !array.Equal(ga, wa) {
					t.Errorf("%s over rows [%d,%d) of a sliced List:\n got:  %v\n want: %v",
						label, w.k, w.k+w.m, ga, wa)
				}
				ga.Release()
				wa.Release()

				compared++
				reached[fn] = true
			}
		}
	}

	// Anti-vacuity, on the three things that could quietly stop being true.
	if len(reached) < 14 {
		t.Errorf("only %d list functions were reached; the enum declares 14", len(reached))
	}
	if compared < 14*len(listWindows) {
		t.Errorf("only %d comparisons ran", compared)
	}
	// The fixture property this whole file exists for, stated exactly.
	//
	// It is NOT "the offsets start past zero", which is what the plan for this step
	// predicted and a tooth refuted: with every window starting at row 0 the sweep
	// still failed, because listReduce absorbed the elements AFTER the window too. The
	// shape that hides the defect is a column whose child is exactly its window — no
	// head and no tail — which is precisely what the Parquet reader builds for every
	// batch, and why every earlier List test passed against broken kernels.
	if narrowWindows == 0 {
		t.Fatal("no window produced a List narrower than its child, so this test " +
			"cannot see the defect it was written for")
	}
}
