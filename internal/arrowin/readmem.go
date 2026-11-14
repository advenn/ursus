package arrowin

import (
	"io"
	"math"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/internal/uerr"
)

// An Arrow IPC input's allocator (step 511).
//
// arrow-go decompresses an IPC buffer into one it sizes by the eight-byte length the
// buffer declares, before it has decompressed anything (ipcSource.buffer, in
// arrow/ipc/file_reader.go). The length is the input's to claim: a file of 4.5 KB
// whose LZ4 buffer declared 2^40 bytes ended the process with "fatal error: runtime:
// out of memory", which no recover catches, and one declaring 2^35 allocated 32 GiB of
// address space. The footer's blocks and a stream's messages are checked against the
// file's size and arrow-go's limits; a decompressed buffer is not.
//
// So an IPC input is read through a ReadMem, which refuses any one request past what
// the input could hold: decompressRatio times its bytes, Zstandard's most, a block of
// four bytes regenerating 128 KiB (LZ4's is 255), plus readSlack. No buffer of a
// well-formed input is refused, whatever its codec; a request past that is a corrupt
// input's, refused as one, KindIO, before it is made.

// decompressRatio is the most one byte of a compressed Arrow buffer decompresses to.
const decompressRatio = 1 << 15

// readSlack is the room every bound leaves past the ratio, for a small input's
// buffers.
const readSlack = 1 << 20

// streamBody is arrow-go's limit on one stream message's body (ipc's
// defaultMaxBodySize), which a stream of unknown size allocates before reading it.
const streamBody = 256 << 20

// ReadBase is the allocator a ReadMem grants requests from: arrow-go's Go allocator,
// as an IPC reader's default is. A fuzz target swaps it for one that records each
// request (internal/readfuzz), and nothing else may.
var ReadBase memory.Allocator = memory.NewGoAllocator()

// ReadMem is the allocator of one Arrow IPC read, named op in its refusals.
type ReadMem struct {
	op   string
	size int64         // the input's bytes, or -1 where they are not known
	read *atomic.Int64 // the bytes of an input of unknown size read so far
}

// NewReadMem is the allocator for reading an IPC input of size bytes.
func NewReadMem(op string, size int64) *ReadMem { return &ReadMem{op: op, size: size} }

// NewStreamMem is the allocator for reading a stream of unknown size, and the stream
// to read, which counts its bytes for it: a buffer is decompressed from bytes
// already read, so a request is bounded by decompressRatio times them, plus a
// message's body, which is allocated before it is read.
func NewStreamMem(op string, r io.Reader) (io.Reader, *ReadMem) {
	m := &ReadMem{op: op, size: -1, read: new(atomic.Int64)}
	return &countingReader{r: r, n: m.read}, m
}

// ReadLimit is the largest one request a read of an IPC input of size bytes is
// granted: decompressRatio times it, plus readSlack.
func ReadLimit(size int64) int64 {
	if size > (math.MaxInt64-readSlack)/decompressRatio {
		return math.MaxInt64
	}
	return decompressRatio*max(size, 0) + readSlack
}

func (m *ReadMem) limit() int64 {
	if m.size >= 0 {
		return ReadLimit(m.size)
	}
	return min(ReadLimit(m.read.Load()), math.MaxInt64-streamBody) + streamBody
}

// check refuses a request of n bytes past m's limit, as arrow-go's allocators refuse
// one they cannot make, by a panic, which the IPC readers' recovery turns into the
// read's error.
func (m *ReadMem) check(n int) {
	lim := m.limit()
	if n >= 0 && int64(n) <= lim {
		return
	}
	var e *uerr.Error
	if m.size >= 0 {
		e = uerr.New(uerr.KindIO, m.op,
			"a buffer of %d bytes, more than an input of %d bytes holds decompressed (%d at most): the input is corrupt",
			n, m.size, lim)
	} else {
		e = uerr.New(uerr.KindIO, m.op,
			"a buffer of %d bytes, more than a message's body and what the %d bytes read hold decompressed (%d at most): the input is corrupt",
			n, m.read.Load(), lim)
	}
	uerr.Raise(e.Hint("an Arrow IPC buffer declares its decompressed length before its bytes are read"))
}

func (m *ReadMem) Allocate(n int) []byte {
	m.check(n)
	return ReadBase.Allocate(n)
}

func (m *ReadMem) Reallocate(n int, b []byte) []byte {
	m.check(n)
	return ReadBase.Reallocate(n, b)
}

func (m *ReadMem) Free(b []byte) { ReadBase.Free(b) }

var _ memory.Allocator = (*ReadMem)(nil)

// countingReader counts the bytes read through it into n.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n.Add(int64(k))
	return k, err
}
