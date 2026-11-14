package fuzzcheck

import (
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

// A bounded allocator's rule (D21 of v0.7.2-plan.md, as the swarm proposed it): no one
// request may exceed 16 MiB plus 16 bytes for each byte of input. A reader that takes
// a length from its input and allocates it before it has seen the data it describes
// is the bug this finds: a footer claiming a 2^40-byte block, a page header claiming
// 2^28 bytes in a file of a kilobyte.
const (
	RequestBase    = 16 << 20
	RequestPerByte = 16
)

// RequestLimit is the largest one request an Allocator grants a call reading an input
// of n bytes: RequestBase + RequestPerByte×n.
func RequestLimit(n int) int { return RequestBase + RequestPerByte*max(n, 0) }

// PoisonByte is what a poisoned Allocator fills buffers with.
const PoisonByte = 0xA5

// Allocator is an arrow-go memory.Allocator that refuses, and records, any one request
// over Limit. A refused request panics with its *Refusal, as arrow-go's allocators
// panic when they cannot allocate; ursus's recovery turns that into an error carrying
// the Refusal, and Run fails the call on it, or on the record when the code under the
// allocator swallowed the panic. A request it grants is arrow-go's Go allocator's,
// 64-byte aligned.
//
// The zero value is ready to use, with Limit RequestLimit(0), 16 MiB. It is safe for
// concurrent use.
type Allocator struct {
	// Limit is the largest one request granted, in bytes; zero is RequestLimit(0).
	Limit int

	// Poison fills each buffer with PoisonByte as it is handed out and as it is freed,
	// so code that takes fresh memory for zeroed, or reads a buffer after freeing it,
	// reads 0xA5 bytes the input may not hold.
	Poison bool

	mu      sync.Mutex
	refused []Refusal
}

// NewAllocator is an Allocator for a call reading an input of n bytes:
// Limit is RequestLimit(n).
func NewAllocator(n int) *Allocator { return &Allocator{Limit: RequestLimit(n)} }

// Refusal is one refused request.
type Refusal struct {
	Size  int    // the bytes asked for
	Limit int    // the allocator's limit
	Stack []byte // the asking goroutine's stack
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("fuzzcheck: a request for %d bytes refused, over the bounded allocator's "+
		"limit of %d", r.Size, r.Limit)
}

func (r *Refusal) report() string {
	return fmt.Sprintf("the bounded allocator refused a request for %d bytes (%.1f MiB), over its "+
		"limit of %d (%.1f MiB):\n%s", r.Size, mib(int64(r.Size)), r.Limit, mib(int64(r.Limit)), r.Stack)
}

var goAlloc = memory.NewGoAllocator()

func (a *Allocator) limit() int {
	if a.Limit > 0 {
		return a.Limit
	}
	return RequestLimit(0)
}

// Allocate returns a buffer of size bytes, or panics with a *Refusal for a request
// over Limit, or of a negative size.
func (a *Allocator) Allocate(size int) []byte {
	if size < 0 || size > a.limit() {
		r := Refusal{Size: size, Limit: a.limit(), Stack: debug.Stack()}
		a.mu.Lock()
		a.refused = append(a.refused, r)
		a.mu.Unlock()
		panic(&r)
	}
	b := goAlloc.Allocate(size)
	if a.Poison {
		fill(b)
	}
	return b
}

// Reallocate resizes b to size bytes, within its capacity where it has room, and
// otherwise by a new Allocate, which may refuse.
func (a *Allocator) Reallocate(size int, b []byte) []byte {
	if size >= 0 && size <= cap(b) {
		old := len(b)
		b = b[:size]
		if a.Poison && size > old {
			fill(b[old:])
		}
		return b
	}
	nb := a.Allocate(size)
	copy(nb, b)
	a.Free(b)
	return nb
}

// Free gives b back; poisoned, it is filled with PoisonByte first.
func (a *Allocator) Free(b []byte) {
	if a.Poison {
		fill(b[:cap(b)])
	}
	goAlloc.Free(b)
}

// Refusals are the requests refused so far, in order.
func (a *Allocator) Refusals() []Refusal {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Refusal(nil), a.refused...)
}

// refusals is how many requests a, which may be nil, has refused.
func (a *Allocator) refusals() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.refused)
}

func fill(b []byte) {
	for i := range b {
		b[i] = PoisonByte
	}
}

var _ memory.Allocator = (*Allocator)(nil)
