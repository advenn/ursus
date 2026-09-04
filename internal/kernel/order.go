package kernel

import (
	"math"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// OrderKeyF64 maps a float64 onto a uint64 whose UNSIGNED ordering is ursus's
// documented total order:
//
//	-Inf < … < -0.0 == +0.0 < … < +Inf < NaN
//
// with all NaNs equal to each other.
//
// # Why this deliberately is not IEEE
//
// IEEE says NaN compares false against everything, including itself. That is
// correct for `Expr.Lt` — asking whether a missing measurement is less than five
// should not invent an answer — but it is fatal for sorting and grouping:
//
//   - A comparison-based sort needs a TOTAL order. With IEEE semantics NaN is
//     unordered, sort becomes non-deterministic, and some algorithms fail to
//     terminate.
//   - Hash grouping needs an equivalence relation. With IEEE, NaN != NaN, so every
//     NaN would form its own group — you could not count them.
//
// So ursus uses IEEE for comparison expressions and this total order for sort,
// top-k and group-by. The two must never be confused; that is why this lives in
// its own file with its own name rather than as a flag on the comparison kernels.
//
// The bit trick: for a positive float, flipping the sign bit gives a uint64 that
// orders correctly. For a negative float, inverting every bit does. Both are exact
// and branch-light.
func OrderKeyF64(f float64) uint64 {
	if f != f { // NaN: one canonical bucket, above everything
		return ^uint64(0)
	}
	b := math.Float64bits(f)
	if b == 0x8000_0000_0000_0000 {
		b = 0 // -0.0 and +0.0 are the same value and must hash and sort alike
	}
	if b>>63 != 0 {
		return ^b // negative
	}
	return b | 1<<63 // positive
}

// OrderKeyF32 is OrderKeyF64 for 32-bit floats.
func OrderKeyF32(f float32) uint32 {
	if f != f {
		return ^uint32(0)
	}
	b := math.Float32bits(f)
	if b == 0x8000_0000 {
		b = 0
	}
	if b>>31 != 0 {
		return ^b
	}
	return b | 1<<31
}

// SortSpec describes one component of an ordering.
type SortSpec struct {
	Descending bool
	// NullsLast places nulls at the END OF THE OUTPUT, independent of direction.
	// Tying it to direction instead would mean `Desc()` silently moved the nulls,
	// which is never what anyone wants.
	NullsLast bool
}

// Comparator orders two row indices. Returns <0, 0 or >0.
type Comparator func(i, j int) int

// NewComparator builds a lexicographic row comparator over key columns.
//
// Per-column accessors are resolved ONCE here rather than per comparison. A sort
// performs O(n log n) comparisons, so a type switch inside the comparator would
// dominate the runtime.
func NewComparator(cols []*data.Column, specs []SortSpec) (Comparator, error) {
	if len(cols) != len(specs) {
		return nil, uerr.Internalf("kernel: %d sort columns but %d specs", len(cols), len(specs))
	}

	cmps := make([]Comparator, len(cols))
	for i, c := range cols {
		vc, err := valueComparator(c)
		if err != nil {
			return nil, err
		}
		cmps[i] = withNulls(c, vc, specs[i])
	}

	if len(cmps) == 1 {
		return cmps[0], nil // the common case, without the loop
	}
	return func(i, j int) int {
		for _, c := range cmps {
			if r := c(i, j); r != 0 {
				return r
			}
		}
		return 0
	}, nil
}

// nullRule resolves a spec's null placement into the two answers a comparator
// returns when exactly one side is null: (null vs value, value vs null).
//
// It is a function rather than four lines inlined twice because the within-run
// comparator and the cross-run one both need it, and the rule it encodes is the
// subtle half of SortSpec: placement is applied BEFORE direction, so NullsLast
// means the same thing ascending and descending. Two copies of that would
// eventually disagree, and the disagreement would be invisible on data with no
// nulls.
func nullRule(spec SortSpec) (nullFirst, nullSecond int) {
	if spec.NullsLast {
		return 1, -1
	}
	return -1, 1
}

// direct applies the ordering direction to a value comparison. Shared with the
// cross-run comparator for the same reason nullRule is.
func direct(spec SortSpec, r int) int {
	if spec.Descending {
		return -r
	}
	return r
}

// withNulls wraps a value comparator with null handling and direction.
//
// Null placement is applied BEFORE direction is, which is what makes NullsLast
// mean the same thing ascending and descending.
func withNulls(c *data.Column, cmp Comparator, spec SortSpec) Comparator {
	valid := c.Validity()
	nullFirst, nullSecond := nullRule(spec)

	return func(i, j int) int {
		vi, vj := valid.Get(i), valid.Get(j)
		switch {
		case !vi && !vj:
			return 0
		case !vi:
			return nullFirst
		case !vj:
			return nullSecond
		}
		return direct(spec, cmp(i, j))
	}
}

func valueComparator(c *data.Column) (Comparator, error) {
	switch c.DType().ID() {
	case dtype.TypeBool:
		bits := c.Bools()
		return func(i, j int) int {
			a, b := bits.Get(i), bits.Get(j)
			switch {
			case a == b:
				return 0
			case !a:
				return -1 // false < true
			default:
				return 1
			}
		}, nil
	}

	if c.DType().HasStringStorage() {
		acc := c.Strings()
		return func(i, j int) int {
			a, b := acc.Get(i), acc.Get(j)
			switch {
			case a < b:
				return -1
			case a > b:
				return 1
			default:
				return 0
			}
		}, nil
	}

	switch c.DType().Physical().ID() {
	case dtype.TypeInt8:
		return intCmp[int8](c)
	case dtype.TypeInt16:
		return intCmp[int16](c)
	case dtype.TypeInt32:
		return intCmp[int32](c)
	case dtype.TypeInt64:
		return intCmp[int64](c)
	case dtype.TypeUint8:
		return intCmp[uint8](c)
	case dtype.TypeUint16:
		return intCmp[uint16](c)
	case dtype.TypeUint32:
		return intCmp[uint32](c)
	case dtype.TypeUint64:
		return intCmp[uint64](c)

	case dtype.TypeFloat32:
		v, err := data.Values[float32](c)
		if err != nil {
			return nil, err
		}
		// Compare the order keys, not the floats: this is where NaN becomes
		// orderable and -0.0 stops being distinguishable from +0.0.
		return func(i, j int) int {
			a, b := OrderKeyF32(v[i]), OrderKeyF32(v[j])
			switch {
			case a < b:
				return -1
			case a > b:
				return 1
			default:
				return 0
			}
		}, nil

	case dtype.TypeFloat64:
		v, err := data.Values[float64](c)
		if err != nil {
			return nil, err
		}
		return func(i, j int) int {
			a, b := OrderKeyF64(v[i]), OrderKeyF64(v[j])
			switch {
			case a < b:
				return -1
			case a > b:
				return 1
			default:
				return 0
			}
		}, nil

	case dtype.TypeInt128:
		v, err := data.Values[i128.Int128](c)
		if err != nil {
			return nil, err
		}
		return func(i, j int) int { return v[i].Cmp(v[j]) }, nil

	default:
		return nil, uerr.New(uerr.KindUnsupported, "sort",
			"cannot order a %s column", c.DType())
	}
}

func intCmp[T data.Primitive](c *data.Column) (Comparator, error) {
	v, err := data.Values[T](c)
	if err != nil {
		return nil, err
	}
	return func(i, j int) int {
		a, b := v[i], v[j]
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	}, nil
}
