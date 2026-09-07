package data_test

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestValuesRejectsTypePunning is a regression test for a bug that shipped a
// wrong answer rather than an error.
//
// Values[T] originally validated only the BIT WIDTH of T against the column's
// physical type. Int64 and Float64 are both 64 bits, so Values[float64] succeeded
// on an Int64 column and returned the integer bit patterns reinterpreted as
// floats. The id column 1,3,4,6 rendered as 5e-324, 1.5e-323, 2e-323, 3e-323 —
// plausible-looking denormals that could easily be mistaken for a real result.
//
// The check is now on the exact physical type.
func TestValuesRejectsTypePunning(t *testing.T) {
	ints := data.NewFixed("id", dtype.Int64, []int64{1, 2, 3}, bitmap.AllSet(3))

	if _, err := data.Values[float64](ints); err == nil {
		t.Fatal("Values[float64] on an Int64 column must fail, not reinterpret the bits")
	}
	if _, err := data.TypedColumn[float64](ints); err == nil {
		t.Fatal("TypedColumn[float64] on an Int64 column must fail")
	}

	// The correct read still works.
	got, err := data.Values[int64](ints)
	if err != nil {
		t.Fatalf("Values[int64] on an Int64 column: %v", err)
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("Values[int64] = %v", got)
	}

	// Same-width, different-signedness must also be rejected.
	if _, err := data.Values[uint64](ints); err == nil {
		t.Error("Values[uint64] on an Int64 column must fail")
	}
	// And narrower reads of a wider column.
	if _, err := data.Values[int32](ints); err == nil {
		t.Error("Values[int32] on an Int64 column must fail")
	}
}

// TestPhysicalPunningIsStillAllowed: a temporal column IS an integer underneath,
// and reading it as one is the intended mechanism by which a single integer kernel
// serves Date, Datetime and Duration. The stricter check must not break it.
func TestPhysicalPunningIsStillAllowed(t *testing.T) {
	ts := data.NewFixed("ts", dtype.Datetime(dtype.Micro, "UTC"),
		[]int64{100, 200, 300}, bitmap.AllSet(3))

	got, err := data.Values[int64](ts)
	if err != nil {
		t.Fatalf("a Datetime column must be readable as []int64: %v", err)
	}
	if got[1] != 200 {
		t.Errorf("Values[int64] on Datetime = %v", got)
	}

	date := data.NewFixed("d", dtype.Date, []int32{19000}, bitmap.AllSet(1))
	if _, err := data.Values[int32](date); err != nil {
		t.Errorf("a Date column must be readable as []int32: %v", err)
	}
	if _, err := data.Values[int64](date); err == nil {
		t.Error("a Date column is 32-bit and must not be readable as []int64")
	}
}

func TestSeriesGenericMethods(t *testing.T) {
	c := data.NewFixed("n", dtype.Int64, []int64{1, 2, 3, 4}, bitmap.AllSet(4))
	s, err := data.TypedColumn[int64](c)
	if err != nil {
		t.Fatal(err)
	}

	// Generic methods on a generic type — the Go 1.27 feature this library needed.
	doubled := s.Map(func(v int64) int64 { return v * 2 })
	if doubled[3] != 8 {
		t.Errorf("Map = %v", doubled)
	}

	labels := s.Map(func(v int64) string { return string(rune('a' + v)) })
	if labels[0] != "b" {
		t.Errorf("Map to a different type = %v", labels)
	}

	sum := s.Fold(0, func(acc, v int64) int64 { return acc + v })
	if sum != 10 {
		t.Errorf("Fold = %d, want 10", sum)
	}
}

func TestNullsAreSkippedNotZeroed(t *testing.T) {
	valid := bitmap.NewBuilder(4)
	for _, v := range []bool{true, false, true, false} {
		valid.Append(v)
	}
	c := data.NewFixed("n", dtype.Int64, []int64{10, 999, 30, 999}, valid.Finish())
	s, err := data.TypedColumn[int64](c)
	if err != nil {
		t.Fatal(err)
	}

	if c.NullCount() != 2 {
		t.Errorf("NullCount = %d, want 2", c.NullCount())
	}
	// Get reports validity rather than boxing; Values collects only the present ones.
	if _, ok := s.Get(1); ok {
		t.Error("row 1 should be null")
	}
	if got := s.Values(); len(got) != 2 || got[0] != 10 || got[1] != 30 {
		t.Errorf("Values() = %v, want the two non-null values", got)
	}
	// Fold skips nulls — it does not fold in a zero.
	if sum := s.Fold(0, func(a, v int64) int64 { return a + v }); sum != 40 {
		t.Errorf("Fold = %d, want 40 (nulls skipped, not zero-filled)", sum)
	}
}

// TestSliceSharesTheFixedPayload pins the change step 37 made, in both directions:
// the slice must SEE the right rows, and it must be a view rather than a copy.
//
// Sharing is what makes splitting an in-memory frame into batches free, and it is
// only safe because nothing writes into an existing column's payload — the two
// data.Reinterpret call sites write into a freshly allocated buffer and read offsets
// for spilling. That is a property of the codebase rather than of this type, so the
// half of this test that matters most is the first: if a future kernel starts
// mutating in place, a slice reading its parent's bytes is how it will show up.
func TestSliceSharesTheFixedPayload(t *testing.T) {
	vals := []int64{10, 11, 12, 13, 14, 15, 16, 17}
	c := data.NewFixed("v", dtype.Int64, vals, bitmap.AllSet(len(vals)))

	s := c.Slice(3, 4)
	got, err := data.Values[int64](s)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{13, 14, 15, 16}
	if len(got) != len(want) {
		t.Fatalf("slice has %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slice = %v, want %v — the sub-buffer starts at the wrong offset",
				got, want)
		}
	}

	// A view, not a copy: the slice's bytes are inside the parent's. Values reads
	// from the START of the buffer, so a shared buffer that was not re-based would
	// have returned the parent's first four values above.
	pb, sb := c.RawFixed(), s.RawFixed()
	if len(sb) != 4*8 {
		t.Fatalf("slice payload is %d bytes, want 32", len(sb))
	}
	if &pb[3*8] != &sb[0] {
		t.Error("Slice copied the fixed payload; it should share the parent's buffer")
	}
}

// TestSliceMovesValidityWithValues: the values and the validity bitmap are sliced by
// separate code, so a slice whose values move while its nulls do not is wrong for
// every row after the first batch — and it is wrong QUIETLY, since the shape is
// right either way.
func TestSliceMovesValidityWithValues(t *testing.T) {
	// An IRREGULAR null pattern, deliberately. With an alternating one — i%2 == 0 —
	// slicing the validity from 0 instead of from the offset gives the same bits
	// whenever the offset is even, so the tooth for this passed against the obvious
	// fixture and proved nothing.
	vals := []int64{0, 1, 2, 3, 4, 5}
	pattern := []bool{true, true, false, true, false, false}
	valid := bitmap.NewBuilder(len(vals))
	for _, v := range pattern {
		valid.Append(v)
	}
	c := data.NewFixed("v", dtype.Int64, vals, valid.Finish())

	s := c.Slice(2, 3) // rows 2, 3, 4 -> null, valid, null
	for i, want := range []bool{false, true, false} {
		if s.IsValid(i) != want {
			t.Errorf("slice row %d valid = %v, want %v (parent rows 2..4 are "+
				"null, valid, null)", i, s.IsValid(i), want)
		}
	}
	got, err := data.Values[int64](s)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{2, 3, 4} {
		if got[i] != want {
			t.Errorf("slice row %d = %d, want %d", i, got[i], want)
		}
	}
}
