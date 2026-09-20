package dtype_test

// A bound is not a value.
//
// FromTime refuses an instant the type cannot hold, which is right for a datum and
// wrong for a window edge: an edge past the end of the type IS the end of the type,
// and every candidate compared against it is itself a representable tick. The as-of
// join's calendar tolerance is the caller that needed the distinction — it was
// treating "this bound does not fit" as "everything matches".

import (
	"math"
	"testing"
	"time"

	"github.com/advenn/ursus/dtype"
)

func TestFromTimeBoundClampsRatherThanLies(t *testing.T) {
	ns := dtype.Datetime(dtype.Nano, "UTC")
	beyond := time.Date(2262, 5, 1, 0, 0, 0, 0, time.UTC) // past the ns range
	before := time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC) // before it
	inside := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, ok := ns.FromTime(beyond); ok {
		t.Fatal("the fixture no longer exceeds Datetime(ns), so this proves nothing")
	}
	if v := ns.FromTimeBound(beyond, true); v != math.MaxInt64 {
		t.Errorf("an upper bound past the range is %d, want MaxInt64", v)
	}
	if v := ns.FromTimeBound(before, false); v != math.MinInt64 {
		t.Errorf("a lower bound before the range is %d, want MinInt64", v)
	}

	// An instant that fits is unchanged, whichever direction is asked for.
	want, _ := ns.FromTime(inside)
	for _, up := range []bool{true, false} {
		if v := ns.FromTimeBound(inside, up); v != want {
			t.Errorf("up=%v: a representable instant became %d, want %d", up, v, want)
		}
	}
}

// TestToTimeOfADateBeyondItsColumnRange: a Date column is int32 days and cannot reach
// this, but ToTime is exported on DataType and its day multiply used to wrap — the
// same defence ToDuration's doc argues for in the other direction.
func TestToTimeOfADateBeyondItsColumnRange(t *testing.T) {
	if _, ok := dtype.Date.ToTime(math.MaxInt64); ok {
		t.Error("a day count that is no year at all was converted anyway")
	}
	if got, ok := dtype.Date.ToTime(19_000); !ok || got.Year() != 2022 {
		t.Errorf("an ordinary date became %v (ok=%v)", got, ok)
	}
}
