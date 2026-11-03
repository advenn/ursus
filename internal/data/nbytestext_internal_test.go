package data

import (
	"testing"

	"github.com/advenn/ursus/internal/bitmap"
)

// TestAStringSliceHoldsItsText (step 425): a slice of a String column shares the
// column's characters and is charged the bytes its rows cover, the buffer's room
// past them included when its rows run to the end. Alone it holds its offsets and
// that text; beside the column it adds only its offsets where they are its own, and
// nothing where they are a view of the column's; the column's batches together hold
// all its text.
func TestAStringSliceHoldsItsText(t *testing.T) {
	strs := make([]string, 1024)
	for i := range strs {
		strs[i] = "abcdefghijklmnopq"[:1+i%17]
	}
	col := NewString("s", strs, bitmap.View{})
	text := func(c *Column) int64 {
		_, sp := c.charsID()
		return sp.Size()
	}
	if got, want := text(col), int64(col.chars.Cap()); got != want {
		t.Fatalf("the column: %d bytes of text, want its buffer's %d", got, want)
	}
	for _, lo := range []int{0, 500, 1024 - 64} {
		part := col.Slice(lo, 64)
		from, to, _ := part.charSpan()
		want := to - from
		if to == int64(col.chars.Len()) {
			want += int64(col.chars.Cap() - col.chars.Len())
		}
		if got := text(part); got != want {
			t.Errorf("rows %d to %d: %d bytes of text, want the %d they cover", lo, lo+64, got, want)
		}
		_, offs := bufferID(part.offs)
		if got := part.NBytes(); got != offs.Size()+want {
			t.Errorf("rows %d to %d alone: %d bytes, want their offsets' %d and their text's %d",
				lo, lo+64, got, offs.Size(), want)
		}
		// Master's Slice views the column's offsets, where 0.7's copied them: a view
		// adds nothing beside the column, and a copy its own offsets.
		extra := offs.Size()
		partID, _ := bufferID(part.offs)
		if colID, _ := bufferID(col.offs); partID == colID {
			extra = 0
		}
		var s BufferSet
		s.AddColumn(part)
		s.AddColumn(col)
		if s.Total() != col.NBytes()+extra {
			t.Errorf("rows %d to %d beside the column: %d bytes, want the column's %d and %d more",
				lo, lo+64, s.Total(), col.NBytes(), extra)
		}
	}
	var all BufferSet
	for lo := 0; lo < col.Len(); lo += 64 {
		id, sp := col.Slice(lo, 64).charsID()
		all.Add(id, sp)
	}
	if all.Total() != text(col) {
		t.Errorf("the column's batches: %d bytes of text, want its %d", all.Total(), text(col))
	}
}
