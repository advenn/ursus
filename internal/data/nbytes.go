package data

import (
	"iter"

	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/internal/bitmap"
)

// BufferID identifies one allocation by the address of its last byte.
//
// Every view of an allocation, a slice from its start or from within it, runs to
// the allocation's end in Go's capacity, so all of them have the one ID, and a Span
// says which of its bytes each holds. It was the first byte (step 424): a slice from
// the start, a frame's Head, had its parent's ID and its own few bytes for a size,
// and seen first hid the parent from the ledger; a slice from within had an ID of
// its own and counted the allocation a second time.
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
// by a later allocation while the entry is still live. That holds because every
// payload is Go heap: Arrow import copies rather than adopting C Data memory, whose
// addresses the collector knows nothing about.
type BufferID = *byte

// Buffers yields each distinct allocation the column holds, with its size.
//
// Size is the buffer's CAPACITY, not the bytes in use: the allocation is what is
// actually held, and an under-filled buffer still occupies all of it. Of a view, a
// slice of a larger allocation, it is the view's bytes: Spans places them in it.
//
// A column has five slots of its own — fixed, offs, chars and the two bitmaps —
// so the within-column deduplication below is a linear scan rather than a map.
// Deduplication ACROSS columns is BufferSet's job.
//
// The scan is over a SLICE rather than a fixed array because a List column also
// yields its child's buffers, and a child may itself be a List: the count is
// bounded by nesting depth, not by five. It was a [5]BufferID, which a single
// list column would have run off the end of.
func (c *Column) Buffers() iter.Seq2[BufferID, int64] {
	return func(yield func(BufferID, int64) bool) {
		for id, sp := range Spans(c) {
			if !yield(id, sp.Size()) {
				return
			}
		}
	}
}

// Spans is Buffers with the part of each allocation c's view holds, which is what
// lets a consumer seeing two views of one allocation count it once.
//
// A function, not a method: the public ursus.Column is this package's Column, so
// a method here is a method of ursus's API, and 0.7.2 adds none (step 436).
func Spans(c *Column) iter.Seq2[BufferID, Span] {
	return func(yield func(BufferID, Span) bool) {
		seen := make([]BufferID, 0, 5)
		emit := func(id BufferID, sp Span) bool {
			if id == nil || sp.Size() == 0 {
				return true
			}
			for _, s := range seen {
				if s == id {
					return true
				}
			}
			seen = append(seen, id)
			return yield(id, sp)
		}
		for _, b := range [...]*memory.Buffer{c.fixed, c.offs} {
			if !emit(bufferID(b)) {
				return
			}
		}
		if !emit(c.charsID()) {
			return
		}
		if !emit(sliceID(c.offs64)) {
			return
		}
		for _, v := range [...]bitmap.View{c.valid, c.bits} {
			if !emit(viewID(v)) {
				return
			}
		}
		// A List's elements live in a child column, and its buffers are as real as
		// any other. Omitting them would under-report a list column by everything
		// it actually holds — the offsets are the small part.
		if c.child != nil {
			for id, sp := range Spans(c.child) {
				if !emit(id, sp) {
					return
				}
			}
		}
		// A Struct is ALL child, having no payload buffer of its own — so omitting
		// these would report a struct column as costing only its validity bitmap.
		for _, f := range c.fields {
			for id, sp := range Spans(f) {
				if !emit(id, sp) {
					return
				}
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

func bufferID(b *memory.Buffer) (BufferID, Span) {
	if b == nil {
		return nil, Span{}
	}
	raw := b.Bytes()
	if len(raw) == 0 {
		return nil, Span{}
	}
	return spanOf(raw, int64(b.Cap()))
}

func viewID(v bitmap.View) (BufferID, Span) {
	raw, _ := v.RawBits()
	if len(raw) == 0 {
		// The all-set form has no storage at all, which is exactly why it is the
		// representation of "no nulls".
		return nil, Span{}
	}
	return spanOf(raw, int64(cap(raw)))
}

// charsID is bufferID for the characters, spanning the bytes the column's rows
// cover. A String column's Slice shares its parent's characters (step 425), so a
// batch of a frame's strings was charged as all the frame's text, and an operator
// holding one batch of 64 rows held 135 KB. Rows covering the characters from their
// first byte, or to their last, hold the buffer from its start, or to its end, an
// under-filled buffer's room included, as bufferID has it.
func (c *Column) charsID() (BufferID, Span) {
	id, sp := bufferID(c.chars)
	lo, hi, ok := c.charSpan()
	if id == nil || !ok {
		return id, sp
	}
	first := sp.Lo
	if lo > 0 {
		sp.Lo = first + lo
	}
	if hi < int64(c.chars.Len()) {
		sp.Hi = first + hi
	}
	return id, sp
}

// A Span is the bytes of an allocation a view holds, [Lo, Hi), counted back from
// the allocation's end, so Lo < Hi <= 0. Every view of an allocation is placed on
// the one line, and two views' spans say what they hold between them.
type Span struct{ Lo, Hi int64 }

// Size is the span's bytes.
func (s Span) Size() int64 { return s.Hi - s.Lo }

// With is the least span covering s and t: what two views of one allocation hold
// between them, any gap between them included, since what stays alive is the
// allocation. A sort holding a frame's batches holds all of it; an operator holding
// one batch at a time holds a batch.
func (s Span) With(t Span) Span { return Span{min(s.Lo, t.Lo), max(s.Hi, t.Hi)} }

// spanOf places a view of size bytes from raw's first byte: raw's Go capacity runs
// to its allocation's end, which is the allocation's ID.
func spanOf(raw []byte, size int64) (BufferID, Span) {
	whole := raw[:cap(raw)]
	lo := -int64(cap(raw))
	return &whole[len(whole)-1], Span{lo, lo + size}
}

// BufferSet accumulates the distinct allocations of any number of columns.
//
// The zero value is ready to use.
type BufferSet struct {
	seen  map[BufferID]Span
	total int64
}

// Add records the part of one allocation a view holds; of an allocation seen
// already, it adds what the view holds beyond what was seen.
func (s *BufferSet) Add(id BufferID, sp Span) {
	if id == nil || sp.Size() == 0 {
		return
	}
	if s.seen == nil {
		s.seen = make(map[BufferID]Span)
	}
	old, dup := s.seen[id]
	if dup {
		sp = old.With(sp)
	}
	s.seen[id] = sp
	s.total += sp.Size() - old.Size()
}

// AddColumn records every allocation the column holds.
func (s *BufferSet) AddColumn(c *Column) {
	if c == nil {
		return
	}
	for id, sp := range Spans(c) {
		s.Add(id, sp)
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
