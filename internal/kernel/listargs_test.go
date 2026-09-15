package kernel_test

// Every `.list` call must REFUSE a missing argument rather than index into a short
// slice.
//
// listGet, listEnds and listSlice each return an Internalf; listSort had a bare
// `args[0].(bool)` and took the process down instead. Unreachable from ListExpr.Sort,
// which always passes the flag — and exactly as reachable from the IR as the contract
// matrix's call arm is, which is the distance that matters now that a matrix drives
// these functions by table.
//
// The whole family is swept rather than the one that was broken, because "the sibling
// that forgot the guard" is a class, not an incident: three of four had it and the
// fourth did not, and nothing said so.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

// listOfInts builds [[1,2],[3]] as a List(Int64) column.
func listOfInts(t *testing.T) *data.Column {
	t.Helper()
	child := data.NewFixed("child", dtype.Int64, []int64{1, 2, 3}, bitmap.AllSet(3))
	return data.NewList("l", []int32{0, 2, 3}, child, bitmap.AllSet(2))
}

func TestListCallsRefuseAMissingArgument(t *testing.T) {
	c := listOfInts(t)

	// Every list function that takes at least one argument. A function added to the
	// family with an argument and no guard is what this is for, so the list is
	// deliberately of the ARGUMENT-TAKING ones rather than of the broken one.
	fns := []expr.CallFn{
		expr.FnListGet, expr.FnListHead, expr.FnListTail,
		expr.FnListSlice, expr.FnListSort,
	}
	for _, fn := range fns {
		t.Run(fn.String(), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s panicked on a missing argument: %v — a kernel must "+
						"return an error, not take the process down", fn, r)
				}
			}()
			out, err := kernel.ListCall(fn, "r", c.DType(), c, nil, nil)
			if err == nil {
				t.Errorf("%s accepted a missing argument and produced %s; the "+
					"argument is not optional", fn, out.DType())
			}
		})
	}
}

// TestListSortStillSortsWithItsArgument is the other direction, so the guard above
// cannot be satisfied by refusing everything.
func TestListSortStillSortsWithItsArgument(t *testing.T) {
	c := listOfInts(t)
	for _, desc := range []bool{false, true} {
		got, err := kernel.ListCall(expr.FnListSort, "r", c.DType(), c, []any{desc}, nil)
		if err != nil {
			t.Fatalf("list.sort(desc=%v): %v", desc, err)
		}
		if got.Len() != c.Len() {
			t.Errorf("list.sort(desc=%v) produced %d rows, want %d",
				desc, got.Len(), c.Len())
		}
	}
}
