package kernel

import (
	"sync"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// Repeater repeats a column of one row, a value every row of a file shares, for a
// batch of n rows: it builds the repeat once, for the most rows asked of that value,
// and hands out slices of it (step 359). Built a batch at a time, a file's path of
// 110 bytes cost a scan of a million rows 40 ms, where Polars' costs it nothing.
type Repeater struct {
	mu   sync.Mutex
	v    *data.Column // the value repeated
	long *data.Column // it, at least as many times as asked
}

// Repeat is v, a column of one row, n times.
func (r *Repeater) Repeat(v *data.Column, n int) (*data.Column, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.v != v || r.long.Len() < n {
		long, err := repeat(v, n)
		if err != nil {
			return nil, err
		}
		r.v, r.long = v, long
	}
	return r.long.Slice(0, n), nil
}

// repeat is v n times: a String's text copied in doubling runs.
func repeat(v *data.Column, n int) (*data.Column, error) {
	if !v.IsValid(0) {
		return NullColumn(v.Name(), v.DType(), n)
	}
	if v.DType() != dtype.String {
		return Take(v, make([]int32, n))
	}
	s := v.Strings().Bytes(0)
	chars := make([]byte, len(s)*n)
	k := copy(chars, s)
	for k < len(chars) {
		k += copy(chars[k:], chars[:k])
	}
	offs := make([]int32, n+1)
	for i := range n {
		offs[i+1] = offs[i] + int32(len(s))
	}
	return data.NewStringOwned(v.Name(), offs, chars, bitmap.AllSet(n)), nil
}
