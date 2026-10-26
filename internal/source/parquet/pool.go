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

// readProps are the properties a file is opened with: the pool. GetStream still reads
// each column chunk into a slice of its own, 81 MB of q14's scan a run. arrow-go's
// buffered stream would read a page straight into its decompression buffer, but with
// it a file whose row group claims more rows than its chunks hold read as no rows and
// no error, and a read that failed mid-scan lost the file's name: TestPanicsAreErrors
// and TestScanParquetFrom caught them (step 376).
func readProps() *parquet.ReaderProperties {
	return parquet.NewReaderProperties(pages)
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
