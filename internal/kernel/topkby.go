package kernel

import (
	"cmp"
	"sort"
	"strings"
	"unsafe"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// topKByAcc implements TopKBy and BottomKBy (step 330): each group's k values whose
// by is the greatest, greatest first, or the least, least first, as a List: Polars'
// top_k_by and bottom_k_by.
//
// # Bounded, as TopK is
//
// Each group holds at most k entries, a by and its value, copied out of their batch,
// so no batch is retained. A row that does not beat the k-th is rejected by one
// comparison; the rest are placed by binary search.
//
// # Order, and why ties go to the value
//
// By ursus's total order of by, NaN the greatest float, and a null by after every
// value, whichever the direction, as Polars puts it. Rows of one by are ordered by
// their values, the greater first for TopKBy and the lesser for BottomKBy, a null
// value last: Polars leaves them to its sort, whose order among ties is its own.
// Taking them by the value makes the answer depend on no row order, so the
// accumulator merges in any order and a group-by holding it keeps its workers, as
// TopK's does, where an earlier-row rule would make it order-dependent, as MaxBy is.
//
// # Nulls
//
// A value's null is kept, as Polars keeps it: the entry is the row's, its value
// null. So a group's list is min(k, its rows) long.
type topKByAcc[K, T any] struct {
	k   int
	top bool // the greatest by, descending; otherwise the least, ascending

	cmpK  func(a, b K) int                        // by's total order
	keys  func(*data.Column) (func(int) K, error) // row i's by
	keepK func(K) K                               // a copy that holds no batch alive
	cmpV  func(a, b T) int                        // the values' total order, for ties
	vals  func(*data.Column) (func(int) T, error) // row i's value
	keepV func(T) T
	build func(name string, vals []T, valid bitmap.View) *data.Column // the List's elements
	size  int64                                                       // bytes an entry holds, for NBytes

	best [][]topKByEntry[K, T] // per group, best first, at most k
	held int64                 // entries held across every group
}

type topKByEntry[K, T any] struct {
	key     K
	val     T
	keyNull bool
	valNull bool
}

// ahead reports whether x belongs ahead of y: by its by, then its value, each in
// the direction, each null last.
func (a *topKByAcc[K, T]) ahead(x, y *topKByEntry[K, T]) bool {
	if x.keyNull != y.keyNull {
		return !x.keyNull
	}
	if !x.keyNull {
		if c := a.cmpK(x.key, y.key); c != 0 {
			return c > 0 == a.top
		}
	}
	if x.valNull != y.valNull {
		return !x.valNull
	}
	if !x.valNull {
		if c := a.cmpV(x.val, y.val); c != 0 {
			return c > 0 == a.top
		}
	}
	return false // alike: the one held stays ahead
}

func (a *topKByAcc[K, T]) Reserve(n int) { a.best = Extend(a.best, n, nil) }

// offer places e among group g's best, if it belongs there, its by and value copied.
func (a *topKByAcc[K, T]) offer(g int, e topKByEntry[K, T]) {
	s := a.best[g]
	if len(s) == a.k && !a.ahead(&e, &s[a.k-1]) {
		return
	}
	i := sort.Search(len(s), func(j int) bool { return a.ahead(&e, &s[j]) })
	if !e.keyNull {
		e.key = a.keepK(e.key)
	}
	if !e.valNull {
		e.val = a.keepV(e.val)
	}
	if len(s) < a.k {
		if s == nil {
			s = make([]topKByEntry[K, T], 0, min(a.k, 8))
		}
		s = append(s, topKByEntry[K, T]{})
		a.held++
	}
	copy(s[i+1:], s[i:len(s)-1])
	s[i] = e
	a.best[g] = s
}

func (a *topKByAcc[K, T]) AddBatch(groups []int32, col *data.Column) error {
	value, by, err := pairFields(col)
	if err != nil {
		return err
	}
	if value, err = readable(value); err != nil {
		return err
	}
	if by, err = readable(by); err != nil {
		return err
	}
	getK, err := a.keys(by)
	if err != nil {
		return err
	}
	getV, err := a.vals(value)
	if err != nil {
		return err
	}
	kv, vv := by.Validity(), value.Validity()
	for i, g := range groups {
		e := topKByEntry[K, T]{keyNull: !kv.Get(i), valNull: !vv.Get(i)}
		if !e.keyNull {
			e.key = getK(i)
		}
		if !e.valNull {
			e.val = getV(i)
		}
		a.offer(int(g), e)
	}
	return nil
}

// Merge offers other's entries to this one's groups. Each group's best k of the union
// is the best k of the two best ks, and the order is total, so nothing other dropped
// can matter, and the merges' order does not either.
func (a *topKByAcc[K, T]) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*topKByAcc[K, T])
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into %T", other, a)
	}
	if a.k != o.k || a.top != o.top {
		return uerr.Internalf("kernel: cannot merge top-k-by accumulators with different parameters")
	}
	a.Reserve(mergeCap(remap, len(o.best)))
	mergeEach(remap, len(o.best), func(dst, src int) {
		for _, e := range o.best[src] {
			a.offer(dst, e)
		}
	})
	return nil
}

func (a *topKByAcc[K, T]) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	offs := make([]int32, 0, nGroups+1)
	offs = append(offs, 0)
	all := make([]T, 0, a.held)
	valid := make([]bool, 0, a.held)
	for g := range nGroups {
		for _, e := range a.best[g] {
			all = append(all, e.val)
			valid = append(valid, !e.valNull)
		}
		offs = append(offs, int32(len(all)))
	}
	elems := a.build(name, all, seenBitmap(valid, len(valid)))
	return data.NewList(name, offs, elems, bitmap.AllSet(nGroups)), nil
}

// NBytes is O(groups), as Accumulator requires: the entries are counted as they
// arrive, not walked.
func (a *topKByAcc[K, T]) NBytes() int64 { return a.held*a.size + int64(cap(a.best))*24 }

// newTopKBy builds TopKBy or BottomKBy for a pair of a value of type value and a by
// of type by.
func newTopKBy(value, by dtype.DataType, k int, top bool) (Accumulator, error) {
	if k < 1 {
		return nil, uerr.Internalf("kernel: top_k_by and bottom_k_by need k >= 1, got %d", k)
	}
	if by.HasStringStorage() {
		return topKByValue(value, k, top, strings.Compare,
			func(c *data.Column) (func(int) string, error) { return c.Strings().Get, nil }, strings.Clone)
	}
	switch by.Physical().ID() {
	case dtype.TypeBool:
		return topKByValue(value, k, top, cmp.Compare[uint8], func(c *data.Column) (func(int) uint8, error) {
			bits := c.Bools()
			return func(i int) uint8 {
				if bits.Get(i) {
					return 1
				}
				return 0
			}, nil
		}, same[uint8])
	case dtype.TypeInt8:
		return topKByValue(value, k, top, cmp.Compare[int8], fixedOf[int8], same[int8])
	case dtype.TypeInt16:
		return topKByValue(value, k, top, cmp.Compare[int16], fixedOf[int16], same[int16])
	case dtype.TypeInt32:
		return topKByValue(value, k, top, cmp.Compare[int32], fixedOf[int32], same[int32])
	case dtype.TypeInt64:
		return topKByValue(value, k, top, cmp.Compare[int64], fixedOf[int64], same[int64])
	case dtype.TypeUint8:
		return topKByValue(value, k, top, cmp.Compare[uint8], fixedOf[uint8], same[uint8])
	case dtype.TypeUint16:
		return topKByValue(value, k, top, cmp.Compare[uint16], fixedOf[uint16], same[uint16])
	case dtype.TypeUint32:
		return topKByValue(value, k, top, cmp.Compare[uint32], fixedOf[uint32], same[uint32])
	case dtype.TypeUint64:
		return topKByValue(value, k, top, cmp.Compare[uint64], fixedOf[uint64], same[uint64])
	case dtype.TypeFloat32:
		return topKByValue(value, k, top, func(x, y float32) int { return compareTotalF64(float64(x), float64(y)) },
			fixedOf[float32], same[float32])
	case dtype.TypeFloat64:
		return topKByValue(value, k, top, compareTotalF64, fixedOf[float64], same[float64])
	case dtype.TypeInt128:
		return topKByValue(value, k, top, i128.Int128.Cmp, fixedOf[i128.Int128], same[i128.Int128])
	}
	return nil, uerr.New(uerr.KindUnsupported, "agg", "top_k_by and bottom_k_by cannot order by %s", by)
}

func same[T any](v T) T { return v }

// compareBool orders false before true.
func compareBool(x, y bool) int {
	switch {
	case x == y:
		return 0
	case !x:
		return -1
	}
	return 1
}

func fixedOf[T data.Fixed](c *data.Column) (func(int) T, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	return func(i int) T { return v[i] }, nil
}

// topKByValue is newTopKBy's second half: the value's type, by's already chosen.
func topKByValue[K any](value dtype.DataType, k int, top bool, cmpK func(a, b K) int,
	keys func(*data.Column) (func(int) K, error), keepK func(K) K) (Accumulator, error) {
	if value.HasStringStorage() {
		return &topKByAcc[K, string]{k: k, top: top, cmpK: cmpK, keys: keys, keepK: keepK, cmpV: strings.Compare,
			vals:  func(c *data.Column) (func(int) string, error) { return c.Strings().Get, nil },
			keepV: strings.Clone,
			build: func(name string, vals []string, valid bitmap.View) *data.Column {
				return data.NewString(name, vals, valid).WithDType(value)
			},
			size: int64(unsafe.Sizeof(topKByEntry[K, string]{})),
		}, nil
	}
	switch value.Physical().ID() {
	case dtype.TypeBool:
		return &topKByAcc[K, bool]{k: k, top: top, cmpK: cmpK, keys: keys, keepK: keepK, cmpV: compareBool,
			vals: func(c *data.Column) (func(int) bool, error) { return c.Bools().Get, nil }, keepV: same[bool],
			build: func(name string, vals []bool, valid bitmap.View) *data.Column {
				b := bitmap.NewBuilder(len(vals))
				for _, v := range vals {
					b.Append(v)
				}
				return data.NewBool(name, b.Finish(), valid)
			},
			size: int64(unsafe.Sizeof(topKByEntry[K, bool]{})),
		}, nil
	case dtype.TypeInt8:
		return topKByFixed[K, int8](value, k, top, cmpK, keys, keepK, cmp.Compare[int8]), nil
	case dtype.TypeInt16:
		return topKByFixed[K, int16](value, k, top, cmpK, keys, keepK, cmp.Compare[int16]), nil
	case dtype.TypeInt32:
		return topKByFixed[K, int32](value, k, top, cmpK, keys, keepK, cmp.Compare[int32]), nil
	case dtype.TypeInt64:
		return topKByFixed[K, int64](value, k, top, cmpK, keys, keepK, cmp.Compare[int64]), nil
	case dtype.TypeUint8:
		return topKByFixed[K, uint8](value, k, top, cmpK, keys, keepK, cmp.Compare[uint8]), nil
	case dtype.TypeUint16:
		return topKByFixed[K, uint16](value, k, top, cmpK, keys, keepK, cmp.Compare[uint16]), nil
	case dtype.TypeUint32:
		return topKByFixed[K, uint32](value, k, top, cmpK, keys, keepK, cmp.Compare[uint32]), nil
	case dtype.TypeUint64:
		return topKByFixed[K, uint64](value, k, top, cmpK, keys, keepK, cmp.Compare[uint64]), nil
	case dtype.TypeFloat32:
		return topKByFixed[K, float32](value, k, top, cmpK, keys, keepK, func(x, y float32) int { return compareSignedZero(float64(x), float64(y)) }), nil
	case dtype.TypeFloat64:
		return topKByFixed[K, float64](value, k, top, cmpK, keys, keepK, compareSignedZero), nil
	case dtype.TypeInt128:
		return topKByFixed[K, i128.Int128](value, k, top, cmpK, keys, keepK, i128.Int128.Cmp), nil
	}
	return nil, uerr.New(uerr.KindUnsupported, "agg", "top_k_by and bottom_k_by are not implemented for %s", value)
}

func topKByFixed[K any, T data.Fixed](value dtype.DataType, k int, top bool, cmpK func(a, b K) int,
	keys func(*data.Column) (func(int) K, error), keepK func(K) K, cmpV func(a, b T) int) *topKByAcc[K, T] {
	return &topKByAcc[K, T]{k: k, top: top, cmpK: cmpK, keys: keys, keepK: keepK, cmpV: cmpV,
		vals: fixedOf[T], keepV: same[T],
		build: func(name string, vals []T, valid bitmap.View) *data.Column {
			return data.NewFixed(name, value, vals, valid)
		},
		size: int64(unsafe.Sizeof(topKByEntry[K, T]{})),
	}
}
