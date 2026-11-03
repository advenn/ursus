package data_test

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestASliceIsItsParentsAllocation (step 424): a slice of a column is a view of the
// column's allocation, so a set holding both holds the allocation once, at its
// size, in either order. A slice from the start, seen first, kept its own few bytes
// and hid the allocation; one from within counted it a second time. A validity
// bitmap slices as a view too. A String's slice is not here: it copies its offsets,
// a new allocation, and shares its characters as the same buffer, which the ledger
// counted once already. A slice alone holds its own bytes, as it did: a stream
// holding one batch of a frame at a time holds a batch, not the frame.
func TestASliceIsItsParentsAllocation(t *testing.T) {
	vals := make([]int64, 1024)
	for i := range vals {
		vals[i] = int64(i)
	}
	valid := bitmap.Zeros(len(vals))
	for _, col := range []*data.Column{
		data.NewFixed("v", dtype.Int64, vals, bitmap.View{}),
		data.NewFixed("v", dtype.Int64, vals, valid),
	} {
		whole := col.NBytes()
		for _, part := range []struct {
			name string
			col  *data.Column
		}{
			{"its first row", col.Slice(0, 1)},
			{"its rows from the second", col.Slice(1, col.Len()-1)},
			{"its middle row", col.Slice(col.Len()/2, 1)},
		} {
			for _, first := range []bool{true, false} {
				var s data.BufferSet
				if first {
					s.AddColumn(part.col)
					s.AddColumn(col)
				} else {
					s.AddColumn(col)
					s.AddColumn(part.col)
				}
				if s.Total() != whole {
					t.Errorf("%s (nulls %v) and %s (slice first: %v): %d bytes, want the column's %d",
						col.DType(), col.NullCount() > 0, part.name, first, s.Total(), whole)
				}
			}
			n := part.col.NBytes()
			if n == 0 || n > whole {
				t.Errorf("%s alone, of %s: %d bytes, want some, at most the column's %d",
					part.name, col.DType(), n, whole)
			}
			if own := int64(part.col.Len() * 8); col.NullCount() == 0 && n != own {
				t.Errorf("%s alone, of %s: %d bytes, want its own %d", part.name, col.DType(), n, own)
			}
		}
	}
}
