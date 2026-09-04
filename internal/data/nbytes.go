package data

import (
	"iter"

	"github.com/apache/arrow-go/v18/arrow/memory"

	"ursus/internal/bitmap"
)

// BufferID identifies one allocation by the address of its first byte.
//
// # Why identity rather than a sum
//
// Columns share payloads. Rename shares one ("O(1): the payload is shared"), so
// does WithDType, so does the character buffer of a sliced String column, and
// every pipeline breaker retains the source's own batches rather than copies. A
// naive per-column sum therefore over-reports, sometimes by a large factor — and
// an over-reporting memory budget fires early and unpredictably, which is worse
// than having no budget at all.
//
// It is a *byte rather than a uintptr on purpose. A real pointer stays meaningful
// to the garbage collector, so an identity held in a ledger can never be recycled
// by a later allocation while the entry is still live.
type BufferID = *byte

// Buffers yields each distinct allocation the column holds, with its size.
//
// Size is the buffer's CAPACITY, not the bytes in use: the allocation is what is
// actually held, and an under-filled buffer still occupies all of it.
//
// A column has at most five slots — fixed, offs, chars and the two bitmaps — so
// the within-column deduplication below is a linear scan over a fixed array
// rather than a map. Deduplication ACROSS columns is BufferSet's job.
func (c *Column) Buffers() iter.Seq2[BufferID, int64] {
	return func(yield func(BufferID, int64) bool) {
		var seen [5]BufferID
		n := 0
		emit := func(id BufferID, size int64) bool {
			if id == nil || size == 0 {
				return true
			}
			for _, s := range seen[:n] {
				if s == id {
					return true
				}
			}
			seen[n] = id
			n++
			return yield(id, size)
		}
		for _, b := range [...]*memory.Buffer{c.fixed, c.offs, c.chars} {
			if !emit(bufferID(b)) {
				return
			}
		}
		for _, v := range [...]bitmap.View{c.valid, c.bits} {
			if !emit(viewID(v)) {
				return
			}
		}
	}
}

// NBytes returns the total capacity of the distinct allocations the column holds.
func (c *Column) NBytes() int64 {
	var total int64
	for _, size := range c.Buffers() {
		total += size
	}
	return total
}

// NBytes returns the total capacity of the distinct allocations the batch holds,
// counting a payload two columns share exactly once.
func (b *Batch) NBytes() int64 {
	var s BufferSet
	s.AddBatch(b)
	return s.Total()
}

func bufferID(b *memory.Buffer) (BufferID, int64) {
	if b == nil {
		return nil, 0
	}
	raw := b.Bytes()
	if len(raw) == 0 {
		return nil, 0
	}
	return &raw[0], int64(b.Cap())
}

func viewID(v bitmap.View) (BufferID, int64) {
	raw, _ := v.RawBits()
	if len(raw) == 0 {
		// The all-set form has no storage at all, which is exactly why it is the
		// representation of "no nulls".
		return nil, 0
	}
	return &raw[0], int64(cap(raw))
}

// BufferSet accumulates the distinct allocations of any number of columns.
//
// The zero value is ready to use.
type BufferSet struct {
	seen  map[BufferID]int64
	total int64
}

// Add records one allocation, ignoring one it has already seen.
func (s *BufferSet) Add(id BufferID, size int64) {
	if id == nil || size == 0 {
		return
	}
	if s.seen == nil {
		s.seen = make(map[BufferID]int64)
	}
	if _, dup := s.seen[id]; dup {
		return
	}
	s.seen[id] = size
	s.total += size
}

// AddColumn records every allocation the column holds.
func (s *BufferSet) AddColumn(c *Column) {
	if c == nil {
		return
	}
	for id, size := range c.Buffers() {
		s.Add(id, size)
	}
}

// AddBatch records every allocation the batch's columns hold.
func (s *BufferSet) AddBatch(b *Batch) {
	if b == nil {
		return
	}
	for _, c := range b.Columns() {
		s.AddColumn(c)
	}
}

// Total returns the summed capacity of the distinct allocations recorded.
func (s *BufferSet) Total() int64 { return s.total }
