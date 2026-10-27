package data

// Step 400: a String column's slice is a view of its parent's offsets, as a fixed
// column's is of its values, where it copied its window of them (B3 of
// v0.8-scope.md).

import (
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
)

// TestAStringSliceIsAView: a slice of a String column, and a slice of that, read
// their windows of the parent's offsets, not copies, and their values and nulls are
// the parent's rows.
func TestAStringSliceIsAView(t *testing.T) {
	vals := []string{"a", "bb", "", "dddd", "e", "ffffff", "g", "hh"}
	valid := bitmap.NewBuilder(len(vals))
	for i := range vals {
		valid.Append(i != 3)
	}
	c := NewString("s", vals, valid.Finish())
	s := c.Slice(2, 5)
	ss := s.Slice(1, 3)
	if &s.RawOffsets()[0] != &c.RawOffsets()[2*4] || &ss.RawOffsets()[0] != &c.RawOffsets()[3*4] {
		t.Fatal("a slice copied its offsets")
	}
	if len(s.RawOffsets()) != 6*4 || len(ss.RawOffsets()) != 4*4 {
		t.Fatalf("offsets of %d and %d bytes, want a row's more than the slice's rows", len(s.RawOffsets()), len(ss.RawOffsets()))
	}
	for _, w := range []struct {
		col *Column
		at  int
	}{{s, 2}, {ss, 3}} {
		for i := range w.col.Len() {
			r := w.at + i
			if got, null := w.col.Strings().Get(i), !w.col.Validity().Get(i); got != vals[r] || null != (r == 3) {
				t.Errorf("row %d of a slice from %d reads %q, null %v; want %q, null %v", i, w.at, got, null, vals[r], r == 3)
			}
		}
	}
}
