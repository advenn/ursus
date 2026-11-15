package kernel

import (
	"slices"
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
//
// # And a tie is a row, for a float
//
// Values the order calls equal can differ in their bits: -0.0 and +0.0, and NaNs of
// different payloads. Sort is stable, so sort(x).first() is the EARLIEST of a tie of
// least values and sort(x).last() the LATEST of a tie of greatest ones, and Min and
// Max answer those bits: Min keeps the first of equal values it is given, Max the
// last. A parallel group-by merges partial states that saw rows dealt round-robin,
// in worker order, which is not row order, so a float's state also keeps the input
// position of the row it chose, and Merge breaks a tie by it (step 523: Min answered
// +0 on four threads where one answered -0).

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
		return &extremumNum[float32]{out: out, max: max, less: lessFloat32, keyed: true, ties: true}, nil
	case dtype.TypeFloat64:
		return &extremumNum[float64]{out: out, max: max, less: lessFloat64, keyed: true, ties: true}, nil
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
	// ties: T is a float, whose equal values can differ in their bits, so Max keeps
	// the last of equal values it is given, where Min keeps the first (step 523).
	ties bool
	// track: rows are placed (PlaceRows), and at holds each group's chosen row's
	// input position, base plus its index in the batch, so Merge can break a tie by
	// row. Only where the value is a zero or a NaN (tieable): two equal values of
	// different bits are two zeros or two NaNs, and two equal numbers else are the
	// same bits, either row's, so their position goes unread and is not written,
	// and at is not made until a zero or a NaN is chosen. A serial group-by never
	// merges and never places, and keeps no position.
	track bool
	at    []int64
	base  int64

	best []T
	seen []bool
}

func (a *extremumNum[T]) Reserve(n int) {
	var zero T
	a.best = Extend(a.best, n, zero)
	a.seen = Extend(a.seen, n, false)
	if a.at != nil {
		a.at = Extend(a.at, n, 0)
	}
}

// PlaceRows sets the input position of the next batch's first row, and from the
// first call keeps positions: see RowPlacer. A caller places every batch from the
// first, so no group was chosen before positions were kept.
func (a *extremumNum[T]) PlaceRows(base int64) {
	a.base = base
	a.track = a.ties
}

// tieable reports whether x is a value an equal one can differ from in its bits: a
// zero, or a NaN.
func tieable[T data.Primitive](x T) bool { return x == 0 || x != x }

// placeAt keeps group g's position, making at at its first use.
func (a *extremumNum[T]) placeAt(g int, pos int64) { a.makeAt()[g] = pos }

// makeAt is at, made at the length and capacity of best if it is not yet.
func (a *extremumNum[T]) makeAt() []int64 {
	if a.at == nil {
		a.at = make([]int64, len(a.best), cap(a.best))
	}
	return a.at
}

// wins reports whether x is strictly better than cur.
func (a *extremumNum[T]) wins(x, cur T) bool {
	if a.max {
		return a.less(cur, x)
	}
	return a.less(x, cur)
}

// replaces reports whether x, the later of the two in the input, takes cur's place:
// when it wins, and for a float's Max when the two are equal too, the latest of a
// tie being the one a stable sort puts last.
func (a *extremumNum[T]) replaces(x, cur T) bool {
	if a.max && a.ties {
		return !a.less(x, cur)
	}
	return a.wins(x, cur)
}

func (a *extremumNum[T]) AddBatch(groups []int32, col *data.Column) error {
	// Once per batch, not once per group: this is the whole point of the rewrite.
	v, err := data.Values[T](col)
	if err != nil {
		return err
	}
	valid := col.Validity()
	if a.ties {
		// A float's: Max takes an equal value, Min does not, each with the row's
		// position where positions are kept.
		best, seen, less, at, base := a.best, a.seen, a.less, a.at, a.base
		switch {
		case a.max && a.track:
			for i, g := range groups {
				if x := v[i]; valid.Get(i) && (!seen[g] || !less(x, best[g])) {
					best[g], seen[g] = x, true
					if tieable(x) {
						if at == nil {
							at = a.makeAt()
						}
						at[g] = base + int64(i)
					}
				}
			}
		case a.max:
			for i, g := range groups {
				if valid.Get(i) && (!seen[g] || !less(v[i], best[g])) {
					best[g], seen[g] = v[i], true
				}
			}
		case a.track:
			for i, g := range groups {
				if x := v[i]; valid.Get(i) && (!seen[g] || less(x, best[g])) {
					best[g], seen[g] = x, true
					if tieable(x) {
						if at == nil {
							at = a.makeAt()
						}
						at[g] = base + int64(i)
					}
				}
			}
		default:
			for i, g := range groups {
				if valid.Get(i) && (!seen[g] || less(v[i], best[g])) {
					best[g], seen[g] = v[i], true
				}
			}
		}
		return nil
	}
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
	// Either both keep positions, or the one that does not has chosen nothing: a
	// partition of a fold, or a worker dealt no batch.
	if a.ties && a.track != o.track {
		switch {
		case !o.track && !slices.Contains(o.seen, true):
		case !a.track && !slices.Contains(a.seen, true):
			a.track = true
		default:
			return uerr.Internalf("kernel: a float's %T merged with one that kept no rows", a)
		}
	}
	a.Reserve(mergeCap(remap, len(o.seen)))
	// o's group src takes the place of this one's group dst when its value wins, or
	// for a float when the two are equal and its row is the earlier for Min, the
	// later for Max. The rows decide, not which state is folded into which: a
	// parallel group-by deals batches round-robin and folds its workers in worker
	// order, so a later worker can hold the earlier row. Without positions, as a
	// test merges, the state folded into keeps a tie.
	//
	// Of a float's values only two zeros or two NaNs are equal with different bits,
	// so only those are a tie, found inline, where asking less both ways was two
	// calls a group; two equal numbers else are the same bits, whichever is kept.
	mergeEach(remap, len(o.seen), func(dst, src int) {
		if !o.seen[src] {
			return
		}
		x := o.best[src]
		if a.seen[dst] {
			cur := a.best[dst]
			if a.track && (x == 0 && cur == 0 || x != x && cur != cur) {
				if (o.at[src] < a.at[dst]) == a.max { // o's row is not the one to keep
					return
				}
			} else if !a.wins(x, cur) {
				return
			}
		}
		a.best[dst], a.seen[dst] = x, true
		if a.track && tieable(x) {
			a.placeAt(dst, o.at[src])
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
	return int64(cap(a.best))*w + int64(cap(a.seen)) + int64(cap(a.at))*8
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
