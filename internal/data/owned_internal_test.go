package data

// Step 163: NewFixedOwned and NewStringOwned take the caller's slices rather than
// copying them, and report what they hold as every buffer does, by its capacity.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
)

func TestOwnedColumnsReadTheirSlices(t *testing.T) {
	vals := make([]int64, 3, 10)
	copy(vals, []int64{7, -1, 42})
	c := NewFixedOwned("v", dtype.Int64, vals, bitmap.View{})
	got := MustValues[int64](c)
	if c.Len() != 3 || len(got) != 3 || &got[0] != &vals[0] {
		t.Fatalf("the column copied its values, or reads %d of them", len(got))
	}
	// Measured by capacity, as Buffers measures every buffer: the allocation held.
	var held int64
	for _, size := range c.Buffers() {
		held += size
	}
	if held != 10*8 {
		t.Fatalf("the column holds %d bytes, its slice's allocation is %d", held, 10*8)
	}

	offs := make([]int32, 3, 8)
	copy(offs, []int32{0, 2, 5})
	chars := append(make([]byte, 0, 16), "abxyz"...)
	s := NewStringOwned("s", offs, chars, bitmap.View{})
	if s.Len() != 2 || s.Strings().Get(0) != "ab" || s.Strings().Get(1) != "xyz" {
		t.Fatalf("the strings read %d rows: %q", s.Len(), []string{s.Strings().Get(0), s.Strings().Get(1)})
	}
	held = 0
	for _, size := range s.Buffers() {
		held += size
	}
	if held != 8*4+16 {
		t.Fatalf("the strings hold %d bytes, their slices' allocations are %d", held, 8*4+16)
	}

	if e := NewStringOwned("e", nil, nil, bitmap.View{}); e.Len() != 0 {
		t.Fatalf("no offsets read as %d rows", e.Len())
	}
	if e := NewFixedOwned("e", dtype.Int32, []int32(nil), bitmap.View{}); e.Len() != 0 {
		t.Fatalf("no values read as %d rows", e.Len())
	}
}

// TestAnOwnedColumnChecksItsType: values of one physical type cannot be taken as a
// column of another, as NewFixed refuses.
func TestAnOwnedColumnChecksItsType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an Int64 column was built over float64 values")
		}
	}()
	NewFixedOwned("v", dtype.Int64, []float64{1}, bitmap.View{})
}
