package data_test

import (
	"testing"

	"ursus/dtype"
	"ursus/internal/bitmap"
	"ursus/internal/data"
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
