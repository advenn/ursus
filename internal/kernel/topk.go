package kernel

import (
	"cmp"
	"math"
	"sort"
	"strings"
	"unsafe"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// topKAcc implements TopK and BottomK: each group's k largest non-null values,
// largest first, or its k smallest, smallest first, as a List.
//
// # Bounded, where implode is not
//
// Each group holds at most k values, copied out of their batch, so the state is
// O(groups × k) and no batch is retained. h2o gb8 — the two largest v3 per id6 —
// was a rank window over a full sort, 92% of its time (step 24); Polars answers it
// with top_k inside a group-by, and so can ursus now.
//
// A group's values are kept sorted, best first. A value that does not beat the
// k-th is rejected by one comparison, which is what nearly every value of a large
// group meets once the group has warmed up; the rest are placed by binary search.
//
// # Order
//
// Values are ordered by ursus's total order, so NaN is the largest float, as Sort
// places it. Which of two equal values is kept does not matter, because equal is
// equal: the answer does not depend on the order the rows arrived in, and the
// accumulator merges, so a parallel group-by keeps its workers.
//
// # Nulls
//
// Skipped, as every reduction skips them: a group with fewer than k values gives a
// shorter list, and a group with none gives null, as its min would.
type topKAcc[T any] struct {
	k   int
	top bool // the largest, descending; otherwise the smallest, ascending

	cmp    func(a, b T) int                         // the total order
	values func(*data.Column) (func(int) T, error)  // row i's value
	keep   func(T) T                                // a copy that holds no batch alive
	build  func(name string, vals []T) *data.Column // the List's element column
	size   int64                                    // bytes a value holds, for NBytes

	vals [][]T // per group, best first, at most k
	held int64 // values held across every group
}

// compareSignedZero is the total order with -0.0 below +0.0.
//
// The total order calls them equal, and offer keeps the first of two equals, so
// TopK(1) of [-0.0, 0.0] gave whichever arrived first, which a parallel group-by
// decides. Its answer is not meant to depend on arrival order.
func compareSignedZero(x, y float64) int {
	if c := compareTotalF64(x, y); c != 0 || x != 0 {
		return c
	}
	switch sx, sy := math.Signbit(x), math.Signbit(y); {
	case sx && !sy:
		return -1
	case !sx && sy:
		return 1
	}
	return 0
}

// better reports whether a belongs ahead of b.
func (a *topKAcc[T]) better(x, y T) bool {
	c := a.cmp(x, y)
	if a.top {
		return c > 0
	}
	return c < 0
}

func (a *topKAcc[T]) Reserve(n int) {
	for len(a.vals) < n {
		a.vals = append(a.vals, nil)
	}
}

// offer places v among group g's best, if it belongs there.
func (a *topKAcc[T]) offer(g int, v T) {
	s := a.vals[g]
	if len(s) == a.k && !a.better(v, s[a.k-1]) {
		return
	}
	// The first slot v beats; an equal value goes after those already held.
	i := sort.Search(len(s), func(j int) bool { return a.better(v, s[j]) })
	v = a.keep(v)
	if len(s) < a.k {
		if s == nil {
			s = make([]T, 0, min(a.k, 8))
		}
		var zero T
		s = append(s, zero)
		a.held++
	}
	copy(s[i+1:], s[i:len(s)-1])
	s[i] = v
	a.vals[g] = s
}

func (a *topKAcc[T]) AddBatch(groups []int32, col *data.Column) error {
	get, err := a.values(col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if valid.Get(i) {
			a.offer(int(g), get(i))
		}
	}
	return nil
}

// Merge offers other's values to this one's groups. Each group's best k of the
// union is the best k of the two best ks, so nothing other dropped can matter.
func (a *topKAcc[T]) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*topKAcc[T])
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into %T", other, a)
	}
	if a.k != o.k || a.top != o.top {
		return uerr.Internalf("kernel: cannot merge top-k accumulators with different parameters")
	}
	a.Reserve(mergeCap(remap, len(o.vals)))
	mergeEach(remap, len(o.vals), func(dst, src int) {
		for _, v := range o.vals[src] {
			a.offer(dst, v)
		}
	})
	return nil
}

func (a *topKAcc[T]) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	offs := make([]int32, 0, nGroups+1)
	offs = append(offs, 0)
	all := make([]T, 0, a.held)
	seen := make([]bool, nGroups)
	for g := range nGroups {
		all = append(all, a.vals[g]...)
		offs = append(offs, int32(len(all)))
		seen[g] = len(a.vals[g]) > 0
	}
	return data.NewList(name, offs, a.build(name, all), seenBitmap(seen, nGroups)), nil
}

// NBytes is O(groups), as Accumulator requires: the values are counted as they
// arrive, not walked.
func (a *topKAcc[T]) NBytes() int64 { return a.held*a.size + int64(len(a.vals))*24 }

// newTopK builds the TopK or BottomK accumulator for a column of type in.
func newTopK(in dtype.DataType, k int, top bool) (Accumulator, error) {
	// Agg.Field refuses k < 1 while the query is planned; this is the assertion
	// that nothing reached here without it, where k-1 would index nothing.
	if k < 1 {
		return nil, uerr.Internalf("kernel: top_k and bottom_k need k >= 1, got %d", k)
	}
	if in.HasStringStorage() {
		return &topKAcc[string]{
			k: k, top: top, cmp: strings.Compare,
			values: func(c *data.Column) (func(int) string, error) { return c.Strings().Get, nil },
			// Get aliases the batch's character buffer; a copy lets the batch go.
			keep: strings.Clone,
			build: func(name string, vals []string) *data.Column {
				return data.NewString(name, vals, bitmap.View{}).WithDType(in)
			},
			size: 16,
		}, nil
	}
	switch in.Physical().ID() {
	case dtype.TypeInt8:
		return newTopKFixed[int8](in, k, top, cmp.Compare[int8]), nil
	case dtype.TypeInt16:
		return newTopKFixed[int16](in, k, top, cmp.Compare[int16]), nil
	case dtype.TypeInt32:
		return newTopKFixed[int32](in, k, top, cmp.Compare[int32]), nil
	case dtype.TypeInt64:
		return newTopKFixed[int64](in, k, top, cmp.Compare[int64]), nil
	case dtype.TypeUint8:
		return newTopKFixed[uint8](in, k, top, cmp.Compare[uint8]), nil
	case dtype.TypeUint16:
		return newTopKFixed[uint16](in, k, top, cmp.Compare[uint16]), nil
	case dtype.TypeUint32:
		return newTopKFixed[uint32](in, k, top, cmp.Compare[uint32]), nil
	case dtype.TypeUint64:
		return newTopKFixed[uint64](in, k, top, cmp.Compare[uint64]), nil
	case dtype.TypeFloat32:
		return newTopKFixed[float32](in, k, top, func(x, y float32) int {
			return compareSignedZero(float64(x), float64(y))
		}), nil
	case dtype.TypeFloat64:
		return newTopKFixed[float64](in, k, top, compareSignedZero), nil
	case dtype.TypeInt128: // Int128 and Decimal, whose scale one column shares
		return newTopKFixed[i128.Int128](in, k, top, i128.Int128.Cmp), nil
	}
	return nil, uerr.New(uerr.KindUnsupported, "agg",
		"top_k and bottom_k are not implemented for %s", in)
}

func newTopKFixed[T data.Fixed](in dtype.DataType, k int, top bool, order func(a, b T) int) *topKAcc[T] {
	return &topKAcc[T]{
		k: k, top: top, cmp: order,
		values: func(c *data.Column) (func(int) T, error) {
			v, err := data.Values[T](c)
			if err != nil {
				return nil, err
			}
			return func(i int) T { return v[i] }, nil
		},
		keep: func(v T) T { return v },
		build: func(name string, vals []T) *data.Column {
			return data.NewFixed(name, in, vals, bitmap.View{})
		},
		size: int64(unsafe.Sizeof(*new(T))),
	}
}
