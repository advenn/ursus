package parquet

import (
	"math/bits"
	"sync"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
)

// pagePool is the memory.Allocator the files are read with: arrow-go's page readers
// decompress each page into buffers they allocate from it, and give them back when
// they close, so the next column chunk's pages are decompressed into the last one's.
//
// The default allocator made every buffer afresh and freed none: a page reader is
// made for each column chunk, and DuckDB's lineitem holds one page of a megabyte a
// chunk, so a scan of q14's four columns allocated 270 MB of page buffers a run, each
// zeroed, and a collection after every few: half of what the scan allocated, where
// the collector, the zeroing and the copying were 29% of its time (step 376, R1 of
// v0.7-scope.md).
//
// A buffer is a power of two from minPooled to maxPooled bytes, kept by its size in a
// sync.Pool, which the collector empties when nothing takes from it. A smaller one or
// a larger one is the runtime's, as before. A buffer comes back holding what it held,
// not zeroed: arrow-go writes a page's bytes before it reads them.
type pagePool struct {
	classes [maxClass - minClass + 1]sync.Pool
}

const (
	minClass  = 12 // 4 KiB
	maxClass  = 28 // 256 MiB
	minPooled = 1 << minClass
	maxPooled = 1 << maxClass
)

// pages is the pool every file of the process is read with.
var pages = &pagePool{}

// readProps are the properties a file is opened with: the pool, or a scan's view of
// it. GetStream still reads each column chunk into a slice of its own, 81 MB of q14's
// scan a run. arrow-go's buffered stream would read a page straight into its
// decompression buffer, but with it a file whose row group claims more rows than its
// chunks hold read as no rows and no error, and a read that failed mid-scan lost the
// file's name: TestPanicsAreErrors and TestScanParquetFrom caught them (step 376).
func readProps(mem memory.Allocator) *parquet.ReaderProperties {
	return parquet.NewReaderProperties(mem)
}

// class is the size class of a buffer of n bytes, or -1 for one the pool does not
// keep.
func class(n int) int {
	if n <= 0 || n > maxPooled {
		return -1
	}
	c := bits.Len(uint(n - 1))
	return max(c, minClass)
}

func (p *pagePool) Allocate(size int) []byte {
	c := class(size)
	if c < 0 {
		return make([]byte, size)
	}
	if b, ok := p.classes[c-minClass].Get().(*[]byte); ok {
		return (*b)[:size]
	}
	return make([]byte, size, 1<<c)
}

func (p *pagePool) Reallocate(size int, b []byte) []byte {
	if size <= cap(b) {
		return b[:size]
	}
	nb := p.Allocate(size)
	copy(nb, b)
	p.Free(b)
	return nb
}

// Free keeps b for a later Allocate of its class: a buffer whose capacity is one of
// the pool's powers of two. Any other is left to the collector.
func (p *pagePool) Free(b []byte) {
	c := cap(b)
	if c < minPooled || c > maxPooled || c&(c-1) != 0 {
		return
	}
	b = b[:0]
	p.classes[bits.Len(uint(c))-1-minClass].Put(&b)
}

var _ memory.Allocator = (*pagePool)(nil)

// scanPages is the pool as one scan reads from it: what its page readers hold out of
// it is told to tell, the scan's query's budget's Pressure, which counts it against
// the room of its engine's other queries (step 400, B3 of v0.8-scope.md). A scan of
// all sixteen of lineitem's columns at SF=1 holds up to 105 MiB of pages at once,
// which no budget saw.
type scanPages struct {
	tell func(int64)

	mu   sync.Mutex
	held int64
	done bool
}

// newScanPages returns the allocator a scan's files are read with: pages itself where
// no one is told.
func newScanPages(tell func(int64)) *scanPages {
	return &scanPages{tell: tell}
}

// alloc is the allocator to open a file with.
func (s *scanPages) alloc() memory.Allocator {
	if s == nil || s.tell == nil {
		return pages
	}
	return s
}

func (s *scanPages) Allocate(size int) []byte {
	b := pages.Allocate(size)
	s.add(int64(cap(b)))
	return b
}

func (s *scanPages) Reallocate(size int, b []byte) []byte {
	if size <= cap(b) {
		return b[:size]
	}
	nb := s.Allocate(size)
	copy(nb, b)
	s.Free(b)
	return nb
}

func (s *scanPages) Free(b []byte) {
	s.add(-int64(cap(b)))
	pages.Free(b)
}

func (s *scanPages) add(d int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.held += d
	s.tell(d)
}

// close tells the budget the scan holds none once it ends: a buffer a page reader
// did not give back is the collector's, and one given back later is not told.
func (s *scanPages) close() {
	if s == nil || s.tell == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		s.done = true
		s.tell(-s.held)
		s.held = 0
	}
}
