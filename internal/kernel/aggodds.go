package kernel

import (
	"bytes"
	"math"
	"unsafe"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// Polars' item, lower_bound, upper_bound and is_sorted as aggregates (step 324).

// --- item ---------------------------------------------------------------------

// itemAcc is Item: a group's one value, as Polars' item. A group of several values
// is refused, and one of none too unless allowEmpty, which makes it null. It counts
// each group's rows and keeps its first, which is the one when there is one.
type itemAcc struct {
	pos        positionAcc
	n          []uint64
	allowEmpty bool
}

func (a *itemAcc) Reserve(n int) {
	a.pos.Reserve(n)
	a.n = Extend(a.n, n, 0)
}

func (a *itemAcc) AddBatch(groups []int32, col *data.Column) error {
	for _, g := range groups {
		a.n[g]++
	}
	return a.pos.AddBatch(groups, col)
}

func (a *itemAcc) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*itemAcc)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into itemAcc", other)
	}
	a.Reserve(mergeCap(remap, len(o.n)))
	mergeEach(remap, len(o.n), func(dst, src int) { a.n[dst] += o.n[src] })
	return a.pos.Merge(&o.pos, remap)
}

func (a *itemAcc) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	for _, n := range a.n[:nGroups] {
		switch {
		case n == 1, n == 0 && a.allowEmpty:
		case n == 0:
			return nil, uerr.New(uerr.KindValue, "item", "aggregation 'item' expected a single value, got none").
				Hint("Item(true) makes it null")
		default:
			return nil, uerr.New(uerr.KindValue, "item",
				"aggregation 'item' expected a single value, got %d values", n)
		}
	}
	return a.pos.Finish(name, nGroups)
}

func (a *itemAcc) NBytes() int64 { return a.pos.NBytes() + int64(cap(a.n))*8 }

// --- lower_bound and upper_bound -----------------------------------------------------

// boundAcc is LowerBound and UpperBound: the least or the greatest value of the
// output type, a group's whatever it holds, as Polars' are. The type is a number's
// own, or what another is stored as, so it is a number's bound.
type boundAcc struct {
	out   dtype.DataType
	upper bool
}

func (a *boundAcc) Reserve(int) {}

func (a *boundAcc) AddBatch([]int32, *data.Column) error { return nil }

func (a *boundAcc) Merge(other Accumulator, _ []int32) error {
	if _, ok := other.(*boundAcc); !ok {
		return uerr.Internalf("kernel: cannot merge %T into boundAcc", other)
	}
	return nil
}

func (a *boundAcc) Finish(name string, nGroups int) (*data.Column, error) {
	switch a.out.Physical().ID() {
	case dtype.TypeInt8:
		return bound[int8](name, a.out, nGroups, math.MinInt8, math.MaxInt8, a.upper), nil
	case dtype.TypeInt16:
		return bound[int16](name, a.out, nGroups, math.MinInt16, math.MaxInt16, a.upper), nil
	case dtype.TypeInt32:
		return bound[int32](name, a.out, nGroups, math.MinInt32, math.MaxInt32, a.upper), nil
	case dtype.TypeInt64:
		return bound[int64](name, a.out, nGroups, math.MinInt64, math.MaxInt64, a.upper), nil
	case dtype.TypeUint8:
		return bound[uint8](name, a.out, nGroups, 0, math.MaxUint8, a.upper), nil
	case dtype.TypeUint16:
		return bound[uint16](name, a.out, nGroups, 0, math.MaxUint16, a.upper), nil
	case dtype.TypeUint32:
		return bound[uint32](name, a.out, nGroups, 0, math.MaxUint32, a.upper), nil
	case dtype.TypeUint64:
		return bound[uint64](name, a.out, nGroups, 0, math.MaxUint64, a.upper), nil
	case dtype.TypeFloat32:
		return bound[float32](name, a.out, nGroups, float32(math.Inf(-1)), float32(math.Inf(1)), a.upper), nil
	case dtype.TypeFloat64:
		return bound[float64](name, a.out, nGroups, math.Inf(-1), math.Inf(1), a.upper), nil
	case dtype.TypeInt128:
		return bound[i128.Int128](name, a.out, nGroups, i128.Min, i128.Max, a.upper), nil
	}
	return nil, uerr.Internalf("kernel: a bound of %s", a.out)
}

func (a *boundAcc) NBytes() int64 { return 0 }

// bound is n rows of lo, or of hi when upper.
func bound[T data.Fixed](name string, dt dtype.DataType, n int, lo, hi T, upper bool) *data.Column {
	v := lo
	if upper {
		v = hi
	}
	vals := make([]T, n)
	for i := range vals {
		vals[i] = v
	}
	return data.NewFixedOwned(name, dt, vals, bitmap.AllSet(n))
}

// --- is_sorted ------------------------------------------------------------------

// sortedRun is what a group's rows so far say about its order, enough to join it to
// the rows after it: whether they are in order, whether a null and a value are among
// them, and the first and the last value.
type sortedRun[T any] struct {
	ok, hasNull, hasValue bool
	first, last           T
}

// isSortedAcc is IsSorted: whether each group's values come in order, as Polars'
// is_sorted: ascending, or descending, ties allowed, NaN the greatest; and its nulls
// all before its values, or all after with nullsLast. A group of none is in order.
//
// Each row joins its group's run, and a run joins another, a worker's or a spilled
// partition's, by the same rule: the second's first value after the first's last,
// and no null and value out of their places. So the answer does not depend on how
// the rows were split, only on their order, which makes it order-dependent.
type isSortedAcc[T any] struct {
	runs            []sortedRun[T]
	desc, nullsLast bool
	less            func(a, b T) bool
	read            func(c *data.Column) (func(i int) T, error)

	// vals and broken are the fast path of a batch of one group without nulls, a
	// Select's: the batch's values, and the first i whose value is out of order
	// after value i-1, or -1. Nil for text and Booleans.
	vals   func(c *data.Column) ([]T, error)
	broken func(v []T, desc bool) int

	// keep copies a value out of its batch, for text, whose bytes are the batch's:
	// a group's first and last are copied once a batch, after it, the groups a batch
	// touched listed in touched by stamp. Nil for any other type.
	keep    func(T) T
	stamp   []int32 // per group: the batch that last touched it
	firstAt []int32 // per group: the batch its first value came in
	batch   int32
	touched []int32
}

// newIsSorted is IsSorted's accumulator for a column of type in.
func newIsSorted(in dtype.DataType, desc, nullsLast bool) (Accumulator, error) {
	if in.ID() == dtype.TypeBool {
		return &isSortedAcc[bool]{desc: desc, nullsLast: nullsLast,
			less: func(a, b bool) bool { return !a && b },
			read: func(c *data.Column) (func(int) bool, error) { return c.Bools().Get, nil }}, nil
	}
	if in.HasStringStorage() {
		return &isSortedAcc[[]byte]{desc: desc, nullsLast: nullsLast,
			less: func(a, b []byte) bool { return bytes.Compare(a, b) < 0 },
			read: func(c *data.Column) (func(int) []byte, error) { return c.Strings().Bytes, nil },
			keep: bytes.Clone}, nil
	}
	switch in.Physical().ID() {
	case dtype.TypeInt8:
		return sortedOf[int8](desc, nullsLast), nil
	case dtype.TypeInt16:
		return sortedOf[int16](desc, nullsLast), nil
	case dtype.TypeInt32:
		return sortedOf[int32](desc, nullsLast), nil
	case dtype.TypeInt64:
		return sortedOf[int64](desc, nullsLast), nil
	case dtype.TypeUint8:
		return sortedOf[uint8](desc, nullsLast), nil
	case dtype.TypeUint16:
		return sortedOf[uint16](desc, nullsLast), nil
	case dtype.TypeUint32:
		return sortedOf[uint32](desc, nullsLast), nil
	case dtype.TypeUint64:
		return sortedOf[uint64](desc, nullsLast), nil
	case dtype.TypeFloat32:
		a := sortedOf[float32](desc, nullsLast)
		a.less = func(x, y float32) bool { return !isNaN32(x) && (isNaN32(y) || x < y) }
		a.broken = brokenFloat[float32]
		return a, nil
	case dtype.TypeFloat64:
		a := sortedOf[float64](desc, nullsLast)
		a.less = func(x, y float64) bool { return !math.IsNaN(x) && (math.IsNaN(y) || x < y) }
		a.broken = brokenFloat[float64]
		return a, nil
	case dtype.TypeInt128:
		return &isSortedAcc[i128.Int128]{desc: desc, nullsLast: nullsLast,
			less: func(a, b i128.Int128) bool { return a.Cmp(b) < 0 },
			read: valuesAt[i128.Int128]}, nil
	}
	return nil, uerr.New(uerr.KindType, "is_sorted", "is_sorted() is not defined for %s", in)
}

func isNaN32(f float32) bool { return f != f }

// sortedOf is IsSorted's accumulator over numbers of type T: integers, and floats
// once their NaN ordering is set.
func sortedOf[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64 | float32 | float64](desc,
	nullsLast bool) *isSortedAcc[T] {
	return &isSortedAcc[T]{desc: desc, nullsLast: nullsLast,
		less: func(a, b T) bool { return a < b }, read: valuesAt[T],
		vals: data.Values[T], broken: brokenOrdered[T]}
}

// brokenOrdered is the first i of v out of order after v[i-1], or -1: a loop the
// compiler keeps tight, with no call a value.
func brokenOrdered[T int8 | int16 | int32 | int64 | uint8 | uint16 | uint32 | uint64 | float32 | float64](v []T,
	desc bool) int {
	if desc {
		for i := 1; i < len(v); i++ {
			if v[i] > v[i-1] {
				return i
			}
		}
		return -1
	}
	for i := 1; i < len(v); i++ {
		if v[i] < v[i-1] {
			return i
		}
	}
	return -1
}

// brokenFloat is brokenOrdered for floats, NaN the greatest: ascending, a value may
// follow a NaN only as a NaN; descending, a NaN may follow only a NaN.
func brokenFloat[T float32 | float64](v []T, desc bool) int {
	for i := 1; i < len(v); i++ {
		x, y := v[i-1], v[i]
		if x != x || y != y {
			if desc && y != y && x == x || !desc && x != x && y == y {
				return i
			}
			continue
		}
		if desc && y > x || !desc && y < x {
			return i
		}
	}
	return -1
}

// valuesAt reads a fixed-width column's values by row.
func valuesAt[T data.Fixed](c *data.Column) (func(int) T, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	return func(i int) T { return v[i] }, nil
}

func (a *isSortedAcc[T]) Reserve(n int) {
	a.runs = Extend(a.runs, n, sortedRun[T]{ok: true})
	if a.keep != nil {
		a.stamp = Extend(a.stamp, n, 0)
		a.firstAt = Extend(a.firstAt, n, 0)
	}
}

// before reports whether x may come before y in the order asked for.
func (a *isSortedAcc[T]) before(x, y T) bool {
	if a.desc {
		return !a.less(x, y)
	}
	return !a.less(y, x)
}

func (a *isSortedAcc[T]) AddBatch(groups []int32, col *data.Column) error {
	valid := col.Validity()
	if col.IsPayloadFree() || col.DType().IsNull() {
		// Every row null.
		for _, g := range groups {
			a.join(&a.runs[g], sortedRun[T]{ok: true, hasNull: true})
		}
		return nil
	}
	if a.vals != nil && len(groups) > 0 && oneGroup(groups) && col.NullCount() == 0 {
		return a.addRun(&a.runs[groups[0]], col)
	}
	at, err := a.read(col)
	if err != nil {
		return err
	}
	a.batch++
	for i, g := range groups {
		r := &a.runs[g]
		if !r.ok {
			continue // out of order already, for good
		}
		if !valid.Get(i) {
			if !a.nullsLast && r.hasValue {
				r.ok = false
			}
			r.hasNull = true
			continue
		}
		v := at(i)
		switch {
		case a.nullsLast && r.hasNull:
			r.ok = false
		case r.hasValue && !a.before(r.last, v):
			r.ok = false
		}
		if a.keep != nil && a.stamp[g] != a.batch {
			a.stamp[g] = a.batch
			a.touched = append(a.touched, g)
		}
		if !r.hasValue {
			r.first, r.hasValue = v, true
			if a.keep != nil {
				a.firstAt[g] = a.batch
			}
		}
		r.last = v
	}
	for _, g := range a.touched {
		r := &a.runs[g]
		r.last = a.keep(r.last)
		if a.firstAt[g] == a.batch {
			r.first = a.keep(r.first)
		}
	}
	a.touched = a.touched[:0]
	return nil
}

// oneGroup reports whether every row of a batch is one group's, as a Select's are.
func oneGroup(groups []int32) bool {
	g := groups[0]
	for _, x := range groups {
		if x != g {
			return false
		}
	}
	return true
}

// addRun joins a batch of one group without nulls to its run, the values checked a
// pair at a time in one loop; a run out of order already reads nothing more.
func (a *isSortedAcc[T]) addRun(r *sortedRun[T], col *data.Column) error {
	if !r.ok || col.Len() == 0 {
		return nil
	}
	v, err := a.vals(col)
	if err != nil {
		return err
	}
	switch {
	case a.nullsLast && r.hasNull:
		r.ok = false
	case r.hasValue && !a.before(r.last, v[0]):
		r.ok = false
	case a.broken(v, a.desc) >= 0:
		r.ok = false
	}
	if !r.hasValue {
		r.first, r.hasValue = v[0], true
	}
	r.last = v[len(v)-1]
	return nil
}

// join adds run b, which came after r, to r.
func (a *isSortedAcc[T]) join(r *sortedRun[T], b sortedRun[T]) {
	switch {
	case !b.ok:
		r.ok = false
	case !a.nullsLast && r.hasValue && b.hasNull:
		r.ok = false
	case a.nullsLast && r.hasNull && b.hasValue:
		r.ok = false
	case r.hasValue && b.hasValue && !a.before(r.last, b.first):
		r.ok = false
	}
	if b.hasValue {
		if !r.hasValue {
			r.first = b.first
		}
		r.last, r.hasValue = b.last, true
	}
	r.hasNull = r.hasNull || b.hasNull
}

// Merge joins other's runs after this one's, as an order-dependent aggregate's
// partials come, a later portion of the rows each.
func (a *isSortedAcc[T]) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*isSortedAcc[T])
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into %T", other, a)
	}
	a.Reserve(mergeCap(remap, len(o.runs)))
	mergeEach(remap, len(o.runs), func(dst, src int) { a.join(&a.runs[dst], o.runs[src]) })
	return nil
}

func (a *isSortedAcc[T]) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	bits := bitmap.NewBuilder(nGroups)
	for _, r := range a.runs[:nGroups] {
		bits.Append(r.ok)
	}
	return data.NewBool(name, bits.Finish(), bitmap.AllSet(nGroups)), nil
}

func (a *isSortedAcc[T]) NBytes() int64 {
	var z sortedRun[T]
	return int64(cap(a.runs))*int64(unsafe.Sizeof(z)) + int64(cap(a.stamp)+cap(a.firstAt))*4
}
