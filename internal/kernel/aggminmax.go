package kernel

import (
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// --- min / max ---------------------------------------------------------------

// Min and Max keep the WINNING VALUE per group, in a flat typed slice.
//
// # Why four types rather than one
//
// This used to be a single implementation holding a representative single-row
// *data.Column per group, which served every type at once. The cost of that
// generality was not small: one heap-allocated Column per group, a
// map[int32]int per batch, and a Take plus a concatColumn PER GROUP PER BATCH.
// h2o gb7 — max and min over 100,000 groups — spent 93 seconds there against
// polars' 1.2, and it was 35% of every object the engine allocated.
//
// sumAcc, thirty lines above, has always used flat typed slices. These four do
// the same, at the price of one type per payload shape:
//
//	extremumNum[T]  every fixed-width numeric, and the temporal types, whose
//	                Physical() is Int32 or Int64 — the same punning data.Values
//	                already permits
//	extremumStr     String and Binary
//	extremumI128    Int128, and therefore Decimal
//	extremumBool    Bool, whose payload is a bitmap rather than a values buffer,
//	                and which is also how Any and AllTrue are implemented
//
// # The identity-value trap is still avoided, differently
//
// The step-1 docs record it: a SIMD implementation must use IfElse(valid,
// identity) with +Inf for Min and -Inf for Max, because the obvious `Masked`
// zero-fills and would make Min([3,null,5]) return 0. The old code sidestepped
// it by holding a row rather than a value. These hold a value — so the guard is
// `seen`, a per-group bool that is false until a NON-NULL value arrives. An
// all-null group never sets it and seenBitmap turns it into a null.
//
// # Comparison is the TOTAL order, not <
//
// Floats go through OrderKeyF64/F32, so NaN is one canonical bucket above
// everything and -0.0 equals +0.0 — the order order.go documents and Sort uses,
// which is what makes `min(x)` and `sort(x).first()` agree. Using `<` here would
// make max over a column containing NaN return a number.

// lessOrdered is the comparison for integers, where Go's < is already the total
// order. Floats do NOT use it; see lessFloat64.
func lessOrdered[T data.Primitive](a, b T) bool { return a < b }

func lessFloat64(a, b float64) bool { return OrderKeyF64(a) < OrderKeyF64(b) }
func lessFloat32(a, b float32) bool { return OrderKeyF32(a) < OrderKeyF32(b) }

// newExtremum builds the accumulator for Min or Max over in, publishing out.
//
// out is bind.Out rather than the physical type, which is what keeps the max of
// a Datetime a Datetime, of a Decimal a Decimal and of an Enum an Enum.
func newExtremum(in, out dtype.DataType, max bool) (Accumulator, error) {
	if in.ID() == dtype.TypeBool {
		return &extremumBool{out: out, max: max}, nil
	}
	if in.HasStringStorage() || in.ID() == dtype.TypeBinary {
		return &extremumStr{out: out, max: max}, nil
	}
	switch in.Physical().ID() {
	case dtype.TypeInt8:
		return &extremumNum[int8]{out: out, max: max, less: lessOrdered[int8], ordered: true}, nil
	case dtype.TypeInt16:
		return &extremumNum[int16]{out: out, max: max, less: lessOrdered[int16], ordered: true}, nil
	case dtype.TypeInt32:
		return &extremumNum[int32]{out: out, max: max, less: lessOrdered[int32], ordered: true}, nil
	case dtype.TypeInt64:
		return &extremumNum[int64]{out: out, max: max, less: lessOrdered[int64], ordered: true}, nil
	case dtype.TypeUint8:
		return &extremumNum[uint8]{out: out, max: max, less: lessOrdered[uint8], ordered: true}, nil
	case dtype.TypeUint16:
		return &extremumNum[uint16]{out: out, max: max, less: lessOrdered[uint16], ordered: true}, nil
	case dtype.TypeUint32:
		return &extremumNum[uint32]{out: out, max: max, less: lessOrdered[uint32], ordered: true}, nil
	case dtype.TypeUint64:
		return &extremumNum[uint64]{out: out, max: max, less: lessOrdered[uint64], ordered: true}, nil
	case dtype.TypeFloat32:
		return &extremumNum[float32]{out: out, max: max, less: lessFloat32, keyed: true}, nil
	case dtype.TypeFloat64:
		return &extremumNum[float64]{out: out, max: max, less: lessFloat64, keyed: true}, nil
	case dtype.TypeInt128:
		return &extremumI128{out: out, max: max}, nil
	default:
		return nil, uerr.New(uerr.KindUnsupported, "agg",
			"cannot take a minimum or maximum of a %s column", in)
	}
}

// --- fixed-width numerics, and the temporal types ------------------------------

type extremumNum[T data.Primitive] struct {
	out  dtype.DataType
	max  bool
	less func(a, b T) bool
	// ordered: less is the type's own <, an integer's; keyed: it is a float's order
	// key's, lessFloat64's or lessFloat32's. AddAll compares either in place (step
	// 372).
	ordered, keyed bool

	best []T
	seen []bool
}

func (a *extremumNum[T]) Reserve(n int) {
	var zero T
	a.best = Extend(a.best, n, zero)
	a.seen = Extend(a.seen, n, false)
}

// wins reports whether x should replace the current best.
func (a *extremumNum[T]) wins(x, cur T) bool {
	if a.max {
		return a.less(cur, x)
	}
	return a.less(x, cur)
}

func (a *extremumNum[T]) AddBatch(groups []int32, col *data.Column) error {
	// Once per batch, not once per group: this is the whole point of the rewrite.
	v, err := data.Values[T](col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue // nulls are skipped, as in every other aggregate
		}
		if !a.seen[g] {
			a.best[g], a.seen[g] = v[i], true
			continue
		}
		if a.wins(v[i], a.best[g]) {
			a.best[g] = v[i]
		}
	}
	return nil
}

func (a *extremumNum[T]) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*extremumNum[T])
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into %T", other, a)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		if !a.seen[dst] {
			a.best[dst], a.seen[dst] = o.best[src], true
			return
		}
		if a.wins(o.best[src], a.best[dst]) {
			a.best[dst] = o.best[src]
		}
	})
	return nil
}

func (a *extremumNum[T]) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return data.NewFixed(name, a.out, a.best[:nGroups], seenBitmap(a.seen, nGroups)), nil
}

func (a *extremumNum[T]) NBytes() int64 {
	w := int64(a.out.Physical().BitWidth() / 8)
	return int64(cap(a.best))*w + int64(cap(a.seen))
}

// --- strings and binary --------------------------------------------------------

type extremumStr struct {
	out  dtype.DataType
	max  bool
	best []string
	seen []bool
}

func (a *extremumStr) Reserve(n int) {
	a.best = Extend(a.best, n, "")
	a.seen = Extend(a.seen, n, false)
}

func (a *extremumStr) wins(x, cur string) bool {
	if a.max {
		return cur < x
	}
	return x < cur
}

func (a *extremumStr) AddBatch(groups []int32, col *data.Column) error {
	acc := col.Strings()
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		// acc.Get ALIASES the batch's character buffer — its doc says so. Comparing
		// against the alias is free; STORING it would pin that buffer for the whole
		// aggregation, so every retained batch would be held alive by one winning
		// string. Clone only on the store, which happens at most once per group per
		// batch and usually far less.
		x := acc.Get(i)
		if !a.seen[g] {
			a.best[g], a.seen[g] = strings.Clone(x), true
			continue
		}
		if a.wins(x, a.best[g]) {
			a.best[g] = strings.Clone(x)
		}
	}
	return nil
}

func (a *extremumStr) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*extremumStr)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into extremumStr", other)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		// o.best is already cloned, so no second copy is needed here.
		if !a.seen[dst] {
			a.best[dst], a.seen[dst] = o.best[src], true
			return
		}
		if a.wins(o.best[src], a.best[dst]) {
			a.best[dst] = o.best[src]
		}
	})
	return nil
}

func (a *extremumStr) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	col := data.NewString(name, a.best[:nGroups], seenBitmap(a.seen, nGroups))
	return col.WithDType(a.out), nil
}

func (a *extremumStr) NBytes() int64 {
	n := int64(cap(a.best))*16 + int64(cap(a.seen)) // string headers plus seen
	for _, s := range a.best {
		n += int64(len(s))
	}
	return n
}

// --- Int128, and therefore Decimal ---------------------------------------------

type extremumI128 struct {
	out  dtype.DataType
	max  bool
	best []i128.Int128
	seen []bool
}

func (a *extremumI128) Reserve(n int) {
	a.best = Extend(a.best, n, i128.Zero)
	a.seen = Extend(a.seen, n, false)
}

func (a *extremumI128) wins(x, cur i128.Int128) bool {
	if a.max {
		return cur.Cmp(x) < 0
	}
	return x.Cmp(cur) < 0
}

func (a *extremumI128) AddBatch(groups []int32, col *data.Column) error {
	v, err := data.Values[i128.Int128](col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		if !a.seen[g] {
			a.best[g], a.seen[g] = v[i], true
			continue
		}
		if a.wins(v[i], a.best[g]) {
			a.best[g] = v[i]
		}
	}
	return nil
}

func (a *extremumI128) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*extremumI128)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into extremumI128", other)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		if !a.seen[dst] {
			a.best[dst], a.seen[dst] = o.best[src], true
			return
		}
		if a.wins(o.best[src], a.best[dst]) {
			a.best[dst] = o.best[src]
		}
	})
	return nil
}

func (a *extremumI128) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	return data.NewFixed(name, a.out, a.best[:nGroups], seenBitmap(a.seen, nGroups)), nil
}

func (a *extremumI128) NBytes() int64 {
	return int64(cap(a.best))*16 + int64(cap(a.seen))
}

// --- Bool, which is also Any and AllTrue ---------------------------------------

type extremumBool struct {
	out  dtype.DataType
	max  bool
	best []bool
	seen []bool
}

func (a *extremumBool) Reserve(n int) {
	a.best = Extend(a.best, n, false)
	a.seen = Extend(a.seen, n, false)
}

func (a *extremumBool) AddBatch(groups []int32, col *data.Column) error {
	bits := col.Bools()
	valid := col.Validity()
	for i, g := range groups {
		if !valid.Get(i) {
			continue
		}
		x := bits.Get(i)
		if !a.seen[g] {
			a.best[g], a.seen[g] = x, true
			continue
		}
		// false < true, so max saturates at true and min at false.
		if a.max && x && !a.best[g] {
			a.best[g] = true
		} else if !a.max && !x && a.best[g] {
			a.best[g] = false
		}
	}
	return nil
}

func (a *extremumBool) Merge(other Accumulator, remap []int32) error {
	o, ok := other.(*extremumBool)
	if !ok {
		return uerr.Internalf("kernel: cannot merge %T into extremumBool", other)
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		if !a.seen[dst] {
			a.best[dst], a.seen[dst] = o.best[src], true
			return
		}
		if a.max {
			a.best[dst] = a.best[dst] || o.best[src]
		} else {
			a.best[dst] = a.best[dst] && o.best[src]
		}
	})
	return nil
}

func (a *extremumBool) Finish(name string, nGroups int) (*data.Column, error) {
	a.Reserve(nGroups)
	vb := bitmap.NewBuilder(nGroups)
	for i := range nGroups {
		vb.Append(a.best[i])
	}
	return data.NewBool(name, vb.Finish(), seenBitmap(a.seen, nGroups)), nil
}

func (a *extremumBool) NBytes() int64 { return int64(cap(a.best)) + int64(cap(a.seen)) }
